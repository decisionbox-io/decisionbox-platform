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
