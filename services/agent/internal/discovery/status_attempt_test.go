package discovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
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
	owns bool
	// cancelled is the run having been killed by the operator while this
	// attempt is still alive — the attempt number is untouched, so only a
	// status-aware probe can see it.
	cancelled bool
	ownsErr   error
	applied   bool
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

// Ownership answers the three-way standing question. `owns` keeps meaning
// "this attempt still owns it"; `cancelled` is the state a cancel leaves
// behind, which an attempt-only comparison cannot see because a cancel does
// not change the attempt.
func (f *fakeRunDoc) Ownership(_ context.Context, _ string, attempt int) (database.RunOwnership, error) {
	f.attempts = append(f.attempts, attempt)
	if f.ownsErr != nil {
		return database.RunOwnedByThisAttempt, f.ownsErr
	}
	if f.cancelled {
		return database.RunCancelled, nil
	}
	if !f.owns {
		return database.RunTakenOverByAnotherAttempt, nil
	}
	return database.RunOwnedByThisAttempt, nil
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

// fakeStepWriter satisfies the other half of enabled() and records which
// attempt each live-feed row was stamped with.
type fakeStepWriter struct {
	steps    int
	attempts []int
}

func (f *fakeStepWriter) AddStep(_ context.Context, _, _ string, attempt int, _ models.RunStep) error {
	f.steps++
	f.attempts = append(f.attempts, attempt)
	return nil
}

func reporterFor(doc *fakeRunDoc, attempt int) *StatusReporter {
	r, _ := reporterAndFeed(doc, attempt)
	return r
}

// reporterAndFeed also hands back the step writer, for the tests that care
// what landed in the live feed rather than on the run document.
func reporterAndFeed(doc *fakeRunDoc, attempt int) (*StatusReporter, *fakeStepWriter) {
	feed := &fakeStepWriter{}
	r := newStatusReporter(doc, feed, "proj", "run-1", 100)
	r.attempt = attempt
	return r, feed
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

// TestStatusReporter_StampsItsAttemptOnEveryLiveFeedRow is the step-row half
// of the fence, and it is the one that was missing in v1.
//
// The run document was fenced from the start, but discovery_run_steps rows —
// the dashboard's live log — were written with no attempt at all. A resume
// starts a new attempt while the superseded agent may still be inside an LLM
// call, so its rows landed in the resumed run's feed, indistinguishable from
// the live attempt's and never cleaned up. Worse, two writer processes break
// the `_id > since_id` cursor's single-writer assumption and the live
// attempt's own rows can be dropped from the stream for good.
//
// So: every reporter method that writes a row must stamp the attempt. This
// drives all six, and fails if a new one is added without it.
func TestStatusReporter_StampsItsAttemptOnEveryLiveFeedRow(t *testing.T) {
	r, feed := reporterAndFeed(newFakeRunDoc(), 4)
	ctx := context.Background()

	r.AddStep(ctx, models.RunStep{Type: "info", Message: "plain"})
	r.AddExplorationStep(ctx, 4, "query_data", "thinking", "SELECT 1", 10, 5, false, "", 1, 2, "")
	r.AddAnalysisStep(ctx, "area-1", "Revenue", 2, "", 1, 2)
	r.AddInsightStep(ctx, "insight", "high", "Revenue")
	r.AddRecommendationStep(ctx, 3, 1, "", 1, 2)
	r.AddValidationStep(ctx, "insight", "verified", 2, 2, 1, 2)

	const wantRows = 6
	if feed.steps != wantRows {
		t.Fatalf("wrote %d live-feed rows, want %d — a step-writing method stopped writing", feed.steps, wantRows)
	}
	for i, got := range feed.attempts {
		if got != 4 {
			t.Errorf("row %d carried attempt %d, want 4 — an unstamped row shows up in the resumed run's feed", i, got)
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
		if got := r.Complete(ctx, "disc-1", 1); got != terminalSuperseded {
			t.Errorf("Complete = %v, want terminalSuperseded", got)
		}
		if got := r.Fail(ctx, "disc-1", "boom"); got != terminalSuperseded {
			t.Errorf("Fail = %v, want terminalSuperseded", got)
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
			r.Complete(ctx, "d", 1) != terminalClaimed || r.Fail(ctx, "d", "e") != terminalClaimed {
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

	// A failed TERMINAL write reports UNKNOWN — not "superseded".
	//
	// This is the distinction a boolean could not carry. "Not claimed" was
	// true for both a lost attempt and a failed write, and the caller then
	// deleted this attempt's own result either way — so a Mongo blip at the
	// terminal write destroyed a result that was perfectly good and reported
	// nothing. Unknown licenses no cleanup at all.
	if got := r.Complete(ctx, "disc-1", 1); got != terminalUnknown {
		t.Errorf("a failed Complete = %v, want terminalUnknown — a write error is not a lost run", got)
	}
	if got := r.Fail(ctx, "disc-1", "boom"); got != terminalUnknown {
		t.Errorf("a failed Fail = %v, want terminalUnknown", got)
	}
}

// TestStatusReporter_AnUnmatchedTerminalWriteIsNotProofOfSupersession is the
// rule that keeps a finished discovery on disk.
//
// A terminal write is fenced on this attempt AND a non-terminal status, so it
// matches nothing in two quite different situations. Only one of them —
// another attempt owns the run — licenses deleting this attempt's own output.
// The other is the API's startup sweep, which marks in-flight runs `failed`
// after a restart WITHOUT reaping their agents: the agent runs on, finishes,
// saves its discovery, and finds its own terminal write unmatched. Reading
// that as supersession had it delete a complete result nobody superseded.
func TestStatusReporter_AnUnmatchedTerminalWriteIsNotProofOfSupersession(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		// owns/ownsErr are what the ownership read says once the terminal
		// write has already failed to match.
		owns    bool
		ownsErr error
		want    terminalOutcome
		why     string
	}{
		{
			name: "already terminal, still ours",
			owns: true,
			want: terminalUnknown,
			why:  "the sweep marked the run failed while this attempt was still working; its result must be kept",
		},
		{
			name: "a newer attempt owns the run",
			owns: false,
			want: terminalSuperseded,
			why:  "positive evidence of supersession is the one case that licenses retiring this attempt's own output",
		},
		{
			name:    "ownership unreadable",
			ownsErr: errors.New("mongo is having a moment"),
			want:    terminalUnknown,
			why:     "guessing either way turns a read blip into a destroyed result",
		},
	}

	for _, tc := range cases {
		for _, write := range []string{"Complete", "Fail"} {
			t.Run(tc.name+"/"+write, func(t *testing.T) {
				doc := newFakeRunDoc()
				doc.applied = false // the terminal write matched nothing
				doc.owns = tc.owns
				doc.ownsErr = tc.ownsErr
				r := reporterFor(doc, 2)

				var got terminalOutcome
				if write == "Complete" {
					got = r.Complete(ctx, "disc-1", 3)
				} else {
					got = r.Fail(ctx, "disc-1", "boom")
				}
				if got != tc.want {
					t.Errorf("%s returned %v, want %v — %s", write, got, tc.want, tc.why)
				}
			})
		}
	}
}

// TestStatusReporter_ReplayExplorationStepWritesTheRowOnly pins the contract
// the resume path depends on.
//
// Scoping the live feed to the current attempt means a resumed run must
// re-emit the replayed prefix, or its log starts partway through. But the
// progress field and the per-action counters live on the run document, which
// already carries them across attempts — the attempt that executed the step
// bumped them. Re-emitting a row must therefore touch the feed and nothing
// else, or every replayed query gets counted twice.
func TestStatusReporter_ReplayExplorationStepWritesTheRowOnly(t *testing.T) {
	doc := newFakeRunDoc()
	r, feed := reporterAndFeed(doc, 2)
	ran := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)

	r.ReplayExplorationStep(context.Background(), models.ExplorationStep{
		Step:      3,
		Action:    "query_data",
		Thinking:  "checking revenue",
		Query:     "SELECT 1",
		RowCount:  7,
		Timestamp: ran,
		TokensIn:  11,
		TokensOut: 22,
	})

	if feed.steps != 1 {
		t.Fatalf("wrote %d live-feed rows, want 1", feed.steps)
	}
	if len(feed.attempts) != 1 || feed.attempts[0] != 2 {
		t.Errorf("row carried attempts %v, want [2] — a replayed row on the wrong attempt is invisible to the feed it was emitted for", feed.attempts)
	}
	// The run document must be untouched: no progress write, no query
	// counter, nothing. doc.attempts records every write that reached it.
	if len(doc.attempts) != 0 {
		t.Errorf("the replay wrote to the run document %d time(s); the executing attempt already counted this step, so this double-counts it", len(doc.attempts))
	}
}

// TestStatusReporter_ReplayExplorationStepKeepsTheOriginalTimestamp — the
// feed renders per-step timestamps, so a replayed row has to say when the
// step ran, not when it was replayed. AddStep only defaults a zero
// timestamp, so this is really asserting the reporter passes the original
// through rather than letting it default.
func TestStatusReporter_ReplayExplorationStepKeepsTheOriginalTimestamp(t *testing.T) {
	w := &fakeRunStepWriter{}
	r := newStatusReporter(newFakeRunDoc(), w, "proj", "run-1", 100)
	r.attempt = 2
	ran := time.Now().UTC().Add(-90 * time.Minute).Truncate(time.Millisecond)

	r.ReplayExplorationStep(context.Background(), models.ExplorationStep{
		Step: 1, Action: "lookup_schema", Timestamp: ran,
	})

	if len(w.steps) != 1 {
		t.Fatalf("got %d rows, want 1", len(w.steps))
	}
	if !w.steps[0].Timestamp.Equal(ran) {
		t.Errorf("row timestamp = %s, want the step's own %s", w.steps[0].Timestamp, ran)
	}
}

// TestOrchestrator_ReplayLiveFeedForResume covers the resume-side loop that
// puts the replayed prefix into this attempt's feed.
//
// The nil cases matter as much as the happy one: this runs on every resumed
// run, and a nil reporter (an agent run with no API) or a resume that found
// nothing replayable must be a no-op rather than a panic.
func TestOrchestrator_ReplayLiveFeedForResume(t *testing.T) {
	ctx := context.Background()

	prefix := []models.ExplorationCheckpoint{
		{Step: models.ExplorationStep{Step: 1, Action: "lookup_schema"}},
		{Step: models.ExplorationStep{Step: 2, Action: "query_data", Query: "SELECT 1", RowCount: 3}},
		{Step: models.ExplorationStep{Step: 3, Action: "query_data", Query: "SELECT 2"}},
	}

	t.Run("emits one row per replayed step, on this attempt", func(t *testing.T) {
		doc := newFakeRunDoc()
		r, feed := reporterAndFeed(doc, 2)
		o := &Orchestrator{
			runID:          "run-1",
			projectID:      "proj",
			statusReporter: r,
			resume: &ResumeState{
				Attempt:     2,
				Checkpoints: &database.CheckpointSet{Steps: prefix, Attempt: 1},
			},
		}

		o.replayLiveFeedForResume(ctx)

		if feed.steps != len(prefix) {
			t.Fatalf("emitted %d rows, want %d — a resumed run's log would start partway through", feed.steps, len(prefix))
		}
		for i, got := range feed.attempts {
			if got != 2 {
				t.Errorf("row %d on attempt %d, want 2 — a row on the old attempt is invisible to the feed it was emitted for", i, got)
			}
		}
		if len(doc.attempts) != 0 {
			t.Errorf("the replay wrote to the run document %d time(s); those counters already carry across attempts", len(doc.attempts))
		}
	})

	t.Run("no-ops when there is nothing to replay", func(t *testing.T) {
		for name, resume := range map[string]*ResumeState{
			"not a resume at all":   nil,
			"resume with no prefix": {Attempt: 2, Checkpoints: &database.CheckpointSet{}},
			"resume with no set":    {Attempt: 2},
		} {
			t.Run(name, func(t *testing.T) {
				r, feed := reporterAndFeed(newFakeRunDoc(), 2)
				o := &Orchestrator{runID: "run-1", projectID: "proj", statusReporter: r, resume: resume}
				o.replayLiveFeedForResume(ctx)
				if feed.steps != 0 {
					t.Errorf("emitted %d rows, want 0", feed.steps)
				}
			})
		}
	})

	t.Run("no reporter is not a panic", func(t *testing.T) {
		o := &Orchestrator{
			runID: "run-1",
			resume: &ResumeState{
				Attempt:     2,
				Checkpoints: &database.CheckpointSet{Steps: prefix},
			},
		}
		o.replayLiveFeedForResume(ctx)
	})
}

// failingDropIndex is a RunStepIndex whose Drop fails while search and write
// keep working — the partial failure that makes a stale index dangerous
// rather than merely absent.
type failingDropIndex struct {
	nearestCalls int
	upsertCalls  int
}

func (f *failingDropIndex) Drop(context.Context) error {
	return errors.New("qdrant refused the delete")
}
func (f *failingDropIndex) Upsert(context.Context, models.ExplorationStep) error {
	f.upsertCalls++
	return nil
}
func (f *failingDropIndex) Search(context.Context, string, RunStepIndexSearchOpts) ([]RunStepIndexHit, error) {
	return nil, nil
}
func (f *failingDropIndex) Nearest(context.Context, models.ExplorationStep) (float64, bool, error) {
	f.nearestCalls++
	// A near-identical neighbour: exactly what a discarded future step would
	// look like to a new step that is actually breaking new ground.
	return 0.99, true, nil
}

// TestOrchestrator_AFailedIndexDropStandsTheIndexDown is the guarantee the
// lifecycle doc makes about resume: it can only ever lengthen a run.
//
// When a resumed run's replayable prefix is shorter than what the previous
// attempt indexed — a checkpoint gap, or a stale tail — the surviving points
// are steps the replay deliberately discarded. If the drop fails and the
// index stays live, the novelty rule compares new work against that discarded
// future, calls it a repeat, and can end the run early on evidence this
// attempt is not entitled to.
func TestOrchestrator_AFailedIndexDropStandsTheIndexDown(t *testing.T) {
	ctx := context.Background()
	idx := &failingDropIndex{}
	o := &Orchestrator{
		runID:        "run-1",
		projectID:    "proj",
		runStepIndex: idx,
		resume: &ResumeState{
			Attempt:     2,
			Checkpoints: &database.CheckpointSet{Steps: []models.ExplorationCheckpoint{{Step: models.ExplorationStep{Step: 1}}}},
		},
	}

	if o.stepIndexUnusable.Load() {
		t.Fatal("the index should start usable")
	}
	o.rebuildStepIndexForResume(ctx)
	if !o.stepIndexUnusable.Load() {
		t.Fatal("a failed drop left the index live; the novelty rule can then judge new work against steps the replay discarded")
	}

	// And the decorator the engine holds must actually refuse, since that is
	// the only thing the engine ever sees.
	dec := countingStepIndexer{inner: idx, ctx: ctx, unusable: &o.stepIndexUnusable}

	if _, found, err := dec.Nearest(ctx, models.ExplorationStep{Step: 7}); err == nil {
		t.Errorf("Nearest returned no error (found=%v); the engine needs an error to treat the step as unjudgeable", found)
	}
	if err := dec.Upsert(ctx, models.ExplorationStep{Step: 7}); err == nil {
		t.Error("Upsert succeeded; an index that keeps nothing is what makes noveltyMeasurable stand the rule down")
	}
	if idx.nearestCalls != 0 || idx.upsertCalls != 0 {
		t.Errorf("reached the stale collection %d time(s) searching and %d writing", idx.nearestCalls, idx.upsertCalls)
	}
}

// TestOrchestrator_ASuccessfulDropKeepsTheIndexUsable is the counter-test, so
// the stand-down cannot be silently permanent.
func TestOrchestrator_ASuccessfulDropKeepsTheIndexUsable(t *testing.T) {
	ctx := context.Background()
	idx := &okDropIndex{}
	o := &Orchestrator{
		runID: "run-1", projectID: "proj", runStepIndex: idx,
		resume: &ResumeState{Attempt: 2, Checkpoints: &database.CheckpointSet{}},
	}

	o.rebuildStepIndexForResume(ctx)

	if o.stepIndexUnusable.Load() {
		t.Error("a successful drop must leave the index usable; the replay re-indexes the prefix into it")
	}
	dec := countingStepIndexer{inner: idx, ctx: ctx, unusable: &o.stepIndexUnusable}
	if err := dec.Upsert(ctx, models.ExplorationStep{Step: 1}); err != nil {
		t.Errorf("Upsert = %v, want the write to reach the index", err)
	}
	if idx.upsertCalls != 1 {
		t.Errorf("index saw %d upserts, want 1", idx.upsertCalls)
	}
}

type okDropIndex struct{ upsertCalls int }

func (o *okDropIndex) Drop(context.Context) error { return nil }
func (o *okDropIndex) Upsert(context.Context, models.ExplorationStep) error {
	o.upsertCalls++
	return nil
}
func (o *okDropIndex) Search(context.Context, string, RunStepIndexSearchOpts) ([]RunStepIndexHit, error) {
	return nil, nil
}
func (o *okDropIndex) Nearest(context.Context, models.ExplorationStep) (float64, bool, error) {
	return 0, false, nil
}

// TestStatusReporter_ACancelledRunIsNotOwned closes the last cancellation
// gap, and it is the one I argued against closing.
//
// A cancel is a hard kill that leaves the attempt number untouched, so an
// attempt-only comparison answers "still yours" for a run the operator has
// already killed — and the workload can still be alive, because the runners
// can return before it is gone. The agent therefore walked through every
// ownership gate and spent its whole remaining budget, and when its terminal
// write was refused (Complete is barred on a cancelled run) the classifier
// read "still owns the run" and KEPT the discovery. A cancelled run ended up
// with a result on display that nothing can explain.
func TestStatusReporter_ACancelledRunIsNotOwned(t *testing.T) {
	ctx := context.Background()

	t.Run("the gates stop", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.cancelled = true
		r := reporterFor(doc, 2)
		if r.OwnsRun(ctx) {
			t.Error("a cancelled run reads as owned; its agent spends the rest of its budget on a run the operator killed")
		}
	})

	t.Run("the result is deleted, and for the right reason", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.cancelled = true
		// applied=false is what a refused terminal write looks like: Complete
		// is barred on a cancelled run.
		doc.applied = false
		r := reporterFor(doc, 2)

		if got := r.Complete(ctx, "disc-1", 3); got != terminalSuperseded {
			t.Errorf("Complete = %v, want terminalSuperseded so the cancelled run's result is retired", got)
		}
		if got := r.Fail(ctx, "disc-1", "boom"); got != terminalSuperseded {
			t.Errorf("Fail = %v, want terminalSuperseded", got)
		}
	})

	t.Run("a live run is still owned", func(t *testing.T) {
		r := reporterFor(newFakeRunDoc(), 2)
		if !r.OwnsRun(ctx) {
			t.Error("a live run on this attempt must read as owned")
		}
	})

	t.Run("a swept failed run keeps its result", func(t *testing.T) {
		// The counter-case that makes this safe. The API's startup sweep
		// marks in-flight runs `failed` without reaping their agents, so a
		// refused terminal write on a run this attempt still owns must NOT
		// delete anything — that is terminalUnknown, and it is why a
		// cancellation had to be told apart from "already terminal" rather
		// than lumped in with it.
		doc := newFakeRunDoc()
		doc.applied = false
		r := reporterFor(doc, 2)
		if got := r.Complete(ctx, "disc-1", 3); got != terminalUnknown {
			t.Errorf("Complete = %v, want terminalUnknown — a swept run's result must survive", got)
		}
	})

	t.Run("an unreadable run deletes nothing", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.applied = false
		doc.ownsErr = errors.New("mongo down")
		r := reporterFor(doc, 2)
		if got := r.Complete(ctx, "disc-1", 3); got != terminalUnknown {
			t.Errorf("Complete = %v, want terminalUnknown — a failed read is not evidence of anything", got)
		}
	})
}
