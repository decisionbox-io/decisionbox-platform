//go:build integration

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/database"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// DiscardRun against the real repositories, because its guard rail spans two
// collections that key a run DIFFERENTLY: discovery_runs by ObjectId, and
// discoveries by run_id as a string. A mocked repo keyed by "run-1" cannot
// tell those apart, so the mismatch below is invisible to the unit tests.
func discardHandlerWithRealRepos(t *testing.T) *DiscoveriesHandler {
	t.Helper()
	h := &DiscoveriesHandler{
		runRepo:     database.NewRunRepository(testDB),
		projectRepo: database.NewProjectRepository(testDB),
		repo:        database.NewDiscoveryRepository(testDB),
	}
	return h.WithCheckpoints(database.NewDiscoveryCheckpointRepository(testDB))
}

// primitive.ObjectIDFromHex accepts UPPERCASE hex, so GetByID and Discard
// both resolve an uppercase run id — while the discoveries collection stores
// run_id as the lowercase string oid.Hex() produces. Taking the id from the
// path rather than from the document therefore compared an uppercase id
// against a lowercase run_id, found no result, and discarded a run whose
// partial discovery is still stored: exactly the orphan the check exists to
// refuse, reached by changing the case of a URL.
func TestInteg_DiscardRun_RefusesAnOrphanWhateverTheCaseOfTheRunID(t *testing.T) {
	ctx := context.Background()
	h := discardHandlerWithRealRepos(t)

	projOID := primitive.NewObjectID()
	if _, err := testDB.Collection("projects").InsertOne(ctx, bson.M{
		"_id": projOID, "name": "discard-case", "org_id": "default",
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	projectID := projOID.Hex()

	runOID := primitive.NewObjectID()
	if _, err := testDB.Collection("discovery_runs").InsertOne(ctx, bson.M{
		"_id": runOID, "project_id": projectID, "status": "failed",
		"started_at": time.Now(), "attempt": 2,
		// Cleared by BeginResume, which is what makes the stored-result
		// check the only thing standing between this run and an orphan.
		"discovery_id":         "",
		"last_checkpoint_step": 5,
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	// The partial discovery the earlier attempt saved, keyed the way the
	// agent writes it: the canonical lowercase hex.
	if _, err := testDB.Collection("discoveries").InsertOne(ctx, bson.M{
		"_id": primitive.NewObjectID(), "project_id": projectID, "run_id": runOID.Hex(),
	}); err != nil {
		t.Fatalf("seed discovery: %v", err)
	}

	for _, tc := range []struct {
		name  string
		runID string
	}{
		{"canonical", runOID.Hex()},
		{"uppercase", strings.ToUpper(runOID.Hex())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.DiscardRun(w, discardReq(tc.runID))

			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body = %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "would be orphaned") {
				t.Errorf("body = %s, want the orphan refusal", w.Body.String())
			}
			var run bson.M
			if err := testDB.Collection("discovery_runs").FindOne(ctx, bson.M{"_id": runOID}).Decode(&run); err != nil {
				t.Fatal(err)
			}
			if run["discarded_at"] != nil {
				t.Error("discarded a run whose stored discovery would be orphaned")
			}
			if step, _ := run["last_checkpoint_step"].(int32); step != 5 {
				t.Errorf("last_checkpoint_step = %v, want 5 — a refused discard changes nothing", run["last_checkpoint_step"])
			}
		})
	}
}
