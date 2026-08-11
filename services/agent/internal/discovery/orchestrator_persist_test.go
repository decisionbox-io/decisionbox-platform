package discovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// fakeDiscoveryLogPersister captures the arguments each Save* call sees so
// the test can assert that persistSplitLogs forwards every payload to the
// right method with the right project / discovery / run identifiers, and so
// that returning a synthetic error from one call does not stop subsequent
// ones (per the "log and continue" contract).
type fakeDiscoveryLogPersister struct {
	explorationCalls   int
	analysisCalls      int
	validationCalls    int
	recommendationCall int

	gotProjectIDs  []string
	gotDiscoveryID []string
	gotRunIDs      []string

	gotExploration  []models.ExplorationStep
	gotAnalysis     []models.AnalysisStep
	gotValidation   []models.ValidationResult
	gotRecommend    *models.RecommendationStep

	saveExplorationErr   error
	saveAnalysisErr      error
	saveValidationErr    error
	saveRecommendationErr error
}

func (f *fakeDiscoveryLogPersister) recordIDs(projectID, discoveryID, runID string) {
	f.gotProjectIDs = append(f.gotProjectIDs, projectID)
	f.gotDiscoveryID = append(f.gotDiscoveryID, discoveryID)
	f.gotRunIDs = append(f.gotRunIDs, runID)
}

func (f *fakeDiscoveryLogPersister) SaveExplorationSteps(_ context.Context, projectID, discoveryID, runID string, steps []models.ExplorationStep) error {
	f.explorationCalls++
	f.recordIDs(projectID, discoveryID, runID)
	f.gotExploration = steps
	return f.saveExplorationErr
}

func (f *fakeDiscoveryLogPersister) SaveAnalysisSteps(_ context.Context, projectID, discoveryID, runID string, steps []models.AnalysisStep) error {
	f.analysisCalls++
	f.recordIDs(projectID, discoveryID, runID)
	f.gotAnalysis = steps
	return f.saveAnalysisErr
}

func (f *fakeDiscoveryLogPersister) SaveValidationResults(_ context.Context, projectID, discoveryID, runID string, results []models.ValidationResult) error {
	f.validationCalls++
	f.recordIDs(projectID, discoveryID, runID)
	f.gotValidation = results
	return f.saveValidationErr
}

func (f *fakeDiscoveryLogPersister) SaveRecommendationLog(_ context.Context, projectID, discoveryID, runID string, step *models.RecommendationStep) error {
	f.recommendationCall++
	f.recordIDs(projectID, discoveryID, runID)
	f.gotRecommend = step
	return f.saveRecommendationErr
}

func TestPersistSplitLogs_ForwardsAllPayloads(t *testing.T) {
	fake := &fakeDiscoveryLogPersister{}
	o := &Orchestrator{
		projectID:        "proj-1",
		runID:            "run-1",
		discoveryLogRepo: fake,
	}

	exploration := []models.ExplorationStep{{Step: 1}, {Step: 2}}
	analysis := []models.AnalysisStep{{AreaID: "churn"}}
	validations := []models.ValidationResult{{InsightID: "i1"}}
	rec := &models.RecommendationStep{}

	o.persistSplitLogs(context.Background(), "disc-1", exploration, analysis, validations, rec)

	if fake.explorationCalls != 1 || fake.analysisCalls != 1 || fake.validationCalls != 1 || fake.recommendationCall != 1 {
		t.Fatalf("expected each Save* called exactly once; got E=%d A=%d V=%d R=%d",
			fake.explorationCalls, fake.analysisCalls, fake.validationCalls, fake.recommendationCall)
	}

	if len(fake.gotProjectIDs) != 4 {
		t.Fatalf("expected 4 forwarded calls, got %d", len(fake.gotProjectIDs))
	}
	for i, p := range fake.gotProjectIDs {
		if p != "proj-1" {
			t.Errorf("call %d project_id = %q, want proj-1", i, p)
		}
		if fake.gotDiscoveryID[i] != "disc-1" {
			t.Errorf("call %d discovery_id = %q, want disc-1", i, fake.gotDiscoveryID[i])
		}
		if fake.gotRunIDs[i] != "run-1" {
			t.Errorf("call %d run_id = %q, want run-1", i, fake.gotRunIDs[i])
		}
	}

	if len(fake.gotExploration) != 2 || fake.gotExploration[0].Step != 1 {
		t.Errorf("exploration payload not forwarded verbatim: %+v", fake.gotExploration)
	}
	if len(fake.gotAnalysis) != 1 || fake.gotAnalysis[0].AreaID != "churn" {
		t.Errorf("analysis payload not forwarded verbatim: %+v", fake.gotAnalysis)
	}
	if len(fake.gotValidation) != 1 || fake.gotValidation[0].InsightID != "i1" {
		t.Errorf("validation payload not forwarded verbatim: %+v", fake.gotValidation)
	}
	if fake.gotRecommend != rec {
		t.Errorf("recommendation pointer not forwarded verbatim")
	}
}

// Each persisted exploration step is tagged with the discovery's warehouse id
// (multi-warehouse), so downstream SQL-example validation / fine-tuning routes
// statements to the datasource the step queried. An already-tagged step is left
// untouched.
func TestPersistSplitLogs_StampsWarehouseIDOnExplorationSteps(t *testing.T) {
	fake := &fakeDiscoveryLogPersister{}
	o := &Orchestrator{projectID: "p", runID: "r", warehouseID: "wh_b", discoveryLogRepo: fake}

	o.persistSplitLogs(context.Background(), "d",
		[]models.ExplorationStep{{Step: 1}, {Step: 2, WarehouseID: "wh_other"}}, nil, nil, nil)

	if len(fake.gotExploration) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(fake.gotExploration))
	}
	if fake.gotExploration[0].WarehouseID != "wh_b" {
		t.Errorf("untagged step got warehouse_id %q, want wh_b", fake.gotExploration[0].WarehouseID)
	}
	if fake.gotExploration[1].WarehouseID != "wh_other" {
		t.Errorf("pre-tagged step must be preserved, got %q", fake.gotExploration[1].WarehouseID)
	}
}

func TestPersistSplitLogs_NilRepoIsNoOp(t *testing.T) {
	// No persister wired and no payloads — must not panic and must do
	// nothing observable. Mirrors the production path where unit-test
	// orchestrators don't bring up MongoDB.
	o := &Orchestrator{projectID: "p", runID: "r"}
	o.persistSplitLogs(context.Background(), "d", nil, nil, nil, nil)
}

func TestPersistSplitLogs_ContinuesAfterEachError(t *testing.T) {
	// Per the contract documented on persistSplitLogs: a Save* failure is
	// logged and swallowed, and the next Save* still runs. We assert that
	// every Save* method got called even when each one returns its own
	// synthetic error — the parent DiscoveryResult is already saved by
	// the time we get here, so dropping later log writes on the floor
	// would silently lose telemetry.
	fake := &fakeDiscoveryLogPersister{
		saveExplorationErr:    errors.New("boom-exploration"),
		saveAnalysisErr:       errors.New("boom-analysis"),
		saveValidationErr:     errors.New("boom-validation"),
		saveRecommendationErr: errors.New("boom-recommendation"),
	}
	o := &Orchestrator{
		projectID:        "p",
		runID:            "r",
		discoveryLogRepo: fake,
	}

	o.persistSplitLogs(context.Background(), "d", nil, nil, nil, nil)

	if fake.explorationCalls != 1 || fake.analysisCalls != 1 || fake.validationCalls != 1 || fake.recommendationCall != 1 {
		t.Fatalf("each Save* must run exactly once even when prior ones error; got E=%d A=%d V=%d R=%d",
			fake.explorationCalls, fake.analysisCalls, fake.validationCalls, fake.recommendationCall)
	}
}

// Compile-time assertions — the production *database.DiscoveryLogRepository
// and the test fake both have to satisfy discoveryLogPersister. If a method
// signature ever drifts (e.g. adds an arg to SaveExplorationSteps), this
// catches it before production wiring breaks.
var (
	_ discoveryLogPersister = (*database.DiscoveryLogRepository)(nil)
	_ discoveryLogPersister = (*fakeDiscoveryLogPersister)(nil)
)

// ctxKey is a typed key for TestPersistContext_PreservesValues. Using
// an unexported type keeps the test self-contained and matches the
// idiomatic pattern that prevents context-key collisions.
type ctxKey struct{ name string }

// fakeRunFinalizer records which terminal method finalizeStatus
// routed to so the regression test can assert the Complete-vs-Fail
// decision without bringing up MongoDB.
type fakeRunFinalizer struct {
	completedID     string
	completedCount  int
	completedCalled bool
	failedID        string
	failedMsg       string
	failedCalled    bool
}

func (f *fakeRunFinalizer) Complete(_ context.Context, discoveryID string, insightsFound int) {
	f.completedCalled = true
	f.completedID = discoveryID
	f.completedCount = insightsFound
}

func (f *fakeRunFinalizer) Fail(_ context.Context, discoveryID, errMsg string) {
	f.failedCalled = true
	f.failedID = discoveryID
	f.failedMsg = errMsg
}

func TestFinalizeStatus_HappyPathCompletes(t *testing.T) {
	// Baseline: compute had no error, so finalizeStatus must mark
	// the run as completed (Complete called, Fail not), and return
	// nil so the caller routes through the success notification.
	rep := &fakeRunFinalizer{}
	result := &models.DiscoveryResult{ID: "disc-123"}

	err := finalizeStatus(context.Background(), rep, nil, result, 7)
	if err != nil {
		t.Fatalf("happy path returned err = %v, want nil", err)
	}
	if !rep.completedCalled {
		t.Error("Complete was not called on happy path")
	}
	if rep.failedCalled {
		t.Error("Fail must NOT be called on happy path")
	}
	if rep.completedID != "disc-123" {
		t.Errorf("Complete discoveryID = %q, want disc-123", rep.completedID)
	}
	if rep.completedCount != 7 {
		t.Errorf("Complete insightsFound = %d, want 7", rep.completedCount)
	}
}

func TestFinalizeStatus_ComputeCancelledCallsFailNotComplete(t *testing.T) {
	// If DISCOVERY_MAX_DURATION expired mid-compute (or any other
	// parent ctx cancellation surfaced as a recorded error),
	// finalizeStatus MUST NOT mark the run as completed — that
	// would trigger the EventDiscoveryCompleted success
	// notification on a run that actually failed mid-way. The
	// partial result has been saved for the dashboard, but the
	// terminal status is Fail and the compute-phase error is
	// wrapped + returned so the caller routes through the
	// failed-notification path.
	//
	// Additional guard: Fail must receive the result.ID so the
	// failed run carries the discovery_id back-reference. Without
	// that, the discovery-log APIs can't navigate to the partial
	// result.
	rep := &fakeRunFinalizer{}
	result := &models.DiscoveryResult{ID: "disc-456"}

	err := finalizeStatus(context.Background(), rep, context.DeadlineExceeded, result, 3)
	if err == nil {
		t.Fatal("expected non-nil error when computeErr != nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("returned error must wrap computeErr; got %v", err)
	}
	if rep.completedCalled {
		t.Error("Complete must NOT be called when computeErr != nil — would trigger success notification on a failed run")
	}
	if !rep.failedCalled {
		t.Error("Fail must be called when computeErr != nil")
	}
	if rep.failedMsg == "" {
		t.Error("Fail message must carry the compute error context")
	}
	if rep.failedID != "disc-456" {
		t.Errorf("Fail discoveryID = %q, want disc-456 — failed runs must stamp the discovery_id back-reference so the partial result is reachable", rep.failedID)
	}
}

func TestFinalizeStatus_ContextCanceledTreatedAsFailure(t *testing.T) {
	// Same contract as the DeadlineExceeded case — a user/operator
	// cancellation must also surface as Fail, not Complete.
	rep := &fakeRunFinalizer{}
	result := &models.DiscoveryResult{ID: "disc-789"}

	err := finalizeStatus(context.Background(), rep, context.Canceled, result, 0)
	if err == nil {
		t.Fatal("expected non-nil error when computeErr != nil")
	}
	if rep.completedCalled {
		t.Error("Complete must NOT be called on user-cancellation path")
	}
	if !rep.failedCalled {
		t.Error("Fail must be called on user-cancellation path")
	}
}

func TestFinalizeStatus_UsesFreshCtxIndependentOfParent(t *testing.T) {
	// Pin the design intent: the parent ctx may already be cancelled
	// by the time finalizeStatus runs (compute deadline elapsed),
	// but the dedicated completeCtx must still be live so the
	// terminal-status UpdateOne lands. We assert the live-ctx
	// invariant INSIDE the reporter callback, since finalizeStatus's
	// own defer-cancel runs as soon as the function returns.
	var capturedErr error
	rep := runFinalizerFunc{
		complete: func(ctx context.Context, _ string, _ int) { capturedErr = ctx.Err() },
		fail:     func(ctx context.Context, _, _ string) { capturedErr = ctx.Err() },
	}
	parent, cancel := context.WithCancel(context.Background())
	cancel()

	_ = finalizeStatus(parent, rep, context.DeadlineExceeded, &models.DiscoveryResult{}, 0)

	if capturedErr != nil {
		t.Errorf("reporter ctx must be live (independent of cancelled parent); got %v", capturedErr)
	}
}

// runFinalizerFunc is a function-typed adapter implementing
// runFinalizer for the ctx-isolation test above. Keeps the test
// self-contained without another struct definition.
type runFinalizerFunc struct {
	complete func(ctx context.Context, discoveryID string, insightsFound int)
	fail     func(ctx context.Context, discoveryID, errMsg string)
}

func (f runFinalizerFunc) Complete(ctx context.Context, discoveryID string, insightsFound int) {
	f.complete(ctx, discoveryID, insightsFound)
}
func (f runFinalizerFunc) Fail(ctx context.Context, discoveryID, errMsg string) {
	f.fail(ctx, discoveryID, errMsg)
}

// Compile-time assertion — the production *StatusReporter satisfies
// runFinalizer. If a method signature ever drifts, this catches it
// before the orchestrator's RunDiscovery wiring breaks.
var _ runFinalizer = (*StatusReporter)(nil)

func TestPersistContext_SurvivesCancelledParent(t *testing.T) {
	// The whole reason persistContext exists: a cancelled parent
	// must not poison the tail-end durable writes. If WithoutCancel
	// is ever removed or replaced with a derived ctx that inherits
	// cancellation, this guard fires immediately.
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	if parent.Err() == nil {
		t.Fatal("parent must report cancellation")
	}

	persist, persistCancel := persistContext(parent)
	defer persistCancel()

	if err := persist.Err(); err != nil {
		t.Fatalf("persist ctx must not inherit parent cancellation; got %v", err)
	}
	if _, ok := persist.Deadline(); !ok {
		t.Fatal("persist ctx must carry its own deadline")
	}
}

func TestPersistContext_SurvivesExpiredParent(t *testing.T) {
	// Production scenario after DISCOVERY_MAX_DURATION elapses
	// mid-compute: parent ctx is already expired by the time we
	// reach the persist tail. persistContext must give the durable
	// writes a fresh budget anyway.
	parent, parentCancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer parentCancel()
	// Spin until the parent observes its own deadline so the test
	// is order-independent of the scheduler.
	for parent.Err() == nil {
		time.Sleep(50 * time.Microsecond)
	}

	persist, persistCancel := persistContext(parent)
	defer persistCancel()

	if err := persist.Err(); err != nil {
		t.Fatalf("persist ctx must not inherit parent expiry; got %v", err)
	}

	deadline, ok := persist.Deadline()
	if !ok {
		t.Fatal("persist ctx must carry its own deadline")
	}
	if remaining := time.Until(deadline); remaining < persistTimeout-time.Second {
		t.Fatalf("persist ctx deadline too short: %s remaining (want ~%s)", remaining, persistTimeout)
	}
}

func TestPersistContext_PreservesValues(t *testing.T) {
	// WithoutCancel preserves values — the orchestrator and its
	// downstream calls rely on this for correlation IDs / telemetry
	// context that flow through the run. If a future refactor
	// swaps WithoutCancel for context.Background(), this guard
	// catches the silent data-attribution loss.
	key := ctxKey{name: "run-id"}
	parent := context.WithValue(context.Background(), key, "run-xyz")

	persist, persistCancel := persistContext(parent)
	defer persistCancel()

	got, _ := persist.Value(key).(string)
	if got != "run-xyz" {
		t.Fatalf("persist ctx lost parent value; got %q want %q", got, "run-xyz")
	}
}

func TestCompleteTimeout_IsShorterThanPersistTimeout(t *testing.T) {
	// completeTimeout exists to decouple statusReporter.Complete from
	// the heavier embed/index step that runs under persistCtx. The
	// invariant the design depends on is "Complete has its own
	// bounded budget, independent of persistCtx" — a future refactor
	// that swaps completeTimeout to share or exceed persistTimeout
	// would silently reintroduce the failure mode codex r1 caught
	// (Phase 9 burns persistCtx → Complete then fires against an
	// already-expired ctx → run is saved but never marked complete).
	if completeTimeout >= persistTimeout {
		t.Fatalf("completeTimeout (%s) must be shorter than persistTimeout (%s) so they are not coupled", completeTimeout, persistTimeout)
	}
	if completeTimeout <= 0 {
		t.Fatalf("completeTimeout (%s) must be positive — a single Mongo UpdateOne needs at least a few seconds", completeTimeout)
	}
}

func TestPersistContext_DeadlineMatchesPersistTimeout(t *testing.T) {
	// Sanity: the budget the orchestrator hands the persist tail
	// is what the constant says. Future refactors that accidentally
	// shrink this would silently re-introduce the original bug.
	persist, persistCancel := persistContext(context.Background())
	defer persistCancel()

	deadline, ok := persist.Deadline()
	if !ok {
		t.Fatal("expected a deadline")
	}
	remaining := time.Until(deadline)
	if remaining > persistTimeout || remaining < persistTimeout-time.Second {
		t.Fatalf("deadline outside expected window: %s (want ~%s)", remaining, persistTimeout)
	}
}

func TestNewOrchestrator_TypedNilDiscoveryLogRepoNormalizes(t *testing.T) {
	// Regression — a caller passing a typed-nil
	// *database.DiscoveryLogRepository must not produce a non-nil
	// interface field on the orchestrator. Without the guard, the
	// `o.discoveryLogRepo == nil` check in persistSplitLogs returns
	// false (interface holds a nil concrete pointer), and the next
	// call dereferences it and panics.
	var typedNil *database.DiscoveryLogRepository
	o := NewOrchestrator(OrchestratorOptions{
		DiscoveryLogRepo: typedNil,
		ProjectID:        "p",
		RunID:            "r",
	})

	if o.discoveryLogRepo != nil {
		t.Fatalf("typed-nil DiscoveryLogRepo must be normalized to nil interface; got %#v", o.discoveryLogRepo)
	}

	// Sanity — the guarded path must be a clean no-op (no panic, no
	// dereference of the nil concrete pointer).
	o.persistSplitLogs(context.Background(), "d", nil, nil, nil, nil)
}
