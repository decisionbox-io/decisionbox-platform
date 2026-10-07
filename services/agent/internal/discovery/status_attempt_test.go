package discovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation/verifier"
)

// The StatusReporter is where the attempt fence is decided — "does this
// attempt still own the run" is answered here, and everything the resume
// feature does about a superseded agent hangs off that answer. These tests
// exercise it directly rather than through a MongoDB container, which is what
// holding the repository behind an interface buys.

// fakeRunDoc records what the reporter wrote and can report that this attempt
// no longer owns the run — which is what an orphaned agent sees.
type fakeRunDoc struct {
	owns    bool
	ownsErr error
	applied bool
	// writeErr fails every write, which is how a Mongo hiccup reaches the
	// reporter. None of them may take the run down with them.
	writeErr error
	statuses []string
	markers  []int
	active   []time.Duration
	events   []models.RunLifecycleEvent
	attempts []int
}

func newFakeRunDoc() *fakeRunDoc { return &fakeRunDoc{owns: true, applied: true} }

func (f *fakeRunDoc) UpdateStatus(_ context.Context, _ string, status, _, _ string, _ int, attempt int) error {
	f.statuses = append(f.statuses, status)
	f.attempts = append(f.attempts, attempt)
	return f.writeErr
}
func (f *fakeRunDoc) Complete(_ context.Context, _, _ string, _ int, attempt int) (bool, error) {
	f.attempts = append(f.attempts, attempt)
	if f.writeErr != nil {
		return false, f.writeErr
	}
	return f.applied, nil
}
func (f *fakeRunDoc) Fail(_ context.Context, _, _, _ string, attempt int) (bool, error) {
	f.attempts = append(f.attempts, attempt)
	if f.writeErr != nil {
		return false, f.writeErr
	}
	return f.applied, nil
}
func (f *fakeRunDoc) OwnsRun(_ context.Context, _ string, attempt int) (bool, error) {
	f.attempts = append(f.attempts, attempt)
	if f.ownsErr != nil {
		return false, f.ownsErr
	}
	return f.owns, nil
}
func (f *fakeRunDoc) MarkExplorationCheckpoint(_ context.Context, _ string, step int, attempt int) (bool, error) {
	f.markers = append(f.markers, step)
	f.attempts = append(f.attempts, attempt)
	if f.writeErr != nil {
		return false, f.writeErr
	}
	return f.applied, nil
}
func (f *fakeRunDoc) AddActiveTime(_ context.Context, _ string, d time.Duration, attempt int) error {
	f.active = append(f.active, d)
	f.attempts = append(f.attempts, attempt)
	return f.writeErr
}
func (f *fakeRunDoc) AppendLifecycle(_ context.Context, _ string, ev models.RunLifecycleEvent, attempt int) error {
	f.events = append(f.events, ev)
	f.attempts = append(f.attempts, attempt)
	return f.writeErr
}
func (f *fakeRunDoc) IncrementQueryCount(_ context.Context, _ string, _ bool, attempt int) error {
	f.attempts = append(f.attempts, attempt)
	return f.writeErr
}
func (f *fakeRunDoc) IncrementSchemaActionCalls(_ context.Context, _, _ string, _ int, attempt int) error {
	f.attempts = append(f.attempts, attempt)
	return f.writeErr
}
func (f *fakeRunDoc) IncrementAnalysisCounter(_ context.Context, _, _ string, _ int, attempt int) error {
	f.attempts = append(f.attempts, attempt)
	return f.writeErr
}
func (f *fakeRunDoc) RecordSchemaContextTelemetry(_ context.Context, _ string, _, _ int, attempt int) error {
	f.attempts = append(f.attempts, attempt)
	return f.writeErr
}

// fakeStepWriter satisfies the other half of enabled().
type fakeStepWriter struct{ steps int }

func (f *fakeStepWriter) AddStep(context.Context, string, string, models.RunStep) error {
	f.steps++
	return nil
}

func reporterFor(doc *fakeRunDoc, attempt int) *StatusReporter {
	r := newStatusReporter(doc, &fakeStepWriter{}, "proj", "run-1", 100)
	r.attempt = attempt
	return r
}

// TestStatusReporter_StampsItsAttemptOnEveryRunDocumentWrite is the fence's
// coverage test: every write the reporter makes must carry the attempt, or an
// orphaned agent's write lands on the live attempt's run.
func TestStatusReporter_StampsItsAttemptOnEveryRunDocumentWrite(t *testing.T) {
	doc := newFakeRunDoc()
	r := reporterFor(doc, 3)
	ctx := context.Background()

	r.SetPhase(ctx, models.PhaseAnalysis, "analysing", 70)
	r.AddExplorationStep(ctx, 4, "query_data", "thinking", "SELECT 1", 10, 5, false, "", 1, 2, "")
	r.RecordSchemaTelemetry(ctx, 100, 20)
	r.IncrementSchemaActionCalls(ctx, "lookup_schema", 1)
	r.IncrementAnalysisCounter(ctx, "steps_dropped", 1)
	r.MarkExplorationCheckpoint(ctx, 4)
	r.AddActiveTime(ctx, 90*time.Second)
	r.AppendLifecycle(ctx, models.RunLifecycleEvent{Status: models.RunStatusFailed})
	r.Complete(ctx, "disc-1", 2)
	r.Fail(ctx, "disc-1", "boom")
	r.OwnsRun(ctx)

	if len(doc.attempts) == 0 {
		t.Fatal("no run-document writes were recorded")
	}
	for i, got := range doc.attempts {
		if got != 3 {
			t.Errorf("write %d carried attempt %d, want 3 — an unfenced write lands on the live attempt's run", i, got)
		}
	}
}

// TestStatusReporter_OwnershipAnswers pins how the two probes behave, because
// the whole supersession path branches on them.
func TestStatusReporter_OwnershipAnswers(t *testing.T) {
	ctx := context.Background()

	t.Run("owns the run", func(t *testing.T) {
		r := reporterFor(newFakeRunDoc(), 2)
		if !r.OwnsRun(ctx) {
			t.Error("OwnsRun should be true")
		}
		if !r.MarkExplorationCheckpoint(ctx, 5) {
			t.Error("the marker should report ownership")
		}
	})

	t.Run("superseded", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.owns, doc.applied = false, false
		r := reporterFor(doc, 1)
		if r.OwnsRun(ctx) {
			t.Error("OwnsRun must be false for a superseded attempt")
		}
		if r.MarkExplorationCheckpoint(ctx, 5) {
			t.Error("the marker must report the loss")
		}
		if r.Complete(ctx, "disc-1", 1) {
			t.Error("Complete must report that it did not claim the run")
		}
		if r.Fail(ctx, "disc-1", "boom") {
			t.Error("Fail must report that it did not claim the run")
		}
	})

	t.Run("a failed read is not evidence of being superseded", func(t *testing.T) {
		// The safe direction: a Mongo hiccup must not make a healthy attempt
		// stand itself down and abandon a working run.
		doc := newFakeRunDoc()
		doc.ownsErr = errors.New("mongo down")
		r := reporterFor(doc, 2)
		if !r.OwnsRun(ctx) {
			t.Error("a failed ownership read must not be read as a loss")
		}
	})

	t.Run("a disabled reporter owns everything", func(t *testing.T) {
		// Single-binary runs have no run document and no competing attempt,
		// so treating them as superseded would break every one of them.
		r := newStatusReporter(nil, nil, "proj", "", 100)
		if !r.OwnsRun(ctx) || !r.MarkExplorationCheckpoint(ctx, 1) ||
			!r.Complete(ctx, "d", 1) || !r.Fail(ctx, "d", "e") {
			t.Error("a disabled reporter must report ownership on every path")
		}
	})
}

// TestCheckpointStep_RefusesToWriteOnceSuperseded is the test round 5 could
// not write, and the ordering it pins is the point: the row-level fence in
// SaveStep only refuses a write where a higher-attempt row already exists, so
// a dead attempt running AHEAD of the live one would splice its own work into
// the prefix. Asking first stops the row being written at all.
func TestCheckpointStep_RefusesToWriteOnceSuperseded(t *testing.T) {
	doc := newFakeRunDoc()
	doc.owns, doc.applied = false, false
	store := &fakeCheckpointStore{}
	o := &Orchestrator{
		projectID: "p", runID: "run-1",
		checkpointRepo: store,
		validationCfg:  verifier.Config{Bundle: verifier.DefaultBundleConfig()},
		statusReporter: reporterFor(doc, 1),
	}

	err := o.checkpointStep(context.Background(), models.ExplorationStep{Step: 7}, models.CheckpointArgs{})

	if !errors.Is(err, ai.ErrAttemptSuperseded) {
		t.Fatalf("err = %v, want ErrAttemptSuperseded", err)
	}
	if len(store.steps) != 0 {
		t.Errorf("a superseded attempt wrote %d checkpoint rows, want 0", len(store.steps))
	}
	if o.keepStepIndex {
		t.Error("a superseded attempt must not pin the run's step index")
	}
}

// TestCheckpointStep_MarkerLossAlsoStops pins the second fence: the probe and
// the marker write are two operations, and a resume landing between them is
// caught by the marker's answer — which keeps the attempt out of the shared
// writes that follow in the engine.
func TestCheckpointStep_MarkerLossAlsoStops(t *testing.T) {
	doc := newFakeRunDoc()
	doc.owns = true     // the probe passes...
	doc.applied = false // ...and the resume lands before the marker write
	store := &fakeCheckpointStore{}
	o := &Orchestrator{
		projectID: "p", runID: "run-1",
		checkpointRepo: store,
		validationCfg:  verifier.Config{Bundle: verifier.DefaultBundleConfig()},
		statusReporter: reporterFor(doc, 1),
	}

	err := o.checkpointStep(context.Background(), models.ExplorationStep{Step: 7}, models.CheckpointArgs{})

	if !errors.Is(err, ai.ErrAttemptSuperseded) {
		t.Fatalf("err = %v, want ErrAttemptSuperseded", err)
	}
	// The row itself got through — that is the documented residual. What
	// must NOT happen is the engine going on to the shared writes.
	if len(store.steps) != 1 {
		t.Errorf("checkpoint rows = %d; the row before the marker is the known residual", len(store.steps))
	}
}

// TestCheckpointExplorationSummary_SupersededStopsBeforeAnalysis pins the last
// cheap place to find out. Everything after it — analysis, recommendations,
// validation — would be spent on a result the attempt then deletes.
func TestCheckpointExplorationSummary_SupersededStopsBeforeAnalysis(t *testing.T) {
	doc := newFakeRunDoc()
	doc.owns = false
	store := &fakeCheckpointStore{}
	o := &Orchestrator{
		projectID: "p", runID: "run-1",
		checkpointRepo: store,
		statusReporter: reporterFor(doc, 1),
	}

	err := o.checkpointExplorationSummary(context.Background(), &ai.ExplorationResult{TotalSteps: 9, Completed: true})

	if !errors.Is(err, ai.ErrAttemptSuperseded) {
		t.Fatalf("err = %v, want ErrAttemptSuperseded", err)
	}
	if len(store.summaries) != 0 {
		t.Errorf("a superseded attempt wrote %d summaries, want 0", len(store.summaries))
	}
}

// TestRecordAttemptOutcome_BooksThisAttemptsTimeAndEvent pins the per-attempt
// bookkeeping a single mutable run document cannot hold.
func TestRecordAttemptOutcome_BooksThisAttemptsTimeAndEvent(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		doc := newFakeRunDoc()
		o := &Orchestrator{
			runID: "run-1", statusReporter: reporterFor(doc, 2),
			llmProvider: "claude", llmModel: "claude-sonnet-5-5",
			resume: &ResumeState{Attempt: 2},
		}
		o.recordAttemptOutcome(ctx, 90*time.Second, nil)

		if len(doc.active) != 1 || doc.active[0] != 90*time.Second {
			t.Errorf("active time booked = %v, want [1m30s]", doc.active)
		}
		if len(doc.events) != 1 {
			t.Fatalf("lifecycle events = %d, want 1", len(doc.events))
		}
		ev := doc.events[0]
		if ev.Status != models.RunStatusCompleted || ev.Attempt != 2 {
			t.Errorf("event = %+v, want completed on attempt 2", ev)
		}
		// Which model ran which attempt is only answerable from here.
		if ev.LLMProvider != "claude" || ev.LLMModel != "claude-sonnet-5-5" {
			t.Errorf("event lost the model provenance: %+v", ev)
		}
	})

	t.Run("failure carries the reason", func(t *testing.T) {
		doc := newFakeRunDoc()
		o := &Orchestrator{runID: "run-1", statusReporter: reporterFor(doc, 1)}
		o.recordAttemptOutcome(ctx, time.Second, context.DeadlineExceeded)

		if len(doc.events) != 1 {
			t.Fatalf("lifecycle events = %d, want 1", len(doc.events))
		}
		if doc.events[0].Status != models.RunStatusFailed {
			t.Errorf("event status = %q, want failed", doc.events[0].Status)
		}
		if doc.events[0].Reason == "" {
			t.Error("a failure event must record why")
		}
	})

	t.Run("no reporter is a no-op", func(t *testing.T) {
		(&Orchestrator{runID: "run-1"}).recordAttemptOutcome(ctx, time.Second, nil)
	})
}

// TestStatusReporter_AWriteFailureNeverTakesTheRunDown pins the swallow
// semantics on every run-document write.
//
// All of these are telemetry or affordance: losing one costs an operator some
// visibility. None of them is worth failing a discovery over, and the fence
// they now carry must not turn a Mongo hiccup into a reason to abandon a
// working run.
func TestStatusReporter_AWriteFailureNeverTakesTheRunDown(t *testing.T) {
	doc := newFakeRunDoc()
	doc.writeErr = errors.New("mongo down")
	r := reporterFor(doc, 2)
	ctx := context.Background()

	// None of these may panic or propagate.
	r.SetPhase(ctx, models.PhaseAnalysis, "analysing", 70)
	r.AddExplorationStep(ctx, 4, "query_data", "think", "SELECT 1", 10, 5, false, "", 1, 2, "")
	r.AddExplorationStep(ctx, 5, "lookup_schema", "think", "", 0, 0, false, "", 1, 2, "")
	r.RecordSchemaTelemetry(ctx, 100, 20)
	r.IncrementAnalysisCounter(ctx, "steps_dropped", 1)
	r.AddActiveTime(ctx, 30*time.Second)
	r.AppendLifecycle(ctx, models.RunLifecycleEvent{Status: models.RunStatusFailed})

	// A failed MARKER write still reports ownership: it is not evidence of
	// being superseded, and reading it as such would have a healthy attempt
	// stand itself down and abandon a run that is working.
	if !r.MarkExplorationCheckpoint(ctx, 4) {
		t.Error("a failed marker write must not be read as a lost run")
	}

	// A failed TERMINAL write reports no claim, which is the safe direction:
	// the attempt cannot prove it owns the run, so it must not go on to
	// delete another attempt's results.
	if r.Complete(ctx, "disc-1", 1) {
		t.Error("a failed Complete must not claim the run")
	}
	if r.Fail(ctx, "disc-1", "boom") {
		t.Error("a failed Fail must not claim the run")
	}
}
