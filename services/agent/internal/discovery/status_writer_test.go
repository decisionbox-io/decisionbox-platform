package discovery

import (
	"context"
	"errors"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// fakeRunStepWriter records every step doc the StatusReporter routes to
// the discovery_run_steps collection so tests can assert per-method
// classification without a Mongo round-trip. The optional err field forces
// a failure path so the "log and swallow" contract gets exercised too.
type fakeRunStepWriter struct {
	steps []models.RunStep
	err   error
}

func (f *fakeRunStepWriter) AddStep(_ context.Context, _, _ string, step models.RunStep) error {
	f.steps = append(f.steps, step)
	return f.err
}

// stubRunRepo has the same exported method surface that StatusReporter
// touches, but rather than spinning up a *database.RunRepository fake we
// rely on the fact that enabled() short-circuits when repo is nil. To
// cover the runStepRepo.AddStep call paths we just need a non-nil repo —
// any non-nil pointer to an empty RunRepository is enough; we never call
// methods on it because the existing tests already cover repo-side
// counter logic via integration tests.
//
// Trick: enabled() needs a non-nil *database.RunRepository, so we
// construct one with a nil DB and rely on the AddExplorationStep counter
// branches NOT being touched by these tests. The counter calls occur
// AFTER the runStepRepo.AddStep call, so the lines we care about (the
// runStepRepo.AddStep itself) get covered before the test panics on
// repo.UpdateStatus / repo.IncrementQueryCount.
//
// Cleaner: we wrap the runStepRepo.AddStep test paths in functions that
// short-circuit before calling repo. Specifically AddStep, AddInsightStep,
// AddAnalysisStep, AddValidationStep, AddExplorationStep all call
// runStepRepo.AddStep and ALSO call repo methods. To keep this unit test
// dependency-free we use the AddStep path which only calls
// runStepRepo.AddStep (no repo touch).

func newEnabledReporter(t *testing.T, w runStepWriter) *StatusReporter {
	t.Helper()
	return newStatusReporter(&database.RunRepository{}, w, "p", "r", 5)
}

func TestStatusReporter_AddStep_RoutesThroughRunStepRepo(t *testing.T) {
	w := &fakeRunStepWriter{}
	sr := newEnabledReporter(t, w)

	step := models.RunStep{Phase: models.PhaseInit, Type: "info", Message: "starting"}
	sr.AddStep(context.Background(), step)

	if len(w.steps) != 1 {
		t.Fatalf("AddStep didn't forward — got %d steps, want 1", len(w.steps))
	}
	if w.steps[0].Type != "info" || w.steps[0].Message != "starting" {
		t.Errorf("forwarded step doesn't match input: %+v", w.steps[0])
	}
}

func TestStatusReporter_AddStep_LogsAndSwallowsError(t *testing.T) {
	// Per the contract — failed AddStep on the writer must NOT panic and
	// must NOT propagate. The reporter logs and continues; the discovery
	// run keeps going.
	w := &fakeRunStepWriter{err: errors.New("boom")}
	sr := newEnabledReporter(t, w)

	sr.AddStep(context.Background(), models.RunStep{Type: "info"})

	if len(w.steps) != 1 {
		t.Fatalf("AddStep didn't call writer despite error path; steps = %d", len(w.steps))
	}
}

func TestStatusReporter_TypedNilRunStepRepo_DisablesReporter(t *testing.T) {
	// The Go gotcha: passing `(*database.RunStepRepository)(nil)` to an
	// interface field would normally produce a non-nil interface with a
	// nil concrete pointer. NewStatusReporter must normalise that to an
	// untyped-nil interface so enabled() returns false.
	var typedNil *database.RunStepRepository
	sr := NewStatusReporter(&database.RunRepository{}, typedNil, "p", "r", 5)

	if sr.enabled() {
		t.Error("typed-nil runStepRepo must disable reporter; got enabled=true")
	}
}

// Every RunStep produced by an LLM-call path must populate InputTokens /
// OutputTokens. The following tests use fakeRunStepWriter to capture the
// step doc without spinning up Mongo, and pass `runID="r"` so that the
// repo methods called downstream (UpdateStatus / IncrementQueryCount /
// IncrementSchemaActionCalls in AddExplorationStep) fail with an
// ObjectID parse error and the StatusReporter's "log + swallow" branch
// kicks in. The runStepRepo.AddStep call happens BEFORE those repo
// calls, so the captured step is observable on every method.

func TestStatusReporter_AddAnalysisStep_StampsTokens(t *testing.T) {
	w := &fakeRunStepWriter{}
	sr := newEnabledReporter(t, w)

	sr.AddAnalysisStep(context.Background(), "churn", "Churn Risks", 3, "", 1500, 400)

	if len(w.steps) != 1 {
		t.Fatalf("AddAnalysisStep didn't forward — got %d steps, want 1", len(w.steps))
	}
	got := w.steps[0]
	if got.Phase != models.PhaseAnalysis {
		t.Errorf("Phase = %q, want %q", got.Phase, models.PhaseAnalysis)
	}
	if got.Type != "analysis" {
		t.Errorf("Type = %q, want analysis", got.Type)
	}
	if got.InputTokens != 1500 || got.OutputTokens != 400 {
		t.Errorf("tokens = (%d, %d), want (1500, 400)", got.InputTokens, got.OutputTokens)
	}
}

func TestStatusReporter_AddAnalysisStep_ErrorBranchStampsErrorType(t *testing.T) {
	// errStr non-empty must flip Type to "error" while still stamping
	// the tokens that were spent on the failed call.
	w := &fakeRunStepWriter{}
	sr := newEnabledReporter(t, w)

	sr.AddAnalysisStep(context.Background(), "churn", "Churn Risks", 0, "timeout", 800, 0)

	if len(w.steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(w.steps))
	}
	got := w.steps[0]
	if got.Type != "error" {
		t.Errorf("Type = %q, want error", got.Type)
	}
	if got.Error != "timeout" {
		t.Errorf("Error = %q, want timeout", got.Error)
	}
	if got.InputTokens != 800 || got.OutputTokens != 0 {
		t.Errorf("tokens = (%d, %d), want (800, 0)", got.InputTokens, got.OutputTokens)
	}
}

func TestStatusReporter_AddValidationStep_StampsTokens(t *testing.T) {
	w := &fakeRunStepWriter{}
	sr := newEnabledReporter(t, w)

	sr.AddValidationStep(context.Background(), "affected_count", "adjusted", 500, 350, 1200, 200)

	if len(w.steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(w.steps))
	}
	got := w.steps[0]
	if got.Phase != models.PhaseValidation || got.Type != "validation" {
		t.Errorf("phase/type wrong: %+v", got)
	}
	if got.InputTokens != 1200 || got.OutputTokens != 200 {
		t.Errorf("tokens = (%d, %d), want (1200, 200)", got.InputTokens, got.OutputTokens)
	}
	// AddValidationStep formats the message differently for claimed > 0 vs
	// claimed == 0. Exercise the claimed-zero path on a second call so
	// the alternative branch is covered too.
	sr.AddValidationStep(context.Background(), "user_count", "confirmed", 0, 0, 0, 0)
	if len(w.steps) != 2 {
		t.Fatalf("second AddValidationStep didn't forward — got %d steps", len(w.steps))
	}
}

func TestStatusReporter_AddInsightStep_NoTokens(t *testing.T) {
	// AddInsightStep is a summary row with no LLM call attached, so
	// it intentionally carries no token fields. The test pins that
	// contract so a future change doesn't accidentally add stale
	// tokens to insight rows.
	w := &fakeRunStepWriter{}
	sr := newEnabledReporter(t, w)

	sr.AddInsightStep(context.Background(), "High Churn", "critical", "churn")

	if len(w.steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(w.steps))
	}
	got := w.steps[0]
	if got.Type != "insight" {
		t.Errorf("Type = %q, want insight", got.Type)
	}
	if got.InsightName != "High Churn" || got.InsightSeverity != "critical" {
		t.Errorf("insight metadata wrong: %+v", got)
	}
	if got.InputTokens != 0 || got.OutputTokens != 0 {
		t.Errorf("AddInsightStep should not stamp tokens; got (%d, %d)", got.InputTokens, got.OutputTokens)
	}
}

func TestStatusReporter_AddRecommendationStep_StampsTokens(t *testing.T) {
	// The recommendation-phase LLM call shows up on the live run-step log.
	w := &fakeRunStepWriter{}
	sr := newEnabledReporter(t, w)

	sr.AddRecommendationStep(context.Background(), 5, "", 2200, 850)

	if len(w.steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(w.steps))
	}
	got := w.steps[0]
	if got.Phase != models.PhaseRecommendations {
		t.Errorf("Phase = %q, want %q", got.Phase, models.PhaseRecommendations)
	}
	if got.Type != "recommendation" {
		t.Errorf("Type = %q, want recommendation", got.Type)
	}
	if got.InputTokens != 2200 || got.OutputTokens != 850 {
		t.Errorf("tokens = (%d, %d), want (2200, 850)", got.InputTokens, got.OutputTokens)
	}
}

func TestStatusReporter_AddRecommendationStep_ErrorBranch(t *testing.T) {
	w := &fakeRunStepWriter{}
	sr := newEnabledReporter(t, w)

	sr.AddRecommendationStep(context.Background(), 0, "LLM unreachable", 0, 0)

	if len(w.steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(w.steps))
	}
	got := w.steps[0]
	if got.Type != "error" {
		t.Errorf("Type = %q, want error", got.Type)
	}
	if got.Error != "LLM unreachable" {
		t.Errorf("Error = %q", got.Error)
	}
}

// TestStatusReporter_AddExplorationStep_StampsTokens uses the same trick
// the existing tests rely on: pass a runID that is NOT a valid Mongo
// ObjectID so the downstream repo calls (UpdateStatus,
// IncrementQueryCount, IncrementSchemaActionCalls) return an early
// "invalid ID" error and get logged+swallowed — without touching the
// nil Mongo collection. The runStepRepo.AddStep call happens BEFORE
// those repo calls so the captured step is fully observable.
func TestStatusReporter_AddExplorationStep_StampsTokens(t *testing.T) {
	w := &fakeRunStepWriter{}
	// newEnabledReporter passes runID="r" which is not a valid ObjectID hex
	// — that is the failure mode we want.
	sr := newEnabledReporter(t, w)

	sr.AddExplorationStep(context.Background(), 1, "query_data", "looking at retention", "SELECT 1", 5, 100, false, "", 350, 120)

	if len(w.steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(w.steps))
	}
	got := w.steps[0]
	if got.Phase != models.PhaseExploration {
		t.Errorf("Phase = %q, want %q", got.Phase, models.PhaseExploration)
	}
	if got.Type != "query" {
		t.Errorf("Type = %q, want query", got.Type)
	}
	if got.InputTokens != 350 || got.OutputTokens != 120 {
		t.Errorf("tokens = (%d, %d), want (350, 120)", got.InputTokens, got.OutputTokens)
	}
	if got.LLMThinking != "looking at retention" || got.Query != "SELECT 1" {
		t.Errorf("step metadata lost: %+v", got)
	}
}

func TestStatusReporter_AddExplorationStep_RejectedActionPath(t *testing.T) {
	// The "complete_rejected" exploration action sets a distinct Type
	// and routes through a different classify branch — pin it so a
	// future StatusReporter rework keeps the live UI's
	// rejected-completion rendering intact while still stamping the
	// tokens spent on the rejected LLM call.
	w := &fakeRunStepWriter{}
	sr := newEnabledReporter(t, w)

	sr.AddExplorationStep(context.Background(), 2, "complete_rejected", "thinking", "", 0, 0, false, "rejected premature completion (2 < 5)", 90, 12)

	if len(w.steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(w.steps))
	}
	got := w.steps[0]
	if got.Type != "complete_rejected" {
		t.Errorf("Type = %q, want complete_rejected", got.Type)
	}
	if got.InputTokens != 90 || got.OutputTokens != 12 {
		t.Errorf("tokens = (%d, %d), want (90, 12)", got.InputTokens, got.OutputTokens)
	}
}

func TestStatusReporter_AddExplorationStep_LookupSchemaAction(t *testing.T) {
	// The "lookup_schema" action hits a different counter branch
	// (IncrementSchemaActionCalls) AFTER AddStep — same ObjectID-parse
	// trick makes the counter call fail safely.
	w := &fakeRunStepWriter{}
	sr := newEnabledReporter(t, w)

	sr.AddExplorationStep(context.Background(), 3, "lookup_schema", "needing schema for orders", "", 0, 0, false, "", 50, 18)

	if len(w.steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(w.steps))
	}
	got := w.steps[0]
	if got.Type != "lookup_schema" {
		t.Errorf("Type = %q, want lookup_schema", got.Type)
	}
	if got.InputTokens != 50 || got.OutputTokens != 18 {
		t.Errorf("tokens = (%d, %d), want (50, 18)", got.InputTokens, got.OutputTokens)
	}
}
