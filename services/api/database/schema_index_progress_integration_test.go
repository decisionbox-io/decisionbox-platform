//go:build integration

package database

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// schema-index progress is a new collection added in this migration.
// These tests exercise the full upsert / FIFO-claim / concurrency behaviour
// against a real Mongo via testcontainers. They share the same testDB fixture
// as the rest of the integration tests (integration_test.go TestMain).

func TestInteg_SchemaIndexProgress_ResetAndGet(t *testing.T) {
	ctx := context.Background()
	r := NewSchemaIndexProgressRepository(testDB)

	projectID := "proj-idx-integ-1"
	t.Cleanup(func() { _ = r.Delete(ctx, projectID) })

	// Get before Reset: returns nil, not error.
	got, err := r.Get(ctx, projectID)
	if err != nil {
		t.Fatalf("Get (no doc): %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil before Reset, got %+v", got)
	}

	if err := r.Reset(ctx, projectID, "run-a"); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	got, err = r.Get(ctx, projectID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil after Reset")
	}
	if got.RunID != "run-a" {
		t.Errorf("RunID = %q", got.RunID)
	}
	if got.Phase != models.SchemaIndexPhaseListingTables {
		t.Errorf("initial phase = %q, want listing_tables", got.Phase)
	}
	if got.TablesTotal != 0 || got.TablesDone != 0 {
		t.Errorf("fresh doc should have zero counters, got %+v", got)
	}
	if got.StartedAt.IsZero() {
		t.Error("StartedAt should be stamped")
	}
}

func TestInteg_SchemaIndexProgress_SecondResetClearsCounters(t *testing.T) {
	ctx := context.Background()
	r := NewSchemaIndexProgressRepository(testDB)

	projectID := "proj-idx-integ-reset-2"
	t.Cleanup(func() { _ = r.Delete(ctx, projectID) })

	_ = r.Reset(ctx, projectID, "run-1")
	_ = r.UpdateTables(ctx, projectID, 500, 300)
	_ = r.RecordError(ctx, projectID, "worker crashed")

	// Second reset (user hit retry) must wipe counters and error.
	if err := r.Reset(ctx, projectID, "run-2"); err != nil {
		t.Fatalf("Reset retry: %v", err)
	}
	got, _ := r.Get(ctx, projectID)
	if got.RunID != "run-2" {
		t.Errorf("RunID = %q", got.RunID)
	}
	if got.TablesTotal != 0 || got.TablesDone != 0 {
		t.Errorf("counters not cleared: %+v", got)
	}
	if got.ErrorMessage != "" {
		t.Errorf("ErrorMessage not cleared: %q", got.ErrorMessage)
	}
}

func TestInteg_SchemaIndexProgress_SetPhaseAndTables(t *testing.T) {
	ctx := context.Background()
	r := NewSchemaIndexProgressRepository(testDB)
	projectID := "proj-idx-integ-phases"
	t.Cleanup(func() { _ = r.Delete(ctx, projectID) })

	_ = r.Reset(ctx, projectID, "run-1")

	if err := r.SetPhase(ctx, projectID, models.SchemaIndexPhaseDescribingTables); err != nil {
		t.Fatalf("SetPhase: %v", err)
	}
	if err := r.UpdateTables(ctx, projectID, 100, 25); err != nil {
		t.Fatalf("UpdateTables: %v", err)
	}

	got, _ := r.Get(ctx, projectID)
	if got.Phase != "describing_tables" {
		t.Errorf("Phase = %q", got.Phase)
	}
	if got.TablesTotal != 100 || got.TablesDone != 25 {
		t.Errorf("counters = %d/%d, want 25/100", got.TablesDone, got.TablesTotal)
	}
}

func TestInteg_SchemaIndexProgress_UpdateTables_ClampsDone(t *testing.T) {
	ctx := context.Background()
	r := NewSchemaIndexProgressRepository(testDB)
	projectID := "proj-idx-integ-clamp"
	t.Cleanup(func() { _ = r.Delete(ctx, projectID) })

	_ = r.Reset(ctx, projectID, "run-1")
	if err := r.UpdateTables(ctx, projectID, 10, 50); err != nil {
		t.Fatalf("UpdateTables: %v", err)
	}
	got, _ := r.Get(ctx, projectID)
	if got.TablesDone != 10 {
		t.Errorf("done = %d, want clamped to total=10", got.TablesDone)
	}
}

func TestInteg_SchemaIndexProgress_IncrementDone_Concurrent(t *testing.T) {
	ctx := context.Background()
	r := NewSchemaIndexProgressRepository(testDB)
	projectID := "proj-idx-integ-concurrent"
	t.Cleanup(func() { _ = r.Delete(ctx, projectID) })

	_ = r.Reset(ctx, projectID, "run-1")
	_ = r.UpdateTables(ctx, projectID, 100, 0)

	// 50 goroutines × 2 increments = 100 total. Atomic $inc must not lose
	// writes under concurrent workers (the default 8-worker blurb pool).
	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_ = r.IncrementDone(ctx, projectID, 1)
			_ = r.IncrementDone(ctx, projectID, 1)
		}()
	}
	wg.Wait()

	got, _ := r.Get(ctx, projectID)
	if got.TablesDone != 100 {
		t.Errorf("TablesDone = %d, want 100 (atomic $inc lost writes?)", got.TablesDone)
	}
}

func TestInteg_SchemaIndexProgress_CallsBeforeResetFail(t *testing.T) {
	ctx := context.Background()
	r := NewSchemaIndexProgressRepository(testDB)
	projectID := "proj-idx-integ-no-reset"
	t.Cleanup(func() { _ = r.Delete(ctx, projectID) })

	if err := r.SetPhase(ctx, projectID, "embedding"); err == nil {
		t.Error("SetPhase before Reset should fail (no doc to match)")
	}
	if err := r.UpdateTables(ctx, projectID, 10, 5); err == nil {
		t.Error("UpdateTables before Reset should fail")
	}
	if err := r.IncrementDone(ctx, projectID, 1); err == nil {
		t.Error("IncrementDone before Reset should fail")
	}
	if err := r.RecordError(ctx, projectID, "boom"); err == nil {
		t.Error("RecordError before Reset should fail")
	}
}

func TestInteg_SchemaIndexProgress_Delete(t *testing.T) {
	ctx := context.Background()
	r := NewSchemaIndexProgressRepository(testDB)
	projectID := "proj-idx-integ-delete"

	_ = r.Reset(ctx, projectID, "run-1")
	if err := r.Delete(ctx, projectID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, _ := r.Get(ctx, projectID)
	if got != nil {
		t.Errorf("Delete should remove doc, got %+v", got)
	}

	// Delete is idempotent — deleting a missing doc does not error.
	if err := r.Delete(ctx, projectID); err != nil {
		t.Errorf("idempotent Delete: %v", err)
	}
}

// --- ProjectRepository schema-index lifecycle helpers ---

func makeTestProject(t *testing.T, ctx context.Context, name string) *models.Project {
	t.Helper()
	p := &models.Project{
		Name:     name,
		Domain:   "gaming",
		Category: "match3",
	}
	repo := NewProjectRepository(testDB)
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("create project %q: %v", name, err)
	}
	t.Cleanup(func() {
		_ = repo.Delete(ctx, p.ID)
	})
	return p
}

func TestInteg_ProjectRepo_SetSchemaIndexStatus_Transitions(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)
	p := makeTestProject(t, ctx, "set-status-project")

	// Initial state: status not set.
	got, _ := repo.GetByID(ctx, p.ID)
	if got.SchemaIndexStatus != "" {
		t.Errorf("initial SchemaIndexStatus = %q, want empty", got.SchemaIndexStatus)
	}

	// pending → indexing → ready stamps schema_index_updated_at.
	if err := repo.SetSchemaIndexStatus(ctx, p.ID, models.SchemaIndexStatusPendingIndexing, ""); err != nil {
		t.Fatalf("set pending: %v", err)
	}
	if err := repo.SetSchemaIndexStatus(ctx, p.ID, models.SchemaIndexStatusIndexing, ""); err != nil {
		t.Fatalf("set indexing: %v", err)
	}
	if err := repo.SetSchemaIndexStatus(ctx, p.ID, models.SchemaIndexStatusReady, ""); err != nil {
		t.Fatalf("set ready: %v", err)
	}
	got, _ = repo.GetByID(ctx, p.ID)
	if got.SchemaIndexStatus != "ready" {
		t.Errorf("status = %q", got.SchemaIndexStatus)
	}
	if got.SchemaIndexUpdatedAt == nil {
		t.Error("schema_index_updated_at should be stamped on ready")
	}
}

func TestInteg_ProjectRepo_SetSchemaIndexStatus_FailedCarriesError(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)
	p := makeTestProject(t, ctx, "failed-status-project")

	if err := repo.SetSchemaIndexStatus(ctx, p.ID, models.SchemaIndexStatusFailed, "Qdrant unreachable"); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	got, _ := repo.GetByID(ctx, p.ID)
	if got.SchemaIndexStatus != "failed" {
		t.Errorf("status = %q", got.SchemaIndexStatus)
	}
	if !strings.Contains(got.SchemaIndexError, "Qdrant unreachable") {
		t.Errorf("error = %q", got.SchemaIndexError)
	}
}

func TestInteg_ProjectRepo_SetSchemaIndexStatus_ClearsErrorOnNonFailed(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)
	p := makeTestProject(t, ctx, "clear-error-project")

	_ = repo.SetSchemaIndexStatus(ctx, p.ID, models.SchemaIndexStatusFailed, "old error")

	if err := repo.SetSchemaIndexStatus(ctx, p.ID, models.SchemaIndexStatusPendingIndexing, ""); err != nil {
		t.Fatalf("set pending after failed: %v", err)
	}
	got, _ := repo.GetByID(ctx, p.ID)
	if got.SchemaIndexError != "" {
		t.Errorf("error should be cleared, got %q", got.SchemaIndexError)
	}
}

func TestInteg_ProjectRepo_SetSchemaIndexStatus_InvalidStatus(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)
	p := makeTestProject(t, ctx, "invalid-status-project")

	err := repo.SetSchemaIndexStatus(ctx, p.ID, "bogus", "")
	if err == nil {
		t.Fatal("invalid status should error")
	}
}

func TestInteg_ProjectRepo_SetSchemaIndexStatus_MissingProject(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)
	err := repo.SetSchemaIndexStatus(ctx, "000000000000000000000000", models.SchemaIndexStatusIndexing, "")
	if err == nil {
		t.Fatal("missing project should error")
	}
}

func TestInteg_ProjectRepo_ClaimNextPendingIndex_FIFOAndEmptyBehavior(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)

	// Empty queue: returns nil, nil.
	got, err := repo.ClaimNextPendingIndex(ctx)
	if err != nil {
		t.Fatalf("empty queue: %v", err)
	}
	if got != nil {
		t.Fatalf("empty queue should return nil, got %+v", got)
	}

	p1 := makeTestProject(t, ctx, "claim-fifo-1")
	p2 := makeTestProject(t, ctx, "claim-fifo-2")
	p3 := makeTestProject(t, ctx, "claim-fifo-3")

	// Stamp pending with staggered updated_at to assert FIFO ordering.
	_ = repo.SetSchemaIndexStatus(ctx, p1.ID, models.SchemaIndexStatusPendingIndexing, "")
	time.Sleep(10 * time.Millisecond)
	_ = repo.SetSchemaIndexStatus(ctx, p2.ID, models.SchemaIndexStatusPendingIndexing, "")
	time.Sleep(10 * time.Millisecond)
	_ = repo.SetSchemaIndexStatus(ctx, p3.ID, models.SchemaIndexStatusPendingIndexing, "")

	// Three claims → three distinct projects in insertion order.
	first, err := repo.ClaimNextPendingIndex(ctx)
	if err != nil || first == nil {
		t.Fatalf("claim 1: %v / nil=%v", err, first == nil)
	}
	if first.ID != p1.ID {
		t.Errorf("first claim = %q, want %q (oldest)", first.ID, p1.ID)
	}
	if first.SchemaIndexStatus != "indexing" {
		t.Errorf("claim must flip status to indexing, got %q", first.SchemaIndexStatus)
	}

	second, _ := repo.ClaimNextPendingIndex(ctx)
	if second.ID != p2.ID {
		t.Errorf("second claim = %q, want %q", second.ID, p2.ID)
	}
	third, _ := repo.ClaimNextPendingIndex(ctx)
	if third.ID != p3.ID {
		t.Errorf("third claim = %q, want %q", third.ID, p3.ID)
	}

	// Queue drained.
	extra, _ := repo.ClaimNextPendingIndex(ctx)
	if extra != nil {
		t.Errorf("queue should be empty, got %+v", extra)
	}
}

func TestInteg_ProjectRepo_ClaimNextPendingIndex_AtomicUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)

	// Create one pending project; N goroutines race to claim it. Exactly
	// one must win — the rest must see an empty queue.
	p := makeTestProject(t, ctx, "claim-race")
	_ = repo.SetSchemaIndexStatus(ctx, p.ID, models.SchemaIndexStatusPendingIndexing, "")

	const goroutines = 10
	results := make([]*models.Project, goroutines)
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = repo.ClaimNextPendingIndex(ctx)
		}(i)
	}
	wg.Wait()

	winners := 0
	for i := 0; i < goroutines; i++ {
		if errs[i] != nil {
			t.Errorf("goroutine %d: %v", i, errs[i])
		}
		if results[i] != nil {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("exactly one goroutine should claim, got %d winners", winners)
	}
}
