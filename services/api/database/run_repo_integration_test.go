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
		"project_id": "proj-integ",
		"status":     status,
		"started_at": startedAt,
		"updated_at": time.Now(),
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

// seedRunForProject inserts a discovery_runs doc for a specific project,
// returning its hex _id. Used by the LatestByProjects tests where the
// per-project keying matters (seedRun pins a single project_id).
func seedRunForProject(t *testing.T, ctx context.Context, projectID, status string, startedAt time.Time, completedAt *time.Time) string {
	t.Helper()
	doc := bson.M{
		"project_id": projectID,
		"status":     status,
		"started_at": startedAt,
		"updated_at": time.Now(),
	}
	if completedAt != nil {
		doc["completed_at"] = *completedAt
	}
	res, err := testDB.Collection("discovery_runs").InsertOne(ctx, doc)
	if err != nil {
		t.Fatalf("seed run for %s: %v", projectID, err)
	}
	return res.InsertedID.(primitive.ObjectID).Hex()
}

func TestInteg_RunRepo_LatestByProjects_PicksMostRecentPerProject(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	completedAt := base.Add(30 * time.Minute)

	// proj-a: an older completed run and a newer running run — the
	// running one must win because it started later.
	_ = seedRunForProject(t, ctx, "proj-a", "completed", base, &completedAt)
	aLatest := seedRunForProject(t, ctx, "proj-a", "running", base.Add(1*time.Hour), nil)
	// proj-b: a single completed run.
	bCompletedAt := base.Add(2 * time.Hour)
	bLatest := seedRunForProject(t, ctx, "proj-b", "completed", base.Add(90*time.Minute), &bCompletedAt)
	// proj-c: a failed run — must still be returned (any latest status).
	cLatest := seedRunForProject(t, ctx, "proj-c", "failed", base.Add(3*time.Hour), nil)
	// proj-z: has runs but is NOT requested — must be absent from result.
	_ = seedRunForProject(t, ctx, "proj-z", "completed", base, &completedAt)

	got, err := repo.LatestByProjects(ctx, []string{"proj-a", "proj-b", "proj-c", "proj-no-runs"})
	if err != nil {
		t.Fatalf("LatestByProjects: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("got %d projects in result, want 3 (a, b, c)", len(got))
	}
	if _, ok := got["proj-no-runs"]; ok {
		t.Error("proj-no-runs has no runs but appeared in result")
	}
	if _, ok := got["proj-z"]; ok {
		t.Error("proj-z was not requested but appeared in result")
	}

	if a := got["proj-a"]; a == nil {
		t.Error("proj-a missing from result")
	} else {
		if a.ID != aLatest {
			t.Errorf("proj-a latest run id = %q, want %q (the newer running run)", a.ID, aLatest)
		}
		if a.Status != "running" {
			t.Errorf("proj-a latest status = %q, want running", a.Status)
		}
		if a.CompletedAt != nil {
			t.Errorf("proj-a running run should have nil CompletedAt, got %v", a.CompletedAt)
		}
	}

	if b := got["proj-b"]; b == nil {
		t.Error("proj-b missing from result")
	} else {
		if b.ID != bLatest {
			t.Errorf("proj-b latest run id = %q, want %q", b.ID, bLatest)
		}
		if b.Status != "completed" || b.CompletedAt == nil {
			t.Errorf("proj-b should be completed with a CompletedAt, got status=%q completed_at=%v", b.Status, b.CompletedAt)
		}
	}

	if c := got["proj-c"]; c == nil {
		t.Error("proj-c missing from result")
	} else if c.ID != cLatest || c.Status != "failed" {
		t.Errorf("proj-c latest = (%q, %q), want (%q, failed)", c.ID, c.Status, cLatest)
	}
}

func TestInteg_RunRepo_LatestByProjects_EmptyInput(t *testing.T) {
	ctx := context.Background()
	repo := NewRunRepository(testDB)

	got, err := repo.LatestByProjects(ctx, nil)
	if err != nil {
		t.Fatalf("LatestByProjects(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries for empty input, want 0", len(got))
	}
}

func TestInteg_RunRepo_LatestByProjects_NoMatches(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	seedRunForProject(t, ctx, "proj-other", "completed", time.Now(), nil)

	got, err := repo.LatestByProjects(ctx, []string{"proj-absent"})
	if err != nil {
		t.Fatalf("LatestByProjects: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries when no project matches, want 0", len(got))
	}
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

// TestInteg_RunRepo_Fail_DoesNotOverwriteCompleted pins the codex r12
// [P2] guard: the K8s watcher's exhaustion fallback can fire
// OnFailure → Fail AFTER the agent has stamped the run as completed.
// Without the terminal-status filter, a successful discovery would
// be silently flipped to failed hours later.
func TestInteg_RunRepo_Fail_DoesNotOverwriteCompleted(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)

	now := time.Now()
	runID := seedRun(t, ctx, "completed", nil, &now, now)
	repo := NewRunRepository(testDB)

	if err := repo.Fail(ctx, runID, "watcher exhausted"); err != nil {
		t.Fatalf("Fail returned error: %v", err)
	}

	got, err := repo.GetByID(ctx, runID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != "completed" {
		t.Errorf("Status = %q after late Fail, want completed", got.Status)
	}
}

// TestInteg_RunRepo_Fail_DoesNotOverwriteCancelled — the other half
// of the terminal-status guard: a user cancellation must not be
// flipped to failed by an in-flight watcher's exhaustion.
func TestInteg_RunRepo_Fail_DoesNotOverwriteCancelled(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)

	now := time.Now()
	runID := seedRun(t, ctx, "cancelled", nil, &now, now)
	repo := NewRunRepository(testDB)

	if err := repo.Fail(ctx, runID, "watcher exhausted"); err != nil {
		t.Fatalf("Fail returned error: %v", err)
	}

	got, err := repo.GetByID(ctx, runID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != "cancelled" {
		t.Errorf("Status = %q after late Fail, want cancelled", got.Status)
	}
}

// TestInteg_RunRepo_Fail_UpdatesRunningRuns — positive counter-test:
// the guard must NOT lock out the genuine running → failed
// transition for in-flight runs that hit an actual failure.
func TestInteg_RunRepo_Fail_UpdatesRunningRuns(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)

	runID := seedRun(t, ctx, "running", nil, nil, time.Now())
	repo := NewRunRepository(testDB)

	if err := repo.Fail(ctx, runID, "agent crashed"); err != nil {
		t.Fatalf("Fail returned error: %v", err)
	}

	got, err := repo.GetByID(ctx, runID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != "failed" {
		t.Errorf("Status = %q, want failed — guard must allow running → failed", got.Status)
	}
}
