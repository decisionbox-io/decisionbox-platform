package llm

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRefusalError(t *testing.T) {
	for _, stop := range []string{"", "end_turn", "tool_use", "max_tokens"} {
		if err := RefusalError(stop, "m", Usage{}); err != nil {
			t.Errorf("stop %q: err = %v, want nil", stop, err)
		}
	}
	usage := Usage{InputTokens: 120}
	err := fmt.Errorf("bedrock/anthropic: %w", RefusalError(StopReasonRefusal, "claude-x", usage))
	if !errors.Is(err, ErrModelRefused) || !strings.Contains(err.Error(), "claude-x") {
		t.Fatalf("err = %v, want ErrModelRefused naming the model", err)
	}
	// A refused call still bills its input; the usage survives wrapping.
	var re *RefusedError
	if !errors.As(err, &re) || re.Usage != usage {
		t.Fatalf("usage not carried: %+v", re)
	}
}
