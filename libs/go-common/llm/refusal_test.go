package llm

import (
	"errors"
	"strings"
	"testing"
)

func TestRefusalError(t *testing.T) {
	for _, stop := range []string{"", "end_turn", "tool_use", "max_tokens"} {
		if err := RefusalError(stop, "m"); err != nil {
			t.Errorf("stop %q: err = %v, want nil", stop, err)
		}
	}
	err := RefusalError(StopReasonRefusal, "claude-x")
	if !errors.Is(err, ErrModelRefused) || !strings.Contains(err.Error(), "claude-x") {
		t.Fatalf("err = %v, want ErrModelRefused naming the model", err)
	}
}
