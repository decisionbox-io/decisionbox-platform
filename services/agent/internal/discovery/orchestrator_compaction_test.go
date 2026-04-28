package discovery

import (
	"context"
	"errors"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// stubStepIndexer captures Upsert calls for the countingStepIndexer
// tests. Returns the configured err on the next call.
type stubStepIndexer struct {
	calls []models.ExplorationStep
	err   error
}

func (s *stubStepIndexer) Upsert(_ context.Context, step models.ExplorationStep) error {
	s.calls = append(s.calls, step)
	return s.err
}

func (s *stubStepIndexer) Search(_ context.Context, _ string, _ RunStepIndexSearchOpts) ([]RunStepIndexHit, error) {
	return nil, nil
}

func (s *stubStepIndexer) Drop(_ context.Context) error { return nil }

func TestCountingStepIndexer_BumpsCounterOnSuccess(t *testing.T) {
	stub := &stubStepIndexer{}
	// nil reporter must be safe — exercises the c.reporter == nil branch.
	c := countingStepIndexer{inner: stub, reporter: nil, ctx: context.Background()}
	if err := c.Upsert(context.Background(), models.ExplorationStep{Step: 1}); err != nil {
		t.Errorf("Upsert: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Errorf("inner Upsert calls: got %d want 1", len(stub.calls))
	}
}

func TestCountingStepIndexer_PropagatesError(t *testing.T) {
	stub := &stubStepIndexer{err: errors.New("inner failed")}
	c := countingStepIndexer{inner: stub, reporter: nil, ctx: context.Background()}
	if err := c.Upsert(context.Background(), models.ExplorationStep{Step: 1}); err == nil {
		t.Errorf("expected propagated error")
	}
}

func TestStepsFromPickResult_ExtractsStepsInOrder(t *testing.T) {
	pr := &PickResult{
		Picked: []PickedStep{
			{Step: models.ExplorationStep{Step: 7}, Score: 0.9, Source: PickSourceVector},
			{Step: models.ExplorationStep{Step: 3}, Score: 0.5, Source: PickSourceExactMatch},
		},
	}
	got := stepsFromPickResult(pr)
	if len(got) != 2 || got[0].Step != 7 || got[1].Step != 3 {
		t.Errorf("got %+v want [{7} {3}] in order", got)
	}
}

func TestStepsFromPickResult_EmptyInput(t *testing.T) {
	got := stepsFromPickResult(&PickResult{})
	if got == nil {
		t.Errorf("must return non-nil empty slice for round-trip safety")
	}
	if len(got) != 0 {
		t.Errorf("len: got %d want 0", len(got))
	}
}

func TestPickedToTelemetry_PreservesScoreAndSource(t *testing.T) {
	picked := []PickedStep{
		{Step: models.ExplorationStep{Step: 1}, Score: 0.91, Source: PickSourceVector},
		{Step: models.ExplorationStep{Step: 2}, Score: 0.55, Source: PickSourceExactMatch},
	}
	got := pickedToTelemetry(picked)
	if len(got) != 2 {
		t.Fatalf("len: got %d want 2", len(got))
	}
	if got[0].Step != 1 || got[0].Score != 0.91 || got[0].Source != "vector" {
		t.Errorf("entry 0: got %+v", got[0])
	}
	if got[1].Source != "exact_match" {
		t.Errorf("entry 1 source: got %q want exact_match", got[1].Source)
	}
}

func TestDroppedToTelemetry_PreservesReason(t *testing.T) {
	dropped := []DroppedStep{
		{StepNumber: 5, Score: 0.2, Reason: DropReasonBelowMinScore},
		{StepNumber: 8, Score: 0.6, Reason: DropReasonOverBudget},
	}
	got := droppedToTelemetry(dropped)
	if len(got) != 2 {
		t.Fatalf("len: got %d want 2", len(got))
	}
	if got[0].Step != 5 || got[0].Reason != "below_min_score" {
		t.Errorf("entry 0: got %+v", got[0])
	}
	if got[1].Reason != "over_budget" {
		t.Errorf("entry 1 reason: got %q want over_budget", got[1].Reason)
	}
}

func TestPickedToTelemetry_EmptyInput(t *testing.T) {
	if got := pickedToTelemetry(nil); len(got) != 0 {
		t.Errorf("nil input: got %d want 0", len(got))
	}
}

func TestDroppedToTelemetry_EmptyInput(t *testing.T) {
	if got := droppedToTelemetry(nil); len(got) != 0 {
		t.Errorf("nil input: got %d want 0", len(got))
	}
}
