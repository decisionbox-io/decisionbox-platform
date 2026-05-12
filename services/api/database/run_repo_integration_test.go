//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// dropRuns wipes the discovery_runs collection so each test starts on a
// clean slate. The package-level testcontainer is reused across the
// suite, so cross-test state would otherwise leak.
func dropRuns(t *testing.T, ctx context.Context) {
	t.Helper()
	if _, err := testDB.Collection("discovery_runs").DeleteMany(ctx, bson.M{}); err != nil {
		t.Fatalf("drop runs: %v", err)
	}
}

// seedRun inserts a discovery_runs document with the fields the
// dispatcher cares about and returns its hex _id.
func seedRun(t *testing.T, ctx context.Context, status string, completionHooksFiredAt *time.Time, completedAt *time.Time, startedAt time.Time) string {
	t.Helper()
	doc := bson.M{
		"project_id":  "proj-integ",
		"status":      status,
		"started_at":  startedAt,
		"updated_at":  time.Now(),
	}
	if completedAt != nil {
		doc["completed_at"] = *completedAt
	}
	if completionHooksFiredAt != nil {
		doc["completion_hooks_fired_at"] = *completionHooksFiredAt
	}
	res, err := testDB.Collection("discovery_runs").InsertOne(ctx, doc)
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	oid := res.InsertedID.(primitive.ObjectID)
	return oid.Hex()
}

func TestInteg_RunRepo_ListTerminalWithoutCompletionHook_FiltersByStatusAndField(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	now := time.Now()
	// In-scope: terminal status, no completion_hooks_fired_at.
	completedID := seedRun(t, ctx, "completed", nil, &now, base.Add(1*time.Minute))
	failedID := seedRun(t, ctx, "failed", nil, &now, base.Add(2*time.Minute))
	cancelledID := seedRun(t, ctx, "cancelled", nil, &now, base.Add(3*time.Minute))
	// Out-of-scope: status not terminal.
	_ = seedRun(t, ctx, "pending", nil, nil, base.Add(4*time.Minute))
	_ = seedRun(t, ctx, "running", nil, nil, base.Add(5*time.Minute))
	// Out-of-scope: already dispatched.
	already := now
	_ = seedRun(t, ctx, "completed", &already, &now, base.Add(6*time.Minute))

	got, err := repo.ListTerminalWithoutCompletionHook(ctx, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	wantIDs := map[string]bool{completedID: false, failedID: false, cancelledID: false}
	for _, r := range got {
		if _, ok := wantIDs[r.ID]; !ok {
			t.Errorf("unexpected run %q in result (status=%q)", r.ID, r.Status)
			continue
		}
		wantIDs[r.ID] = true
	}
	for id, seen := range wantIDs {
		if !seen {
			t.Errorf("expected run %q in result but it was missing", id)
		}
	}
}

func TestInteg_RunRepo_ListTerminalWithoutCompletionHook_FIFOOrder(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	// Older runs must surface before newer ones — bound tail latency.
	now := time.Now()
	first := seedRun(t, ctx, "completed", nil, &now, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	second := seedRun(t, ctx, "completed", nil, &now, time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC))
	third := seedRun(t, ctx, "failed", nil, &now, time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC))

	got, err := repo.ListTerminalWithoutCompletionHook(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d runs, want 3", len(got))
	}
	if got[0].ID != first || got[1].ID != second || got[2].ID != third {
		t.Errorf("FIFO order broken: %s, %s, %s — want %s, %s, %s",
			got[0].ID, got[1].ID, got[2].ID, first, second, third)
	}
}

func TestInteg_RunRepo_ListTerminalWithoutCompletionHook_RespectsLimit(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	now := time.Now()
	for i := 0; i < 5; i++ {
		seedRun(t, ctx, "completed", nil, &now, time.Date(2026, 5, 1, i, 0, 0, 0, time.UTC))
	}

	got, err := repo.ListTerminalWithoutCompletionHook(ctx, 3)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d runs, want 3", len(got))
	}
}

func TestInteg_RunRepo_ListTerminalWithoutCompletionHook_DefaultLimitOnZero(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	now := time.Now()
	// Seed 55 — limit defaults to 50.
	for i := 0; i < 55; i++ {
		seedRun(t, ctx, "completed", nil, &now, time.Date(2026, 5, 1, 0, i, 0, 0, time.UTC))
	}

	got, err := repo.ListTerminalWithoutCompletionHook(ctx, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 50 {
		t.Fatalf("got %d runs, want 50 (default limit)", len(got))
	}
}

func TestInteg_RunRepo_ListTerminalWithoutCompletionHook_NoMatches(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	now := time.Now()
	// All runs have completion_hooks_fired_at set.
	seedRun(t, ctx, "completed", &now, &now, time.Now())
	seedRun(t, ctx, "failed", &now, &now, time.Now())

	got, err := repo.ListTerminalWithoutCompletionHook(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d runs, want 0", len(got))
	}
}

func TestInteg_RunRepo_MarkCompletionHooksFired_SetsField(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	now := time.Now()
	runID := seedRun(t, ctx, "completed", nil, &now, time.Now())

	// MongoDB stores time at millisecond precision so the round-trip
	// truncates the sub-millisecond portion. Compare against a
	// pre-truncated lower bound to avoid spurious "before" failures
	// (e.g. 10:15:27.056117 → stored as 10:15:27.056 which is technically
	// before the captured nanosecond timestamp).
	before := time.Now().Truncate(time.Millisecond)
	if err := repo.MarkCompletionHooksFired(ctx, runID); err != nil {
		t.Fatalf("mark: %v", err)
	}
	after := time.Now()

	run, err := repo.GetByID(ctx, runID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if run.CompletionHooksFiredAt == nil {
		t.Fatal("CompletionHooksFiredAt is nil after Mark")
	}
	got := *run.CompletionHooksFiredAt
	if got.Before(before) || got.After(after.Add(1*time.Second)) {
		t.Errorf("CompletionHooksFiredAt = %v, want between %v and %v", got, before, after)
	}
}

func TestInteg_RunRepo_MarkCompletionHooksFired_RemovesFromListResult(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	now := time.Now()
	a := seedRun(t, ctx, "completed", nil, &now, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	b := seedRun(t, ctx, "completed", nil, &now, time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC))

	if err := repo.MarkCompletionHooksFired(ctx, a); err != nil {
		t.Fatalf("mark: %v", err)
	}
	got, err := repo.ListTerminalWithoutCompletionHook(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].ID != b {
		ids := make([]string, len(got))
		for i, r := range got {
			ids[i] = r.ID
		}
		t.Fatalf("after marking %s, list = %v, want [%s]", a, ids, b)
	}
}

func TestInteg_RunRepo_MarkCompletionHooksFired_InvalidRunIDErrors(t *testing.T) {
	ctx := context.Background()
	repo := NewRunRepository(testDB)
	if err := repo.MarkCompletionHooksFired(ctx, "not-a-hex-objectid"); err == nil {
		t.Fatal("expected error for invalid run ID, got nil")
	}
}

func TestInteg_RunRepo_MarkCompletionHooksFired_AlsoUpdatesUpdatedAt(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	now := time.Now()
	runID := seedRun(t, ctx, "completed", nil, &now, time.Now())
	original, err := repo.GetByID(ctx, runID)
	if err != nil {
		t.Fatalf("get original: %v", err)
	}
	originalUpdatedAt := original.UpdatedAt

	// Sleep long enough that a sub-millisecond clock difference won't
	// flake the comparison.
	time.Sleep(10 * time.Millisecond)

	if err := repo.MarkCompletionHooksFired(ctx, runID); err != nil {
		t.Fatalf("mark: %v", err)
	}
	updated, err := repo.GetByID(ctx, runID)
	if err != nil {
		t.Fatalf("get updated: %v", err)
	}
	if !updated.UpdatedAt.After(originalUpdatedAt) {
		t.Errorf("UpdatedAt = %v, want strictly after %v", updated.UpdatedAt, originalUpdatedAt)
	}
}
