package ai

import "testing"

// Truncated has to recognise both spellings. The Anthropic wire says
// "max_tokens"; the OpenAI-compatible wire — including the managed gateway,
// which translates for it — says "length". A caller that only knew one would
// treat the other's truncation as a model that wrote nonsense.
func TestChatResultTruncated(t *testing.T) {
	cases := []struct {
		stop string
		want bool
	}{
		{"max_tokens", true},
		{"length", true},
		{"MAX_TOKENS", true},
		{"  length\t", true}, // surrounding whitespace is trimmed
		{"end_turn", false},
		{"stop", false},
		{"tool_use", false},
		{"refusal", false},
		{"", false},
		{"max_tokens_ish", false},
	}
	for _, c := range cases {
		r := &ChatResult{StopReason: c.stop}
		if got := r.Truncated(); got != c.want {
			t.Errorf("StopReason %q: Truncated() = %v, want %v", c.stop, got, c.want)
		}
	}
	// A nil result is not a truncation — callers reach this on an error path.
	var nilResult *ChatResult
	if nilResult.Truncated() {
		t.Error("a nil ChatResult reported truncation")
	}
}
