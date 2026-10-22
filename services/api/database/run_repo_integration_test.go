//go:build integration

package database

import (
	"context"
	"errors"
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
	if err := repo.MarkCompletionHooksFired(ctx, runID, 1); err != nil {
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

	if err := repo.MarkCompletionHooksFired(ctx, a, 1); err != nil {
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
	if err := repo.MarkCompletionHooksFired(ctx, "not-a-hex-objectid", 1); err == nil {
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

	if err := repo.MarkCompletionHooksFired(ctx, runID, 1); err != nil {
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

// TestInteg_RunRepo_LatestPrefersTheActiveRun is what keeps a resumed run
// reachable.
//
// Resume re-enters the run it resumes and leaves started_at alone — that is
// what started_at means, and active_ms is what carries compute across
// attempts. So resuming a failed run after a NEWER run has since finished
// leaves the live run holding the OLDER started_at. Answering "latest" with
// started_at alone then hands back the finished run, and the project's status
// endpoint shows a completed run while a resumed one burns budget behind it:
// no progress, and no way to cancel it.
//
// Both readers are checked together, because the only reason the API is
// allowed two of them is that they agree.
func TestInteg_RunRepo_LatestPrefersTheActiveRun(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	newerCompletedAt := base.Add(3 * time.Hour)

	// The run that failed first and was then resumed, so it is active again
	// with the older started_at.
	resumed := seedRunForProject(t, ctx, "proj-r", "failed", base, nil)
	// A whole run came and went in the meantime.
	_ = seedRunForProject(t, ctx, "proj-r", "completed", base.Add(2*time.Hour), &newerCompletedAt)

	if _, err := repo.BeginResume(ctx, resumed); err != nil {
		t.Fatalf("BeginResume: %v", err)
	}

	one, err := repo.GetLatestByProject(ctx, "proj-r")
	if err != nil {
		t.Fatalf("GetLatestByProject: %v", err)
	}
	if one == nil {
		t.Fatal("GetLatestByProject returned nothing")
	}
	if one.ID != resumed {
		t.Errorf("GetLatestByProject = %q (status %q), want the resumed run %q", one.ID, one.Status, resumed)
	}

	many, err := repo.LatestByProjects(ctx, []string{"proj-r"})
	if err != nil {
		t.Fatalf("LatestByProjects: %v", err)
	}
	if got := many["proj-r"]; got == nil {
		t.Fatal("proj-r missing from LatestByProjects")
	} else if got.ID != resumed {
		t.Errorf("LatestByProjects = %q (status %q), want the resumed run %q", got.ID, got.Status, resumed)
	}
}

// TestInteg_RunRepo_LatestFallsBackToStartedAtWithNoActiveRun is the other
// half: preferring an active run must not change the answer for a project
// whose runs have all finished, which is the ordinary case.
func TestInteg_RunRepo_LatestFallsBackToStartedAtWithNoActiveRun(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	oldCompletedAt := base.Add(10 * time.Minute)
	newCompletedAt := base.Add(2 * time.Hour)

	_ = seedRunForProject(t, ctx, "proj-q", "completed", base, &oldCompletedAt)
	newest := seedRunForProject(t, ctx, "proj-q", "failed", base.Add(90*time.Minute), &newCompletedAt)

	one, err := repo.GetLatestByProject(ctx, "proj-q")
	if err != nil {
		t.Fatalf("GetLatestByProject: %v", err)
	}
	if one == nil || one.ID != newest {
		t.Errorf("GetLatestByProject = %v, want the newest-started run %q", one, newest)
	}

	many, err := repo.LatestByProjects(ctx, []string{"proj-q"})
	if err != nil {
		t.Fatalf("LatestByProjects: %v", err)
	}
	if got := many["proj-q"]; got == nil || got.ID != newest {
		t.Errorf("LatestByProjects = %v, want the newest-started run %q", got, newest)
	}

	// And a project with no runs at all is still nothing, not an error.
	if one, err := repo.GetLatestByProject(ctx, "proj-none"); err != nil || one != nil {
		t.Errorf("GetLatestByProject for a project with no runs = (%v, %v), want (nil, nil)", one, err)
	}
}

// seedResumedRun inserts a run that has been resumed: original started_at,
// a later last_resumed_at, and a terminal status.
func seedResumedRun(t *testing.T, ctx context.Context, projectID, status string, startedAt, resumedAt time.Time) string {
	t.Helper()
	res, err := testDB.Collection("discovery_runs").InsertOne(ctx, bson.M{
		"project_id":      projectID,
		"status":          status,
		"started_at":      startedAt,
		"last_resumed_at": resumedAt,
		"attempt":         2,
		"updated_at":      time.Now(),
	})
	if err != nil {
		t.Fatalf("seed resumed run: %v", err)
	}
	return res.InsertedID.(primitive.ObjectID).Hex()
}

// TestInteg_RunRepo_LatestKeepsAResumedRunVisibleAfterItFailsAgain closes the
// half of the active-run rule that only held while the run was RUNNING.
//
// A resumed run keeps its original started_at. While it is active it wins
// outright, but the moment it fails again the fallback ordered by started_at
// handed back any newer run instead — so the project panel showed an
// unrelated completed run and offered no Resume for the attempt that had just
// failed. The resume affordance is the feature; it has to survive the second
// failure as much as the first.
func TestInteg_RunRepo_LatestKeepsAResumedRunVisibleAfterItFailsAgain(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	newerDone := base.Add(90 * time.Minute)

	// Failed at 10:00, resumed at 12:00, failed again.
	resumed := seedResumedRun(t, ctx, "proj-r", "failed", base, base.Add(2*time.Hour))
	// A whole newer run came and went in between.
	_ = seedRunForProject(t, ctx, "proj-r", "completed", base.Add(1*time.Hour), &newerDone)

	one, err := repo.GetLatestByProject(ctx, "proj-r")
	if err != nil {
		t.Fatalf("GetLatestByProject: %v", err)
	}
	if one == nil || one.ID != resumed {
		t.Errorf("GetLatestByProject = %v, want the resumed run %q that failed most recently", one, resumed)
	}

	many, err := repo.LatestByProjects(ctx, []string{"proj-r"})
	if err != nil {
		t.Fatalf("LatestByProjects: %v", err)
	}
	if got := many["proj-r"]; got == nil || got.ID != resumed {
		t.Errorf("LatestByProjects = %v, want the resumed run %q", got, resumed)
	}
}

// TestInteg_RunRepo_LatestPicksTheMostRecentlyResumedRun is why the resumed
// pass orders by last_resumed_at and not by started_at: a project can hold
// two resumed runs where the one that STARTED earlier was resumed later, and
// each pass only ever surfaces one row, so the wrong sort key here cannot be
// repaired by comparing afterwards.
func TestInteg_RunRepo_LatestPicksTheMostRecentlyResumedRun(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	// Started first, resumed LAST — this is the one the operator just touched.
	older := seedResumedRun(t, ctx, "proj-r", "failed", base, base.Add(5*time.Hour))
	// Started later, resumed earlier.
	_ = seedResumedRun(t, ctx, "proj-r", "failed", base.Add(1*time.Hour), base.Add(2*time.Hour))

	one, err := repo.GetLatestByProject(ctx, "proj-r")
	if err != nil {
		t.Fatalf("GetLatestByProject: %v", err)
	}
	if one == nil || one.ID != older {
		t.Errorf("GetLatestByProject = %v, want %q — the most recently RESUMED run", one, older)
	}

	many, err := repo.LatestByProjects(ctx, []string{"proj-r"})
	if err != nil {
		t.Fatalf("LatestByProjects: %v", err)
	}
	if got := many["proj-r"]; got == nil || got.ID != older {
		t.Errorf("LatestByProjects = %v, want %q", got, older)
	}
}

// TestInteg_RunRepo_ResumedPassExcludesNeverResumedRuns pins the predicate
// the resumed pass uses, because the Mongo semantics read backwards and a
// reviewer has already proposed replacing it with the wrong thing.
//
// Mongo treats a MISSING field as null, so `$ne: nil` excludes both the
// missing field and an explicit null — exactly "has actually been resumed".
// `$exists: true` would be weaker: it lets an explicit null through.
func TestInteg_RunRepo_ResumedPassExcludesNeverResumedRuns(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	done := base.Add(30 * time.Minute)

	// Never resumed: no last_resumed_at field at all.
	newest := seedRunForProject(t, ctx, "proj-n", "completed", base.Add(2*time.Hour), &done)
	_ = seedRunForProject(t, ctx, "proj-n", "completed", base, &done)
	// And one carrying an EXPLICIT null, which $exists would wrongly admit.
	if _, err := testDB.Collection("discovery_runs").InsertOne(ctx, bson.M{
		"project_id": "proj-n", "status": "completed",
		"started_at": base.Add(1 * time.Hour), "completed_at": done,
		"last_resumed_at": nil,
	}); err != nil {
		t.Fatal(err)
	}

	// Asserted on the resumed PASS directly, not through the public result.
	//
	// A leak cannot be observed from GetLatestByProject: LatestAttemptAt
	// falls back to started_at for a row with no resume timestamp, and the
	// started_at pass already maximises started_at — so a leaked row can
	// never beat it, whatever the predicate. The public answer is therefore
	// identical either way, which is exactly why the earlier version of this
	// test passed against `$exists: true` and pinned nothing.
	//
	// What the predicate actually governs is how WIDE that pass is: with
	// $exists it would sort every run carrying an explicit null, in memory,
	// on a field that is null for nearly all of them. So the thing to assert
	// is emptiness of the pass itself.
	resumedPass, err := repo.newestPerProject(ctx, bson.M{
		"project_id":      bson.M{"$in": []string{"proj-n"}},
		"last_resumed_at": bson.M{"$ne": nil},
	}, "last_resumed_at")
	if err != nil {
		t.Fatalf("resumed pass: %v", err)
	}
	if len(resumedPass) != 0 {
		t.Errorf("the resumed pass matched %d project(s) with no genuinely resumed run; a missing or explicit-null last_resumed_at must not enter it", len(resumedPass))
	}

	// And the public answer is the newest by started_at, as it must be.
	one, err := repo.GetLatestByProject(ctx, "proj-n")
	if err != nil {
		t.Fatalf("GetLatestByProject: %v", err)
	}
	if one == nil || one.ID != newest {
		t.Errorf("GetLatestByProject = %v, want the newest-started run %q", one, newest)
	}

	many, err := repo.LatestByProjects(ctx, []string{"proj-n"})
	if err != nil {
		t.Fatalf("LatestByProjects: %v", err)
	}
	if got := many["proj-n"]; got == nil || got.ID != newest {
		t.Errorf("LatestByProjects = %v, want %q", got, newest)
	}
}

// TestInteg_RunRepo_StampReservationOwnerEndedAt_IsWriteOnce is the half of
// the preserved-end-time fix that a handler test cannot reach.
//
// The point of the field is that the FIRST resume to find a lingering
// reservation records when its owning attempt stopped, and a later resume
// cannot overwrite it with its own, later time. That write-once behaviour
// lives in the query's $exists guard, so a mock implementing it proves
// nothing about the repository — this asserts it against Mongo.
func TestInteg_RunRepo_StampReservationOwnerEndedAt_IsWriteOnce(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	started := time.Now().UTC().Add(-6 * time.Hour)
	runID := seedRun(t, ctx, "failed", nil, nil, started)

	first := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Millisecond)
	if err := repo.StampReservationOwnerEndedAt(ctx, runID, first); err != nil {
		t.Fatalf("first stamp: %v", err)
	}
	got, err := repo.GetByID(ctx, runID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.PolicyReservationOwnerEndedAt == nil || !got.PolicyReservationOwnerEndedAt.Equal(first) {
		t.Fatalf("preserved = %v, want %s", got.PolicyReservationOwnerEndedAt, first)
	}

	// A second resume tries to record its own time. It must not win.
	second := time.Now().UTC().Truncate(time.Millisecond)
	if err := repo.StampReservationOwnerEndedAt(ctx, runID, second); err != nil {
		t.Fatalf("second stamp: %v", err)
	}
	got, err = repo.GetByID(ctx, runID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !got.PolicyReservationOwnerEndedAt.Equal(first) {
		t.Errorf("preserved = %s, want it still %s — a later resume overwrote the owning attempt's end time", got.PolicyReservationOwnerEndedAt, first)
	}
}

// TestInteg_RunRepo_StampReservationOwnerEndedAt_IgnoresAZeroTime — a zero
// time means "unknown", and persisting it would make a later close report the
// epoch instead of falling back to whatever the document still knows.
func TestInteg_RunRepo_StampReservationOwnerEndedAt_IgnoresAZeroTime(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	runID := seedRun(t, ctx, "failed", nil, nil, time.Now().UTC().Add(-time.Hour))
	if err := repo.StampReservationOwnerEndedAt(ctx, runID, time.Time{}); err != nil {
		t.Fatalf("zero stamp: %v", err)
	}
	got, err := repo.GetByID(ctx, runID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.PolicyReservationOwnerEndedAt != nil {
		t.Errorf("preserved = %s, want nothing written for an unknown end time", got.PolicyReservationOwnerEndedAt)
	}
}

// TestInteg_RunRepo_ClearPolicyReservationID_AlsoClearsThePreservedEndTime —
// the preserved time describes one specific reservation. Left behind, it would
// hand a future reservation on this run an answer belonging to a dead attempt.
func TestInteg_RunRepo_ClearPolicyReservationID_AlsoClearsThePreservedEndTime(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	runID := seedRun(t, ctx, "failed", nil, nil, time.Now().UTC().Add(-time.Hour))
	oid := mustOID(t, runID)
	if _, err := testDB.Collection("discovery_runs").UpdateByID(ctx, oid, bson.M{
		"$set": bson.M{"policy_reservation_id": "res-attempt-1"},
	}); err != nil {
		t.Fatalf("seed reservation: %v", err)
	}
	if err := repo.StampReservationOwnerEndedAt(ctx, runID, time.Now().UTC().Add(-2*time.Hour)); err != nil {
		t.Fatalf("stamp: %v", err)
	}

	if err := repo.ClearPolicyReservationID(ctx, runID); err != nil {
		t.Fatalf("ClearPolicyReservationID: %v", err)
	}

	got, err := repo.GetByID(ctx, runID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.PolicyReservationID != "" {
		t.Errorf("reservation id = %q, want cleared", got.PolicyReservationID)
	}
	if got.PolicyReservationOwnerEndedAt != nil {
		t.Errorf("preserved end time = %s, want it cleared with the id it describes", got.PolicyReservationOwnerEndedAt)
	}
}

// --- Discard ---------------------------------------------------------------

// Discard's filter is the whole guarantee: terminal, resultless, not already
// discarded. Against real Mongo because the `discarded_at: nil` predicate is
// the kind of thing that reads the wrong way round — a missing field compares
// equal to null, which is what makes "not discarded" expressible at all.
func TestInteg_RunRepo_Discard(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		status  string
		wantErr bool
	}{
		{"a failed run is discardable", "failed", false},
		{"a cancelled run is discardable", "cancelled", false},
		{"a completed run is NOT — its discovery is the project's history", "completed", true},
		{"a running run is NOT — cancel is the verb for that", "running", true},
		{"a pending run is NOT", "pending", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dropRuns(t, ctx)
			repo := NewRunRepository(testDB)
			id := seedRunForProject(t, ctx, "proj-discard", tc.status, base, nil)
			// Give it a checkpoint marker, so clearing it is observable.
			if _, err := testDB.Collection("discovery_runs").UpdateByID(ctx,
				mustOID(t, id), bson.M{"$set": bson.M{"last_checkpoint_step": 7}}); err != nil {
				t.Fatalf("seed marker: %v", err)
			}

			err := repo.Discard(ctx, id, 1)
			if tc.wantErr {
				if !errors.Is(err, ErrNotDiscardable) {
					t.Fatalf("want ErrNotDiscardable, got %v", err)
				}
				run, _ := repo.GetByID(ctx, id)
				if run.DiscardedAt != nil {
					t.Error("a refused discard must not have written discarded_at")
				}
				if run.LastCheckpointStep != 7 {
					t.Errorf("a refused discard must not clear the marker, got %d", run.LastCheckpointStep)
				}
				return
			}
			if err != nil {
				t.Fatalf("discard: %v", err)
			}
			run, err := repo.GetByID(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if run.DiscardedAt == nil {
				t.Error("discarded_at was not set")
			}
			if run.Status != tc.status {
				t.Errorf("status changed to %q — a discarded run keeps its outcome, and its error, as the record", run.Status)
			}
			if run.LastCheckpointStep != 0 {
				t.Errorf("last_checkpoint_step = %d, want cleared so Resume cannot be offered", run.LastCheckpointStep)
			}
		})
	}
}

// The attempt fence, against real Mongo, because the legacy half of it is
// expressed as a set membership over a field that may not exist — the kind of
// predicate a unit test with a map cannot get wrong and a query can.
//
// What it protects: the caller reads a failed run, decides to discard it, and
// in the meantime someone resumes it. The new attempt fails too, so the
// status predicate matches again — and the discard purges exploration
// checkpoints belonging to an attempt nobody evaluated. Permanently: the
// checkpoints are what made the run resumable.
func TestInteg_RunRepo_Discard_IsFencedOnTheAttemptItWasDecidedOn(t *testing.T) {
	ctx := context.Background()
	repo := NewRunRepository(testDB)

	t.Run("a stale attempt matches nothing", func(t *testing.T) {
		dropRuns(t, ctx)
		id := seedRunForProject(t, ctx, "proj-attempt", "failed", time.Now(), nil)
		// Resumed and failed again, after the caller's read.
		if _, err := testDB.Collection("discovery_runs").UpdateByID(ctx, mustOID(t, id),
			bson.M{"$set": bson.M{"attempt": 2, "status": "failed", "last_checkpoint_step": 11}}); err != nil {
			t.Fatalf("seed resume: %v", err)
		}

		if err := repo.Discard(ctx, id, 1); !errors.Is(err, ErrNotDiscardable) {
			t.Fatalf("discard on attempt 1: want ErrNotDiscardable, got %v", err)
		}
		run, _ := repo.GetByID(ctx, id)
		if run.DiscardedAt != nil {
			t.Error("discarded an attempt the caller never evaluated")
		}
		if run.LastCheckpointStep != 11 {
			t.Errorf("last_checkpoint_step = %d, want 11 — the newer attempt is still resumable", run.LastCheckpointStep)
		}

		// And the right attempt still works, so the fence is a fence and not
		// a wall.
		if err := repo.Discard(ctx, id, 2); err != nil {
			t.Errorf("discard on attempt 2: %v", err)
		}
	})

	t.Run("a run that predates the attempt counter is discardable", func(t *testing.T) {
		dropRuns(t, ctx)
		id := seedRunForProject(t, ctx, "proj-legacy", "failed", time.Now(), nil)
		// No attempt field at all, which is what such a run looks like.
		if _, err := testDB.Collection("discovery_runs").UpdateByID(ctx, mustOID(t, id),
			bson.M{"$unset": bson.M{"attempt": ""}}); err != nil {
			t.Fatalf("strip attempt: %v", err)
		}
		var raw bson.M
		if err := testDB.Collection("discovery_runs").FindOne(ctx, bson.M{"_id": mustOID(t, id)}).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		if _, present := raw["attempt"]; present {
			t.Fatal("precondition: the attempt field should be absent")
		}

		// 0 is what GetByID reports for such a run, and it has to mean the
		// same unresumed run as 1 — otherwise the fence makes every
		// pre-upgrade failed run permanently undiscardable.
		if err := repo.Discard(ctx, id, 0); err != nil {
			t.Errorf("discard with no attempt field: %v", err)
		}
	})
}

// Idempotent by refusal rather than by silently rewriting: a second discard,
// or one racing a resume, must match nothing.
func TestInteg_RunRepo_Discard_IsNotRepeatable(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)
	id := seedRunForProject(t, ctx, "proj-discard", "failed", time.Now(), nil)

	if err := repo.Discard(ctx, id, 1); err != nil {
		t.Fatalf("first discard: %v", err)
	}
	first, _ := repo.GetByID(ctx, id)
	if err := repo.Discard(ctx, id, 1); !errors.Is(err, ErrNotDiscardable) {
		t.Fatalf("second discard: want ErrNotDiscardable, got %v", err)
	}
	second, _ := repo.GetByID(ctx, id)
	if !first.DiscardedAt.Equal(*second.DiscardedAt) {
		t.Error("the refused second discard moved discarded_at")
	}
}

// The reason the endpoint exists. GetStatus answers with GetLatestByProject,
// which is the newest run whatever its status — so a failed run an operator
// is done with stayed pinned to the project page. Both readers have to agree,
// which is the only thing that justifies having two of them.
func TestInteg_RunRepo_DiscardedRunStopsRepresentingTheProject(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	base := time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC)
	completedAt := base.Add(20 * time.Minute)
	older := seedRunForProject(t, ctx, "proj-latest", "completed", base, &completedAt)
	newer := seedRunForProject(t, ctx, "proj-latest", "failed", base.Add(1*time.Hour), nil)

	latest, err := repo.GetLatestByProject(ctx, "proj-latest")
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil || latest.ID != newer {
		t.Fatalf("precondition: latest = %v, want the newer failed run", latest)
	}

	if err := repo.Discard(ctx, newer, 1); err != nil {
		t.Fatalf("discard: %v", err)
	}

	latest, err = repo.GetLatestByProject(ctx, "proj-latest")
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil || latest.ID != older {
		t.Errorf("GetLatestByProject = %v, want it to fall back to the older completed run", latest)
	}

	byProjects, err := repo.LatestByProjects(ctx, []string{"proj-latest"})
	if err != nil {
		t.Fatal(err)
	}
	if got := byProjects["proj-latest"]; got == nil || got.ID != older {
		t.Errorf("LatestByProjects = %v, want the same answer as GetLatestByProject", got)
	}
}

// A discarded run that was resumed still has last_resumed_at, so it would come
// back through the SECOND pass of the latest-run rule even once the first pass
// skipped it. Both passes need the predicate, and only a resumed run proves it.
func TestInteg_RunRepo_DiscardedResumedRunDoesNotReturnViaTheResumePass(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	base := time.Date(2026, 6, 3, 9, 0, 0, 0, time.UTC)
	completedAt := base.Add(2 * time.Hour)
	// The resumed run started FIRST but was resumed last, which is exactly
	// the case the second pass exists for.
	resumed := seedRunForProject(t, ctx, "proj-resumed", "failed", base, nil)
	completed := seedRunForProject(t, ctx, "proj-resumed", "completed", base.Add(1*time.Hour), &completedAt)
	if _, err := testDB.Collection("discovery_runs").UpdateByID(ctx, mustOID(t, resumed),
		bson.M{"$set": bson.M{"last_resumed_at": base.Add(3 * time.Hour), "attempt": 2}}); err != nil {
		t.Fatalf("seed resume: %v", err)
	}

	latest, _ := repo.GetLatestByProject(ctx, "proj-resumed")
	if latest == nil || latest.ID != resumed {
		t.Fatalf("precondition: want the resumed run to win on last_resumed_at, got %v", latest)
	}

	if err := repo.Discard(ctx, resumed, 2); err != nil {
		t.Fatalf("discard: %v", err)
	}
	latest, _ = repo.GetLatestByProject(ctx, "proj-resumed")
	if latest == nil || latest.ID != completed {
		t.Errorf("GetLatestByProject = %v, want the completed run — the resume pass must skip discarded runs too", latest)
	}
}

// Discard unsets the marker so the dashboard hides Resume, but the button is
// not the guard. A direct POST must not resurrect a run the operator threw
// away, and BeginResume's filter is the only place that can be enforced
// without a race.
func TestInteg_RunRepo_BeginResumeRefusesADiscardedRun(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)
	id := seedRunForProject(t, ctx, "proj-resume-guard", "failed", time.Now(), nil)

	// Sanity: resumable before the discard, so the refusal after it is
	// attributable to the discard and not to some other precondition.
	if _, err := repo.BeginResume(ctx, id); err != nil {
		t.Fatalf("precondition: want a resumable run, got %v", err)
	}
	if _, err := testDB.Collection("discovery_runs").UpdateByID(ctx, mustOID(t, id),
		bson.M{"$set": bson.M{"status": "failed"}}); err != nil {
		t.Fatalf("reset to failed: %v", err)
	}
	if err := repo.Discard(ctx, id, 2); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if _, err := repo.BeginResume(ctx, id); !errors.Is(err, ErrNoResumableRun) {
		t.Errorf("BeginResume on a discarded run: want ErrNoResumableRun, got %v", err)
	}
}

// A `failed` run is not necessarily resultless: the agent's Fail stamps
// discovery_id on a run that saved a partial result before dying (an analysis
// failure, or the outer duration cap) precisely so the result stays
// reachable. Discarding such a run would orphan that discovery exactly as
// discarding a completed one would — the status is not the test, the presence
// of a result is.
func TestInteg_RunRepo_Discard_RefusesARunThatSavedADiscovery(t *testing.T) {
	ctx := context.Background()
	for _, status := range []string{"failed", "cancelled"} {
		t.Run(status+" with a discovery", func(t *testing.T) {
			dropRuns(t, ctx)
			repo := NewRunRepository(testDB)
			id := seedRunForProject(t, ctx, "proj-result", status, time.Now(), nil)
			if _, err := testDB.Collection("discovery_runs").UpdateByID(ctx, mustOID(t, id),
				bson.M{"$set": bson.M{"discovery_id": "disc-partial-1"}}); err != nil {
				t.Fatalf("seed discovery_id: %v", err)
			}

			if err := repo.Discard(ctx, id, 1); !errors.Is(err, ErrNotDiscardable) {
				t.Fatalf("want ErrNotDiscardable, got %v", err)
			}
			run, _ := repo.GetByID(ctx, id)
			if run.DiscardedAt != nil {
				t.Error("a run with a saved discovery was discarded")
			}
		})
	}
	// An empty discovery_id is "no result", not "a result called nothing":
	// BeginResume clears the field by writing "", so a resumed-then-failed
	// run carries an empty one and must stay discardable.
	t.Run("an empty discovery_id is still discardable", func(t *testing.T) {
		dropRuns(t, ctx)
		repo := NewRunRepository(testDB)
		id := seedRunForProject(t, ctx, "proj-result", "failed", time.Now(), nil)
		if _, err := testDB.Collection("discovery_runs").UpdateByID(ctx, mustOID(t, id),
			bson.M{"$set": bson.M{"discovery_id": ""}}); err != nil {
			t.Fatalf("seed empty discovery_id: %v", err)
		}
		if err := repo.Discard(ctx, id, 1); err != nil {
			t.Fatalf("discard: %v", err)
		}
	})
}

// The hazard this PR exists for, applied to discard: the startup sweep marks
// in-flight runs failed WITHOUT reaping their agents, so a discard can land
// while an agent is still alive. If that agent can write the run back to
// `running`, it un-discards itself and reappears as the project's state.
//
// Two lines of defence, both asserted: the agent's writes are fenced (tested
// in the agent package, which owns those filters), and the readers skip a
// discarded run whatever its status — so a reader is correct even if a writer
// is not.
func TestInteg_RunRepo_ADiscardedRunStaysHiddenEvenIfItGoesActiveAgain(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	base := time.Date(2026, 6, 4, 9, 0, 0, 0, time.UTC)
	completedAt := base.Add(10 * time.Minute)
	older := seedRunForProject(t, ctx, "proj-zombie", "completed", base, &completedAt)
	zombie := seedRunForProject(t, ctx, "proj-zombie", "failed", base.Add(1*time.Hour), nil)

	if err := repo.Discard(ctx, zombie, 1); err != nil {
		t.Fatalf("discard: %v", err)
	}
	// A surviving agent writing itself back to life, bypassing its own fences.
	if _, err := testDB.Collection("discovery_runs").UpdateByID(ctx, mustOID(t, zombie),
		bson.M{"$set": bson.M{"status": "running"}}); err != nil {
		t.Fatalf("resurrect: %v", err)
	}

	latest, err := repo.GetLatestByProject(ctx, "proj-zombie")
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil || latest.ID != older {
		t.Errorf("GetLatestByProject = %v, want the older completed run — a discarded run must not win the ACTIVE query either", latest)
	}
	byProjects, err := repo.LatestByProjects(ctx, []string{"proj-zombie"})
	if err != nil {
		t.Fatal(err)
	}
	if got := byProjects["proj-zombie"]; got == nil || got.ID != older {
		t.Errorf("LatestByProjects = %v, want the same answer — its active pass needs the predicate too", got)
	}
}

// A discarded run must not count as the project's active run. It can look
// active: an agent that survived the startup sweep — including one running a
// build older than the discard fences, which is the upgrade case — can write
// `running` back onto a run the operator threw away. The latest-run readers
// hide that document, but the CONCURRENCY readers are a different question
// with a different consequence: a zombie nobody can resume would block every
// future run for the project.
func TestInteg_RunRepo_ADiscardedRunDoesNotBlockNewRuns(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)

	zombie := seedRunForProject(t, ctx, "proj-block", "failed", time.Now(), nil)
	if err := repo.Discard(ctx, zombie, 1); err != nil {
		t.Fatalf("discard: %v", err)
	}
	// The surviving agent writing itself back to life.
	if _, err := testDB.Collection("discovery_runs").UpdateByID(ctx, mustOID(t, zombie),
		bson.M{"$set": bson.M{"status": "running"}}); err != nil {
		t.Fatalf("resurrect: %v", err)
	}

	running, err := repo.GetRunningByProject(ctx, "proj-block")
	if err != nil {
		t.Fatal(err)
	}
	if running != nil {
		t.Errorf("GetRunningByProject = %s, want nil — a discarded run is not active, so it must not gate a new run", running.ID)
	}

	// A valid ObjectId: GetOtherRunningByProject parses the exclusion, so a
	// placeholder string fails before the filter is ever applied.
	other, err := repo.GetOtherRunningByProject(ctx, "proj-block", primitive.NewObjectID().Hex())
	if err != nil {
		t.Fatal(err)
	}
	if other != nil {
		t.Errorf("GetOtherRunningByProject = %s, want nil — the two readers answer the same question and must agree", other.ID)
	}

	// A genuinely running run is still found, which is what these gates exist
	// for and what the predicate must not have broken.
	live := seedRunForProject(t, ctx, "proj-block", "running", time.Now(), nil)
	if got, err := repo.GetRunningByProject(ctx, "proj-block"); err != nil || got == nil || got.ID != live {
		t.Errorf("GetRunningByProject = %v (err %v), want the live run", got, err)
	}
}

// HasResultForRun against real Mongo, because the whole point of it is that
// the run document's pointer and the stored discovery can disagree:
// BeginResume clears discovery_id, so the only reliable answer lives in the
// discoveries collection.
func TestInteg_DiscoveryRepo_HasResultForRun(t *testing.T) {
	ctx := context.Background()
	if _, err := testDB.Collection("discoveries").DeleteMany(ctx, bson.M{}); err != nil {
		t.Fatalf("drop discoveries: %v", err)
	}
	repo := NewDiscoveryRepository(testDB)

	if _, err := testDB.Collection("discoveries").InsertOne(ctx, bson.M{
		"project_id": "proj-res", "run_id": "run-with-result",
	}); err != nil {
		t.Fatalf("seed discovery: %v", err)
	}

	for _, tc := range []struct {
		name  string
		runID string
		want  bool
	}{
		{"a run whose discovery is stored", "run-with-result", true},
		{"a run with none", "run-without", false},
		{"an empty id is not a run", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.HasResultForRun(ctx, tc.runID)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("HasResultForRun(%q) = %v, want %v", tc.runID, got, tc.want)
			}
		})
	}
}
