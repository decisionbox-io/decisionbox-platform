package llm

import "testing"

func TestIsLengthStopReason(t *testing.T) {
	// One row per wire this platform talks to, because a reason it fails to
	// recognise is a truncation a caller re-asks for identically.
	cases := []struct {
		name, reason string
		want         bool
	}{
		{"openai-compatible", "length", true},
		{"anthropic wire", "max_tokens", true},
		{"vertex gemini", "MAX_TOKENS", true},
		{"padded", "  length  ", true},
		{"mixed case", "Max_Tokens", true},
		{"normal turn", "end_turn", false},
		{"openai stop", "stop", false},
		{"tool use", "tool_use", false},
		{"safety", "SAFETY", false},
		{"empty", "", false},
		{"unknown", "something_else", false},
	}
	for _, c := range cases {
		if got := IsLengthStopReason(c.reason); got != c.want {
			t.Errorf("%s: IsLengthStopReason(%q) = %v, want %v", c.name, c.reason, got, c.want)
		}
	}
}

func TestIsToolStopReason(t *testing.T) {
	for _, r := range []string{"tool_use", "tool_calls", "TOOL_USE", " tool_use "} {
		if !isToolStopReason(r) {
			t.Errorf("isToolStopReason(%q) = false, want true", r)
		}
	}
	for _, r := range []string{"end_turn", "max_tokens", "length", "", "stop"} {
		if isToolStopReason(r) {
			t.Errorf("isToolStopReason(%q) = true, want false", r)
		}
	}
}
