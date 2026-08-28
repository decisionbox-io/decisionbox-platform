package ai

import (
	"regexp"
	"strconv"
	"strings"
)

// Adaptive context-overflow handling.
//
// Some models reject a request outright when input + requested output exceeds
// their context window, e.g. Bedrock/GLM:
//
//	ValidationException: "This model's maximum context length is 202752 tokens.
//	However, you requested 64000 output tokens and your prompt contains at least
//	138753 input tokens, for a total of at least 202753 tokens. Please reduce the
//	length of the input prompt or the number of requested output tokens."
//
// or OpenAI:
//
//	"This model's maximum context length is 8192 tokens. However, you requested
//	9000 tokens (5000 in the messages, 4000 in the completion). Please reduce ..."
//
// The proactive per-call output budget (discovery.budgetedMaxOutputTokens)
// prevents this in the common case, but the window it budgets against is an
// estimate for uncatalogued models. This layer is the exact net: on such a 400
// the error itself states the model's true window and input size, so a single
// re-issue with a correctly-recomputed max_tokens fits. It is deterministic
// (not a transient backoff), bounded to one extra call, and leaves every other
// 4xx untouched.

// contextOverflowMarkers identify a context-length 400 across providers. Match
// is a case-insensitive substring scan — gollm does not surface typed HTTP
// errors at the interface boundary (same rationale as retry.go's status-code
// substring matching).
var contextOverflowMarkers = []string{
	"maximum context length",
	"context_length_exceeded",
	"reduce the length of the input prompt or the number of requested output",
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
)

// isContextLengthError reports whether err looks like a context-window overflow
// rejection from any provider.
func isContextLengthError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	for _, m := range contextOverflowMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// parseContextLengthError extracts the model's true context window and input
// token count from an overflow error string. Handles the Bedrock/GLM shape
// ("... prompt contains at least N input tokens ...") and the OpenAI shape
// ("... N in the messages ..."). Returns ok=false when either number is
// missing so the caller falls back to a conservative reduction instead of a
// bogus computed value.
func parseContextLengthError(msg string) (window, input int, ok bool) {
	lower := strings.ToLower(msg)

	wm := reContextWindow.FindStringSubmatch(lower)
	if len(wm) < 2 {
		return 0, 0, false
	}
	window, err := strconv.Atoi(wm[1])
	if err != nil || window <= 0 {
		return 0, 0, false
	}

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

// reducedMaxTokensForContextOverflow computes a smaller max_tokens to retry an
// overflow with, or ok=false when this is not an overflow error, or when
// reducing output alone cannot help (the input alone already fills the window),
// or when the computed value would not actually shrink the request. When the
// numbers parse it returns window − input − margin (exact); when the error is a
// recognised overflow whose numbers do not parse it falls back to a single
// halving of the current cap.
func reducedMaxTokensForContextOverflow(currentMax int, err error) (int, bool) {
	if !isContextLengthError(err) {
		return 0, false
	}
	if window, input, ok := parseContextLengthError(err.Error()); ok {
		margin := window * contextOverflowRetryMarginPct / 100
		newMax := window - input - margin
		if newMax < contextOverflowMinRetryTokens {
			return 0, false // input alone overfills the window; output reduction can't save it
		}
		if currentMax > 0 && newMax >= currentMax {
			return 0, false // would not actually reduce; avoid a pointless re-issue / loop
		}
		return newMax, true
	}
	// Recognised overflow, unparseable numbers → one blind halving.
	newMax := currentMax / 2
	if newMax < contextOverflowMinRetryTokens {
		return 0, false
	}
	return newMax, true
}

// windowFromContextLengthError returns the model's true context window parsed
// from an overflow error (0 when unavailable). Used to feed the self-
// calibration observer so later areas / runs budget against the real window.
func windowFromContextLengthError(err error) int {
	if !isContextLengthError(err) {
		return 0
	}
	if window, _, ok := parseContextLengthError(err.Error()); ok {
		return window
	}
	return 0
}
