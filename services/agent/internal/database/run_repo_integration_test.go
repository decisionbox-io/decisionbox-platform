//go:build integration

package database

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TestRunRepository_Complete_StampsDiscoveryID is the round-trip
// guard for the discovery_id back-reference. The agent calls
// Complete(runID, discoveryID, insightsFound) immediately after
// saving the discovery document; the test confirms the field lands
// in Mongo and is readable on subsequent fetches — without the
// stamp, run-completion hook consumers can't query insights /
// recommendations (both keyed on discovery_id).
func TestRunRepository_Complete_StampsDiscoveryID(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewRunRepository(db)

	runID, err := repo.Create(ctx, &models.DiscoveryRun{ProjectID: "proj-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const discoveryID = "69f64ae5494f0c382c059adf"
	if _, err := repo.Complete(ctx, runID, discoveryID, 7, 0); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	oid, _ := primitive.ObjectIDFromHex(runID)
	var got models.DiscoveryRun
	if err := db.Collection("discovery_runs").FindOne(ctx, bson.M{"_id": oid}).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.DiscoveryID != discoveryID {
		t.Errorf("DiscoveryID = %q, want %q", got.DiscoveryID, discoveryID)
	}
	if got.Status != models.RunStatusCompleted {
		t.Errorf("Status = %q, want %q", got.Status, models.RunStatusCompleted)
	}
	if got.InsightsFound != 7 {
		t.Errorf("InsightsFound = %d, want 7", got.InsightsFound)
	}
}

// TestRunRepository_Complete_RejectsEmptyDiscoveryID encodes the
// "discovery_id is required" contract: a caller that forgets to
// pass it gets a clear error instead of a silently half-completed
// run that hook consumers later trip over.
func TestRunRepository_Complete_RejectsEmptyDiscoveryID(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewRunRepository(db)

	runID, err := repo.Create(ctx, &models.DiscoveryRun{ProjectID: "proj-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = repo.Complete(ctx, runID, "", 0, 0)
	if err == nil {
		t.Fatal("Complete accepted empty discovery_id")
	}
	if !strings.Contains(err.Error(), "discovery_id") {
		t.Errorf("err = %v, want it to mention the missing discovery_id", err)
	}
}

// TestRunRepository_Complete_InvalidRunIDErrors confirms the
// existing invalid-hex guard still surfaces — even with the new
// signature, callers passing a malformed run id should fail loudly
// rather than write to an unknown document.
func TestRunRepository_Complete_InvalidRunIDErrors(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewRunRepository(db)
	if _, err := repo.Complete(ctx, "not-a-hex", "disc-1", 1, 0); err == nil {
		t.Fatal("Complete accepted malformed run id")
	}
}

// TestRunRepository_Fail_DoesNotOverwriteCompleted is the codex r11
// [P2] regression guard. The K8s watcher's exhaustion fallback (and
// any other defense-in-depth Fail path) MUST NOT flip a run from a
// terminal status back to failed — that would obliterate a
// successful discovery hours after the agent stamped completed.
func TestRunRepository_Fail_DoesNotOverwriteCompleted(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewRunRepository(db)

	runID, err := repo.Create(ctx, &models.DiscoveryRun{ProjectID: "proj-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const discoveryID = "69f64ae5494f0c382c059adf"
	if _, err := repo.Complete(ctx, runID, discoveryID, 7, 0); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Simulate the watcher exhaustion path firing AFTER Complete.
	if _, err := repo.Fail(ctx, runID, "", "watcher exhausted", 0); err != nil {
		t.Fatalf("Fail returned error: %v", err)
	}

	oid, _ := primitive.ObjectIDFromHex(runID)
	var got models.DiscoveryRun
	if err := db.Collection("discovery_runs").FindOne(ctx, bson.M{"_id": oid}).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != models.RunStatusCompleted {
		t.Errorf("Status = %q after late Fail, want %q (terminal-status guard missing)", got.Status, models.RunStatusCompleted)
	}
	if got.DiscoveryID != discoveryID {
		t.Errorf("DiscoveryID = %q after late Fail, want %q", got.DiscoveryID, discoveryID)
	}
}

// TestRunRepository_Fail_DoesNotOverwriteCancelled is the other half
// of the terminal-status guard — a user cancellation must not be
// flipped to failed by an in-flight watcher's exhaustion fallback.
func TestRunRepository_Fail_DoesNotOverwriteCancelled(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewRunRepository(db)

	runID, err := repo.Create(ctx, &models.DiscoveryRun{ProjectID: "proj-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Stamp the cancelled status directly — the agent-side repo
	// doesn't expose Cancel, but the cancel-handler path writes
	// "cancelled" to the same collection.
	oid, _ := primitive.ObjectIDFromHex(runID)
	if _, err := db.Collection("discovery_runs").UpdateByID(ctx, oid, bson.M{"$set": bson.M{"status": "cancelled"}}); err != nil {
		t.Fatalf("seed cancelled: %v", err)
	}

	if _, err := repo.Fail(ctx, runID, "", "watcher exhausted", 0); err != nil {
		t.Fatalf("Fail returned error: %v", err)
	}

	var got models.DiscoveryRun
	if err := db.Collection("discovery_runs").FindOne(ctx, bson.M{"_id": oid}).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != "cancelled" {
		t.Errorf("Status = %q after late Fail, want cancelled", got.Status)
	}
}

// TestRunRepository_Fail_UpdatesRunningRuns is the positive case for
// the new guard: a run still in RunStatusRunning must transition to
// failed normally. Without this counter-test the guard could be
// silently too restrictive.
func TestRunRepository_Fail_UpdatesRunningRuns(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewRunRepository(db)

	runID, err := repo.Create(ctx, &models.DiscoveryRun{ProjectID: "proj-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Force running status.
	oid, _ := primitive.ObjectIDFromHex(runID)
	if _, err := db.Collection("discovery_runs").UpdateByID(ctx, oid, bson.M{"$set": bson.M{"status": models.RunStatusRunning}}); err != nil {
		t.Fatalf("seed running: %v", err)
	}

	if _, err := repo.Fail(ctx, runID, "disc-99", "compute error", 0); err != nil {
		t.Fatalf("Fail returned error: %v", err)
	}

	var got models.DiscoveryRun
	if err := db.Collection("discovery_runs").FindOne(ctx, bson.M{"_id": oid}).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != models.RunStatusFailed {
		t.Errorf("Status = %q, want %q — guard must allow running → failed", got.Status, models.RunStatusFailed)
	}
	if got.DiscoveryID != "disc-99" {
		t.Errorf("DiscoveryID = %q, want disc-99", got.DiscoveryID)
	}
}

// TestRunRepository_TerminalWritesAreFencedByAttempt is the other half of the
// two-agents-on-one-run fence, and the more damaging half: the agent's
// terminal status is the authoritative one.
//
// Reachable because the API's startup sweep marks in-flight runs `failed`
// after a restart WITHOUT reaping their workloads. An operator resuming such
// a run has the orphaned previous agent still alive; when it eventually
// finishes it would stamp `completed` (or `failed`) over the live attempt,
// reporting an outcome the live attempt never reached.
func TestRunRepository_TerminalWritesAreFencedByAttempt(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewRunRepository(db)

	newRunOnAttempt := func(t *testing.T, attempt int) string {
		t.Helper()
		id, err := repo.Create(ctx, &models.DiscoveryRun{ProjectID: "proj-1"})
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.UpdateStatus(ctx, id, models.RunStatusRunning, models.PhaseExploration, "", 20, 0); err != nil {
			t.Fatal(err)
		}
		oid, _ := primitive.ObjectIDFromHex(id)
		if _, err := db.Collection(CollectionDiscoveryRuns).UpdateByID(ctx, oid, bson.M{
			"$set": bson.M{"attempt": attempt},
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}

	t.Run("an orphan cannot complete a run the live attempt owns", func(t *testing.T) {
		runID := newRunOnAttempt(t, 2)

		// The orphaned attempt 1 finishes and tries to stamp success.
		if _, err := repo.Complete(ctx, runID, "disc-from-orphan", 9, 1); err != nil {
			t.Fatal(err)
		}

		run, err := repo.GetByID(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != models.RunStatusRunning {
			t.Errorf("status = %q, want running — an orphan reported an outcome for the live attempt", run.Status)
		}
		if run.DiscoveryID != "" {
			t.Errorf("discovery_id = %q, want untouched", run.DiscoveryID)
		}

		// The owning attempt's own write still lands.
		if _, err := repo.Complete(ctx, runID, "disc-from-live", 3, 2); err != nil {
			t.Fatal(err)
		}
		run, _ = repo.GetByID(ctx, runID)
		if run.Status != models.RunStatusCompleted || run.DiscoveryID != "disc-from-live" {
			t.Errorf("run = %q / %q, want completed with the live attempt's result", run.Status, run.DiscoveryID)
		}
	})

	t.Run("an orphan cannot fail a run the live attempt owns", func(t *testing.T) {
		runID := newRunOnAttempt(t, 3)

		if _, err := repo.Fail(ctx, runID, "", "orphan gave up", 2); err != nil {
			t.Fatal(err)
		}
		run, _ := repo.GetByID(ctx, runID)
		if run.Status != models.RunStatusRunning {
			t.Errorf("status = %q, want running", run.Status)
		}
		if run.Error != "" {
			t.Errorf("error = %q, want untouched by the orphan", run.Error)
		}

		if _, err := repo.Fail(ctx, runID, "", "live attempt gave up", 3); err != nil {
			t.Fatal(err)
		}
		run, _ = repo.GetByID(ctx, runID)
		if run.Status != models.RunStatusFailed || run.Error != "live attempt gave up" {
			t.Errorf("run = %q / %q, want failed with the live attempt's error", run.Status, run.Error)
		}
	})

	t.Run("attempt 1 matches a run with no attempt recorded", func(t *testing.T) {
		// Every run created before the counter existed reads as absent, and
		// its agent reports attempt 1. It must still be able to finish.
		id, err := repo.Create(ctx, &models.DiscoveryRun{ProjectID: "proj-1"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Complete(ctx, id, "disc-legacy", 1, 1); err != nil {
			t.Fatal(err)
		}
		run, _ := repo.GetByID(ctx, id)
		if run.Status != models.RunStatusCompleted {
			t.Errorf("status = %q, want completed — a legacy run must still be completable", run.Status)
		}
	})

	t.Run("attempt 0 means unknown and matches anything", func(t *testing.T) {
		// The behaviour every caller had before attempts existed.
		runID := newRunOnAttempt(t, 4)
		if _, err := repo.Complete(ctx, runID, "disc-any", 1, 0); err != nil {
			t.Fatal(err)
		}
		run, _ := repo.GetByID(ctx, runID)
		if run.Status != models.RunStatusCompleted {
			t.Errorf("status = %q, want completed", run.Status)
		}
	})
}

// TestRunRepository_MarkExplorationCheckpointIsTheOwnershipProbe pins the
// write the checkpoint path uses to find out whether it still owns the run.
//
// It is attempt-fenced, so its applied-ness answers that question for free on
// a write the agent makes anyway — which is what lets a superseded agent stop
// before writing a checkpoint for a step the live attempt has not reached
// yet. A row like that would otherwise end up in the prefix, and a later
// resume would replay a transcript spliced from two different runs.
func TestRunRepository_MarkExplorationCheckpointIsTheOwnershipProbe(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewRunRepository(db)

	runID, err := repo.Create(ctx, &models.DiscoveryRun{ProjectID: "proj-1"})
	if err != nil {
		t.Fatal(err)
	}
	oid, _ := primitive.ObjectIDFromHex(runID)
	if _, err := db.Collection(CollectionDiscoveryRuns).UpdateByID(ctx, oid, bson.M{
		"$set": bson.M{"attempt": 2},
	}); err != nil {
		t.Fatal(err)
	}

	// The live attempt owns the run, and its marker lands.
	owns, err := repo.MarkExplorationCheckpoint(ctx, runID, 40, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !owns {
		t.Fatal("the live attempt must own the run")
	}
	run, _ := repo.GetByID(ctx, runID)
	if run.LastCheckpointStep != 40 {
		t.Errorf("last_checkpoint_step = %d, want 40", run.LastCheckpointStep)
	}

	// The orphaned attempt learns it does not, and leaves the marker alone.
	owns, err = repo.MarkExplorationCheckpoint(ctx, runID, 41, 1)
	if err != nil {
		t.Fatal(err)
	}
	if owns {
		t.Error("a superseded attempt must not be told it owns the run")
	}
	run, _ = repo.GetByID(ctx, runID)
	if run.LastCheckpointStep != 40 {
		t.Errorf("last_checkpoint_step = %d, want 40 — the orphan advanced the live attempt's marker", run.LastCheckpointStep)
	}

	// $max, so the live attempt re-checkpointing a replayed prefix does not
	// walk the marker backwards.
	if _, err := repo.MarkExplorationCheckpoint(ctx, runID, 3, 2); err != nil {
		t.Fatal(err)
	}
	run, _ = repo.GetByID(ctx, runID)
	if run.LastCheckpointStep != 40 {
		t.Errorf("last_checkpoint_step = %d, want 40 — a replayed prefix must not lower it", run.LastCheckpointStep)
	}
}

// TestRunRepository_OwnsRun pins the dedicated probe, used where there is no
// write to piggyback the question on: the end-of-exploration summary, whose
// loss to a superseded attempt is the worst case of all — a later resume
// reads it, believes exploration finished, and skips Phase 3 over work
// another attempt did.
func TestRunRepository_OwnsRun(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewRunRepository(db)

	runID, err := repo.Create(ctx, &models.DiscoveryRun{ProjectID: "proj-1"})
	if err != nil {
		t.Fatal(err)
	}
	oid, _ := primitive.ObjectIDFromHex(runID)
	if _, err := db.Collection(CollectionDiscoveryRuns).UpdateByID(ctx, oid, bson.M{
		"$set": bson.M{"attempt": 3},
	}); err != nil {
		t.Fatal(err)
	}

	cases := map[int]bool{
		3: true,  // the live attempt
		2: false, // superseded
		1: false, // superseded
		0: true,  // "unknown" owns everything — the behaviour before attempts
	}
	for attempt, want := range cases {
		owns, err := repo.OwnsRun(ctx, runID, attempt)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if owns != want {
			t.Errorf("OwnsRun(attempt %d) = %v, want %v", attempt, owns, want)
		}
	}

	// A run with no attempt recorded is on its first.
	legacy := seedRunDocWithoutAttempt(t, ctx, db)
	if owns, _ := repo.OwnsRun(ctx, legacy, 1); !owns {
		t.Error("attempt 1 must own a run that predates the counter")
	}
	if owns, _ := repo.OwnsRun(ctx, legacy, 2); owns {
		t.Error("attempt 2 must not own a run that never advanced past its first")
	}
}

// seedRunDocWithoutAttempt inserts a run document with no attempt field —
// how every run created before the counter existed reads.
func seedRunDocWithoutAttempt(t *testing.T, ctx context.Context, db *DB) string {
	t.Helper()
	res, err := db.Collection(CollectionDiscoveryRuns).InsertOne(ctx, bson.M{
		"project_id": "proj-1",
		"status":     models.RunStatusRunning,
		"started_at": time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return res.InsertedID.(primitive.ObjectID).Hex()
}

// seedRunDoc inserts a run document with the given fields merged over the
// defaults, so a test can pin the exact status/attempt it needs.
func seedRunDoc(t *testing.T, ctx context.Context, db *DB, fields bson.M) string {
	t.Helper()
	doc := bson.M{
		"project_id": "proj-1",
		"status":     models.RunStatusRunning,
		"started_at": time.Now(),
	}
	for k, v := range fields {
		doc[k] = v
	}
	res, err := db.Collection(CollectionDiscoveryRuns).InsertOne(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	return res.InsertedID.(primitive.ObjectID).Hex()
}

// readRunDoc reads a run document back as a raw map, so a test can assert on
// fields the typed model may not carry.
func readRunDoc(t *testing.T, ctx context.Context, db *DB, runID string) bson.M {
	t.Helper()
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		t.Fatal(err)
	}
	var out bson.M
	if err := db.Collection(CollectionDiscoveryRuns).FindOne(ctx, bson.M{"_id": oid}).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestInteg_RunRepo_CompleteCannotOverrideACancellation is the guard that
// keeps a hard kill terminal.
//
// Cancel is deliberate: the API writes `cancelled` and deletes the run's
// checkpoints. It does NOT change the attempt, so an agent still finishing
// its save matched on (_id, attempt) and flipped the run to `completed`.
// That was already wrong, and became destructive once this write started
// reporting whether it claimed the run — a claim licenses
// retireSupersededAttempts and discardCheckpoints, so a late Complete after
// a cancel would begin deleting on the strength of it.
func TestInteg_RunRepo_CompleteCannotOverrideACancellation(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	repo := NewRunRepository(db)

	runID := seedRunDoc(t, ctx, db, bson.M{"status": models.RunStatusCancelled, "attempt": 1})

	applied, err := repo.Complete(ctx, runID, "disc-1", 3, 1)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if applied {
		t.Error("a late Complete claimed a cancelled run; it would then retire results and discard checkpoints")
	}
	run := readRunDoc(t, ctx, db, runID)
	if run["status"] != models.RunStatusCancelled {
		t.Errorf("status = %v, want it to stay cancelled", run["status"])
	}
	if _, ok := run["discovery_id"]; ok {
		t.Error("a cancelled run was given a discovery_id by a late Complete")
	}
}

// TestInteg_RunRepo_CompleteStillRecoversASweptRun is the other half, and the
// reason this guard is NOT the pending/running predicate Fail uses.
//
// The API's startup sweep marks in-flight runs `failed` WITHOUT reaping their
// agents — the premise the whole resume feature is built on. An agent that
// then finishes genuinely has a discovery to record. Refusing it would leave
// a complete result saved but unreachable behind a failed run, and invite a
// resume that re-runs analysis for nothing.
func TestInteg_RunRepo_CompleteStillRecoversASweptRun(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	repo := NewRunRepository(db)

	runID := seedRunDoc(t, ctx, db, bson.M{"status": models.RunStatusFailed, "attempt": 1})

	applied, err := repo.Complete(ctx, runID, "disc-1", 3, 1)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !applied {
		t.Fatal("an agent that finished after the sweep could not record its discovery")
	}
	run := readRunDoc(t, ctx, db, runID)
	if run["status"] != models.RunStatusCompleted {
		t.Errorf("status = %v, want completed", run["status"])
	}
	if run["discovery_id"] != "disc-1" {
		t.Errorf("discovery_id = %v, want disc-1", run["discovery_id"])
	}
}

// TestInteg_RunRepo_CompleteIsStillAttemptFenced keeps the fence this guard
// sits beside: a superseded attempt is excluded by its ATTEMPT, not by the
// run's status, which is why `failed` had to stay matchable above.
func TestInteg_RunRepo_CompleteIsStillAttemptFenced(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	repo := NewRunRepository(db)

	// The run has been resumed: it is running again, on attempt 2.
	runID := seedRunDoc(t, ctx, db, bson.M{"status": "running", "attempt": 2})

	applied, err := repo.Complete(ctx, runID, "disc-old", 1, 1)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if applied {
		t.Error("attempt 1's late Complete claimed a run that has moved to attempt 2")
	}
	run := readRunDoc(t, ctx, db, runID)
	if run["status"] != "running" {
		t.Errorf("status = %v, want the live attempt untouched", run["status"])
	}
}

// TestRunRepository_UpdateStatus_CannotReviveACancelledRun closes the last
// hole in "cancel stays terminal".
//
// A cancel does not change the attempt and does not stop the agent instantly:
// the API writes `cancelled` and kills the workload, and the process can emit
// one more SetPhase on the way out. That write sets status to `running`, which
// does not merely mislabel the run — it clears the `cancelled` that Complete's
// own guard tests for, so the pair of them undoes the cancellation entirely.
// And the resulting Complete reports a CLAIM, which is what licenses retiring
// other attempts' results and discarding the checkpoints.
func TestRunRepository_UpdateStatus_CannotReviveACancelledRun(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewRunRepository(db)

	runID, err := repo.Create(ctx, &models.DiscoveryRun{ProjectID: "proj-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	oid, _ := primitive.ObjectIDFromHex(runID)
	if _, err := db.Collection("discovery_runs").UpdateByID(ctx, oid, bson.M{"$set": bson.M{"status": "cancelled"}}); err != nil {
		t.Fatalf("seed cancelled: %v", err)
	}

	// The late phase update from an agent that has not died yet. Same
	// attempt, so the attempt fence alone lets it through.
	if err := repo.UpdateStatus(ctx, runID, models.RunStatusRunning, models.PhaseAnalysis, "analysing", 70, 1); err != nil {
		t.Fatalf("UpdateStatus returned error: %v", err)
	}

	var got models.DiscoveryRun
	if err := db.Collection("discovery_runs").FindOne(ctx, bson.M{"_id": oid}).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != "cancelled" {
		t.Errorf("Status = %q after a late phase update, want cancelled — reviving the run also re-opens Complete, which then claims it", got.Status)
	}
	if got.Phase == models.PhaseAnalysis {
		t.Errorf("Phase = %q: the update was applied despite the cancellation", got.Phase)
	}

	// And a cancelled run must stay uncompletable, which is the half of the
	// invariant that was already guarded — asserted here because it is what
	// the revival was defeating.
	applied, err := repo.Complete(ctx, runID, "disc-1", 3, 1)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if applied {
		t.Error("Complete claimed a cancelled run")
	}
}

// TestRunRepository_UpdateStatus_StillUpdatesLiveAndSweptRuns is the
// counter-test, so the guard cannot be silently too restrictive.
//
// `running` is the ordinary case. `failed` has to keep working for the same
// reason Complete allows it: the API's startup sweep marks in-flight runs
// failed WITHOUT reaping their agents, and an agent that is still working has
// progress worth showing.
func TestRunRepository_UpdateStatus_StillUpdatesLiveAndSweptRuns(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewRunRepository(db)

	for _, seeded := range []string{"running", "failed"} {
		t.Run("from "+seeded, func(t *testing.T) {
			runID, err := repo.Create(ctx, &models.DiscoveryRun{ProjectID: "proj-1"})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			oid, _ := primitive.ObjectIDFromHex(runID)
			if _, err := db.Collection("discovery_runs").UpdateByID(ctx, oid, bson.M{"$set": bson.M{"status": seeded}}); err != nil {
				t.Fatalf("seed %s: %v", seeded, err)
			}

			if err := repo.UpdateStatus(ctx, runID, models.RunStatusRunning, models.PhaseAnalysis, "analysing", 70, 1); err != nil {
				t.Fatalf("UpdateStatus: %v", err)
			}

			var got models.DiscoveryRun
			if err := db.Collection("discovery_runs").FindOne(ctx, bson.M{"_id": oid}).Decode(&got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Phase != models.PhaseAnalysis || got.Progress != 70 {
				t.Errorf("phase=%q progress=%d, want the update applied to a %s run", got.Phase, got.Progress, seeded)
			}
		})
	}
}

// TestRunRepository_MarkExplorationCheckpointRefusesACancelledRun closes the
// write half of the cancelled-run checkpoint race.
//
// Cancel is a hard kill that purges the checkpoints, but it leaves the attempt
// unchanged and the runners can return before the workload is actually gone.
// So a dying agent passes an attempt-only probe and re-creates a checkpoint
// row for a run that can never be resumed — and that row reads as "resumable"
// to the boot-time orphan sweep, which then holds the run's whole per-run
// vector collection open until the checkpoint TTL reclaims it.
func TestRunRepository_MarkExplorationCheckpointRefusesACancelledRun(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewRunRepository(db)

	runID, err := repo.Create(ctx, &models.DiscoveryRun{ProjectID: "proj-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	oid, _ := primitive.ObjectIDFromHex(runID)

	// Still live: the marker lands and reports ownership.
	applied, err := repo.MarkExplorationCheckpoint(ctx, runID, 4, 1)
	if err != nil {
		t.Fatalf("MarkExplorationCheckpoint: %v", err)
	}
	if !applied {
		t.Fatal("a live run's marker must land")
	}

	if _, err := db.Collection("discovery_runs").UpdateByID(ctx, oid, bson.M{"$set": bson.M{"status": "cancelled"}}); err != nil {
		t.Fatalf("seed cancelled: %v", err)
	}

	applied, err = repo.MarkExplorationCheckpoint(ctx, runID, 5, 1)
	if err != nil {
		t.Fatalf("MarkExplorationCheckpoint after cancel: %v", err)
	}
	if applied {
		t.Error("the marker landed on a cancelled run; the agent carries on checkpointing a run that can never be resumed")
	}

	var got models.DiscoveryRun
	if err := db.Collection("discovery_runs").FindOne(ctx, bson.M{"_id": oid}).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.LastCheckpointStep != 4 {
		t.Errorf("last_checkpoint_step = %d, want it frozen at 4", got.LastCheckpointStep)
	}
}

// TestRunRepository_ResumableOrActiveIDs guards both directions of the orphan
// sweep's keep-set, and the second direction is a regression this narrowing
// already caused once.
//
// Too wide, and a cancelled run's stray checkpoint row holds a Qdrant
// collection open until the TTL reclaims the row. Narrowed to `failed` — the
// only resumable status, which is what the first version asked for — and a
// RESUMED ATTEMPT THAT IS RUNNING gets dropped: those carry checkpoint rows
// too, and the sweep's active query can miss them because a resume keeps the
// run's original started_at. Another agent's boot sweep would then delete a
// live run's index under it, mid-flight.
func TestRunRepository_ResumableOrActiveIDs(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewRunRepository(db)

	ids := map[string]string{}
	for _, status := range []string{"failed", "cancelled", "completed", "running", "pending"} {
		id, err := repo.Create(ctx, &models.DiscoveryRun{ProjectID: "proj-1"})
		if err != nil {
			t.Fatalf("Create %s: %v", status, err)
		}
		oid, _ := primitive.ObjectIDFromHex(id)
		if _, err := db.Collection("discovery_runs").UpdateByID(ctx, oid, bson.M{"$set": bson.M{"status": status}}); err != nil {
			t.Fatalf("seed %s: %v", status, err)
		}
		ids[status] = id
	}

	all := []string{
		ids["failed"], ids["cancelled"], ids["completed"], ids["running"], ids["pending"],
		"not-an-objectid", primitive.NewObjectID().Hex(),
	}
	got, err := repo.ResumableOrActiveIDs(ctx, all)
	if err != nil {
		t.Fatalf("ResumableOrActiveIDs: %v", err)
	}

	kept := map[string]bool{}
	for _, id := range got {
		kept[id] = true
	}
	for _, want := range []string{"failed", "running", "pending"} {
		if !kept[ids[want]] {
			t.Errorf("a %s run was dropped from the keep-set; the sweep would delete the index of a run that still needs it", want)
		}
	}
	for _, unwanted := range []string{"cancelled", "completed"} {
		if kept[ids[unwanted]] {
			t.Errorf("a %s run was kept; nothing can ever want its index again", unwanted)
		}
	}
	if len(got) != 3 {
		t.Errorf("kept %d runs, want exactly 3 (failed, running, pending): %v", len(got), got)
	}

	if out, err := repo.ResumableOrActiveIDs(ctx, nil); err != nil || len(out) != 0 {
		t.Errorf("ResumableOrActiveIDs(nil) = %v, %v; want empty and no error", out, err)
	}
	if out, err := repo.ResumableOrActiveIDs(ctx, []string{"garbage"}); err != nil || len(out) != 0 {
		t.Errorf("ResumableOrActiveIDs(garbage) = %v, %v; want empty and no error — a malformed id cannot name a run", out, err)
	}
}

// TestRunRepository_AppendLifecycleRefusesACancelledRun keeps the lifecycle
// log from contradicting the run it describes.
//
// recordAttemptOutcome appends `completed` or `failed` just before the
// terminal write, and that write is already refused on a cancelled run. With
// the append unfenced the two disagreed: the run read `cancelled` while its
// lifecycle log — the human-readable record of what happened — claimed it
// completed.
func TestRunRepository_AppendLifecycleRefusesACancelledRun(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewRunRepository(db)

	runID, err := repo.Create(ctx, &models.DiscoveryRun{ProjectID: "proj-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	oid, _ := primitive.ObjectIDFromHex(runID)

	// While live, events land.
	if err := repo.AppendLifecycle(ctx, runID, models.RunLifecycleEvent{Status: models.RunStatusRunning, Attempt: 1}, 1); err != nil {
		t.Fatalf("AppendLifecycle on a live run: %v", err)
	}

	if _, err := db.Collection("discovery_runs").UpdateByID(ctx, oid, bson.M{"$set": bson.M{"status": "cancelled"}}); err != nil {
		t.Fatalf("seed cancelled: %v", err)
	}

	// The killed attempt reaches its persistence tail and reports success.
	if err := repo.AppendLifecycle(ctx, runID, models.RunLifecycleEvent{Status: models.RunStatusCompleted, Attempt: 1}, 1); err != nil {
		t.Fatalf("AppendLifecycle after cancel: %v", err)
	}

	var got models.DiscoveryRun
	if err := db.Collection("discovery_runs").FindOne(ctx, bson.M{"_id": oid}).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Lifecycle) != 1 {
		t.Fatalf("lifecycle has %d events, want 1 — the cancelled run recorded an outcome from the attempt it killed: %+v", len(got.Lifecycle), got.Lifecycle)
	}
	if got.Lifecycle[0].Status != models.RunStatusRunning {
		t.Errorf("surviving event = %q, want the pre-cancel one", got.Lifecycle[0].Status)
	}
}

// TestRunRepository_AddActiveTimeStillCountsOnACancelledRun is the line the
// cancellation guards are drawn along, asserted so a future sweep does not
// blanket-guard everything that is attempt-fenced.
//
// A lifecycle event asserts an OUTCOME, so a cancel must override it. Active
// compute is effort that really was spent and a cancel does not refund it —
// losing it would understate what the operator was charged for.
func TestRunRepository_AddActiveTimeStillCountsOnACancelledRun(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewRunRepository(db)

	runID, err := repo.Create(ctx, &models.DiscoveryRun{ProjectID: "proj-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	oid, _ := primitive.ObjectIDFromHex(runID)
	if _, err := db.Collection("discovery_runs").UpdateByID(ctx, oid, bson.M{"$set": bson.M{"status": "cancelled"}}); err != nil {
		t.Fatalf("seed cancelled: %v", err)
	}

	if err := repo.AddActiveTime(ctx, runID, 90*time.Second, 1); err != nil {
		t.Fatalf("AddActiveTime: %v", err)
	}

	var got models.DiscoveryRun
	if err := db.Collection("discovery_runs").FindOne(ctx, bson.M{"_id": oid}).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ActiveMs != 90_000 {
		t.Errorf("active_ms = %d, want 90000 — the work happened and a cancel does not refund it", got.ActiveMs)
	}
}
