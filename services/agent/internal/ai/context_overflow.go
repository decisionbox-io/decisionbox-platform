package ai

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// estimateRequestInputTokens estimates the input token count of a chat request
// with the tokenizer-free rune/4 heuristic (system prompt + every message
// content). Used to make the adaptive over-limit retry leave room for the
// input. Deliberately cheap and local — the safety margin absorbs its drift.
func estimateRequestInputTokens(req gollm.ChatRequest) int {
	runes := utf8.RuneCountInString(req.SystemPrompt)
	for _, m := range req.Messages {
		runes += utf8.RuneCountInString(m.Content)
	}
	return runes / 4
}

// Adaptive over-limit handling.
//
// Some models reject a request outright when the requested output — or the
// input + requested output — exceeds a limit. Real shapes observed on Bedrock
// openai-compat models (the class in issue #347) and OpenAI:
//
//	// Total context window exceeded (input + output), tokens reported:
//	"This model's maximum context length is 202752 tokens. However, you requested
//	 64000 output tokens and your prompt contains at least 138753 input tokens ..."
//
//	// Same, but input reported as characters ("0 input tokens"):
//	"This model's maximum context length is 32768 tokens. However, you requested
//	 32768 output tokens and your prompt contains 600069 characters ..."
//
//	// Output cap alone exceeded (input irrelevant):
//	"'max_tokens' (990000) exceeds model maximum (32768)"
//
//	// OpenAI phrasing:
//	"maximum context length is 8192 tokens. However, you requested 9000 tokens
//	 (5000 in the messages, 4000 in the completion) ..."
//
// The proactive per-call output budget (discovery.budgetedMaxOutputTokens)
// prevents these in the common case, but the window it budgets against is an
// estimate for uncatalogued models. This layer is the net: on such a 400 the
// error itself names the model's true window and/or output cap, so a single
// re-issue with a corrected max_tokens fits. It is deterministic (not a
// transient backoff), bounded to one extra call, and leaves every other 4xx
// untouched. The true context window is also surfaced to the self-calibration
// observer so later areas / runs budget against it.

// overLimitMarkers identify an over-limit 400 across providers. Match is a
// case-insensitive substring scan — gollm does not surface typed HTTP errors at
// the interface boundary (same rationale as retry.go's status-code matching).
var overLimitMarkers = []string{
	"maximum context length",
	"context_length_exceeded",
	"reduce the length of the input prompt or the number of requested output",
	"exceeds model maximum", // Bedrock openai-compat output-cap rejection
}

// contextOverflowRetryMarginPct is the headroom kept below the model's stated
// window when recomputing max_tokens, absorbing disagreement between the
// model's tokenizer and our rune/4 estimate of the prompt.
const contextOverflowRetryMarginPct = 2

// contextOverflowMinRetryTokens is the smallest max_tokens worth retrying with.
// Below this the generation would be uselessly short, so we give up and surface
// the original error rather than issue a doomed second call.
const contextOverflowMinRetryTokens = 512

var (
	reContextWindow      = regexp.MustCompile(`maximum context length is\s+(\d+)`)
	reInputTokensBedrock = regexp.MustCompile(`contains at least\s+(\d+)\s+input tokens`)
	reInputTokensOpenAI  = regexp.MustCompile(`(\d+)\s+in the messages`)
	reModelMaxOutput     = regexp.MustCompile(`exceeds model maximum\s*\((\d+)\)`)
)

// isContextLengthError reports whether err looks like an over-limit rejection
// (total-context or output-cap) from any provider.
func isContextLengthError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	for _, m := range overLimitMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// parseContextLengthError extracts the model's true context window and input
// token count from a total-context overflow error. Handles the Bedrock/GLM
// shape ("... prompt contains at least N input tokens ...") and the OpenAI
// shape ("... N in the messages ..."). Returns ok=false when either number is
// missing (e.g. the input is reported in characters, not tokens) so the caller
// does not compute a bogus value.
func parseContextLengthError(msg string) (window, input int, ok bool) {
	window = contextWindowFromError(msg)
	if window <= 0 {
		return 0, 0, false
	}
	lower := strings.ToLower(msg)
	var err error
	if im := reInputTokensBedrock.FindStringSubmatch(lower); len(im) >= 2 {
		input, err = strconv.Atoi(im[1])
	} else if im := reInputTokensOpenAI.FindStringSubmatch(lower); len(im) >= 2 {
		input, err = strconv.Atoi(im[1])
	} else {
		return 0, 0, false
	}
	if err != nil || input <= 0 {
		return 0, 0, false
	}
	return window, input, true
}

// contextWindowFromError extracts just the "maximum context length is N" value
// (0 when absent). This is the model's true context window and is used to drive
// self-calibration even when the input side is reported in characters.
func contextWindowFromError(msg string) int {
	m := reContextWindow.FindStringSubmatch(strings.ToLower(msg))
	if len(m) < 2 {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// modelMaxOutputFromError extracts the stated output cap from the
// "'max_tokens' (X) exceeds model maximum (N)" shape (0 when absent).
func modelMaxOutputFromError(msg string) int {
	m := reModelMaxOutput.FindStringSubmatch(strings.ToLower(msg))
	if len(m) < 2 {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// reducedMaxTokensForContextOverflow computes a smaller max_tokens to retry an
// over-limit error with, or ok=false when this is not one, when reducing output
// cannot help (input alone fills the window), or when the computed value would
// not actually shrink the request.
//
// It is input-aware: inputTokens is the caller's estimate of the request's
// input size, so the retry leaves room for the input rather than requesting the
// whole window/output-cap. This matters on models whose output cap equals their
// context window (e.g. Bedrock qwen3-32b at 32768/32768) — requesting the full
// cap always re-overflows once the input is counted. The model's own reported
// input token count (when present, not characters) overrides the estimate.
func reducedMaxTokensForContextOverflow(currentMax, inputTokens int, err error) (int, bool) {
	if !isContextLengthError(err) {
		return 0, false
	}
	msg := err.Error()
	window := contextWindowFromError(msg)
	outCap := modelMaxOutputFromError(msg)

	effInput := inputTokens
	if effInput < 0 {
		effInput = 0
	}
	if _, inTok, ok := parseContextLengthError(msg); ok && inTok > effInput {
		effInput = inTok
	}

	// Budget the retry against whichever ceiling the model named, leaving the
	// input and a margin free.
	var ceiling int
	switch {
	case window > 0:
		ceiling = window
	case outCap > 0:
		ceiling = outCap
	default:
		// Recognised over-limit but no usable number (e.g. input reported in
		// characters). Rely on self-calibration (the window is still learned)
		// and the window-coupled input budget on the next area/run.
		return 0, false
	}
	margin := ceiling * contextOverflowRetryMarginPct / 100
	candidate := ceiling - effInput - margin
	// Never exceed an explicit output cap, even when the window is larger.
	if outCap > 0 && candidate > outCap {
		candidate = outCap
	}
	return clampRetryMaxTokens(candidate, currentMax)
}

// clampRetryMaxTokens accepts a recomputed max_tokens only when it is usable
// (>= the floor) and actually smaller than the current request.
func clampRetryMaxTokens(newMax, currentMax int) (int, bool) {
	if newMax < contextOverflowMinRetryTokens {
		return 0, false
	}
	if currentMax > 0 && newMax >= currentMax {
		return 0, false
	}
	return newMax, true
}

// windowFromContextLengthError returns the model's true context window parsed
// from a total-context overflow error (0 when unavailable — including the
// output-cap shape, whose number is an output limit, not a window). Feeds the
// self-calibration observer so later areas / runs budget against the real
// window.
func windowFromContextLengthError(err error) int {
	if err == nil {
		return 0
	}
	return contextWindowFromError(err.Error())
}
