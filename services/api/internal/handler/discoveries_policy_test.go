package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/policy"
	"github.com/decisionbox-io/decisionbox/services/api/internal/runner"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// quietRunner is a minimal runner.Runner that succeeds without spawning
// anything. Used when we just want to exercise the policy hook path.
type quietRunner struct{}

func (quietRunner) Run(_ context.Context, _ runner.RunOptions) error { return nil }
func (quietRunner) RunSync(_ context.Context, _ runner.RunSyncOptions) (*runner.RunSyncResult, error) {
	return &runner.RunSyncResult{}, nil
}
func (quietRunner) Cancel(_ context.Context, _ string) error { return nil }

// failingRunner simulates a runner that cannot spawn the agent.
type failingRunner struct{ err error }

func (f failingRunner) Run(_ context.Context, _ runner.RunOptions) error { return f.err }
func (f failingRunner) RunSync(_ context.Context, _ runner.RunSyncOptions) (*runner.RunSyncResult, error) {
	return nil, f.err
}
func (failingRunner) Cancel(_ context.Context, _ string) error { return nil }

func newTriggerRequest(projectID string) *http.Request {
	req := httptest.NewRequest("POST", "/api/v1/projects/"+projectID+"/discover", strings.NewReader(`{}`))
	req.SetPathValue("id", projectID)
	return req
}

func TestDiscoveriesHandler_Trigger_PolicyDeniesStart(t *testing.T) {
	stub := &stubChecker{
		startRunErr: &policy.PolicyError{
			Kind: "limit", Limit: "concurrent_runs_per_project", Current: 2, Max: 2, PlanID: "pro_t1",
		},
	}
	swapChecker(t, stub)

	projRepo := newMockProjectRepo()
	projRepo.projects["p1"] = &models.Project{ID: "p1"}
	runRepo := newMockRunRepo()
	discRepo := newMockDiscoveryRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, quietRunner{})

	req := newTriggerRequest("p1")
	w := httptest.NewRecorder()
	h.TriggerDiscovery(w, req)

	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", w.Code)
	}
	// Run record was created but immediately marked failed with policy-denied.
	runRepo.mu.Lock()
	defer runRepo.mu.Unlock()
	if len(runRepo.runs) != 1 {
		t.Fatalf("runs = %d", len(runRepo.runs))
	}
	for _, r := range runRepo.runs {
		if r.Status != "failed" {
			t.Errorf("policy-denied run status = %q, want failed", r.Status)
		}
	}
}

func TestDiscoveriesHandler_Trigger_RunnerFailure_ReleasesReservation(t *testing.T) {
	stub := &stubChecker{}
	swapChecker(t, stub)

	projRepo := newMockProjectRepo()
	projRepo.projects["p1"] = &models.Project{ID: "p1"}
	runRepo := newMockRunRepo()
	discRepo := newMockDiscoveryRepo()

	spawn := &spawnFailError{msg: "spawn failed"}
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, failingRunner{err: spawn})

	req := newTriggerRequest("p1")
	w := httptest.NewRecorder()
	h.TriggerDiscovery(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.releases) != 1 {
		t.Errorf("want 1 Release call for the discovery-run reservation, got %v", stub.releases)
	}
}

func TestDiscoveriesHandler_CancelRun_ConfirmsReservation(t *testing.T) {
	stub := &stubChecker{}
	swapChecker(t, stub)

	projRepo := newMockProjectRepo()
	runRepo := newMockRunRepo()
	runRepo.addRun(&models.DiscoveryRun{
		ID:                  "run-42",
		ProjectID:           "p1",
		Status:              "running",
		PolicyReservationID: "res-run-42",
	})
	discRepo := newMockDiscoveryRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, quietRunner{})

	req := httptest.NewRequest("DELETE", "/api/v1/runs/run-42", nil)
	req.SetPathValue("runId", "run-42")
	w := httptest.NewRecorder()
	h.CancelRun(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.confirms) != 1 {
		t.Fatalf("want 1 Confirm, got %d", len(stub.confirms))
	}
	if stub.confirms[0].Status != "cancelled" {
		t.Errorf("Confirm outcome status = %q", stub.confirms[0].Status)
	}
}

func TestDiscoveriesHandler_Trigger_LimitError_BodyIncludesStructuredFields(t *testing.T) {
	stub := &stubChecker{
		startRunErr: &policy.PolicyError{
			Kind: "limit", Limit: "discovery_runs_per_period", Current: 10, Max: 10, PlanID: "free",
		},
	}
	swapChecker(t, stub)

	projRepo := newMockProjectRepo()
	projRepo.projects["p1"] = &models.Project{ID: "p1"}
	runRepo := newMockRunRepo()
	discRepo := newMockDiscoveryRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, quietRunner{})

	req := newTriggerRequest("p1")
	w := httptest.NewRecorder()
	h.TriggerDiscovery(w, req)

	var resp APIResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data was not a map: %T", resp.Data)
	}
	if got := data["limit"]; got != "discovery_runs_per_period" {
		t.Errorf("body.limit = %v", got)
	}
	if got := data["plan_id"]; got != "free" {
		t.Errorf("body.plan_id = %v", got)
	}
}

type spawnFailError struct{ msg string }

func (e *spawnFailError) Error() string { return e.msg }
