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
	if len(got.Tables) != 2 || got.Tables[0] != "dbo.orders" || got.Tables[1] != "dbo.customers" {
		t.Errorf("tables = %v, want [dbo.orders dbo.customers]", got.Tables)
	}
	// An agent that doesn't report unlisted_datasets is taken at its word: the
	// field's absence is an older image, not evidence of a partial listing.
	if !got.Complete {
		t.Error("Complete = false for an agent that reported no unlisted datasets")
	}
}

func TestAgentTableLister_ReportsUnlistedDatasets(t *testing.T) {
	// The agent skips a dataset it cannot read and still reports success, so the
	// names it DID return are an incomplete catalogue. The caller has to be able
	// to tell.
	l := NewAgentTableLister(&stubListRunner{
		mockRunner: newMockRunner(),
		out:        []byte(`{"success":true,"tables":["public.orders"],"unlisted_datasets":["staging"]}`),
	})
	got, err := l.ListWarehouseTables(context.Background(), "p1", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Complete {
		t.Error("Complete = true despite a dataset the agent could not read")
	}
	if len(got.UnlistedDatasets) != 1 || got.UnlistedDatasets[0] != "staging" {
		t.Errorf("UnlistedDatasets = %v, want [staging]", got.UnlistedDatasets)
	}
	// The tables that DID list are still returned — a partial catalogue beats none.
	if len(got.Tables) != 1 || got.Tables[0] != "public.orders" {
		t.Errorf("tables = %v, want [public.orders]", got.Tables)
	}
}

func TestAgentTableLister_EmptyUnlistedDatasets_IsComplete(t *testing.T) {
	l := NewAgentTableLister(&stubListRunner{
		mockRunner: newMockRunner(),
		out:        []byte(`{"success":true,"tables":["public.orders"],"unlisted_datasets":[]}`),
	})
	got, err := l.ListWarehouseTables(context.Background(), "p1", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Complete {
		t.Error("Complete = false for an explicitly empty unlisted_datasets")
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
