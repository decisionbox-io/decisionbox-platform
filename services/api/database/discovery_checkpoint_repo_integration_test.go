//go:build integration

package database

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// mustOID parses a hex run id back into the ObjectId the collection is keyed
// on, for the few places a test writes a field the repository has no setter
// for.
func mustOID(t *testing.T, hex string) primitive.ObjectID {
	t.Helper()
	oid, err := primitive.ObjectIDFromHex(hex)
	if err != nil {
		t.Fatalf("invalid run id %q: %v", hex, err)
	}
	return oid
}

// dropCheckpoints wipes the checkpoint collection between tests. The
// package-level testcontainer is shared across the suite.
func dropCheckpoints(t *testing.T, ctx context.Context) {
	t.Helper()
	if _, err := testDB.Collection(gomongo.CollectionDiscoveryCheckpoints).DeleteMany(ctx, bson.M{}); err != nil {
		t.Fatalf("drop checkpoints: %v", err)
	}
}

// seedCheckpointStep writes one checkpoint row the way the agent would.
func seedCheckpointStep(t *testing.T, ctx context.Context, runID string, step int) {
	t.Helper()
	_, err := testDB.Collection(gomongo.CollectionDiscoveryCheckpoints).InsertOne(ctx, bson.M{
		"run_id":      runID,
		"project_id":  "proj-integ",
		"step_number": step,
		"attempt":     1,
		"kind":        "step",
		"created_at":  time.Now(),
	})
	if err != nil {
		t.Fatalf("seed checkpoint step %d: %v", step, err)
	}
}

// seedCheckpointSummary writes the exploration-summary row (step_number 0).
func seedCheckpointSummary(t *testing.T, ctx context.Context, runID string, totalSteps int) {
	t.Helper()
	_, err := testDB.Collection(gomongo.CollectionDiscoveryCheckpoints).InsertOne(ctx, bson.M{
		"run_id":      runID,
		"project_id":  "proj-integ",
		"step_number": 0,
		"attempt":     1,
		"kind":        "exploration_summary",
		"completed":   true,
		"total_steps": totalSteps,
		"created_at":  time.Now(),
	})
	if err != nil {
		t.Fatalf("seed checkpoint summary: %v", err)
	}
}

// TestInteg_CheckpointRepo_ResumeStateCountsTheReplayablePrefix pins what the
// API promises the operator. It reports the length of the CONTIGUOUS prefix,
// not the row count: a hole means an earlier checkpoint write failed, and
// counting the rows after it would let the API offer a resume it cannot
// deliver.
func TestInteg_CheckpointRepo_ResumeStateCountsTheReplayablePrefix(t *testing.T) {
	ctx := context.Background()
	dropCheckpoints(t, ctx)
	repo := NewDiscoveryCheckpointRepository(testDB)

	cases := []struct {
		name  string
		steps []int
		// summaryTotalSteps > 0 writes the exploration-summary row claiming
		// that many steps; 0 writes none.
		summaryTotalSteps int
		wantPrefix        int
		wantDone          bool
	}{
		{"nothing written", nil, 0, 0, false},
		{"a clean prefix", []int{1, 2, 3}, 0, 3, false},
		{"a hole at 3", []int{1, 2, 4, 5}, 0, 2, false},
		{"a hole at 1", []int{2, 3}, 0, 0, false},
		{"exploration finished", []int{1, 2, 3}, 3, 3, true},
		// Resumable with no replayable steps at all: the summary landed and
		// the step rows have since been pruned, so the run goes straight to
		// analysis.
		{"summary only", nil, 0, 0, false},
		{"summary only, claiming nothing", nil, -1, 0, true},
		// The gap is the stronger fact — a summary over a broken prefix must
		// not send the run to analysis over an incomplete step set.
		{"summary over a hole", []int{1, 3}, 3, 1, false},
		// The shape a gap check alone misses: the write that failed was for
		// the LAST step, so the prefix looks clean while the summary claims
		// one more than it holds.
		{"summary claiming more than the prefix holds", []int{1, 2}, 3, 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dropCheckpoints(t, ctx)
			for _, s := range tc.steps {
				seedCheckpointStep(t, ctx, "run-1", s)
			}
			if tc.summaryTotalSteps != 0 {
				total := tc.summaryTotalSteps
				if total < 0 {
					total = 0
				}
				seedCheckpointSummary(t, ctx, "run-1", total)
			}

			prefix, done, err := repo.ResumeState(ctx, "run-1")
			if err != nil {
				t.Fatalf("ResumeState: %v", err)
			}
			if prefix != tc.wantPrefix {
				t.Errorf("prefix len = %d, want %d", prefix, tc.wantPrefix)
			}
			if done != tc.wantDone {
				t.Errorf("explorationComplete = %v, want %v", done, tc.wantDone)
			}
		})
	}
}

// TestInteg_CheckpointRepo_IsRunScoped pins that neither the read nor the
// purge can reach another run's rows. The purge fires on cancel, so
// over-reaching would silently make an unrelated in-flight run unresumable.
func TestInteg_CheckpointRepo_IsRunScoped(t *testing.T) {
	ctx := context.Background()
	dropCheckpoints(t, ctx)
	repo := NewDiscoveryCheckpointRepository(testDB)

	for _, run := range []string{"run-a", "run-b"} {
		seedCheckpointStep(t, ctx, run, 1)
		seedCheckpointStep(t, ctx, run, 2)
	}

	if prefix, _, _ := repo.ResumeState(ctx, "run-a"); prefix != 2 {
		t.Errorf("run-a prefix = %d, want 2", prefix)
	}

	deleted, err := repo.DeleteByRun(ctx, "run-a")
	if err != nil {
		t.Fatalf("DeleteByRun: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}
	if prefix, _, _ := repo.ResumeState(ctx, "run-a"); prefix != 0 {
		t.Errorf("run-a still has a prefix of %d", prefix)
	}
	if prefix, _, _ := repo.ResumeState(ctx, "run-b"); prefix != 2 {
		t.Errorf("run-b lost checkpoints: prefix = %d, want 2", prefix)
	}
}

// TestInteg_RunRepo_BeginResumeOnlyFlipsAFailedRun pins the status gate at
// the storage layer, where it is the actual guard rather than a convenience
// check in the handler.
func TestInteg_RunRepo_BeginResumeOnlyFlipsAFailedRun(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	for _, status := range []string{"pending", "running", "completed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			dropRuns(t, ctx)
			runID := seedRun(t, ctx, status, nil, nil, time.Now())

			if _, err := repo.BeginResume(ctx, runID); !errors.Is(err, ErrNoResumableRun) {
				t.Errorf("BeginResume on a %s run = %v, want ErrNoResumableRun", status, err)
			}
			run, _ := repo.GetByID(ctx, runID)
			if run.Status != status {
				t.Errorf("the run's status changed to %q; a refused resume must not touch it", run.Status)
			}
		})
	}

	t.Run("unknown run", func(t *testing.T) {
		dropRuns(t, ctx)
		// A valid ObjectId that no document uses.
		gone := seedRun(t, ctx, "failed", nil, nil, time.Now())
		dropRuns(t, ctx)
		if _, err := repo.BeginResume(ctx, gone); !errors.Is(err, ErrNoResumableRun) {
			t.Errorf("BeginResume on a deleted run = %v, want ErrNoResumableRun", err)
		}
	})
}

// TestInteg_RunRepo_BeginResumeClearsThePreviousFailure pins what the run
// document looks like to the dashboard after a resume: running, with no
// leftover error or completion time from the attempt that died. The failure
// is not lost — the lifecycle log keeps it.
func TestInteg_RunRepo_BeginResumeClearsThePreviousFailure(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	completed := time.Now().Add(-time.Hour)
	runID := seedRun(t, ctx, "failed", nil, &completed, time.Now().Add(-2*time.Hour))
	if _, err := testDB.Collection("discovery_runs").UpdateByID(ctx, mustOID(t, runID), bson.M{
		"$set": bson.M{"error": "stale: API restarted while run was in progress", "attempt": 1},
	}); err != nil {
		t.Fatal(err)
	}

	resumed, err := repo.BeginResume(ctx, runID)
	if err != nil {
		t.Fatalf("BeginResume: %v", err)
	}
	if resumed.Status != "running" {
		t.Errorf("status = %q, want running", resumed.Status)
	}
	if resumed.Attempt != 2 {
		t.Errorf("attempt = %d, want 2", resumed.Attempt)
	}
	if resumed.LastResumedAt == nil {
		t.Error("last_resumed_at must be stamped")
	}
	if resumed.Error != "" {
		t.Errorf("error = %q, want it cleared — a running run with an error reads as broken", resumed.Error)
	}
	if resumed.CompletedAt != nil {
		t.Errorf("completed_at = %v, want it cleared", resumed.CompletedAt)
	}
	// The failure and the resume both survive in the lifecycle log.
	if len(resumed.Lifecycle) != 1 {
		t.Fatalf("lifecycle = %d events, want the resume event", len(resumed.Lifecycle))
	}
	ev := resumed.Lifecycle[0]
	if ev.Status != "running" || ev.Attempt != 2 {
		t.Errorf("lifecycle event = %+v, want running/attempt 2", ev)
	}
}

// TestInteg_RunRepo_BeginResumeIsAtomicUnderConcurrency is the race the
// endpoint exists to close. Two agents on one run id would mean two processes
// writing the same run's checkpoints and results.
func TestInteg_RunRepo_BeginResumeIsAtomicUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)
	runID := seedRun(t, ctx, "failed", nil, nil, time.Now())

	const racers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	won, refused, other := 0, 0, 0

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.BeginResume(ctx, runID)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case errors.Is(err, ErrNoResumableRun):
				refused++
			default:
				other++
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if won != 1 {
		t.Errorf("%d callers won the flip, want exactly 1 — two agents would write one run", won)
	}
	if refused != racers-1 {
		t.Errorf("%d callers refused, want %d", refused, racers-1)
	}

	run, _ := repo.GetByID(ctx, runID)
	if run.Attempt != 2 {
		t.Errorf("attempt = %d, want 2 — only the winning flip may increment it", run.Attempt)
	}
}

// TestInteg_RunRepo_CreatePersistsTheRunsOwnParameters is the prerequisite
// for resume replaying the operator's budget rather than the agent's
// defaults. Nothing recorded these before.
func TestInteg_RunRepo_CreatePersistsTheRunsOwnParameters(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	runID, err := repo.Create(ctx, "proj-integ", models.RunParams{
		MaxSteps: 80, MinSteps: 48,
		Areas:  []string{"churn", "monetization"},
		Effort: "high", Source: "schedule:nightly",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	run, err := repo.GetByID(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Attempt != 1 {
		t.Errorf("attempt = %d, want 1", run.Attempt)
	}
	if run.MaxSteps != 80 || run.MinSteps != 48 {
		t.Errorf("step budget = (%d, %d), want (80, 48)", run.MaxSteps, run.MinSteps)
	}
	if len(run.Areas) != 2 || run.Areas[0] != "churn" {
		t.Errorf("areas = %v", run.Areas)
	}
	if run.Effort != "high" {
		t.Errorf("effort = %q, want high", run.Effort)
	}
	if len(run.Lifecycle) != 1 || run.Lifecycle[0].Status != "pending" {
		t.Fatalf("lifecycle = %+v, want one pending event", run.Lifecycle)
	}
	if run.Lifecycle[0].Reason != "schedule:nightly" {
		t.Errorf("the first lifecycle event must record what triggered the run, got %q", run.Lifecycle[0].Reason)
	}

	// A resume then reads them straight back off the document.
	if _, err := testDB.Collection("discovery_runs").UpdateByID(ctx, mustOID(t, runID), bson.M{
		"$set": bson.M{"status": "failed"},
	}); err != nil {
		t.Fatal(err)
	}
	resumed, err := repo.BeginResume(ctx, runID)
	if err != nil {
		t.Fatalf("BeginResume: %v", err)
	}
	if resumed.MaxSteps != 80 || resumed.MinSteps != 48 || len(resumed.Areas) != 2 {
		t.Errorf("a resumed run lost its parameters: max=%d min=%d areas=%v",
			resumed.MaxSteps, resumed.MinSteps, resumed.Areas)
	}
}

// TestInteg_RunRepo_BeginResumeOnALegacyRunReportsAttemptTwo covers a run
// created before the attempt counter existed: it reads as 0, so its first
// resume must still be attempt 2 — the K8s Job name suffix depends on it.
func TestInteg_RunRepo_BeginResumeOnALegacyRunReportsAttemptTwo(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)
	runID := seedRun(t, ctx, "failed", nil, nil, time.Now()) // no attempt field

	resumed, err := repo.BeginResume(ctx, runID)
	if err != nil {
		t.Fatalf("BeginResume: %v", err)
	}
	if resumed.Attempt != 2 {
		t.Errorf("attempt = %d, want 2 — a legacy run's first resume is still its second attempt", resumed.Attempt)
	}
	if len(resumed.Lifecycle) != 1 || resumed.Lifecycle[0].Attempt != 2 {
		t.Errorf("the lifecycle event must agree with the reported attempt: %+v", resumed.Lifecycle)
	}
	// And the STORED counter agrees too. $inc on a missing field would leave
	// it at 1 — a document claiming a resumed run is on its first attempt,
	// disagreeing with the event beside it.
	stored, err := repo.GetByID(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Attempt != 2 {
		t.Errorf("stored attempt = %d, want 2", stored.Attempt)
	}
}

// TestInteg_RunRepo_BeginResumeReArmsTheCompletionHooks pins the fix for a
// silent hook loss.
//
// A failed run is terminal, so the completion-hook dispatcher fires its hooks
// and stamps `completion_hooks_fired_at`. If a resume left that in place,
// ListTerminalWithoutCompletionHook would filter the run out forever — and
// when the resumed attempt finally produced a result, no plugin would ever
// see it. The run has a terminal outcome still to come, so it has to read as
// dispatch-pending again.
func TestInteg_RunRepo_BeginResumeReArmsTheCompletionHooks(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	fired := time.Now().Add(-30 * time.Minute)
	completed := time.Now().Add(-time.Hour)
	runID := seedRun(t, ctx, "failed", &fired, &completed, time.Now().Add(-2*time.Hour))

	// Before the resume the dispatcher correctly ignores it.
	pending, err := repo.ListTerminalWithoutCompletionHook(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range pending {
		if r.ID == runID {
			t.Fatal("test setup is wrong: the run should already be marked hooks-fired")
		}
	}

	if _, err := repo.BeginResume(ctx, runID); err != nil {
		t.Fatalf("BeginResume: %v", err)
	}

	run, err := repo.GetByID(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.CompletionHooksFiredAt != nil {
		t.Errorf("completion_hooks_fired_at = %v, want cleared — otherwise the resumed attempt's result never reaches a hook", run.CompletionHooksFiredAt)
	}

	// Once the resumed attempt terminates, the dispatcher picks it up again.
	if _, err := testDB.Collection("discovery_runs").UpdateByID(ctx, mustOID(t, runID), bson.M{
		"$set": bson.M{"status": "completed", "completed_at": time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	pending, err = repo.ListTerminalWithoutCompletionHook(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range pending {
		if r.ID == runID {
			found = true
		}
	}
	if !found {
		t.Error("the completed resumed run is not dispatch-pending; its hooks will never fire")
	}
}

// TestInteg_RunRepo_BeginResumeDropsTheStaleResultPointer pins that a resumed
// run stops claiming the previous attempt's output.
//
// The resume re-arms completion-hook dispatch. Leaving discovery_id pointing
// at the failed attempt's partial result would aim those re-fired hooks at
// output this attempt did not produce — and if the resumed attempt fails
// before saving anything, that stale result is all a consumer would ever see
// for it.
func TestInteg_RunRepo_BeginResumeDropsTheStaleResultPointer(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	fired := time.Now().Add(-time.Minute)
	runID := seedRun(t, ctx, "failed", &fired, nil, time.Now())
	if _, err := testDB.Collection("discovery_runs").UpdateByID(ctx, mustOID(t, runID), bson.M{
		"$set": bson.M{"discovery_id": "disc-from-attempt-1", "attempt": 1},
	}); err != nil {
		t.Fatal(err)
	}

	resumed, err := repo.BeginResume(ctx, runID)
	if err != nil {
		t.Fatalf("BeginResume: %v", err)
	}
	if resumed.DiscoveryID != "" {
		t.Errorf("discovery_id = %q, want cleared — the resumed attempt has produced nothing yet", resumed.DiscoveryID)
	}

	// And the run now reads as dispatch-pending with no result, which is
	// exactly what a fresh run that failed early looks like.
	stored, err := repo.GetByID(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DiscoveryID != "" {
		t.Errorf("stored discovery_id = %q, want cleared", stored.DiscoveryID)
	}
	if stored.CompletionHooksFiredAt != nil {
		t.Error("completion_hooks_fired_at must be cleared alongside it")
	}
}

// TestInteg_RunRepo_HookMarkIsFencedOnTheSelectedAttempt closes the second
// half of the hook-marker race, which a status fence alone does not reach.
//
// The dispatcher reads a batch of terminal runs and marks each one after. A
// resume landing in between clears the marker — and if the RESUMED attempt
// then reaches a terminal state before the stale mark arrives, a status-only
// filter matches again and stamps the marker for an attempt whose hooks were
// never dispatched. ListTerminalWithoutCompletionHook then skips it for ever.
func TestInteg_RunRepo_HookMarkIsFencedOnTheSelectedAttempt(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	runID := seedRun(t, ctx, "failed", nil, nil, time.Now())
	if _, err := testDB.Collection("discovery_runs").UpdateByID(ctx, mustOID(t, runID), bson.M{
		"$set": bson.M{"attempt": 1},
	}); err != nil {
		t.Fatal(err)
	}

	// The dispatcher selects attempt 1...
	pending, err := repo.ListTerminalWithoutCompletionHook(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Attempt != 1 {
		t.Fatalf("selected %d runs (attempt %v), want one on attempt 1", len(pending), pending)
	}

	// ...a resume advances it, and attempt 2 finishes before the stale mark.
	if _, err := repo.BeginResume(ctx, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.Collection("discovery_runs").UpdateByID(ctx, mustOID(t, runID), bson.M{
		"$set": bson.M{"status": "completed", "completed_at": time.Now()},
	}); err != nil {
		t.Fatal(err)
	}

	// The stale mark names attempt 1 and must not land.
	if err := repo.MarkCompletionHooksFired(ctx, runID, 1); err != nil {
		t.Fatalf("a no-op mark must not be an error: %v", err)
	}
	run, _ := repo.GetByID(ctx, runID)
	if run.CompletionHooksFiredAt != nil {
		t.Error("a mark for attempt 1 landed on attempt 2; its hooks would never fire")
	}

	// Attempt 2 is still dispatch-pending, and ITS mark sticks.
	pending, _ = repo.ListTerminalWithoutCompletionHook(ctx, 50)
	found := false
	for _, r := range pending {
		if r.ID == runID {
			found = true
			if r.Attempt != 2 {
				t.Errorf("selected attempt %d, want 2", r.Attempt)
			}
		}
	}
	if !found {
		t.Fatal("the completed resumed attempt is not dispatch-pending")
	}
	if err := repo.MarkCompletionHooksFired(ctx, runID, 2); err != nil {
		t.Fatal(err)
	}
	run, _ = repo.GetByID(ctx, runID)
	if run.CompletionHooksFiredAt == nil {
		t.Error("the mark for the attempt that was actually dispatched must land")
	}
}

// TestInteg_RunRepo_FailAttemptIgnoresASupersededAttempt is the P1 race.
//
// A previous attempt's background watcher outlives the attempt itself — the
// K8s Job watcher polls, the Docker watcher waits on the container — and the
// agent writes its own `failed` status before the watcher observes the dead
// Job. So: agent writes failed → operator resumes → run is `running` again →
// the OLD watcher finally fires. An unguarded Fail there marks the LIVE
// attempt failed on behalf of the dead one, killing a run that is working.
func TestInteg_RunRepo_FailAttemptIgnoresASupersededAttempt(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	runID, err := repo.Create(ctx, "proj-integ", models.RunParams{MaxSteps: 50, MinSteps: 30})
	if err != nil {
		t.Fatal(err)
	}
	// Attempt 1 fails, the operator resumes, attempt 2 is live.
	if _, err := repo.FailAttempt(ctx, runID, 1, "agent exited: signal: killed"); err != nil {
		t.Fatal(err)
	}
	resumed, err := repo.BeginResume(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Attempt != 2 || resumed.Status != "running" {
		t.Fatalf("resumed run = attempt %d / %q, want 2 / running", resumed.Attempt, resumed.Status)
	}

	// Attempt 1's watcher finally fires. It must not touch attempt 2.
	applied, err := repo.FailAttempt(ctx, runID, 1, "job failed (observed late)")
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Error("a stale callback from attempt 1 was applied to the live attempt 2")
	}
	run, _ := repo.GetByID(ctx, runID)
	if run.Status != "running" {
		t.Errorf("run status = %q, want running — a dead attempt's watcher killed the live one", run.Status)
	}
	if run.Error != "" {
		t.Errorf("run error = %q, want it untouched", run.Error)
	}

	// The LIVE attempt's own callback still works.
	applied, err = repo.FailAttempt(ctx, runID, 2, "attempt 2 died too")
	if err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("the live attempt's own failure callback was ignored")
	}
	run, _ = repo.GetByID(ctx, runID)
	if run.Status != "failed" || run.Error != "attempt 2 died too" {
		t.Errorf("run = %q / %q, want failed with attempt 2's error", run.Status, run.Error)
	}
}

// TestInteg_RunRepo_FailAttemptOnALegacyRun covers a run created before the
// attempt counter existed: it carries no attempt field, and its watcher
// reports attempt 1, so the guard must still let it through.
func TestInteg_RunRepo_FailAttemptOnALegacyRun(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)
	runID := seedRun(t, ctx, "running", nil, nil, time.Now()) // no attempt field

	applied, err := repo.FailAttempt(ctx, runID, 1, "agent crashed")
	if err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Error("a legacy run with no attempt field must still be failable by its attempt-1 watcher")
	}
	run, _ := repo.GetByID(ctx, runID)
	if run.Status != "failed" {
		t.Errorf("status = %q, want failed", run.Status)
	}
}

// TestInteg_RunRepo_FailAttemptLeavesTerminalRunsAlone keeps the invariant
// Fail already had: a run that reached a terminal status is not flipped back
// by a late watcher. The K8s watcher's exhaustion fallback fires OnFailure
// even when the agent has already stamped Complete.
func TestInteg_RunRepo_FailAttemptLeavesTerminalRunsAlone(t *testing.T) {
	ctx := context.Background()
	repo := NewRunRepository(testDB)

	for _, status := range []string{"completed", "cancelled", "failed"} {
		t.Run(status, func(t *testing.T) {
			dropRuns(t, ctx)
			runID := seedRun(t, ctx, status, nil, nil, time.Now())

			applied, err := repo.FailAttempt(ctx, runID, 1, "late watcher")
			if err != nil {
				t.Fatal(err)
			}
			if applied {
				t.Errorf("a %s run must not be flipped to failed by a late watcher", status)
			}
			run, _ := repo.GetByID(ctx, runID)
			if run.Status != status {
				t.Errorf("status = %q, want %q", run.Status, status)
			}
		})
	}
}

// TestInteg_RunRepo_HookMarkDoesNotLandOnAResumedRun closes the race the
// resume's hook-marker clearing opens.
//
// The dispatcher selects a batch of terminal runs and marks each one
// afterwards. A resume can land in between: it clears the marker to re-arm
// dispatch, and an unfenced mark would stamp it straight back onto the
// now-RUNNING resumed attempt. When that attempt finished,
// ListTerminalWithoutCompletionHook would filter it out and the hooks for the
// final discovery would never fire — the bug the clearing exists to prevent,
// reintroduced by a race.
func TestInteg_RunRepo_HookMarkDoesNotLandOnAResumedRun(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	runID := seedRun(t, ctx, "failed", nil, nil, time.Now())

	// The dispatcher has selected this run and is about to mark it...
	pending, err := repo.ListTerminalWithoutCompletionHook(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("dispatch-pending runs = %d, want 1", len(pending))
	}

	// ...but a resume gets there first.
	if _, err := repo.BeginResume(ctx, runID); err != nil {
		t.Fatal(err)
	}

	// The late mark carries the attempt the dispatcher read off the run it
	// selected, and must not land on the running attempt.
	if err := repo.MarkCompletionHooksFired(ctx, runID, pending[0].Attempt); err != nil {
		t.Fatalf("a no-op mark must not be an error: %v", err)
	}
	run, err := repo.GetByID(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.CompletionHooksFiredAt != nil {
		t.Error("a stale mark landed on the resumed attempt; its hooks would never fire")
	}

	// And once the resumed attempt terminates, the dispatcher picks it up and
	// the mark sticks.
	if _, err := testDB.Collection("discovery_runs").UpdateByID(ctx, mustOID(t, runID), bson.M{
		"$set": bson.M{"status": "completed", "completed_at": time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	rescanned, err := repo.ListTerminalWithoutCompletionHook(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rescanned) != 1 {
		t.Fatalf("re-scanned runs = %d, want 1 (the resumed attempt must be dispatchable)", len(rescanned))
	}
	if err := repo.MarkCompletionHooksFired(ctx, runID, rescanned[0].Attempt); err != nil {
		t.Fatal(err)
	}
	run, _ = repo.GetByID(ctx, runID)
	if run.CompletionHooksFiredAt == nil {
		t.Error("the mark must land once the run is terminal again")
	}

	// And the stale mark still cannot land, even now that the run is
	// terminal again — that is the whole point of the attempt fence.
	if _, err := testDB.Collection("discovery_runs").UpdateByID(ctx, mustOID(t, runID), bson.M{
		"$unset": bson.M{"completion_hooks_fired_at": ""},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkCompletionHooksFired(ctx, runID, pending[0].Attempt); err != nil {
		t.Fatalf("a no-op mark must not be an error: %v", err)
	}
	run, _ = repo.GetByID(ctx, runID)
	if run.CompletionHooksFiredAt != nil {
		t.Error("a stale mark landed on a later attempt; its hooks would never fire")
	}
}
