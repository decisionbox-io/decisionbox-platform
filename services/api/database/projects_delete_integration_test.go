//go:build integration

package database

import (
	"context"
	"strings"
	"testing"
	"time"

	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
)

// TestInteg_ProjectRepo_DeleteCascade is the deletion safety net. It
// seeds *both* projects across every project-scoped collection, runs
// DeleteCascade on one of them, and asserts:
//
//  1. Every collection has zero rows for the deleted project_id.
//  2. Feedback rows whose discovery_id belongs to the deleted project
//     are gone (the feedback collection is keyed by discovery_id, not
//     project_id, so a missed discovery-id lookup leaks feedback).
//  3. The OTHER project's rows in every collection are untouched —
//     this is the only test that catches a stray $in / global delete.
//  4. Re-running DeleteCascade on the same id is a no-op (idempotent).
//  5. The list of collections we seed matches projectChildCollections +
//     "projects" + "feedback" — adding a new child collection without
//     extending this test is the exact mistake this guard rejects.
func TestInteg_ProjectRepo_DeleteCascade(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)

	// --- seed projects collection ---
	// Create discards any pre-set ID; we capture the auto-generated
	// hex ObjectIDs after each call.
	projDelDoc := &models.Project{Name: "deleted", Domain: "gaming", Category: "match3", CreatedAt: time.Now()}
	if err := repo.Create(ctx, projDelDoc); err != nil {
		t.Fatalf("seed deleted project: %v", err)
	}
	projKeepDoc := &models.Project{Name: "kept", Domain: "gaming", Category: "match3", CreatedAt: time.Now()}
	if err := repo.Create(ctx, projKeepDoc); err != nil {
		t.Fatalf("seed kept project: %v", err)
	}
	projDel := projDelDoc.ID
	projKeep := projKeepDoc.ID
	const (
		discDel  = "disc-del-A1"
		discKeep = "disc-del-B1"
	)

	// --- seed every project_id-keyed child collection ---
	// Each entry inserts one doc per project. Keep this list aligned
	// with projectChildCollections — the assertion below catches a
	// missing entry. We disambiguate identifying fields (list_id /
	// target_id) per project so collections with unique compound
	// indexes (e.g. bookmarks on list_id+target_type+target_id) don't
	// dup-key on the second insert.
	for _, name := range projectChildCollections {
		col := testDB.Collection(name)
		for _, projID := range []string{projDel, projKeep} {
			discoveryID := discDel
			if projID == projKeep {
				discoveryID = discKeep
			}
			doc := bson.M{
				"project_id":   projID,
				"discovery_id": discoveryID,
				"list_id":      "list-" + projID,
				"target_type":  "insight",
				"target_id":    "ins-" + projID,
				"created_at":   time.Now(),
			}
			// "discoveries" rows need _id = discoveryID so the cascade
			// can find them on its first pass. Other collections use
			// generated ObjectIDs.
			if name == "discoveries" {
				doc["_id"] = discoveryID
			}
			// discovery_checkpoints carries a unique (run_id, step_number)
			// index, so the two projects' rows need distinct run ids — the
			// same disambiguation list_id / target_id do above. Without it
			// both rows key on (null, null) and the second insert
			// dup-keys.
			if name == gomongo.CollectionDiscoveryCheckpoints {
				doc["run_id"] = "run-" + projID
				doc["step_number"] = 1
			}
			// Same disambiguation for the per-area analysis checkpoints
			// and the in-flight drafts beside them, whose unique index is
			// also (run_id, area_id).
			if name == gomongo.CollectionDiscoveryAnalysisCheckpoints ||
				name == gomongo.CollectionDiscoveryAnalysisDrafts {
				doc["run_id"] = "run-" + projID
				doc["area_id"] = "churn"
			}
			// And the per-run recommendation checkpoint, whose unique index
			// is run_id alone.
			if name == gomongo.CollectionDiscoveryRecommendationCheckpoints {
				doc["run_id"] = "run-" + projID
			}
			if _, err := col.InsertOne(ctx, doc); err != nil {
				t.Fatalf("seed %s for %s: %v", name, projID, err)
			}
		}
	}

	// --- seed every _id-keyed settings collection ---
	// One document per project, whose _id IS the project id. These cannot be
	// reached by a DeleteMany on project_id, which is the whole reason they
	// have their own cascade step — and the reason they leaked before it.
	//
	// The LIST is pinned literally first, because this test walks the same
	// list the cascade does. A name misspelled or dropped from production
	// would leave this loop seeding nothing, verifying nothing, and passing
	// green over the leak it exists to catch — the one failure a test built
	// on the code under test always has.
	if want := []string{"insight_scoring"}; !equalStrings(projectIDKeyedCollections, want) {
		t.Fatalf("projectIDKeyedCollections = %v, want %v — extend this pin when a plugin adds one, and the cascade will be covered",
			projectIDKeyedCollections, want)
	}
	for _, name := range projectIDKeyedCollections {
		col := testDB.Collection(name)
		for _, projID := range []string{projDel, projKeep} {
			if _, err := col.InsertOne(ctx, bson.M{
				"_id":              projID,
				"category_weights": bson.M{"compliance": 3},
				"updated_by":       "someone@example.com",
				"created_at":       time.Now(),
			}); err != nil {
				t.Fatalf("seed %s for %s: %v", name, projID, err)
			}
		}
		// And the deployment-wide document that sits beside them, which
		// belongs to no project and must survive.
		if _, err := col.InsertOne(ctx, bson.M{
			"_id":              "__default__",
			"category_weights": bson.M{"compliance": 2},
			"created_at":       time.Now(),
		}); err != nil {
			t.Fatalf("seed %s default: %v", name, err)
		}
	}

	// --- seed feedback (keyed by discovery_id, not project_id) ---
	feedbackCol := testDB.Collection("feedback")
	for _, discID := range []string{discDel, discKeep} {
		if _, err := feedbackCol.InsertOne(ctx, bson.M{
			"discovery_id": discID,
			"target_type":  "insight",
			"target_id":    "ins-1",
			"rating":       5,
			"created_at":   time.Now(),
		}); err != nil {
			t.Fatalf("seed feedback for %s: %v", discID, err)
		}
	}

	// --- act ---
	if err := repo.DeleteCascade(ctx, projDel); err != nil {
		t.Fatalf("DeleteCascade: %v", err)
	}

	// --- assert: deleted project is gone in every place ---
	// GetByID returns (nil, nil) on not-found — so we check the
	// pointer, not the error, otherwise this assertion is a no-op.
	if got, err := repo.GetByID(ctx, projDel); err != nil {
		t.Fatalf("GetByID after delete: %v", err)
	} else if got != nil {
		t.Errorf("deleted project still found via GetByID: %+v", got)
	}

	for _, name := range projectChildCollections {
		n, err := testDB.Collection(name).CountDocuments(ctx, bson.M{"project_id": projDel})
		if err != nil {
			t.Fatalf("count %s for deleted project: %v", name, err)
		}
		if n != 0 {
			t.Errorf("collection %s leaked %d row(s) for deleted project", name, n)
		}
	}

	// The deleted project's settings documents are gone, the kept project's
	// are intact, and the deployment-wide one is untouched — that last is the
	// assertion that matters, because a sweep keyed on the wrong thing would
	// take a whole deployment's configuration with one project.
	for _, name := range projectIDKeyedCollections {
		col := testDB.Collection(name)
		if n, err := col.CountDocuments(ctx, bson.M{"_id": projDel}); err != nil {
			t.Fatalf("count %s for deleted project: %v", name, err)
		} else if n != 0 {
			t.Errorf("collection %s leaked the deleted project's settings", name)
		}
		if n, err := col.CountDocuments(ctx, bson.M{"_id": projKeep}); err != nil {
			t.Fatalf("count %s for kept project: %v", name, err)
		} else if n != 1 {
			t.Errorf("collection %s lost the kept project's settings (%d rows)", name, n)
		}
		if n, err := col.CountDocuments(ctx, bson.M{"_id": "__default__"}); err != nil {
			t.Fatalf("count %s default: %v", name, err)
		} else if n != 1 {
			t.Errorf("collection %s lost the deployment default (%d rows)", name, n)
		}
	}

	// Feedback for the deleted project's discovery is gone too.
	n, err := feedbackCol.CountDocuments(ctx, bson.M{"discovery_id": discDel})
	if err != nil {
		t.Fatalf("count feedback for deleted discovery: %v", err)
	}
	if n != 0 {
		t.Errorf("feedback for deleted discovery leaked: %d row(s)", n)
	}

	// --- assert: control project is fully intact ---
	keptProj, err := repo.GetByID(ctx, projKeep)
	if err != nil || keptProj == nil {
		t.Fatalf("control project missing after cascade: %v", err)
	}
	for _, name := range projectChildCollections {
		n, err := testDB.Collection(name).CountDocuments(ctx, bson.M{"project_id": projKeep})
		if err != nil {
			t.Fatalf("count %s for kept project: %v", name, err)
		}
		if n != 1 {
			t.Errorf("collection %s: kept project should have 1 row, got %d", name, n)
		}
	}
	n, err = feedbackCol.CountDocuments(ctx, bson.M{"discovery_id": discKeep})
	if err != nil {
		t.Fatalf("count feedback for kept discovery: %v", err)
	}
	if n != 1 {
		t.Errorf("feedback for kept discovery wrong row count: %d, want 1", n)
	}

	// --- idempotency: a second cascade is a clean no-op ---
	if err := repo.DeleteCascade(ctx, projDel); err != nil {
		t.Errorf("second DeleteCascade should be a no-op, got: %v", err)
	}
}

// TestInteg_ProjectRepo_DeleteCascade_NoDiscoveries verifies the cascade
// doesn't crash when a project has nothing in the `discoveries`
// collection — the discovery-id loop must produce an empty slice and
// the feedback delete must be skipped (otherwise we'd issue a $in []
// query that, depending on driver version, can be misinterpreted).
// TestInteg_ProjectRepo_DeleteCascade_UppercaseID.
//
// An ObjectID parses case-insensitively, so a delete issued with uppercase
// hex finds and removes the project. The _id-keyed settings sweep has to use
// the canonical form or it looks for a document under a string nothing ever
// wrote — leaving one project's weights, thresholds and editor subject behind
// after the project itself is gone.
func TestInteg_ProjectRepo_DeleteCascade_UppercaseID(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)

	proj := &models.Project{Name: "upper", Domain: "gaming", Category: "match3", CreatedAt: time.Now()}
	if err := repo.Create(ctx, proj); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	for _, name := range projectIDKeyedCollections {
		if _, err := testDB.Collection(name).InsertOne(ctx, bson.M{
			"_id":              proj.ID,
			"category_weights": bson.M{"compliance": 3},
			"updated_by":       "someone@example.com",
		}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	if err := repo.DeleteCascade(ctx, strings.ToUpper(proj.ID)); err != nil {
		t.Fatalf("DeleteCascade with uppercase id: %v", err)
	}

	for _, name := range projectIDKeyedCollections {
		n, err := testDB.Collection(name).CountDocuments(ctx, bson.M{"_id": proj.ID})
		if err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if n != 0 {
			t.Errorf("collection %s leaked settings after an uppercase-id delete", name)
		}
	}
}

func TestInteg_ProjectRepo_DeleteCascade_NoDiscoveries(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)

	doc := &models.Project{Name: "empty", CreatedAt: time.Now()}
	if err := repo.Create(ctx, doc); err != nil {
		t.Fatalf("seed: %v", err)
	}
	projID := doc.ID

	// Seed an unrelated feedback row so we can prove it survives.
	if _, err := testDB.Collection("feedback").InsertOne(ctx, bson.M{
		"discovery_id": "disc-unrelated",
		"target_type":  "insight",
		"target_id":    "ins-1",
		"rating":       5,
		"created_at":   time.Now(),
	}); err != nil {
		t.Fatalf("seed unrelated feedback: %v", err)
	}

	if err := repo.DeleteCascade(ctx, projID); err != nil {
		t.Fatalf("DeleteCascade: %v", err)
	}

	if got, err := repo.GetByID(ctx, projID); err != nil {
		t.Fatalf("GetByID after delete: %v", err)
	} else if got != nil {
		t.Errorf("project still present after cascade: %+v", got)
	}

	// Unrelated feedback survived — proves we didn't issue an empty
	// $in that matched everything.
	n, err := testDB.Collection("feedback").CountDocuments(ctx, bson.M{"discovery_id": "disc-unrelated"})
	if err != nil {
		t.Fatalf("count unrelated feedback: %v", err)
	}
	if n != 1 {
		t.Errorf("unrelated feedback was deleted: %d rows remain, want 1", n)
	}
}

// TestInteg_ProjectRepo_DeleteCascade_EmptyID guards the explicit
// argument check — passing "" must error out before any collection is
// touched. Without this, a programming bug ("delete project at id
// project.ID" where ID was never populated) would wipe every row whose
// project_id is "".
func TestInteg_ProjectRepo_DeleteCascade_EmptyID(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)

	if err := repo.DeleteCascade(ctx, ""); err == nil {
		t.Error("DeleteCascade(\"\") should error, got nil")
	}
}

// equalStrings compares two name lists element for element.
//
// Written out rather than reached for, because this package builds under the
// `integration` tag against the repo's own toolchain floor and a test helper
// is not the place to find out about it.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
