//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"go.mongodb.org/mongo-driver/bson"
)

// seedStep inserts one live-feed row. attempt < 0 writes NO attempt field at
// all, which is how every row written before the field existed looks — the
// case a filter is easy to get wrong on.
func seedStep(t *testing.T, db *DB, runID string, attempt, stepNum int, msg string) {
	t.Helper()
	doc := bson.M{
		"run_id":     runID,
		"project_id": "proj-1",
		"created_at": time.Now().UTC(),
		"step_num":   stepNum,
		"type":       "query",
		"message":    msg,
		"timestamp":  time.Now().UTC(),
	}
	if attempt >= 0 {
		doc["attempt"] = attempt
	}
	if _, err := db.Collection(gomongo.CollectionDiscoveryRunSteps).InsertOne(context.Background(), doc); err != nil {
		t.Fatalf("seed step %q: %v", msg, err)
	}
}

func stepMessages(docs []RunStepDoc) []string {
	out := make([]string, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.Message)
	}
	return out
}

func sameStrings(a, b []string) bool {
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

// TestRunStepRepository_ListByRunScopesToAttempt is the live-feed fence.
//
// A resume starts a new attempt while the superseded agent may still be
// writing — it only learns it was replaced at its next ownership gate. Its
// rows carry its own attempt, so the reader has to scope to the attempt the
// run is on now. Two independent reasons, both covered here: the operator
// must not see a dead attempt's rows, and the `_id > since_id` cursor
// assumes one writer per run, which two live attempts break.
func TestRunStepRepository_ListByRunScopesToAttempt(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupTestMongoDB(t)
	defer cleanup()
	repo := NewRunStepRepository(db)

	const runID = "run-resumed"
	// Interleaved on purpose: the superseded attempt keeps writing after the
	// resume has already written rows of its own.
	seedStep(t, db, runID, 1, 1, "a1-step1")
	seedStep(t, db, runID, 1, 2, "a1-step2")
	seedStep(t, db, runID, 2, 1, "a2-step1")
	seedStep(t, db, runID, 1, 3, "a1-late-tail")
	seedStep(t, db, runID, 2, 2, "a2-step2")

	t.Run("a resumed run sees only its own attempt", func(t *testing.T) {
		got, err := repo.ListByRun(ctx, runID, "", 0, 2)
		if err != nil {
			t.Fatalf("ListByRun: %v", err)
		}
		want := []string{"a2-step1", "a2-step2"}
		if msgs := stepMessages(got); !sameStrings(msgs, want) {
			t.Errorf("attempt 2 feed = %v, want %v — a superseded attempt's rows reached the live feed", msgs, want)
		}
	})

	t.Run("the superseded attempt's own feed is still readable", func(t *testing.T) {
		got, err := repo.ListByRun(ctx, runID, "", 0, 1)
		if err != nil {
			t.Fatalf("ListByRun: %v", err)
		}
		want := []string{"a1-step1", "a1-step2", "a1-late-tail"}
		if msgs := stepMessages(got); !sameStrings(msgs, want) {
			t.Errorf("attempt 1 feed = %v, want %v", msgs, want)
		}
	})

	t.Run("the cursor stays inside one attempt", func(t *testing.T) {
		first, err := repo.ListByRun(ctx, runID, "", 1, 2)
		if err != nil {
			t.Fatalf("first page: %v", err)
		}
		if len(first) != 1 || first[0].Message != "a2-step1" {
			t.Fatalf("first page = %v, want [a2-step1]", stepMessages(first))
		}
		// The row inserted between the two attempt-2 rows belongs to the
		// superseded attempt and sorts between them by _id. A cursor that
		// did not carry the attempt filter would surface it here.
		next, err := repo.ListByRun(ctx, runID, first[0].IDHex, 0, 2)
		if err != nil {
			t.Fatalf("second page: %v", err)
		}
		want := []string{"a2-step2"}
		if msgs := stepMessages(next); !sameStrings(msgs, want) {
			t.Errorf("second page = %v, want %v", msgs, want)
		}
	})
}

// TestRunStepRepository_ListByRunAdmitsRowsWithNoAttempt covers every row
// written before the attempt field existed. They must stay visible on a
// first-attempt run, or upgrading the binary would blank the live log of
// every historical run. Attempt 1 and "attempt unknown" both mean the same
// thing here, which is the convention the run document's own fence uses.
func TestRunStepRepository_ListByRunAdmitsRowsWithNoAttempt(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupTestMongoDB(t)
	defer cleanup()
	repo := NewRunStepRepository(db)

	const runID = "run-legacy"
	seedStep(t, db, runID, -1, 1, "pre-field-1")
	seedStep(t, db, runID, -1, 2, "pre-field-2")
	seedStep(t, db, runID, 1, 3, "stamped-attempt-1")

	for _, asked := range []int{0, 1} {
		got, err := repo.ListByRun(ctx, runID, "", 0, asked)
		if err != nil {
			t.Fatalf("ListByRun(attempt=%d): %v", asked, err)
		}
		want := []string{"pre-field-1", "pre-field-2", "stamped-attempt-1"}
		if msgs := stepMessages(got); !sameStrings(msgs, want) {
			t.Errorf("attempt %d feed = %v, want %v — a historical run's log must not go blank", asked, msgs, want)
		}
	}

	// A resume of that historical run is attempt 2, and the unstamped rows
	// belong to attempt 1. They are the superseded attempt's, so they are
	// out — the replayed prefix is re-emitted under attempt 2 by the agent
	// (StatusReporter.ReplayExplorationStep), which is what keeps the
	// resumed feed complete.
	got, err := repo.ListByRun(ctx, runID, "", 0, 2)
	if err != nil {
		t.Fatalf("ListByRun(attempt=2): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("attempt 2 feed = %v, want empty — unstamped rows belong to attempt 1", stepMessages(got))
	}
}
