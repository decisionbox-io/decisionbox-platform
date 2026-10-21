package discovery

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	goconfig "github.com/decisionbox-io/decisionbox/libs/go-common/config"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// Telling "the model answered badly" apart from "the model never got to
// finish", for a phase that asks for a JSON document.
//
// Both arrive at the parser as the same thing -- a string that will not
// decode -- and the repair they need is opposite. Malformed output is worth
// re-asking for with the mistake named, which is what the repair suffixes do.
// A reply that ran out of output budget is not: re-asking produces the same
// too-long answer and the same cut, which is exactly what the recommendation
// phase was observed doing, twice per run, for the whole output budget each
// time. That one needs a *smaller* request.
//
// Two signals, in order of authority:
//
//  1. The provider's stop reason. It says so directly, and
//     llm.IsLengthStopReason reads it across every wire's spelling.
//  2. The output-token count against what was requested. A provider that
//     reports no usable stop reason (or one a future wire spells in a way
//     this does not know) still cannot emit more tokens than it was given,
//     so spending the budget to the last token is the same statement.
//
// A third shape sits alongside them: a response that stopped at the cap
// having emitted almost nothing but whitespace. It is not a long answer that
// needed more room -- it is a model that fell into a repeating run and burned
// the budget on it. Shrinking the request does not fix the sampler, but
// classifying it separately keeps the retry from banking on the reply and
// keeps the operator from reading it as "the document was too big".
//
// That one is read two ways, because a provider may trim the reply before the
// caller sees it (Ollama does) and take the evidence with it: the share of the
// text that is whitespace, and -- when the text is too short to hold the
// tokens the reply is billed for -- the arithmetic that survives trimming.
const (
	// defaultResponseMinNonSpacePct is the share of a response that must be
	// something other than whitespace before it is treated as content. A
	// healthy JSON document (indented, one field per line) measures in the
	// high tens of percent; a degenerate whitespace run measures under one.
	// The floor sits between them, far from both.
	defaultResponseMinNonSpacePct = 20

	// responseMinNonSpacePctEnv overrides defaultResponseMinNonSpacePct.
	responseMinNonSpacePctEnv = "LLM_RESPONSE_MIN_NONSPACE_PCT"

	// defaultResponseDegenerateMinRunes is the length below which the
	// whitespace share is not judged at all. A short reply can be mostly
	// whitespace for innocent reasons (a blank line around a one-line
	// envelope), and there is nothing to salvage either way.
	defaultResponseDegenerateMinRunes = 512

	// responseDegenerateMinRunesEnv overrides defaultResponseDegenerateMinRunes.
	responseDegenerateMinRunesEnv = "LLM_RESPONSE_DEGENERATE_MIN_RUNES"

	// responseMinRunesPerTenTokens is the floor on how much text a response
	// must carry for the output tokens it spent, as runes per ten tokens.
	//
	// It catches the whitespace flood where the share above cannot: a provider
	// that trims its response before returning it (Ollama does) hands back the
	// short prefix and drops the thousands of newlines after it, so the share
	// is measured on text that no longer contains the problem. What survives
	// trimming is the arithmetic -- a reply that spent thousands of tokens and
	// came back as a few dozen characters emitted something that is not in the
	// text, whatever it was.
	//
	// Half a rune per token is far under any real tokenizer: dense JSON runs
	// about two, and the densest scripts run about one. Deliberately not
	// env-tunable -- it is not a threshold to tune, it is a floor below which
	// no tokenizer can produce text, and the operator-facing dials for a
	// verbose model are the per-call output ceilings.
	responseMinRunesPerTenTokens = 5
)

// responseFacts is what is known about one LLM reply at the point of judging
// it. Grouped rather than passed loose because the reasoning flag is easy to
// get wrong silently: without it, a thinking model's hidden tokens read as
// output that never arrived.
type responseFacts struct {
	// stopReason is the provider's own reason for ending generation.
	stopReason string

	// outputTokens is what the provider says the reply cost, and
	// requestedMaxTokens the budget it was given. Either being zero means "not
	// reported", and the token signals are skipped rather than guessed at.
	outputTokens       int
	requestedMaxTokens int

	// content is the reply as the caller received it -- after any trimming or
	// normalisation the provider applied.
	content string

	// reasoning is true when this model emits a hidden thinking channel. Its
	// tokens are counted in outputTokens and its text is not in content, so a
	// reply that thought at length and answered briefly is the normal case
	// there, not a reply that went missing.
	reasoning bool
}

// responseHealth is what one LLM reply was, beyond whether it parsed.
type responseHealth struct {
	// truncated is set when generation stopped at the output limit, so the
	// reply is incomplete through no fault of its shape.
	truncated bool

	// degenerate is set when the reply spent its budget on something there
	// was never an answer in.
	degenerate bool

	// missingOutput narrows degenerate to the case where the text cannot
	// account for the tokens the reply was billed for: it generated something
	// the caller did not receive. The repair is the opposite of the one a
	// too-long answer needs -- the budget was consumed before the answer
	// began, so the next attempt needs *more* room, not a shorter answer.
	// This is what a model that thinks without being catalogued as a thinking
	// model looks like from the outside.
	missingOutput bool

	// reason is a short operator-facing sentence naming what happened, for
	// the step status and the retry log. Empty when the reply is healthy.
	reason string
}

// unusable reports whether the reply cannot be trusted regardless of what the
// parser made of it.
func (h responseHealth) unusable() bool { return h.truncated || h.degenerate }

// classifyResponse judges one LLM reply from the provider's stop reason, the
// output tokens it spent against the budget it was given, and the text itself.
func classifyResponse(f responseFacts) responseHealth {
	var h responseHealth

	switch {
	case gollm.IsLengthStopReason(f.stopReason):
		h.truncated = true
		h.reason = fmt.Sprintf("the provider stopped at the output limit (stop reason %q)", f.stopReason)
	case f.requestedMaxTokens > 0 && f.outputTokens > 0 && f.outputTokens >= f.requestedMaxTokens:
		h.truncated = true
		h.reason = fmt.Sprintf("the response spent its whole output budget (%d of %d tokens)", f.outputTokens, f.requestedMaxTokens)
	}

	if reason, missing, isDegenerate := degenerateReason(f); isDegenerate {
		h.degenerate = true
		h.missingOutput = missing
		if h.reason == "" {
			h.reason = reason
		} else {
			h.reason = h.reason + ", and " + reason
		}
	}

	return h
}

// degenerateReason reports whether a reply spent its budget on something there
// was never an answer in, which way it showed, and -- in missingOutput --
// whether what showed was text that cannot account for the tokens billed.
func degenerateReason(f responseFacts) (reason string, missingOutput, degenerate bool) {
	runes, nonSpace := countRunes(f.content)

	if runes >= responseDegenerateMinRunes() {
		if pct := nonSpace * 100 / runes; pct < responseMinNonSpacePct() {
			return fmt.Sprintf("%d%% of the response was anything other than whitespace", pct), false, true
		}
	}

	// A reply that cost thousands of tokens and arrived as a few dozen
	// characters emitted something that is not in the text.
	//
	// Two kinds of reply are exempt, both because the gap between the tokens
	// and the text is an accounting artifact rather than a missing answer:
	//
	//   - A thinking model's, where the hidden channel is counted in the
	//     tokens and absent from the text by design.
	//   - One whose text is a complete JSON document. The answer is intact,
	//     whatever the tokens went on -- and on the Anthropic wire they went
	//     on a tool call whose scaffolding the fold discards, so the count
	//     describes a reply the caller no longer holds. Without this, a
	//     structured-output provider's perfectly good empty answer reads as
	//     degenerate and gets argued with.
	if f.reasoning || isCompleteJSON(f.content) {
		return "", false, false
	}
	if f.outputTokens > 0 && runes*10 < f.outputTokens*responseMinRunesPerTenTokens {
		return fmt.Sprintf("the response cost %d output tokens and arrived as %d characters, so most of what it generated is not in the text", f.outputTokens, runes), true, true
	}

	return "", false, false
}

// isCompleteJSON reports whether content is one complete JSON value and
// nothing else. A document cut off mid-structure is not, which is the whole
// distinction being drawn: what arrived either is an answer or is the start of
// one that never finished.
func isCompleteJSON(content string) bool {
	trimmed := strings.TrimSpace(content)
	return trimmed != "" && json.Valid([]byte(trimmed))
}

// countRunes returns how many runes content holds and how many of them are not
// whitespace.
func countRunes(content string) (runes, nonSpace int) {
	for _, r := range content {
		runes++
		if !unicode.IsSpace(r) {
			nonSpace++
		}
	}
	return runes, nonSpace
}

// responseMinNonSpacePct returns the configured whitespace floor, falling back
// to the default for an unset or non-positive value. Values at or above 100
// would call every response degenerate, so they fall back too.
func responseMinNonSpacePct() int {
	v := goconfig.GetEnvAsInt(responseMinNonSpacePctEnv, defaultResponseMinNonSpacePct)
	if v <= 0 || v >= 100 {
		return defaultResponseMinNonSpacePct
	}
	return v
}

// responseDegenerateMinRunes returns the configured minimum length for the
// whitespace check, falling back to the default for an unset or non-positive
// value.
func responseDegenerateMinRunes() int {
	if v := goconfig.GetEnvAsInt(responseDegenerateMinRunesEnv, defaultResponseDegenerateMinRunes); v > 0 {
		return v
	}
	return defaultResponseDegenerateMinRunes
}
