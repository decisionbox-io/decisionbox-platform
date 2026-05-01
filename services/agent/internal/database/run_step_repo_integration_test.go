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
// honours a `since` cursor so the dashboard reads only new rows on each
// poll.
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

	// Three steps, ordered timestamps. We set them explicitly so the
	// `since` cursor below is deterministic — InsertOne uses time.Now()
	// when Timestamp is zero, but here we control it.
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

	// First poll: no `since` → all three.
	all, err := repo.ListByRun(ctx, runID, time.Time{}, 0)
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("first poll: got %d steps, want 3", len(all))
	}
	if all[0].StepNum != 1 || all[2].StepNum != 3 {
		t.Errorf("ascending order broken: %d -> %d", all[0].StepNum, all[2].StepNum)
	}

	// Subsequent poll: `since=t0+50ms` → only step 3 (strictly after).
	tail, err := repo.ListByRun(ctx, runID, t0.Add(50*time.Millisecond), 0)
	if err != nil {
		t.Fatalf("ListByRun since: %v", err)
	}
	if len(tail) != 1 || tail[0].StepNum != 3 {
		t.Errorf("since cursor broken: got %d steps, want 1 with StepNum=3", len(tail))
	}

	// limit clamps the response without breaking the order.
	limited, err := repo.ListByRun(ctx, runID, time.Time{}, 2)
	if err != nil {
		t.Fatalf("ListByRun limit: %v", err)
	}
	if len(limited) != 2 || limited[0].StepNum != 1 || limited[1].StepNum != 2 {
		t.Errorf("limit broke order/count: %+v", limited)
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
	a, _ := repo.ListByRun(ctx, "run-A", time.Time{}, 0)
	b, _ := repo.ListByRun(ctx, "run-B", time.Time{}, 0)
	if len(a) != 1 || a[0].Message != "run-A" {
		t.Errorf("run-A: got %+v", a)
	}
	if len(b) != 1 || b[0].Message != "run-B" {
		t.Errorf("run-B: got %+v", b)
	}
}

// TestRunStepRepository_TimestampDefaulted exercises the auto-fill: a step
// with zero timestamp should land with the insert-time value, not stay
// zero (otherwise sorting by timestamp would clump everything at epoch).
func TestRunStepRepository_TimestampDefaulted(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewRunStepRepository(db)
	before := time.Now().UTC()
	if err := repo.AddStep(ctx, "run-z", "proj", models.RunStep{Type: "info", Message: "no-ts"}); err != nil {
		t.Fatalf("AddStep: %v", err)
	}
	got, _ := repo.ListByRun(ctx, "run-z", time.Time{}, 0)
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
