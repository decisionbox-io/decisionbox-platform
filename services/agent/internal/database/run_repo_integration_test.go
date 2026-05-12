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
