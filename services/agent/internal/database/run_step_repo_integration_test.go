//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// TestRunStepRepository_StreamsAcrossSinceCursor is the dashboard-polling
// contract: each AddStep is a fresh InsertOne (no $push), and ListByRun
// honours an opaque ObjectID cursor so the dashboard reads only new
// rows on each poll.
func TestRunStepRepository_StreamsAcrossSinceCursor(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewRunStepRepository(db)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	const runID = "run-stream"
	t0 := time.Now().UTC().Truncate(time.Millisecond)

	for i, ts := range []time.Time{t0, t0.Add(50 * time.Millisecond), t0.Add(100 * time.Millisecond)} {
		err := repo.AddStep(ctx, runID, "proj-1", models.RunStep{
			Phase:     models.PhaseExploration,
			StepNum:   i + 1,
			Type:      "query",
			Message:   "step",
			Timestamp: ts,
		})
		if err != nil {
			t.Fatalf("AddStep %d: %v", i, err)
		}
	}

	// First poll: empty cursor → all three, ordered by ObjectID
	// (which is monotonic per-process and matches insertion order).
	all, err := repo.ListByRun(ctx, runID, "", 0)
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("first poll: got %d steps, want 3", len(all))
	}
	if all[0].StepNum != 1 || all[2].StepNum != 3 {
		t.Errorf("ascending order broken: %d -> %d", all[0].StepNum, all[2].StepNum)
	}
	for i, d := range all {
		if d.IDHex == "" {
			t.Errorf("doc %d: IDHex not populated", i)
		}
	}

	// Subsequent poll: pass the second row's id → only step 3 (strictly after).
	tail, err := repo.ListByRun(ctx, runID, all[1].IDHex, 0)
	if err != nil {
		t.Fatalf("ListByRun since: %v", err)
	}
	if len(tail) != 1 || tail[0].StepNum != 3 {
		t.Errorf("since cursor broken: got %d steps, want 1 with StepNum=3", len(tail))
	}

	// limit clamps the response without breaking the order.
	limited, err := repo.ListByRun(ctx, runID, "", 2)
	if err != nil {
		t.Fatalf("ListByRun limit: %v", err)
	}
	if len(limited) != 2 || limited[0].StepNum != 1 || limited[1].StepNum != 2 {
		t.Errorf("limit broke order/count: %+v", limited)
	}
}

// TestRunStepRepository_SameMillisecondCollisionIsNotDropped is the
// regression for the timestamp-cursor bug: two AddStep calls inside the
// same BSON millisecond used to collide on the cursor and a $gt
// timestamp filter would silently skip the later row on the next poll.
// The ObjectID cursor must keep them ordered and never drop one.
func TestRunStepRepository_SameMillisecondCollisionIsNotDropped(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewRunStepRepository(db)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	const runID = "run-collide"
	// Same explicit timestamp on both inserts — BSON datetimes are
	// ms-precision, so this models the worst case.
	ts := time.Now().UTC().Truncate(time.Millisecond)

	for i := 1; i <= 3; i++ {
		err := repo.AddStep(ctx, runID, "p", models.RunStep{
			Phase:     models.PhaseExploration,
			StepNum:   i,
			Type:      "query",
			Timestamp: ts,
		})
		if err != nil {
			t.Fatalf("AddStep %d: %v", i, err)
		}
	}

	all, err := repo.ListByRun(ctx, runID, "", 0)
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("first poll dropped colliding rows: got %d, want 3", len(all))
	}

	// Page 1 of 2 — limit=2 forces the dashboard's typical "tail"
	// behaviour. Cursor = page 1's last id.
	page1, err := repo.ListByRun(ctx, runID, "", 2)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page1 len = %d, want 2", len(page1))
	}

	page2, err := repo.ListByRun(ctx, runID, page1[1].IDHex, 0)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 1 || page2[0].StepNum != 3 {
		t.Errorf("page2 dropped colliding row: %+v", page2)
	}
}

// TestRunStepRepository_RunIsolation pins the run scoping: steps for
// run A never appear in reads for run B.
func TestRunStepRepository_RunIsolation(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewRunStepRepository(db)
	for _, run := range []string{"run-A", "run-B"} {
		_ = repo.AddStep(ctx, run, "proj", models.RunStep{StepNum: 1, Type: "info", Message: run})
	}
	a, _ := repo.ListByRun(ctx, "run-A", "", 0)
	b, _ := repo.ListByRun(ctx, "run-B", "", 0)
	if len(a) != 1 || a[0].Message != "run-A" {
		t.Errorf("run-A: got %+v", a)
	}
	if len(b) != 1 || b[0].Message != "run-B" {
		t.Errorf("run-B: got %+v", b)
	}
}

// TestRunStepRepository_TimestampDefaulted exercises the auto-fill: a step
// with zero timestamp should land with the insert-time value, not stay
// zero. The cursor uses ObjectID, but the timestamp is still a
// per-step rendering field the dashboard shows in the live panel.
func TestRunStepRepository_TimestampDefaulted(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewRunStepRepository(db)
	before := time.Now().UTC()
	if err := repo.AddStep(ctx, "run-z", "proj", models.RunStep{Type: "info", Message: "no-ts"}); err != nil {
		t.Fatalf("AddStep: %v", err)
	}
	got, _ := repo.ListByRun(ctx, "run-z", "", 0)
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if got[0].Timestamp.IsZero() {
		t.Error("expected default timestamp, got zero")
	}
	if got[0].Timestamp.Before(before.Add(-time.Second)) {
		t.Errorf("timestamp absurdly old: %v", got[0].Timestamp)
	}
}

// TestRunStepRepository_InvalidCursorReturnsError checks the input
// validation path: a malformed hex sinceID must surface as an error so
// the API handler can map it to a 400.
func TestRunStepRepository_InvalidCursorReturnsError(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewRunStepRepository(db)
	_, err := repo.ListByRun(ctx, "run-x", "not-an-objectid", 0)
	if err == nil {
		t.Fatal("expected error for invalid sinceID, got nil")
	}
}
