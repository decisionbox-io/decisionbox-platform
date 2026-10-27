package discovery

import (
	"context"
	"errors"
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
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

// Nearest answers "nothing to compare against", which is what a stub with no
// stored vectors honestly knows. The engine reads that as unjudgeable and
// keeps the step floor, so these tests are unaffected by the stopping rule.
func (s *stubStepIndexer) Nearest(_ context.Context, _ models.ExplorationStep) (float64, bool, error) {
	return 0, false, nil
}

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

// TestRecompactSurvivorsTighter_LeavesAReplayedSamplesDigestAlone pins the
// distinction the caller's comment already promises — "only steps that still
// carry their raw rows can be rebuilt" — and that the implementation used to
// decide on emptiness alone.
//
// BuildCompactResultWithLimits derives a digest's population statistics and
// its head/tail from the rows handed to it, treating any nonempty slice as the
// complete result. A replayed step breaks that premise: the checkpoint keeps a
// row SAMPLE with the true total in RowCount, which is the same asymmetry
// replayedQueryResult depends on to render "Showing 10 of 50000 rows" for a
// step whose stored rows number 50. Rebuilding from the sample hands the model
// statistics that describe the sample while claiming to describe the result.
func TestRecompactSurvivorsTighter_LeavesAReplayedSamplesDigestAlone(t *testing.T) {
	sample := make([]map[string]interface{}, 0, 3)
	for i := 0; i < 3; i++ {
		sample = append(sample, map[string]interface{}{"n": i})
	}
	original := gomodels.BuildCompactResult(sample)

	picked := []PickedStep{{
		Step: models.ExplorationStep{
			Step:        1,
			QueryResult: sample,
			// The query really returned 50 000 rows; these 3 are the
			// checkpoint's sample of them.
			RowCount:      50000,
			CompactResult: &original,
		},
	}}

	if changed := recompactSurvivorsTighter(picked); changed {
		t.Error("recompacted a step whose rows are a checkpoint sample; the rebuilt digest would describe the sample while claiming the result's row total")
	}
	if picked[0].Step.CompactResult != &original {
		t.Error("the replayed step's stored digest was replaced; it was built from the full rows at execution time and is the only accurate one available")
	}
}

// And the live path is untouched: a step whose rows ARE the whole result still
// gets the tighter digest, or smart overflow stops saving anything and every
// over-budget run goes back to dropping steps.
func TestRecompactSurvivorsTighter_StillShrinksAFullResult(t *testing.T) {
	rows := make([]map[string]interface{}, 0, 40)
	for i := 0; i < 40; i++ {
		rows = append(rows, map[string]interface{}{"n": i, "pad": "xxxxxxxxxxxxxxxxxxxx"})
	}
	full := gomodels.BuildCompactResult(rows)

	picked := []PickedStep{{
		Step: models.ExplorationStep{
			Step: 1, QueryResult: rows,
			// RowCount agrees with the rows in hand: nothing was sampled.
			RowCount:      len(rows),
			CompactResult: &full,
		},
	}}

	if changed := recompactSurvivorsTighter(picked); !changed {
		t.Fatal("a full result was not recompacted; smart overflow has nothing left to try before dropping steps")
	}
	if picked[0].Step.CompactResult == &full {
		t.Error("the digest was not replaced")
	}
}

// A step that never recorded a RowCount behaves exactly as it did before the
// sample check existed — the check only fires when RowCount positively
// contradicts the rows in hand.
func TestRecompactSurvivorsTighter_NoRowCountIsTreatedAsFullRows(t *testing.T) {
	rows := make([]map[string]interface{}, 0, 40)
	for i := 0; i < 40; i++ {
		rows = append(rows, map[string]interface{}{"n": i, "pad": "xxxxxxxxxxxxxxxxxxxx"})
	}
	picked := []PickedStep{{
		Step: models.ExplorationStep{Step: 1, QueryResult: rows}, // RowCount zero
	}}
	if changed := recompactSurvivorsTighter(picked); !changed {
		t.Error("a step with no RowCount was skipped; that is a behaviour change for every step written before RowCount was recorded")
	}
}
