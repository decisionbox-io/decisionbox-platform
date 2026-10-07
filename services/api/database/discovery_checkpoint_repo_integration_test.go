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
func seedCheckpointSummary(t *testing.T, ctx context.Context, runID string) {
	t.Helper()
	_, err := testDB.Collection(gomongo.CollectionDiscoveryCheckpoints).InsertOne(ctx, bson.M{
		"run_id":      runID,
		"project_id":  "proj-integ",
		"step_number": 0,
		"attempt":     1,
		"kind":        "exploration_summary",
		"completed":   true,
		"total_steps": 3,
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
		name       string
		steps      []int
		summary    bool
		wantPrefix int
		wantDone   bool
	}{
		{"nothing written", nil, false, 0, false},
		{"a clean prefix", []int{1, 2, 3}, false, 3, false},
		{"a hole at 3", []int{1, 2, 4, 5}, false, 2, false},
		{"a hole at 1", []int{2, 3}, false, 0, false},
		{"exploration finished", []int{1, 2, 3}, true, 3, true},
		// Resumable with no replayable steps at all: the summary landed and
		// the step rows have since been pruned, so the run goes straight to
		// analysis.
		{"summary only", nil, true, 0, true},
		// The gap is the stronger fact — a summary over a broken prefix must
		// not send the run to analysis over an incomplete step set.
		{"summary over a hole", []int{1, 3}, true, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dropCheckpoints(t, ctx)
			for _, s := range tc.steps {
				seedCheckpointStep(t, ctx, "run-1", s)
			}
			if tc.summary {
				seedCheckpointSummary(t, ctx, "run-1")
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
