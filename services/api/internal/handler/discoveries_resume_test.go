package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/policy"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/internal/discoverytrigger"
	"github.com/decisionbox-io/decisionbox/services/api/internal/runner"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// --- fakes -----------------------------------------------------------------

// mockCheckpointRepo answers "is there anything to resume from" and records
// the purges the cancel path issues.
type mockCheckpointRepo struct {
	mu sync.Mutex

	prefixLen           int
	explorationComplete bool
	stateErr            error

	deleted   []string
	deleteErr error
}

func (m *mockCheckpointRepo) ResumeState(_ context.Context, _ string) (int, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stateErr != nil {
		return 0, false, m.stateErr
	}
	return m.prefixLen, m.explorationComplete, nil
}

func (m *mockCheckpointRepo) DeleteByRun(_ context.Context, runID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteErr != nil {
		return 0, m.deleteErr
	}
	m.deleted = append(m.deleted, runID)
	return 2, nil
}

// recordingRunner captures the RunOptions the handler built, which is how the
// test asserts the resumed agent was given the run's OWN parameters rather
// than the current defaults.
type recordingRunner struct {
	mu   sync.Mutex
	opts []runner.RunOptions
	err  error
}

func (r *recordingRunner) Run(_ context.Context, o runner.RunOptions) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opts = append(r.opts, o)
	return r.err
}
func (r *recordingRunner) RunSync(context.Context, runner.RunSyncOptions) (*runner.RunSyncResult, error) {
	return &runner.RunSyncResult{}, nil
}
func (r *recordingRunner) Cancel(context.Context, string) error { return nil }
func (r *recordingRunner) RunIndexSchema(context.Context, runner.IndexSchemaOptions) error {
	return nil
}
func (r *recordingRunner) RunValidateDoc(context.Context, runner.ValidateDocOptions) error {
	return nil
}

func (r *recordingRunner) calls() []runner.RunOptions {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]runner.RunOptions, len(r.opts))
	copy(out, r.opts)
	return out
}

// meteringChecker is a stubChecker that ALSO implements OperationCharger, so
// a test can prove the resume path never meters. Without the extension the
// ChargeIfMetered / RefundIfMetered helpers no-op and the assertion would
// pass for the wrong reason.
type meteringChecker struct {
	stubChecker

	cmu          sync.Mutex
	charges      []policy.Operation
	refunds      []string
	reservations int
	// confirmErr makes ConfirmDiscoveryRunEnded fail, which is what a
	// control plane that cannot be reached looks like.
	confirmErr error
}

func (m *meteringChecker) ConfirmDiscoveryRunEnded(ctx context.Context, id string, outcome policy.RunOutcome) error {
	if m.confirmErr != nil {
		return m.confirmErr
	}
	return m.stubChecker.ConfirmDiscoveryRunEnded(ctx, id, outcome)
}

// CheckStartDiscoveryRun counts reservations so the test can assert resume
// opens none — one would consume another runs-per-period slot.
func (m *meteringChecker) CheckStartDiscoveryRun(ctx context.Context, dep, proj, run string) (*policy.Reservation, error) {
	m.cmu.Lock()
	m.reservations++
	m.cmu.Unlock()
	return m.stubChecker.CheckStartDiscoveryRun(ctx, dep, proj, run)
}

func (m *meteringChecker) reservationCount() int {
	m.cmu.Lock()
	defer m.cmu.Unlock()
	return m.reservations
}

func (m *meteringChecker) ChargeOperation(_ context.Context, _ string, op policy.Operation) (*policy.OperationCharge, error) {
	m.cmu.Lock()
	defer m.cmu.Unlock()
	m.charges = append(m.charges, op)
	return &policy.OperationCharge{Charged: 1}, nil
}

func (m *meteringChecker) RefundOperation(_ context.Context, _, reference string) error {
	m.cmu.Lock()
	defer m.cmu.Unlock()
	m.refunds = append(m.refunds, reference)
	return nil
}

// --- harness ---------------------------------------------------------------

type resumeFixture struct {
	h      *DiscoveriesHandler
	runs   *mockRunRepo
	projs  *mockProjectRepo
	cps    *mockCheckpointRepo
	runner *recordingRunner
}

// newResumeFixture wires a project that passes every gate and one failed run
// with a resumable checkpoint — the happy path each test then perturbs.
func newResumeFixture(t *testing.T) *resumeFixture {
	t.Helper()
	projs := newMockProjectRepo()
	projs.projects["p1"] = &models.Project{ID: "p1", SchemaIndexStatus: models.SchemaIndexStatusReady}

	runs := newMockRunRepo()
	runs.runs["run-1"] = &models.DiscoveryRun{
		ID: "run-1", ProjectID: "p1", Status: "failed",
		Attempt: 1, LastCheckpointStep: 42,
		MaxSteps: 80, MinSteps: 48, Areas: []string{"churn", "monetization"}, Effort: "high",
	}

	cps := &mockCheckpointRepo{prefixLen: 42}
	rr := &recordingRunner{}
	h := NewDiscoveriesHandler(newMockDiscoveryRepo(), projs, runs, nil, nil, nil, rr).
		WithCheckpoints(cps)
	return &resumeFixture{h: h, runs: runs, projs: projs, cps: cps, runner: rr}
}

func (f *resumeFixture) post(runID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/v1/runs/"+runID+"/resume", nil)
	req.SetPathValue("runId", runID)
	w := httptest.NewRecorder()
	f.h.ResumeRun(w, req)
	return w
}

// --- happy path ------------------------------------------------------------

// TestResumeRun_ReplaysTheRunsOwnParameters is the §2.8 fix in action. A
// resumed run spawned with the agent's defaults would silently change its own
// step budget halfway through, and nothing recorded those parameters before.
func TestResumeRun_ReplaysTheRunsOwnParameters(t *testing.T) {
	f := newResumeFixture(t)

	w := f.post("run-1")

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}
	// Responses ride in the API's standard {"data": ...} envelope.
	var envelope struct {
		Data struct {
			Status  string `json:"status"`
			RunID   string `json:"run_id"`
			Attempt int    `json:"attempt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	body := envelope.Data
	if body.Status != "resumed" || body.RunID != "run-1" {
		t.Errorf("body = %+v, want status=resumed run_id=run-1", body)
	}
	if body.Attempt != 2 {
		t.Errorf("attempt = %d, want 2", body.Attempt)
	}

	calls := f.runner.calls()
	if len(calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(calls))
	}
	got := calls[0]
	if !got.Resume {
		t.Error("the agent must be told to resume, not to start fresh")
	}
	if got.Attempt != 2 {
		t.Errorf("Attempt = %d, want 2 — the K8s Job name depends on it", got.Attempt)
	}
	if got.RunID != "run-1" || got.ProjectID != "p1" {
		t.Errorf("identity = (%q, %q), want (run-1, p1)", got.RunID, got.ProjectID)
	}
	if got.MaxSteps != 80 || got.MinSteps != 48 {
		t.Errorf("step budget = (%d, %d), want the run's own (80, 48), not the defaults",
			got.MaxSteps, got.MinSteps)
	}
	if len(got.Areas) != 2 || got.Areas[0] != "churn" || got.Areas[1] != "monetization" {
		t.Errorf("Areas = %v, want the run's own selection", got.Areas)
	}
}

// TestResumeRun_WorksWithOnlyTheExplorationSummary covers the second
// acceptance criterion's precondition: a run that died AFTER exploration
// finished is resumable even with no replayable step prefix, because it is
// going straight to analysis.
func TestResumeRun_WorksWithOnlyTheExplorationSummary(t *testing.T) {
	f := newResumeFixture(t)
	f.cps.prefixLen = 0
	f.cps.explorationComplete = true

	if w := f.post("run-1"); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}
}

// --- the refusals ----------------------------------------------------------

func TestResumeRun_UnknownRunIs404(t *testing.T) {
	f := newResumeFixture(t)
	if w := f.post("nope"); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
	if n := len(f.runner.calls()); n != 0 {
		t.Errorf("spawned %d agents for an unknown run", n)
	}
}

// TestResumeRun_OnlyFailedRunsAreResumable pins the status gate for every
// other state, and that the refusal names the actual status rather than
// leaving a stale dashboard's user guessing. `cancelled` is in here
// deliberately: cancel is a hard kill and stays terminal.
func TestResumeRun_OnlyFailedRunsAreResumable(t *testing.T) {
	for _, status := range []string{"pending", "running", "completed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			f := newResumeFixture(t)
			f.runs.runs["run-1"].Status = status

			w := f.post("run-1")

			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409", w.Code)
			}
			if !strings.Contains(w.Body.String(), status) {
				t.Errorf("the refusal must name the run's actual status; body = %s", w.Body.String())
			}
			if n := len(f.runner.calls()); n != 0 {
				t.Errorf("spawned %d agents for a %s run", n, status)
			}
		})
	}
}

// TestResumeRun_NoCheckpointIs409 pins that "nothing to resume from" is a
// real answer rather than a server error: checkpoints are bounded by their
// retention, and a run that died before its first step never wrote one.
func TestResumeRun_NoCheckpointIs409(t *testing.T) {
	f := newResumeFixture(t)
	f.cps.prefixLen = 0
	f.cps.explorationComplete = false

	w := f.post("run-1")

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "expired") {
		t.Errorf("the refusal should explain WHY there is nothing to resume; body = %s", w.Body.String())
	}
	if n := len(f.runner.calls()); n != 0 {
		t.Errorf("spawned %d agents with no checkpoint", n)
	}
}

// TestResumeRun_NoCheckpointRepoIs409 covers a deployment built without the
// checkpoint collection. Refuse clearly rather than panicking on a nil repo.
func TestResumeRun_NoCheckpointRepoIs409(t *testing.T) {
	f := newResumeFixture(t)
	f.h = NewDiscoveriesHandler(newMockDiscoveryRepo(), f.projs, f.runs, nil, nil, nil, f.runner)

	if w := f.post("run-1"); w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

// TestResumeRun_AppliesTheSameProjectGatesAsAFreshRun is the gate that
// matters most. A resume re-enters exploration — it queries the warehouse and
// reads the schema index exactly as a fresh run does — so a resume path that
// skipped these checks would be a way to run discovery against a project the
// normal route refuses.
func TestResumeRun_AppliesTheSameProjectGatesAsAFreshRun(t *testing.T) {
	cases := map[string]func(p *models.Project){
		"schema index not ready": func(p *models.Project) {
			p.SchemaIndexStatus = models.SchemaIndexStatusPendingIndexing
		},
		"schema index failed": func(p *models.Project) {
			p.SchemaIndexStatus = models.SchemaIndexStatusFailed
		},
		"schema cache cleared": func(p *models.Project) {
			p.SchemaIndexStatus = models.SchemaIndexStatusNeedsReindex
		},
		"never indexed": func(p *models.Project) {
			p.SchemaIndexStatus = ""
		},
		"project state owned by a plugin": func(p *models.Project) {
			p.State = "provisioning"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newResumeFixture(t)
			mutate(f.projs.projects["p1"])

			w := f.post("run-1")

			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body = %s", w.Code, w.Body.String())
			}
			if n := len(f.runner.calls()); n != 0 {
				t.Errorf("spawned %d agents past a project gate", n)
			}
			// And the run must be left resumable — a refused resume costs
			// nothing.
			if got := f.runs.runs["run-1"].Status; got != "failed" {
				t.Errorf("run status = %q, want it left failed and resumable", got)
			}
		})
	}
}

// TestResumeRun_AnotherActiveRunIs409 pins the concurrency bound. This is the
// ONLY thing bounding per-project concurrency on the resume path, because
// resume deliberately opens no policy reservation — so it is applied
// unconditionally rather than only under the self-hosted checker.
func TestResumeRun_AnotherActiveRunIs409(t *testing.T) {
	f := newResumeFixture(t)
	f.runs.runs["run-2"] = &models.DiscoveryRun{ID: "run-2", ProjectID: "p1", Status: "running"}

	w := f.post("run-1")

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
	// In the standard error envelope, not under `data`: the dashboard's
	// request helper reads only the top-level `error` on a non-2xx, so a body
	// under `data` would surface as a bare "API error: 409".
	var envelope struct {
		Error string `json:"error"`
		Data  any    `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error == "" {
		t.Errorf("the refusal must ride in the top-level error field; body = %s", w.Body.String())
	}
	if !strings.Contains(envelope.Error, "run-2") {
		t.Errorf("the refusal should name the run that is in the way; got %q", envelope.Error)
	}
	if n := len(f.runner.calls()); n != 0 {
		t.Errorf("spawned %d agents alongside a running one", n)
	}
}

// TestResumeRun_DoubleClickSpawnsExactlyOneAgent is the race this endpoint's
// atomic flip exists for. Two requests, two agents on one run id would mean
// two processes writing one run's checkpoints and results.
func TestResumeRun_DoubleClickSpawnsExactlyOneAgent(t *testing.T) {
	f := newResumeFixture(t)

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = f.post("run-1").Code
		}(i)
	}
	wg.Wait()

	accepted, conflicted := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusAccepted:
			accepted++
		case http.StatusConflict:
			conflicted++
		default:
			t.Errorf("unexpected status %d", c)
		}
	}
	if accepted != 1 || conflicted != 1 {
		t.Errorf("got %d accepted and %d conflicted, want exactly one of each", accepted, conflicted)
	}
	if n := len(f.runner.calls()); n != 1 {
		t.Errorf("spawned %d agents, want exactly 1", n)
	}
}

// TestResumeRun_LostFlipIs409 is the same guarantee at the repository
// boundary: BeginResume matching nothing is a conflict, not a 500.
func TestResumeRun_LostFlipIs409(t *testing.T) {
	f := newResumeFixture(t)
	f.runs.beginResumeErr = database.ErrNoResumableRun

	if w := f.post("run-1"); w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

// TestResumeRun_SpawnFailureLeavesTheRunResumable pins that a failed spawn
// costs nothing but the attempt counter. The checkpoints are untouched, so
// the operator can simply try again.
func TestResumeRun_SpawnFailureLeavesTheRunResumable(t *testing.T) {
	f := newResumeFixture(t)
	f.runner.err = errors.New("no capacity")

	w := f.post("run-1")

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", w.Code, w.Body.String())
	}
	if got := f.runs.runs["run-1"].Status; got != "failed" {
		t.Errorf("run status = %q, want failed so it is resumable again", got)
	}
	if len(f.cps.deleted) != 0 {
		t.Errorf("a failed spawn must NOT discard the checkpoints, got %v", f.cps.deleted)
	}
}

// TestResumeRun_FailureCallbackIsScopedToItsOwnAttempt pins the P1 race at the
// handler boundary: the callback the resumed run registers must name THIS
// attempt, so that when it fires late — after another resume has superseded it
// — it cannot mark the live attempt failed.
func TestResumeRun_FailureCallbackIsScopedToItsOwnAttempt(t *testing.T) {
	f := newResumeFixture(t)

	if w := f.post("run-1"); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}
	calls := f.runner.calls()
	if len(calls) != 1 || calls[0].OnFailure == nil {
		t.Fatal("the resumed run registered no failure callback")
	}
	onFailure := calls[0].OnFailure

	// Another resume supersedes attempt 2. (Marking it failed first is what
	// an operator would be reacting to.)
	f.runs.runs["run-1"].Status = "failed"
	if w := f.post("run-1"); w.Code != http.StatusAccepted {
		t.Fatalf("second resume: status = %d; body = %s", w.Code, w.Body.String())
	}
	if got := f.runs.runs["run-1"].Attempt; got != 3 {
		t.Fatalf("attempt = %d, want 3", got)
	}

	// Attempt 2's watcher finally fires.
	onFailure("run-1", "job failed (observed late)")

	if got := f.runs.runs["run-1"].Status; got != "running" {
		t.Errorf("run status = %q, want running — attempt 2's stale callback killed the live attempt 3", got)
	}
	if got := f.runs.runs["run-1"].Error; got != "" {
		t.Errorf("run error = %q, want it untouched by the stale callback", got)
	}

	// And the LIVE attempt's own callback still works.
	live := f.runner.calls()[1].OnFailure
	live("run-1", "attempt 3 died")
	if got := f.runs.runs["run-1"].Status; got != "failed" {
		t.Errorf("run status = %q, want failed — the live attempt's callback must apply", got)
	}
}

// TestStartRun_FailureCallbackIsScopedToAttemptOne is the same guarantee for a
// fresh run: its watcher must not be able to kill the first resume.
func TestStartRun_FailureCallbackIsScopedToAttemptOne(t *testing.T) {
	projs := newMockProjectRepo()
	projs.projects["p1"] = &models.Project{ID: "p1", SchemaIndexStatus: models.SchemaIndexStatusReady}
	runs := newMockRunRepo()
	rr := &recordingRunner{}
	cps := &mockCheckpointRepo{prefixLen: 12}
	h := NewDiscoveriesHandler(newMockDiscoveryRepo(), projs, runs, nil, nil, nil, rr).WithCheckpoints(cps)

	res, err := h.StartRun(context.Background(), discoveryTriggerOptions("p1", 50, nil, nil))
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	onFailure := rr.calls()[0].OnFailure
	if onFailure == nil {
		t.Fatal("StartRun registered no failure callback")
	}

	// It fails, then is resumed.
	runs.runs[res.RunID].Status = "failed"
	req := httptest.NewRequest("POST", "/api/v1/runs/"+res.RunID+"/resume", nil)
	req.SetPathValue("runId", res.RunID)
	w := httptest.NewRecorder()
	h.ResumeRun(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("resume: status = %d; body = %s", w.Code, w.Body.String())
	}

	// Attempt 1's watcher fires late.
	onFailure(res.RunID, "job failed (observed late)")

	if got := runs.runs[res.RunID].Status; got != "running" {
		t.Errorf("run status = %q, want running — attempt 1's stale callback killed the resumed attempt", got)
	}
}

// --- the money question ----------------------------------------------------

// TestResumeRun_NeverMetersOrReserves is the "no double-charge, no
// refund-then-free-resume leak" acceptance criterion, asserted against a
// checker that actually implements metering — so the absence of a charge is
// a real absence rather than a no-op helper.
//
// The run's charge is keyed on its run id and resume re-enters the same id,
// so resume is free by construction. A new CheckStartDiscoveryRun reservation
// would consume another runs-per-period slot, which is a hidden charge for
// work already paid for.
func TestResumeRun_NeverMetersOrReserves(t *testing.T) {
	ck := &meteringChecker{}
	swapChecker(t, ck)

	f := newResumeFixture(t)

	if w := f.post("run-1"); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}

	if n := len(ck.charges); n != 0 {
		t.Errorf("resume metered %d operations (%+v), want 0 — the run's charge is keyed on its run id", n, ck.charges)
	}
	if n := len(ck.refunds); n != 0 {
		t.Errorf("resume issued %d refunds (%v), want 0 — a refund here would be the leak that makes the next resume free", n, ck.refunds)
	}
	if n := ck.reservationCount(); n != 0 {
		t.Errorf("resume opened %d policy reservations, want 0 — one would consume another runs-per-period slot", n)
	}
}

// TestResumeRun_EndsTheSupersededAttemptsReservation pins the bookkeeping that
// makes "resume opens no reservation" coherent.
//
// The previous attempt's reservation is still on the document. Resume opens
// none of its own, so leaving it would have the post-completion confirmer
// report the RESUMED attempt's outcome against a reservation that attempt
// never made. It is confirmed (not released — the period counter stays
// consumed; the concurrent-runs counter is what must come down) and cleared.
func TestResumeRun_EndsTheSupersededAttemptsReservation(t *testing.T) {
	ck := &meteringChecker{}
	swapChecker(t, ck)

	f := newResumeFixture(t)
	f.runs.runs["run-1"].PolicyReservationID = "res-attempt-1"

	if w := f.post("run-1"); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}

	if len(ck.confirms) != 1 {
		t.Fatalf("confirms = %d, want 1 — the superseded attempt's reservation must be ended", len(ck.confirms))
	}
	if got := ck.confirms[0].Status; got != "failure" {
		t.Errorf("confirmed outcome = %q, want failure — the attempt did fail", got)
	}
	// Still no NEW reservation, which is the whole point.
	if n := ck.reservationCount(); n != 0 {
		t.Errorf("resume opened %d reservations, want 0", n)
	}
	if n := len(ck.charges); n != 0 {
		t.Errorf("resume metered %d operations, want 0", n)
	}
	// Cleared only AFTER the confirm landed.
	if got := f.runs.runs["run-1"].PolicyReservationID; got != "" {
		t.Errorf("reservation id = %q, want cleared once confirmed", got)
	}
}

// TestResumeRun_KeepsTheReservationIdWhenTheConfirmFails pins the ordering
// that matters more than the happy path: the id is the only handle anyone has
// on the reservation, so it must survive a failed confirm. Clearing first
// would turn any crash in between into a leaked concurrent-run slot with
// nothing left to reconcile from.
func TestResumeRun_KeepsTheReservationIdWhenTheConfirmFails(t *testing.T) {
	ck := &meteringChecker{}
	ck.confirmErr = errors.New("control plane unreachable")
	swapChecker(t, ck)

	f := newResumeFixture(t)
	f.runs.runs["run-1"].PolicyReservationID = "res-attempt-1"

	// The resume still goes ahead — refusing it over an accounting problem
	// would be the wrong trade.
	if w := f.post("run-1"); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}
	if got := f.runs.runs["run-1"].PolicyReservationID; got != "res-attempt-1" {
		t.Errorf("reservation id = %q, want it kept so the background confirmer can retry", got)
	}
}

// TestResumeRun_NoReservationToEndIsSilent covers the self-hosted path and a
// run whose reservation was already released: nothing to confirm, and the
// resume must not invent one.
func TestResumeRun_NoReservationToEndIsSilent(t *testing.T) {
	ck := &meteringChecker{}
	swapChecker(t, ck)

	f := newResumeFixture(t) // no PolicyReservationID

	if w := f.post("run-1"); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}
	if len(ck.confirms) != 0 {
		t.Errorf("confirms = %d, want 0 — there was no reservation to end", len(ck.confirms))
	}
}

// TestResumeRun_SpawnFailureStillDoesNotRefund is the other half of the leak.
// The start path refunds on a spawn failure because nothing ran; here the
// run's earlier attempt DID run and was paid for, so a refund would hand back
// money for work that was done and leave the next resume free.
func TestResumeRun_SpawnFailureStillDoesNotRefund(t *testing.T) {
	ck := &meteringChecker{}
	swapChecker(t, ck)

	f := newResumeFixture(t)
	f.runner.err = errors.New("no capacity")

	f.post("run-1")

	if n := len(ck.refunds); n != 0 {
		t.Errorf("a failed resume refunded %v, want nothing — the work the run already did was real", ck.refunds)
	}
}

// --- cancel purges checkpoints --------------------------------------------

// TestCancelRun_DiscardsCheckpoints pins that cancellation stays terminal.
// Dropped eagerly rather than left to the retention TTL, both to reclaim the
// rows and so the agent's boot-time sweep stops treating the run as live and
// keeping its per-run vector collection alive.
func TestCancelRun_DiscardsCheckpoints(t *testing.T) {
	f := newResumeFixture(t)
	f.runs.runs["run-1"].Status = "running"

	req := httptest.NewRequest("DELETE", "/api/v1/runs/run-1", nil)
	req.SetPathValue("runId", "run-1")
	w := httptest.NewRecorder()
	f.h.CancelRun(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if len(f.cps.deleted) != 1 || f.cps.deleted[0] != "run-1" {
		t.Errorf("cancel deleted %v, want [run-1] — cancelled is terminal and not resumable", f.cps.deleted)
	}
}

// TestCancelRun_SurvivesACheckpointPurgeFailure pins that the purge is
// best-effort: the retention TTL is the backstop, and failing the cancel over
// it would leave a run the operator asked to kill still running.
func TestCancelRun_SurvivesACheckpointPurgeFailure(t *testing.T) {
	f := newResumeFixture(t)
	f.runs.runs["run-1"].Status = "running"
	f.cps.deleteErr = errors.New("mongo down")

	req := httptest.NewRequest("DELETE", "/api/v1/runs/run-1", nil)
	req.SetPathValue("runId", "run-1")
	w := httptest.NewRecorder()
	f.h.CancelRun(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 — a failed checkpoint purge must not fail the cancel", w.Code)
	}
	if got := f.runs.runs["run-1"].Status; got != "cancelled" {
		t.Errorf("run status = %q, want cancelled", got)
	}
}

// --- run creation records its own shape -----------------------------------

// discoveryTriggerOptions builds the trigger options a StartRun test needs.
func discoveryTriggerOptions(projectID string, maxSteps int, minSteps *int, areas []string) discoverytrigger.Options {
	return discoverytrigger.Options{
		ProjectID: projectID,
		MaxSteps:  maxSteps,
		MinSteps:  minSteps,
		Areas:     areas,
		Source:    "manual",
	}
}

// TestStartRun_PersistsTheRunsParameters is the prerequisite for everything
// above: without it there is nothing for a resume to replay.
func TestStartRun_PersistsTheRunsParameters(t *testing.T) {
	projs := newMockProjectRepo()
	projs.projects["p1"] = &models.Project{ID: "p1", SchemaIndexStatus: models.SchemaIndexStatusReady}
	runs := newMockRunRepo()
	h := NewDiscoveriesHandler(newMockDiscoveryRepo(), projs, runs, nil, nil, nil, quietRunner{})

	min := 30
	_, err := h.StartRun(context.Background(), discoveryTriggerOptions("p1", 50, &min, []string{"churn"}))
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	if len(runs.createdParams) != 1 {
		t.Fatalf("Create calls = %d, want 1", len(runs.createdParams))
	}
	got := runs.createdParams[0]
	if got.MaxSteps != 50 || got.MinSteps != 30 {
		t.Errorf("persisted budget = (%d, %d), want (50, 30)", got.MaxSteps, got.MinSteps)
	}
	if len(got.Areas) != 1 || got.Areas[0] != "churn" {
		t.Errorf("persisted areas = %v, want [churn]", got.Areas)
	}
	if got.Source == "" {
		t.Error("the trigger source must be recorded on the first lifecycle event")
	}
}

// TestStartRun_PersistsTheComputedMinStepsFloor pins that the resume replays
// the floor the run ACTUALLY used. The handler computes 60% of max_steps when
// the caller omits min_steps, and persisting the request's zero instead would
// silently drop the floor on every resumed run.
func TestStartRun_PersistsTheComputedMinStepsFloor(t *testing.T) {
	projs := newMockProjectRepo()
	projs.projects["p1"] = &models.Project{ID: "p1", SchemaIndexStatus: models.SchemaIndexStatusReady}
	runs := newMockRunRepo()
	h := NewDiscoveriesHandler(newMockDiscoveryRepo(), projs, runs, nil, nil, nil, quietRunner{})

	if _, err := h.StartRun(context.Background(), discoveryTriggerOptions("p1", 100, nil, nil)); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	if got := runs.createdParams[0].MinSteps; got != 60 {
		t.Errorf("persisted MinSteps = %d, want the computed floor of 60", got)
	}
}
