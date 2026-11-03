package discovery

// Operator-pause tests (issue #508). The cooperative stop is a read the agent
// already made — the ownership probe — plus one status write, so what these
// pin is the DECISIONS around it: that a pause request never reads as a loss
// of ownership, that the stop lands after the work is durable and not before,
// and that a pause that could not be recorded does not become a silent exit.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// TestStanding_PauseRequestIsNotALossOfOwnership is the regression that would
// otherwise abort every working run the moment anybody clicked Pause.
//
// A pause request has to travel through the same probe that answers "do I
// still own this run", because that probe is the only thing the agent already
// calls at every phase boundary. The danger in reusing it is that four of the
// five answers mean "stop and claim nothing" and the fifth means "carry on,
// and stop at the next clean seam" — so a gate that cannot tell them apart
// treats a paused run as a superseded one and exits without writing anything.
func TestStanding_PauseRequestIsNotALossOfOwnership(t *testing.T) {
	ctx := context.Background()

	t.Run("OwnsRun stays true under a pause request", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.pauseRequested = true
		r := reporterFor(doc, 1)

		if !r.OwnsRun(ctx) {
			t.Fatal("OwnsRun said false for a pause request: every gate past the pause window would abort a healthy run as superseded")
		}
		if got := r.Standing(ctx); got != standingPauseRequested {
			t.Errorf("Standing = %v, want standingPauseRequested", got)
		}
	})

	t.Run("a real loss still stops the run", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			set  func(*fakeRunDoc)
		}{
			{"taken over", func(d *fakeRunDoc) { d.owns = false }},
			{"cancelled", func(d *fakeRunDoc) { d.cancelled = true }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				doc := newFakeRunDoc()
				tc.set(doc)
				r := reporterFor(doc, 1)
				if r.OwnsRun(ctx) {
					t.Error("OwnsRun said true for a run this attempt has lost")
				}
				if got := r.Standing(ctx); got != standingLost {
					t.Errorf("Standing = %v, want standingLost", got)
				}
			})
		}
	})

	t.Run("a read that fails carries on rather than pausing", func(t *testing.T) {
		// Fails open in BOTH directions, which is the point. A Mongo blip
		// must not abort a run that is working — and it must not invent a
		// pause either: a pause the operator did not get is a button they
		// press again, while a pause conjured from a failed read stops a run
		// for no reason they can see.
		doc := newFakeRunDoc()
		doc.ownsErr = errors.New("mongo is down")
		r := reporterFor(doc, 1)

		if got := r.Standing(ctx); got != standingCarryOn {
			t.Errorf("Standing = %v on a failed read, want standingCarryOn", got)
		}
		if !r.OwnsRun(ctx) {
			t.Error("a failed ownership read must not read as a loss")
		}
	})
}

// TestClassifyUnappliedTerminal_PauseRequestKeepsTheResult is the single
// highest-consequence case in this feature.
//
// classifyUnappliedTerminal's default arm returns terminalSuperseded, and the
// orchestrator's answer to that is to DELETE this attempt's discovery. So a
// pause request arriving while the terminal write is in flight must land in
// the still-ours arm: the run is still this attempt's, its write simply did
// not match (the document was already terminal), and the result has to stay.
// Reached the default arm instead, a complete discovery is destroyed because
// somebody clicked Pause a second too early.
func TestClassifyUnappliedTerminal_PauseRequestKeepsTheResult(t *testing.T) {
	ctx := context.Background()

	for _, write := range []string{"completion", "failure"} {
		t.Run(write, func(t *testing.T) {
			doc := newFakeRunDoc()
			doc.applied = false // the terminal write matched nothing
			doc.pauseRequested = true
			r := reporterFor(doc, 1)

			var got terminalOutcome
			if write == "completion" {
				got = r.Complete(ctx, "disc-1", 3)
			} else {
				got = r.Fail(ctx, "disc-1", "boom")
			}
			if got == terminalSuperseded {
				t.Fatal("a pause request made the terminal write read as supersession — the caller deletes this attempt's discovery on that answer")
			}
			if got != terminalUnknown {
				t.Errorf("outcome = %v, want terminalUnknown (result kept, nothing claimed)", got)
			}
			if !r.StillOwnsRun() {
				t.Error("ownership should be positively confirmed: the probe said the run is still this attempt's")
			}
		})
	}
}

// TestStatusReporterPause_ReportsApplicationAndErrorSeparately pins the split
// that keeps a failed pause from stranding a run.
//
// Not applied and errored look alike and demand opposite endings. Not applied
// means the run moved on, so this attempt exits quietly. Errored means nothing
// was recorded, and exiting quietly there leaves the run `running` with no
// agent behind it and a zero exit status no runner reports — a zombie until
// the next API restart.
func TestStatusReporterPause_ReportsApplicationAndErrorSeparately(t *testing.T) {
	ctx := context.Background()

	t.Run("applied", func(t *testing.T) {
		doc := newFakeRunDoc()
		r := reporterFor(doc, 2)
		applied, err := r.Pause(ctx, "Paused after exploration step 7 — resume to continue")
		if err != nil || !applied {
			t.Fatalf("Pause() = (%v, %v), want (true, nil)", applied, err)
		}
		if len(doc.pauses) != 1 {
			t.Fatalf("pause writes = %d, want 1", len(doc.pauses))
		}
		if doc.pauses[0] == "" {
			t.Error("the reason was dropped; it is what the dashboard shows as the run's phase detail")
		}
		// The write must carry this attempt, or a previous attempt's agent
		// could pause the live one.
		var sawAttempt bool
		for _, a := range doc.attempts {
			if a == 2 {
				sawAttempt = true
			}
		}
		if !sawAttempt {
			t.Error("the pause write did not carry the reporter's attempt")
		}
	})

	t.Run("not applied — the run moved on", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.pauseApplied = false
		r := reporterFor(doc, 1)
		applied, err := r.Pause(ctx, "whatever")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if applied {
			t.Error("Pause reported applied for a run that no longer accepts it")
		}
	})

	t.Run("errored — surfaced, not swallowed", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.pauseErr = errors.New("mongo is down")
		r := reporterFor(doc, 1)
		if _, err := r.Pause(ctx, "whatever"); err == nil {
			t.Fatal("a failed pause write reported no error; the caller would exit 0 and strand the run as running with no agent")
		}
	})

	t.Run("no run document — nothing to pause", func(t *testing.T) {
		r := newStatusReporter(nil, nil, "proj", "", 100)
		applied, err := r.Pause(ctx, "whatever")
		if applied || err != nil {
			t.Errorf("Pause() = (%v, %v) with reporting disabled, want (false, nil)", applied, err)
		}
	})
}

// TestPauseAttempt_BooksCompute pins that a paused attempt's compute is
// recorded, once, before the status flip.
//
// active_ms is what the dashboard shows as elapsed, and it is booked per
// attempt at its terminal write. A pause IS that attempt's ending, so skipping
// the booking would make a run that was paused and resumed report less work
// than it was charged for — and the gap grows with every pause.
func TestPauseAttempt_BooksCompute(t *testing.T) {
	doc := newFakeRunDoc()
	r, feed := reporterAndFeed(doc, 2)
	o := &Orchestrator{
		runID:            "run-1",
		statusReporter:   r,
		attemptStartedAt: time.Now().Add(-90 * time.Second),
	}

	err := o.pauseAttempt(context.Background(), "Paused after exploration step 7 — resume to continue")
	if !errors.Is(err, ai.ErrPauseRequested) {
		t.Fatalf("pauseAttempt() = %v, want ErrPauseRequested", err)
	}
	if len(doc.active) != 1 {
		t.Fatalf("active-time bookings = %d, want exactly 1", len(doc.active))
	}
	if doc.active[0] < 80*time.Second {
		t.Errorf("booked %v, want roughly the 90s this attempt ran", doc.active[0])
	}
	// One row in the live feed, so the log the operator is watching explains
	// why it stops instead of just ending.
	if feed.steps != 1 {
		t.Errorf("live-feed rows = %d, want 1 explaining the pause", feed.steps)
	}
}

// TestPauseAttempt_WriteOutcomesMapToTheRightEnding pins which signal each
// outcome produces, because the three endings are not interchangeable: one
// exits 0 with the run paused, one exits 0 claiming nothing, and one fails the
// run on purpose.
func TestPauseAttempt_WriteOutcomesMapToTheRightEnding(t *testing.T) {
	ctx := context.Background()

	t.Run("not applied becomes supersession, so the attempt exits quietly", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.pauseApplied = false
		o := &Orchestrator{runID: "run-1", statusReporter: reporterFor(doc, 1), attemptStartedAt: time.Now()}
		if err := o.pauseAttempt(ctx, "detail"); !errors.Is(err, ai.ErrAttemptSuperseded) {
			t.Fatalf("pauseAttempt() = %v, want ErrAttemptSuperseded", err)
		}
	})

	t.Run("a failed write fails the run rather than exiting silently", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.pauseErr = errors.New("mongo is down")
		o := &Orchestrator{runID: "run-1", statusReporter: reporterFor(doc, 1), attemptStartedAt: time.Now()}
		err := o.pauseAttempt(ctx, "detail")
		if err == nil {
			t.Fatal("a failed pause write produced no error: the agent would exit 0 and leave the run running with no agent behind it")
		}
		if errors.Is(err, ai.ErrPauseRequested) || errors.Is(err, ai.ErrAttemptSuperseded) {
			t.Errorf("err = %v, want a plain failure so the run is reported failed (and stays resumable)", err)
		}
	})
}

// TestStopBefore_OnlyPausesWhenThereIsSomethingToResumeFrom pins the
// precondition that keeps a pause from stranding a run.
//
// `paused` with no checkpoint is a dead end: the resume endpoint refuses a run
// with no replayable prefix and no exploration summary, so the run would sit
// in a status nothing can leave. A request that arrives before the first
// checkpoint therefore has to wait for it — which is exactly what "finish the
// current step, then stop" means.
func TestStopBefore_OnlyPausesWhenThereIsSomethingToResumeFrom(t *testing.T) {
	ctx := context.Background()

	t.Run("no resume point yet — carries on", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.pauseRequested = true
		o := &Orchestrator{runID: "run-1", statusReporter: reporterFor(doc, 1), attemptStartedAt: time.Now()}
		// keepStepIndex false: nothing checkpointed.
		if err := o.stopBefore(ctx, "analysis"); err != nil {
			t.Fatalf("stopBefore() = %v, want nil so the run continues to its first checkpoint", err)
		}
		if len(doc.pauses) != 0 {
			t.Error("wrote a pause with nothing to resume from; the run would be stranded in `paused`")
		}
	})

	t.Run("resume point present — pauses", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.pauseRequested = true
		o := &Orchestrator{runID: "run-1", statusReporter: reporterFor(doc, 1), attemptStartedAt: time.Now()}
		o.keepStepIndex = true
		if err := o.stopBefore(ctx, "analysis"); !errors.Is(err, ai.ErrPauseRequested) {
			t.Fatalf("stopBefore() = %v, want ErrPauseRequested", err)
		}
	})

	t.Run("supersession outranks a pause", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.pauseRequested = true
		doc.owns = false
		o := &Orchestrator{runID: "run-1", statusReporter: reporterFor(doc, 1), attemptStartedAt: time.Now()}
		o.keepStepIndex = true
		if err := o.stopBefore(ctx, "analysis"); !errors.Is(err, ai.ErrAttemptSuperseded) {
			t.Fatalf("stopBefore() = %v, want ErrAttemptSuperseded", err)
		}
		if len(doc.pauses) != 0 {
			t.Error("a superseded attempt wrote a pause onto a run it does not own")
		}
	})

	t.Run("nothing requested — carries on", func(t *testing.T) {
		doc := newFakeRunDoc()
		o := &Orchestrator{runID: "run-1", statusReporter: reporterFor(doc, 1), attemptStartedAt: time.Now()}
		o.keepStepIndex = true
		if err := o.stopBefore(ctx, "analysis"); err != nil {
			t.Fatalf("stopBefore() = %v, want nil", err)
		}
	})
}

// TestCheckpointStep_PauseStopsAFTERTheStepIsDurable is the acceptance
// criterion expressed as a test: "stops after the current step with a clean
// checkpoint".
//
// The ordering is the whole feature. The probe that discovers the pause runs
// BEFORE the write — it has to, because a superseded attempt must be stopped
// before it splices a row into the prefix — so the pause has to be remembered
// and acted on afterwards. Act on it where it is discovered and the operator
// loses the step they were already paying for, and the resume re-executes it.
func TestCheckpointStep_PauseStopsAFTERTheStepIsDurable(t *testing.T) {
	ctx := context.Background()

	t.Run("the checkpoint is written, then the pause is signalled", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.pauseRequested = true
		store := &fakeCheckpointStore{}
		o := &Orchestrator{
			runID:            "run-1",
			projectID:        "proj",
			statusReporter:   reporterFor(doc, 1),
			checkpointRepo:   store,
			attemptStartedAt: time.Now(),
		}

		err := o.checkpointStep(ctx, models.ExplorationStep{Step: 7, Action: "query_data"}, models.CheckpointArgs{})
		if !errors.Is(err, ai.ErrPauseRequested) {
			t.Fatalf("checkpointStep() = %v, want ErrPauseRequested", err)
		}
		if len(store.steps) != 1 || store.steps[0].Step.Step != 7 {
			t.Fatalf("saved steps = %+v, want one row for step 7: the step the operator interrupted must be durable before anything stops", store.steps)
		}
		if len(doc.markers) == 0 || doc.markers[len(doc.markers)-1] != 7 {
			t.Errorf("markers = %v, want the run marked resumable at step 7", doc.markers)
		}
		// SIGNALS, and must not write the status itself. The engine goes on
		// to record this step's counters, and that recording writes progress
		// through UpdateStatus — which would put `running` straight back over
		// a `paused` written here, leaving a run nobody is working on looking
		// live. The orchestrator takes the stop once the engine has finished.
		if len(doc.pauses) != 0 {
			t.Errorf("the hook wrote the pause itself (%v); the engine's next write would overwrite it with `running`", doc.pauses)
		}
	})

	t.Run("supersession skips the write entirely", func(t *testing.T) {
		// The opposite order, and for the opposite reason: a dead attempt
		// running ahead of the live one must not splice its work into the
		// prefix a later resume replays.
		doc := newFakeRunDoc()
		doc.owns = false
		store := &fakeCheckpointStore{}
		o := &Orchestrator{
			runID:          "run-1",
			statusReporter: reporterFor(doc, 1),
			checkpointRepo: store,
		}
		err := o.checkpointStep(ctx, models.ExplorationStep{Step: 7}, models.CheckpointArgs{})
		if !errors.Is(err, ai.ErrAttemptSuperseded) {
			t.Fatalf("checkpointStep() = %v, want ErrAttemptSuperseded", err)
		}
		if len(store.steps) != 0 {
			t.Errorf("a superseded attempt wrote %d checkpoint rows", len(store.steps))
		}
	})
}

// TestFenceReplayedStep_PausesWithoutWritingRows pins that a pause during
// replay is honoured and costs nothing.
//
// Replay is the cheapest place there is to stop: the rows belong to the
// attempt that earned them and are deliberately left alone, so stopping leaves
// the run exactly as resumable as it was. What must NOT happen is the replay
// re-stamping those rows with this attempt — that is what makes a twice-killed
// run throw away valid steps.
func TestFenceReplayedStep_PausesWithoutWritingRows(t *testing.T) {
	doc := newFakeRunDoc()
	doc.pauseRequested = true
	store := &fakeCheckpointStore{}
	o := &Orchestrator{
		runID:            "run-1",
		statusReporter:   reporterFor(doc, 2),
		checkpointRepo:   store,
		attemptStartedAt: time.Now(),
	}

	err := o.fenceReplayedStep(context.Background(), 3)
	if !errors.Is(err, ai.ErrPauseRequested) {
		t.Fatalf("fenceReplayedStep() = %v, want ErrPauseRequested", err)
	}
	if len(store.steps) != 0 {
		t.Errorf("replay wrote %d checkpoint rows; the prefix must keep the attempt that earned it", len(store.steps))
	}
	// Signals only, like checkpointStep.
	if len(doc.pauses) != 0 {
		t.Errorf("the replay fence wrote the pause itself (%v)", doc.pauses)
	}
}

// TestRunStatusPaused_IsNeitherActiveNorTerminal pins the property every
// status list in both services depends on.
//
// `paused` has to be absent from the {pending, running} lists, or a paused run
// blocks the project's next run and gets swept by the startup sweep; and absent
// from the {completed, failed, cancelled} lists, or its completion hooks fire
// and its plan reservation is closed for a run whose outcome is still to come.
func TestRunStatusPaused_IsNeitherActiveNorTerminal(t *testing.T) {
	for _, s := range []string{models.RunStatusPending, models.RunStatusRunning} {
		if models.RunStatusPaused == s {
			t.Errorf("paused must not be one of the active statuses (%s)", s)
		}
	}
	for _, s := range []string{models.RunStatusCompleted, models.RunStatusFailed, models.RunStatusCancelled} {
		if models.RunStatusPaused == s {
			t.Errorf("paused must not be one of the terminal statuses (%s)", s)
		}
	}
	if models.RunStatusPaused != "paused" {
		t.Errorf("RunStatusPaused = %q; the API writes the same literal into its filters", models.RunStatusPaused)
	}
}

// --- the per-area analysis seam (v2's checkpoints, #510) --------------------

// TestCheckpointAnalysisArea_PauseStopsAFTERTheAreaIsDurable is the analysis
// twin of TestCheckpointStep_PauseStopsAFTERTheStepIsDurable, and it exists
// because v2 made the per-area seam a free pause point: a completed area is
// checkpointed, so a resume restores it instead of re-analysing it.
//
// Same ordering, same reason. The probe runs before the write so a superseded
// attempt cannot plant an area the live attempt has not reached; the pause is
// therefore remembered and taken afterwards, once the area is durable.
func TestCheckpointAnalysisArea_PauseStopsAFTERTheAreaIsDurable(t *testing.T) {
	ctx := context.Background()

	t.Run("the area is written, then the pause is signalled", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.pauseRequested = true
		store := &fakeAnalysisCheckpointStore{}
		o := &Orchestrator{
			runID:                     "run-1",
			projectID:                 "proj",
			statusReporter:            reporterFor(doc, 1),
			analysisCheckpointRepo:    store,
			explorationSummaryAttempt: 1,
			attemptStartedAt:          time.Now(),
		}

		err := o.checkpointAnalysisArea(ctx, models.AnalysisStep{AreaID: "churn"}, 4)
		if !errors.Is(err, ai.ErrPauseRequested) {
			t.Fatalf("checkpointAnalysisArea() = %v, want ErrPauseRequested", err)
		}
		if len(store.saved) != 1 || store.saved[0].Step.AreaID != "churn" {
			t.Fatalf("saved areas = %+v, want one row for churn: the area the operator interrupted must be durable before anything stops", store.saved)
		}
		if len(doc.pauses) != 1 {
			t.Errorf("pause writes = %d, want 1", len(doc.pauses))
		}
	})

	t.Run("supersession skips the write entirely", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.owns = false
		store := &fakeAnalysisCheckpointStore{}
		o := &Orchestrator{
			runID:                     "run-1",
			statusReporter:            reporterFor(doc, 1),
			analysisCheckpointRepo:    store,
			explorationSummaryAttempt: 1,
		}
		err := o.checkpointAnalysisArea(ctx, models.AnalysisStep{AreaID: "churn"}, 0)
		if !errors.Is(err, ai.ErrAttemptSuperseded) {
			t.Fatalf("checkpointAnalysisArea() = %v, want ErrAttemptSuperseded", err)
		}
		if len(store.saved) != 0 {
			t.Errorf("a superseded attempt wrote %d area rows", len(store.saved))
		}
	})

	t.Run("no pause where nothing durable lands", func(t *testing.T) {
		// Each of these returns before the write. A pause taken here would
		// stop the run having checkpointed nothing for this area — the work
		// would be re-analysed, which is the opposite of what a pause
		// promises — so the request has to keep standing instead.
		for _, tc := range []struct {
			name string
			set  func(*Orchestrator)
			step models.AnalysisStep
		}{
			{"no durable exploration summary", func(o *Orchestrator) { o.explorationSummaryAttempt = 0 }, models.AnalysisStep{AreaID: "churn"}},
			{"the area errored", func(*Orchestrator) {}, models.AnalysisStep{AreaID: "churn", Error: "llm timeout"}},
			{"no area id", func(*Orchestrator) {}, models.AnalysisStep{}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				doc := newFakeRunDoc()
				doc.pauseRequested = true
				store := &fakeAnalysisCheckpointStore{}
				o := &Orchestrator{
					runID:                     "run-1",
					statusReporter:            reporterFor(doc, 1),
					analysisCheckpointRepo:    store,
					explorationSummaryAttempt: 1,
					attemptStartedAt:          time.Now(),
				}
				tc.set(o)

				if err := o.checkpointAnalysisArea(ctx, tc.step, 0); err != nil {
					t.Fatalf("checkpointAnalysisArea() = %v, want nil so the run carries on to a real pause point", err)
				}
				if len(doc.pauses) != 0 {
					t.Errorf("paused with nothing checkpointed for this area; the work would be re-analysed on resume")
				}
			})
		}
	})

	t.Run("a failed area write does not pause either", func(t *testing.T) {
		// The write is best-effort — a checkpoint must never abort a working
		// run — so a failure is swallowed. But the pause must be swallowed
		// with it: stopping now would claim an area is durable when the write
		// that was meant to make it so just failed.
		doc := newFakeRunDoc()
		doc.pauseRequested = true
		store := &fakeAnalysisCheckpointStore{saveErr: errors.New("mongo is down")}
		o := &Orchestrator{
			runID:                     "run-1",
			statusReporter:            reporterFor(doc, 1),
			analysisCheckpointRepo:    store,
			explorationSummaryAttempt: 1,
			attemptStartedAt:          time.Now(),
		}

		if err := o.checkpointAnalysisArea(ctx, models.AnalysisStep{AreaID: "churn"}, 0); err != nil {
			t.Fatalf("checkpointAnalysisArea() = %v, want nil — a failed checkpoint must not abort the run", err)
		}
		if len(doc.pauses) != 0 {
			t.Error("paused on an area whose checkpoint write failed; the resume would re-analyse it")
		}
	})
}

// TestPauseAttempt_BooksNoComputeWhenTheWriteFails is the second half of a
// Codex finding, and the subtler half.
//
// A pause can be attempted at every seam until one lands, and AddActiveTime
// adds time.Since(attemptStartedAt) — the attempt's WHOLE duration, not a
// delta since the last call. Booked before the write, a pause that failed
// would leave the run going and bank that full duration again at the next
// seam, so active_ms would inflate once per attempt and the dashboard would
// report a run as having spent several times the compute it did.
func TestPauseAttempt_BooksNoComputeWhenTheWriteFails(t *testing.T) {
	ctx := context.Background()

	t.Run("a failed write books nothing", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.pauseErr = errors.New("mongo is down")
		o := &Orchestrator{
			runID:            "run-1",
			statusReporter:   reporterFor(doc, 1),
			attemptStartedAt: time.Now().Add(-time.Minute),
		}
		if err := o.pauseAttempt(ctx, "detail"); err == nil {
			t.Fatal("a failed pause write produced no error")
		}
		if len(doc.active) != 0 {
			t.Errorf("active-time bookings = %d, want 0 — an attempt that did not reach its terminal write contributes nothing", len(doc.active))
		}
	})

	t.Run("a write that did not apply books nothing either", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.pauseApplied = false
		o := &Orchestrator{
			runID:            "run-1",
			statusReporter:   reporterFor(doc, 1),
			attemptStartedAt: time.Now().Add(-time.Minute),
		}
		if err := o.pauseAttempt(ctx, "detail"); !errors.Is(err, ai.ErrAttemptSuperseded) {
			t.Fatalf("pauseAttempt() = %v, want ErrAttemptSuperseded", err)
		}
		if len(doc.active) != 0 {
			t.Errorf("active-time bookings = %d, want 0 — the run moved on, so this attempt claims nothing", len(doc.active))
		}
	})
}

// TestPauseDetailFor_NamesTheStepItStoppedAfter pins the phase detail, which
// is both what the dashboard shows and the reason on the lifecycle event.
func TestPauseDetailFor_NamesTheStepItStoppedAfter(t *testing.T) {
	o := &Orchestrator{}
	if got := o.pauseDetailFor(&ai.ExplorationResult{TotalSteps: 42}); !strings.Contains(got, "42") {
		t.Errorf("detail = %q, want it to name step 42", got)
	}
	// A pause during replay, before any step was re-emitted: naming no step
	// beats naming step 0.
	for _, res := range []*ai.ExplorationResult{nil, {TotalSteps: 0}} {
		got := o.pauseDetailFor(res)
		if strings.Contains(got, "0") {
			t.Errorf("detail = %q, want no step number when none is known", got)
		}
		if got == "" {
			t.Error("detail is empty; the dashboard would show a paused run with no explanation")
		}
	}
}
