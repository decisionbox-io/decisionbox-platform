package discovery

import (
	"fmt"
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
)

// responseHealth is what one LLM reply was, beyond whether it parsed.
type responseHealth struct {
	// truncated is set when generation stopped at the output limit, so the
	// reply is incomplete through no fault of its shape.
	truncated bool

	// degenerate is set when the reply is overwhelmingly whitespace, so
	// there was never an answer in it to parse.
	degenerate bool

	// reason is a short operator-facing sentence naming what happened, for
	// the step status and the retry log. Empty when the reply is healthy.
	reason string
}

// unusable reports whether the reply cannot be trusted regardless of what the
// parser made of it.
func (h responseHealth) unusable() bool { return h.truncated || h.degenerate }

// classifyResponse judges one LLM reply from the provider's stop reason, the
// output tokens it spent against the budget it was given, and the text
// itself. requestedMaxTokens <= 0 means "not known", in which case the token
// signal is skipped rather than guessed at.
func classifyResponse(stopReason string, outputTokens, requestedMaxTokens int, content string) responseHealth {
	var h responseHealth

	switch {
	case gollm.IsLengthStopReason(stopReason):
		h.truncated = true
		h.reason = fmt.Sprintf("the provider stopped at the output limit (stop reason %q)", stopReason)
	case requestedMaxTokens > 0 && outputTokens > 0 && outputTokens >= requestedMaxTokens:
		h.truncated = true
		h.reason = fmt.Sprintf("the response spent its whole output budget (%d of %d tokens)", outputTokens, requestedMaxTokens)
	}

	if pct, judged := nonSpacePct(content); judged && pct < responseMinNonSpacePct() {
		h.degenerate = true
		degenerate := fmt.Sprintf("%d%% of the response was anything other than whitespace", pct)
		if h.reason == "" {
			h.reason = degenerate
		} else {
			h.reason = h.reason + ", and " + degenerate
		}
	}

	return h
}

// nonSpacePct returns the percentage of content that is not whitespace, and
// whether content was long enough to judge at all.
func nonSpacePct(content string) (pct int, judged bool) {
	total, nonSpace := 0, 0
	for _, r := range content {
		total++
		if !unicode.IsSpace(r) {
			nonSpace++
		}
	}
	if total < responseDegenerateMinRunes() {
		return 0, false
	}
	return nonSpace * 100 / total, true
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
