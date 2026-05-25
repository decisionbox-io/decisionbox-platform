package verifier

import (
	"context"
	"strings"
	"testing"
)

// Codex prod-r3 P1 — when the manual validate-doc agent runs
// without a SchemaProvider wired (Qdrant not configured for the
// project), the executor used to nil-deref on the first
// lookup_schema action and panic the whole agent. The guard now
// surfaces a tool-error string the model can read, which routes
// through the verifier's existing recent_tool_errors handling
// rather than crashing.
func TestDefaultExecutor_LookupSchema_NilProviderReturnsToolError(t *testing.T) {
	e := &DefaultExecutor{
		SchemaProvider: nil,
	}
	_, err := e.LookupSchema(context.Background(), []string{"orders"})
	if err == nil {
		t.Fatalf("LookupSchema with nil SchemaProvider must return an error, not panic")
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Errorf("error should explain the lookup_schema is unavailable, got %q", err.Error())
	}
}
