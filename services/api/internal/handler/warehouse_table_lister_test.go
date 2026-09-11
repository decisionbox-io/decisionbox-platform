package handler

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/internal/runner"
)

// stubListRunner embeds mockRunner (which satisfies runner.Runner) and overrides
// RunSync so the adapter test can drive the agent's stdout + exit behaviour.
type stubListRunner struct {
	*mockRunner
	out []byte
	err error
}

func (r *stubListRunner) RunSync(_ context.Context, _ runner.RunSyncOptions) (*runner.RunSyncResult, error) {
	return &runner.RunSyncResult{Output: r.out, Error: "boom"}, r.err
}

func TestAgentTableLister_ParsesTables(t *testing.T) {
	l := NewAgentTableLister(&stubListRunner{
		mockRunner: newMockRunner(),
		out:        []byte(`{"success":true,"tables":["dbo.orders","dbo.customers"]}`),
	})
	got, err := l.ListWarehouseTables(context.Background(), "p1", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0] != "dbo.orders" || got[1] != "dbo.customers" {
		t.Errorf("tables = %v, want [dbo.orders dbo.customers]", got)
	}
}

func TestAgentTableLister_AgentExitError_UsesJSONError(t *testing.T) {
	l := NewAgentTableLister(&stubListRunner{
		mockRunner: newMockRunner(),
		out:        []byte(`{"success":false,"error":"credentials not read-only"}`),
		err:        errors.New("exit status 1"),
	})
	_, err := l.ListWarehouseTables(context.Background(), "p1", "")
	if err == nil || !strings.Contains(err.Error(), "credentials not read-only") {
		t.Errorf("err = %v, want the agent's JSON error message", err)
	}
}

func TestAgentTableLister_SuccessFalse_NoExitError(t *testing.T) {
	l := NewAgentTableLister(&stubListRunner{
		mockRunner: newMockRunner(),
		out:        []byte(`{"success":false,"error":"no warehouse configured"}`),
	})
	_, err := l.ListWarehouseTables(context.Background(), "p1", "wh_x")
	if err == nil || !strings.Contains(err.Error(), "no warehouse configured") {
		t.Errorf("err = %v, want the success=false error surfaced", err)
	}
}

func TestNewAgentTableLister_NilRunner(t *testing.T) {
	if NewAgentTableLister(nil) != nil {
		t.Error("NewAgentTableLister(nil) must return nil so the handler skips the live fallback")
	}
}
