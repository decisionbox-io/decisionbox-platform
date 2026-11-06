package discovery

// Operator-pause tests (issue #508). The cooperative stop is a read the agent
// already made — the ownership probe — plus one status write, so what these
// pin is the DECISIONS around it: that a pause request never reads as a loss
// of ownership, that the stop lands after the work is durable and not before,
// and that a pause that could not be recorded does not become a silent exit.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
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
		applied, err := r.Pause(ctx, "Paused after exploration step 7 — resume to continue", 90*time.Second, 6, "bedrock", "claude-sonnet-4-6")
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
		applied, err := r.Pause(ctx, "whatever", time.Minute, 6, "bedrock", "m")
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
		if _, err := r.Pause(ctx, "whatever", time.Minute, 6, "bedrock", "m"); err == nil {
			t.Fatal("a failed pause write reported no error; the caller would exit 0 and strand the run as running with no agent")
		}
	})

	t.Run("no run document — nothing to pause", func(t *testing.T) {
		r := newStatusReporter(nil, nil, "proj", "", 100)
		applied, err := r.Pause(ctx, "whatever", time.Minute, 6, "bedrock", "m")
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
	// Booked IN the pause write, not by a separate AddActiveTime call: the
	// run is visibly `paused` the moment that write lands, so an operator can
	// resume instantly and bump the attempt — and a separate, attempt-fenced
	// increment would then match nothing and lose the whole attempt's
	// compute. Reported by a Codex round.
	if len(doc.active) != 0 {
		t.Errorf("a separate AddActiveTime call was made (%v); an immediate resume would orphan it", doc.active)
	}
	if len(doc.pauseActiveMs) != 1 {
		t.Fatalf("pause writes = %d, want exactly 1", len(doc.pauseActiveMs))
	}
	if doc.pauseActiveMs[0] < 80_000 {
		t.Errorf("booked %dms with the pause, want roughly the 90s this attempt ran", doc.pauseActiveMs[0])
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

	t.Run("a contiguous prefix — pauses", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.pauseRequested = true
		o := &Orchestrator{runID: "run-1", statusReporter: reporterFor(doc, 1), attemptStartedAt: time.Now()}
		o.contiguousCheckpointThrough = 6
		if err := o.stopBefore(ctx, "analysis"); !errors.Is(err, ai.ErrPauseRequested) {
			t.Fatalf("stopBefore() = %v, want ErrPauseRequested", err)
		}
	})

	t.Run("exploration already complete — pauses with no prefix of its own", func(t *testing.T) {
		// The resume skips Phase 3 entirely, so it needs no replayable steps.
		doc := newFakeRunDoc()
		doc.pauseRequested = true
		o := &Orchestrator{
			runID: "run-1", statusReporter: reporterFor(doc, 1), attemptStartedAt: time.Now(),
			resume: &ResumeState{Attempt: 2, Checkpoints: &database.CheckpointSet{
				Summary: &models.ExplorationCheckpointSummary{Completed: true},
			}},
		}
		if err := o.stopBefore(ctx, "analysis"); !errors.Is(err, ai.ErrPauseRequested) {
			t.Fatalf("stopBefore() = %v, want ErrPauseRequested", err)
		}
	})

	t.Run("checkpoints exist but NOT contiguously — carries on", func(t *testing.T) {
		// The regression a Codex round found. Checkpoint writes are
		// best-effort so they can never abort a run, so step 1's write can
		// fail and step 2's succeed. keepStepIndex is then true — there IS a
		// row — but LoadPrefix stops at the gap, so the resume endpoint
		// refuses the run. Pausing on that would strand a working run in a
		// status nothing can leave, which is the one thing pause must never
		// do.
		doc := newFakeRunDoc()
		doc.pauseRequested = true
		o := &Orchestrator{runID: "run-1", statusReporter: reporterFor(doc, 1), attemptStartedAt: time.Now()}
		o.keepStepIndex = true            // a row was saved...
		o.contiguousCheckpointThrough = 0 // ...but step 1's was not

		if err := o.stopBefore(ctx, "analysis"); err != nil {
			t.Fatalf("stopBefore() = %v, want nil — the run has nothing a resume could replay", err)
		}
		if len(doc.pauses) != 0 {
			t.Error("paused a run with no replayable prefix; the resume endpoint would refuse it and the run could only be cancelled")
		}
	})

	t.Run("supersession outranks a pause", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.pauseRequested = true
		doc.owns = false
		o := &Orchestrator{runID: "run-1", statusReporter: reporterFor(doc, 1), attemptStartedAt: time.Now()}
		o.contiguousCheckpointThrough = 6
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
		o.contiguousCheckpointThrough = 6
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
		// Step 7 arrives with six contiguous checkpoints behind it, as it
		// does on a real run. Without that the seam correctly refuses to
		// pause — see TestCheckpointStep_WillNotPauseOntoAGap.
		o.contiguousCheckpointThrough = 6

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
	// A replay always has a prefix — that is what it is replaying — so the
	// contiguity this attempt inherited is where it starts. Without it the
	// shared guard would (correctly) refuse to pause.
	o.contiguousCheckpointThrough = 3

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
		// An attempt that reached analysis explored its way there, so it has
		// a contiguous prefix behind it. The shared guard needs that: a run
		// whose step-1 write failed is not resumable even with a summary,
		// because ResumeRun refuses an empty prefix.
		o.contiguousCheckpointThrough = 12

		checkpointed, err := o.checkpointAnalysisArea(ctx, models.AnalysisStep{AreaID: "churn"}, 4)
		if !errors.Is(err, ai.ErrPauseRequested) {
			t.Fatalf("checkpointAnalysisArea() = %v, want ErrPauseRequested", err)
		}
		// TRUE on the pause path, and this is the assertion that pins the
		// #515/#516 interaction down: the row landed before the pause was
		// signalled, so the draft it supersedes is spent and the caller must
		// still discard it. False here would leave a draft the restored
		// completed-area row makes unreachable, kept until the TTL swept it.
		if !checkpointed {
			t.Error("checkpointed = false on the pause path; the row landed, so the caller must still discard the draft")
		}
		if len(store.saved) != 1 || store.saved[0].Step.AreaID != "churn" {
			t.Fatalf("saved areas = %+v, want one row for churn: the area the operator interrupted must be durable before anything stops", store.saved)
		}
		// Signals only — the caller still has this area's analysis, insight
		// and validation feed rows to write, and stopping here would leave
		// the paused attempt's log missing the area it stopped after.
		if len(doc.pauses) != 0 {
			t.Errorf("the hook wrote the pause itself (%v); the area's feed rows come first", doc.pauses)
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
		checkpointed, err := o.checkpointAnalysisArea(ctx, models.AnalysisStep{AreaID: "churn"}, 0)
		if !errors.Is(err, ai.ErrAttemptSuperseded) {
			t.Fatalf("checkpointAnalysisArea() = %v, want ErrAttemptSuperseded", err)
		}
		if checkpointed {
			t.Error("checkpointed = true for a superseded attempt that never wrote a row")
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

				checkpointed, err := o.checkpointAnalysisArea(ctx, tc.step, 0)
				if err != nil {
					t.Fatalf("checkpointAnalysisArea() = %v, want nil so the run carries on to a real pause point", err)
				}
				if checkpointed {
					t.Error("checkpointed = true on a path that returns before the write")
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

		checkpointed, err := o.checkpointAnalysisArea(ctx, models.AnalysisStep{AreaID: "churn"}, 0)
		if err != nil {
			t.Fatalf("checkpointAnalysisArea() = %v, want nil — a failed checkpoint must not abort the run", err)
		}
		// FALSE, so the caller KEEPS the draft: the write that was meant to
		// make this area durable just failed, and the draft is now the only
		// recoverable copy of validation this attempt paid for.
		if checkpointed {
			t.Error("checkpointed = true after the area write failed; the caller would discard the only recoverable copy")
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
		// Atomic with the status, so a write that failed books nothing by
		// construction — which is the rule active_ms already documents.
		if len(doc.active) != 0 {
			t.Errorf("a separate active-time write was made (%v)", doc.active)
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
			t.Errorf("a separate active-time write was made (%v); the run moved on, so this attempt claims nothing", doc.active)
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

// TestCheckpointStep_ContiguityTracksGapsNotRows is the read-side of the same
// Codex finding: the counter the pause gate consults must only advance on an
// unbroken sequence.
//
// Checkpoint writes are best-effort — a Mongo hiccup must never abort a
// working run — so gaps are a normal, if rare, outcome. LoadPrefix stops at
// the first one, so a row beyond a gap buys a resume nothing.
func TestCheckpointStep_ContiguityTracksGapsNotRows(t *testing.T) {
	ctx := context.Background()

	t.Run("a clean sequence advances", func(t *testing.T) {
		o := &Orchestrator{runID: "run-1", statusReporter: reporterFor(newFakeRunDoc(), 1), checkpointRepo: &fakeCheckpointStore{}}
		for step := 1; step <= 3; step++ {
			if err := o.checkpointStep(ctx, models.ExplorationStep{Step: step}, models.CheckpointArgs{}); err != nil {
				t.Fatalf("step %d: %v", step, err)
			}
		}
		if o.contiguousCheckpointThrough != 3 {
			t.Errorf("contiguousCheckpointThrough = %d, want 3", o.contiguousCheckpointThrough)
		}
		if !o.hasResumePoint() {
			t.Error("hasResumePoint() = false after three clean checkpoints")
		}
	})

	t.Run("a failed first write leaves nothing replayable", func(t *testing.T) {
		store := &fakeCheckpointStore{stepErr: errors.New("mongo is down")}
		o := &Orchestrator{runID: "run-1", statusReporter: reporterFor(newFakeRunDoc(), 1), checkpointRepo: store}
		// Step 1's write fails. The hook reports it; ExplorationEngine's own
		// checkpoint wrapper is what swallows an ordinary failure so the run
		// is never aborted over a Mongo hiccup — asserted in
		// TestCheckpointFailureDoesNotAbortTheRun. What matters here is what
		// it leaves behind.
		if err := o.checkpointStep(ctx, models.ExplorationStep{Step: 1}, models.CheckpointArgs{}); err == nil {
			t.Fatal("a failed SaveStep reported no error")
		}
		if o.contiguousCheckpointThrough != 0 {
			t.Fatalf("contiguousCheckpointThrough = %d after a failed write, want 0", o.contiguousCheckpointThrough)
		}
		// Step 2 succeeds, so a row exists and the index is worth keeping...
		store.stepErr = nil
		if err := o.checkpointStep(ctx, models.ExplorationStep{Step: 2}, models.CheckpointArgs{}); err != nil {
			t.Fatal(err)
		}
		if !o.keepStepIndex {
			t.Error("keepStepIndex = false though a row was saved")
		}
		// ...but there is still no prefix a resume could replay.
		if o.contiguousCheckpointThrough != 0 {
			t.Errorf("contiguousCheckpointThrough = %d, want 0 — step 1 is missing, so LoadPrefix stops at the gap", o.contiguousCheckpointThrough)
		}
		if o.hasResumePoint() {
			t.Error("hasResumePoint() = true with a gap at step 1; a pause here would strand the run in `paused` with a resume the API refuses")
		}
	})

	t.Run("a resumed run continues the prefix it was handed", func(t *testing.T) {
		o := &Orchestrator{
			runID: "run-1", statusReporter: reporterFor(newFakeRunDoc(), 2),
			checkpointRepo: &fakeCheckpointStore{},
			resume: &ResumeState{Attempt: 2, Checkpoints: &database.CheckpointSet{
				Steps: []models.ExplorationCheckpoint{{Step: models.ExplorationStep{Step: 1}}, {Step: models.ExplorationStep{Step: 2}}},
			}},
		}
		// What RunDiscovery does at the top: a replayable prefix is contiguous
		// by construction, so it is where this attempt's own contiguity starts.
		o.contiguousCheckpointThrough = o.resume.prefixLen()
		if err := o.checkpointStep(ctx, models.ExplorationStep{Step: 3}, models.CheckpointArgs{}); err != nil {
			t.Fatal(err)
		}
		if o.contiguousCheckpointThrough != 3 {
			t.Errorf("contiguousCheckpointThrough = %d, want 3", o.contiguousCheckpointThrough)
		}
	})
}

// TestCheckpointStep_WillNotPauseOntoAGap is the third round of the same
// finding, on the path the second round's fix did not cover.
//
// stopBefore guards the phase gates, but this seam is reached straight from
// the engine, so the gate never sees it. A transient step-1 write failure
// followed by a successful step-2 write therefore signalled a pause with no
// replayable prefix, and the orchestrator wrote `paused` for a run the resume
// endpoint then refuses — a working run stranded with no way out but cancel.
func TestCheckpointStep_WillNotPauseOntoAGap(t *testing.T) {
	ctx := context.Background()
	doc := newFakeRunDoc()
	doc.pauseRequested = true
	store := &fakeCheckpointStore{stepErr: errors.New("mongo is down")}
	o := &Orchestrator{
		runID: "run-1", projectID: "proj",
		statusReporter:   reporterFor(doc, 1),
		checkpointRepo:   store,
		attemptStartedAt: time.Now(),
	}

	// Step 1's write fails. Swallowed one layer up, so the run continues.
	if err := o.checkpointStep(ctx, models.ExplorationStep{Step: 1}, models.CheckpointArgs{}); err == nil {
		t.Fatal("a failed SaveStep reported no error")
	}

	// Step 2's lands, so a row now exists — but the prefix has a hole in it.
	store.stepErr = nil
	err := o.checkpointStep(ctx, models.ExplorationStep{Step: 2}, models.CheckpointArgs{})
	if err != nil {
		t.Fatalf("checkpointStep() = %v, want nil: there is nothing a resume could replay, so the pause must wait", err)
	}
	if errors.Is(err, ai.ErrPauseRequested) {
		t.Error("signalled a pause onto a gap; the run would land in `paused` with a resume the API refuses")
	}

	// And the request still stands, so the next contiguous step honours it.
	// Step 3 does not close the gap at step 1, so it must not pause either.
	if err := o.checkpointStep(ctx, models.ExplorationStep{Step: 3}, models.CheckpointArgs{}); err != nil {
		t.Fatalf("step 3: %v, want nil — the gap at step 1 is still there", err)
	}
	if len(doc.pauses) != 0 {
		t.Errorf("wrote %d pauses onto an unresumable run", len(doc.pauses))
	}
}

// TestCheckpointStep_PausesOnceTheFirstStepLands is the other side of it: the
// precondition must not withhold a pause for ever. A run whose first write
// lands has a replayable prefix and pauses at that very step.
func TestCheckpointStep_PausesOnceTheFirstStepLands(t *testing.T) {
	doc := newFakeRunDoc()
	doc.pauseRequested = true
	o := &Orchestrator{
		runID: "run-1", projectID: "proj",
		statusReporter:   reporterFor(doc, 1),
		checkpointRepo:   &fakeCheckpointStore{},
		attemptStartedAt: time.Now(),
	}

	err := o.checkpointStep(context.Background(), models.ExplorationStep{Step: 1}, models.CheckpointArgs{})
	if !errors.Is(err, ai.ErrPauseRequested) {
		t.Fatalf("checkpointStep() = %v, want ErrPauseRequested — step 1 is a replayable prefix of one", err)
	}
}

// TestHasResumePoint_AFailedFinalCheckpointIsAGapToo is round six of the same
// family, on the one hole the previous fix could not see.
//
// checkpointGapSeen was inferred from a LATER step landing beyond the hole,
// so a failure on the final exploration step left nothing to notice it — and
// a pause pending at the Phase 4 gate was honoured over a prefix one step
// short of the summary, which a resume has to re-execute. Steps arrive in
// sequence, so a failed write is the only way a hole appears; recording it at
// the failure is both simpler and complete.
func TestHasResumePoint_AFailedFinalCheckpointIsAGapToo(t *testing.T) {
	ctx := context.Background()
	store := &fakeCheckpointStore{}
	o := &Orchestrator{
		runID: "run-1", statusReporter: reporterFor(newFakeRunDoc(), 1),
		checkpointRepo: store,
	}

	for step := 1; step <= 3; step++ {
		if err := o.checkpointStep(ctx, models.ExplorationStep{Step: step}, models.CheckpointArgs{}); err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
	}
	if !o.hasResumePoint() {
		t.Fatal("three clean checkpoints should be a resume point")
	}

	// The LAST step's write fails, and nothing follows it.
	store.stepErr = errors.New("mongo is down")
	if err := o.checkpointStep(ctx, models.ExplorationStep{Step: 4}, models.CheckpointArgs{}); err == nil {
		t.Fatal("a failed SaveStep reported no error")
	}

	if !o.checkpointGapSeen {
		t.Error("the failed final write was not recorded as a gap")
	}
	if o.hasResumePoint() {
		t.Error("approved a pause over a prefix that is one step short of what the run executed; the resume must re-run that step")
	}
}

// TestPauseSignalIfResumable_IsTheOneGateEverySeamUses is the structural
// assertion behind three rounds of the same finding.
//
// Reviews found the replayability precondition missing at the phase gates,
// then at the exploration step seam, then at the completed-analysis-area
// seam — the same bug three times, because the decision was duplicated per
// seam. It now lives in one function; this pins that every seam routes
// through it, so a fourth seam cannot quietly reintroduce the class.
func TestPauseSignalIfResumable_IsTheOneGateEverySeamUses(t *testing.T) {
	src := func(f string) string {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		return string(b)
	}
	resume := src("orchestrator_resume.go")
	analysis := src("analysis_resume.go")

	// Every seam asks the shared helper.
	for _, want := range []string{
		`o.pauseSignalIfResumable(fmt.Sprintf("exploration step %d", step.Step))`,
		`o.pauseSignalIfResumable(fmt.Sprintf("replay of step %d", step))`,
	} {
		if !strings.Contains(resume, want) {
			t.Errorf("orchestrator_resume.go does not route through the shared pause guard: %s", want)
		}
	}
	if !strings.Contains(analysis, `o.pauseSignalIfResumable("analysis area " + step.AreaID)`) {
		t.Error("the analysis seam does not route through the shared pause guard")
	}
	// And no SEAM returns the sentinel directly, which is how the class got
	// in three times. Scoped to the seam functions on purpose:
	// pauseSignalIfResumable and pauseAttempt own the sentinel and return it
	// by design, so a file-wide check would forbid the very thing it is
	// asking for.
	body := func(src, fn string) string {
		start := strings.Index(src, "func (o *Orchestrator) "+fn+"(")
		if start < 0 {
			t.Fatalf("cannot find %s", fn)
		}
		rest := src[start:]
		if end := strings.Index(rest, "\n}\n"); end >= 0 {
			return rest[:end]
		}
		return rest
	}
	for _, seam := range []struct{ src, fn string }{
		{resume, "checkpointStep"},
		{resume, "fenceReplayedStep"},
		{analysis, "checkpointAnalysisArea"},
	} {
		for _, line := range strings.Split(body(seam.src, seam.fn), "\n") {
			if strings.TrimSpace(line) == "return ai.ErrPauseRequested" {
				t.Errorf("%s returns ai.ErrPauseRequested directly; it must go through pauseSignalIfResumable, or a run with a checkpoint gap can be paused into an unresumable state", seam.fn)
			}
		}
	}
}

// TestPauseEventCarriesModelProvenance — a paused attempt never reaches
// recordAttemptOutcome, which is where the terminal events get their
// provider/model. So the pause event is the only record of which model ran
// the attempt, and the case that matters is an operator who pauses, changes
// the model, and resumes.
func TestPauseEventCarriesModelProvenance(t *testing.T) {
	doc := newFakeRunDoc()
	o := &Orchestrator{
		runID: "run-1", statusReporter: reporterFor(doc, 2),
		attemptStartedAt: time.Now(),
		llmProvider:      "bedrock",
		llmModel:         "us.anthropic.claude-sonnet-4-6",
	}

	if err := o.pauseAttempt(context.Background(), "detail"); !errors.Is(err, ai.ErrPauseRequested) {
		t.Fatalf("pauseAttempt() = %v, want ErrPauseRequested", err)
	}
	if len(doc.pauseProvenance) != 1 {
		t.Fatalf("pause writes = %d, want 1", len(doc.pauseProvenance))
	}
	if doc.pauseProvenance[0] != "bedrock/us.anthropic.claude-sonnet-4-6" {
		t.Errorf("provenance = %q, want the attempt's provider/model", doc.pauseProvenance[0])
	}
}

// TestHasResumePoint_RefusesALossyPause is the fifth round's finding, and a
// different failure from the one the contiguity counter was added for.
//
// A run whose step-2 write failed still has a contiguous prefix of one, so
// "is the prefix non-empty" approves a pause — and LoadPrefix then truncates
// at the hole, so the resume discards every step after it and pays the
// warehouse and the model for them again. Resumable, but LOSSY, which breaks
// the same promise as unresumable: no re-paid exploration.
//
// A gap therefore disqualifies a pause for the rest of the attempt. It cannot
// heal — nothing backfills a missed step — so the honest outcome is that the
// request lapses and the operator is told, rather than a stop that quietly
// throws work away.
func TestHasResumePoint_RefusesALossyPause(t *testing.T) {
	ctx := context.Background()
	store := &fakeCheckpointStore{}
	o := &Orchestrator{
		runID: "run-1", statusReporter: reporterFor(newFakeRunDoc(), 1),
		checkpointRepo: store,
	}

	// Step 1 lands.
	if err := o.checkpointStep(ctx, models.ExplorationStep{Step: 1}, models.CheckpointArgs{}); err != nil {
		t.Fatal(err)
	}
	if !o.hasResumePoint() {
		t.Fatal("a one-step prefix should be a resume point")
	}

	// Step 2's write fails.
	store.stepErr = errors.New("mongo is down")
	if err := o.checkpointStep(ctx, models.ExplorationStep{Step: 2}, models.CheckpointArgs{}); err == nil {
		t.Fatal("a failed SaveStep reported no error")
	}
	store.stepErr = nil

	// Step 3 lands, beyond the hole.
	if err := o.checkpointStep(ctx, models.ExplorationStep{Step: 3}, models.CheckpointArgs{}); err != nil {
		t.Fatal(err)
	}
	if !o.checkpointGapSeen {
		t.Error("the hole at step 2 was not noticed")
	}
	if o.contiguousCheckpointThrough != 1 {
		t.Errorf("contiguousCheckpointThrough = %d, want 1 — the prefix stops at the hole", o.contiguousCheckpointThrough)
	}
	if o.hasResumePoint() {
		t.Error("approved a pause that would discard step 3's work on resume")
	}

	// And a later step does not rehabilitate it: the hole is still there.
	if err := o.checkpointStep(ctx, models.ExplorationStep{Step: 4}, models.CheckpointArgs{}); err != nil {
		t.Fatal(err)
	}
	if o.hasResumePoint() {
		t.Error("a gap healed itself; nothing backfills a missed step")
	}
}

// TestPauseStampsItsOwnResumeMarker is round seven's finding, and the
// user-visible dead end it describes.
//
// MarkExplorationCheckpoint deliberately returns true when its write ERRORS —
// documented as costing the dashboard's Resume affordance rather than the
// run, which is the right trade for a failed run. For a paused one it is not:
// the dashboard gates Resume on last_checkpoint_step, so a Mongo blip on the
// very step an operator paused at would leave a perfectly resumable run
// showing only Cancel, with the API as the only way back in.
//
// So the pause stamps the marker itself, from the prefix it knows it has.
func TestPauseStampsItsOwnResumeMarker(t *testing.T) {
	doc := newFakeRunDoc()
	o := &Orchestrator{
		runID: "run-1", statusReporter: reporterFor(doc, 1),
		attemptStartedAt: time.Now(),
	}
	o.contiguousCheckpointThrough = 6

	if err := o.pauseAttempt(context.Background(), "detail"); !errors.Is(err, ai.ErrPauseRequested) {
		t.Fatalf("pauseAttempt() = %v, want ErrPauseRequested", err)
	}
	if len(doc.pauseMarkers) != 1 {
		t.Fatalf("pause writes = %d, want 1", len(doc.pauseMarkers))
	}
	if doc.pauseMarkers[0] != 6 {
		t.Errorf("stamped marker = %d, want 6 — the dashboard gates Resume on this, so a paused run without it offers only Cancel", doc.pauseMarkers[0])
	}
}

// TestPauseMarker_SurvivesAFailedPerStepMarkerWrite is the same finding from
// the other end: the per-step marker write failing must not cost the paused
// run its Resume button, because the pause no longer depends on it.
func TestPauseMarker_SurvivesAFailedPerStepMarkerWrite(t *testing.T) {
	doc := newFakeRunDoc()
	doc.pauseRequested = true
	// writeErr makes every run-document write fail, which is how a Mongo
	// hiccup reaches the reporter — MarkExplorationCheckpoint still answers
	// "carry on", by design.
	doc.writeErr = errors.New("mongo is down")
	store := &fakeCheckpointStore{}
	o := &Orchestrator{
		runID: "run-1", projectID: "proj",
		statusReporter:   reporterFor(doc, 1),
		checkpointRepo:   store,
		attemptStartedAt: time.Now(),
	}

	// Step 1's checkpoint ROW lands even though the marker write errors.
	err := o.checkpointStep(context.Background(), models.ExplorationStep{Step: 1}, models.CheckpointArgs{})
	if !errors.Is(err, ai.ErrPauseRequested) {
		t.Fatalf("checkpointStep() = %v, want ErrPauseRequested — a failed marker write must not cost the pause", err)
	}
	if len(store.steps) != 1 {
		t.Fatalf("checkpoint rows = %d, want 1", len(store.steps))
	}
	// And the contiguity the pause will stamp reflects the row that landed.
	if o.contiguousCheckpointThrough != 1 {
		t.Errorf("contiguousCheckpointThrough = %d, want 1", o.contiguousCheckpointThrough)
	}
}
