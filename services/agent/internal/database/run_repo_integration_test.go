//go:build integration

package database

import (
	"context"
	"strings"
	"testing"

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
	if err := repo.Complete(ctx, runID, discoveryID, 7); err != nil {
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

	err = repo.Complete(ctx, runID, "", 0)
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
	if err := repo.Complete(ctx, "not-a-hex", "disc-1", 1); err == nil {
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
	if err := repo.Complete(ctx, runID, discoveryID, 7); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Simulate the watcher exhaustion path firing AFTER Complete.
	if err := repo.Fail(ctx, runID, "", "watcher exhausted"); err != nil {
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

	if err := repo.Fail(ctx, runID, "", "watcher exhausted"); err != nil {
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

	if err := repo.Fail(ctx, runID, "disc-99", "compute error"); err != nil {
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
