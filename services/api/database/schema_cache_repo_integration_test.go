//go:build integration

package database

import (
	"context"
	"strconv"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

// The agent module owns Save/Find for project_schema_cache; the API
// module only needs Invalidate so the dashboard's Advanced → "Clear
// schema cache" button can drop the cache without hopping to the agent.
// These tests exercise that single method against a real Mongo via the
// shared testDB fixture (integration_test.go TestMain).

// seedCacheRows writes raw cache rows for a project so we can verify
// Invalidate clears them. Mirrors the on-disk shape produced by the
// agent-side repo — kept inline to avoid a cross-module import.
func seedCacheRows(t *testing.T, projectID, hash string, n int) {
	t.Helper()
	col := testDB.Collection("project_schema_cache")
	now := time.Now().UTC()
	docs := make([]interface{}, 0, n)
	for i := 0; i < n; i++ {
		docs = append(docs, bson.M{
			"project_id":     projectID,
			"warehouse_hash": hash,
			"schema_key":     "dbo.t" + strconv.Itoa(i),
			"schema":         bson.M{"name": "t"},
			"cached_at":      now,
		})
	}
	if _, err := col.InsertMany(context.Background(), docs); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func countCacheRows(t *testing.T, projectID string) int64 {
	t.Helper()
	n, err := testDB.Collection("project_schema_cache").CountDocuments(
		context.Background(), bson.M{"project_id": projectID})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestInteg_SchemaCacheRepo_Invalidate_ClearsAllRows(t *testing.T) {
	ctx := context.Background()
	r := NewSchemaCacheRepository(testDB)

	projectID := "proj-cache-integ-1"
	t.Cleanup(func() { _ = r.Invalidate(ctx, projectID) })

	seedCacheRows(t, projectID, "hash-1", 5)
	if got := countCacheRows(t, projectID); got != 5 {
		t.Fatalf("setup: expected 5 rows, got %d", got)
	}

	if err := r.Invalidate(ctx, projectID); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if got := countCacheRows(t, projectID); got != 0 {
		t.Errorf("after Invalidate: expected 0 rows, got %d", got)
	}
}

func TestInteg_SchemaCacheRepo_Invalidate_OnlyTargetsProject(t *testing.T) {
	ctx := context.Background()
	r := NewSchemaCacheRepository(testDB)

	target := "proj-cache-integ-target"
	bystander := "proj-cache-integ-bystander"
	t.Cleanup(func() {
		_ = r.Invalidate(ctx, target)
		_ = r.Invalidate(ctx, bystander)
	})

	seedCacheRows(t, target, "h", 3)
	seedCacheRows(t, bystander, "h", 4)

	if err := r.Invalidate(ctx, target); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}

	if got := countCacheRows(t, target); got != 0 {
		t.Errorf("target rows after Invalidate: %d", got)
	}
	if got := countCacheRows(t, bystander); got != 4 {
		t.Errorf("bystander rows touched: got %d, want 4", got)
	}
}

func TestInteg_SchemaCacheRepo_Invalidate_MultipleHashesSameProject(t *testing.T) {
	// A project's effective warehouse hash can change (config edit, cache
	// version bump). Save() is supposed to wipe prior hashes for the
	// project, but if a partial write left rows for two hashes, an
	// explicit Invalidate must drop ALL of them — not just one hash.
	ctx := context.Background()
	r := NewSchemaCacheRepository(testDB)

	projectID := "proj-cache-integ-multihash"
	t.Cleanup(func() { _ = r.Invalidate(ctx, projectID) })

	seedCacheRows(t, projectID, "hash-old", 2)
	seedCacheRows(t, projectID, "hash-new", 3)
	if got := countCacheRows(t, projectID); got != 5 {
		t.Fatalf("setup: expected 5 rows across 2 hashes, got %d", got)
	}

	if err := r.Invalidate(ctx, projectID); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if got := countCacheRows(t, projectID); got != 0 {
		t.Errorf("rows remaining: %d", got)
	}
}

func TestInteg_SchemaCacheRepo_Invalidate_EmptyCache_NoError(t *testing.T) {
	// Idempotency: clearing a project that has nothing cached is a
	// no-op, never an error. The UI button can be clicked freely.
	ctx := context.Background()
	r := NewSchemaCacheRepository(testDB)

	if err := r.Invalidate(ctx, "proj-cache-integ-empty"); err != nil {
		t.Fatalf("Invalidate on empty cache: %v", err)
	}
}

func TestInteg_SchemaCacheRepo_Invalidate_RejectsEmptyProjectID(t *testing.T) {
	// Projects can't have empty IDs. A bug or malformed request must
	// not turn into a "delete every cache row" wildcard.
	r := NewSchemaCacheRepository(testDB)
	if err := r.Invalidate(context.Background(), ""); err == nil {
		t.Error("expected error for empty projectID, got nil")
	}
}

func TestInteg_SchemaCacheRepo_Invalidate_Idempotent(t *testing.T) {
	// Clicking the UI button twice in a row must keep working. Second
	// call has nothing left to delete and must return nil.
	ctx := context.Background()
	r := NewSchemaCacheRepository(testDB)

	projectID := "proj-cache-integ-idempotent"
	t.Cleanup(func() { _ = r.Invalidate(ctx, projectID) })

	seedCacheRows(t, projectID, "h", 2)
	if err := r.Invalidate(ctx, projectID); err != nil {
		t.Fatalf("first Invalidate: %v", err)
	}
	if err := r.Invalidate(ctx, projectID); err != nil {
		t.Fatalf("second Invalidate (idempotency): %v", err)
	}
}

// seedCacheRowsAt seeds rows pinned to a specific cached_at, useful for
// LastCachedAt assertions where ordering matters.
func seedCacheRowsAt(t *testing.T, projectID, hash string, n int, cachedAt time.Time) {
	t.Helper()
	col := testDB.Collection("project_schema_cache")
	docs := make([]interface{}, 0, n)
	for i := 0; i < n; i++ {
		docs = append(docs, bson.M{
			"project_id":     projectID,
			"warehouse_hash": hash,
			"schema_key":     "dbo.t" + strconv.Itoa(i),
			"schema":         bson.M{"name": "t"},
			"cached_at":      cachedAt,
		})
	}
	if _, err := col.InsertMany(context.Background(), docs); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestInteg_SchemaCacheRepo_LastCachedAt_ReturnsMostRecent(t *testing.T) {
	ctx := context.Background()
	r := NewSchemaCacheRepository(testDB)

	projectID := "proj-cache-integ-lastcached"
	t.Cleanup(func() { _ = r.Invalidate(ctx, projectID) })

	older := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 4, 25, 10, 30, 0, 0, time.UTC)
	seedCacheRowsAt(t, projectID, "h-old", 2, older)
	seedCacheRowsAt(t, projectID, "h-new", 3, newer)

	got, err := r.LastCachedAt(ctx, projectID)
	if err != nil {
		t.Fatalf("LastCachedAt: %v", err)
	}
	// Mongo BSON datetime is millisecond-precision; allow up to 1ms drift.
	if !got.Equal(newer) && got.Sub(newer).Abs() > time.Millisecond {
		t.Errorf("LastCachedAt = %v, want %v", got, newer)
	}
}

func TestInteg_SchemaCacheRepo_LastCachedAt_EmptyCache(t *testing.T) {
	ctx := context.Background()
	r := NewSchemaCacheRepository(testDB)

	got, err := r.LastCachedAt(ctx, "proj-cache-integ-lastcached-empty")
	if err != nil {
		t.Fatalf("LastCachedAt on empty cache: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("LastCachedAt on empty cache = %v, want zero time", got)
	}
}

func TestInteg_SchemaCacheRepo_LastCachedAt_OnlyTargetsProject(t *testing.T) {
	ctx := context.Background()
	r := NewSchemaCacheRepository(testDB)

	target := "proj-cache-integ-lastcached-target"
	bystander := "proj-cache-integ-lastcached-bystander"
	t.Cleanup(func() {
		_ = r.Invalidate(ctx, target)
		_ = r.Invalidate(ctx, bystander)
	})

	targetTime := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	bystanderTime := time.Date(2026, 4, 25, 10, 30, 0, 0, time.UTC) // newer but different project
	seedCacheRowsAt(t, target, "h", 1, targetTime)
	seedCacheRowsAt(t, bystander, "h", 1, bystanderTime)

	got, err := r.LastCachedAt(ctx, target)
	if err != nil {
		t.Fatalf("LastCachedAt: %v", err)
	}
	if !got.Equal(targetTime) && got.Sub(targetTime).Abs() > time.Millisecond {
		t.Errorf("LastCachedAt = %v, want target time %v (must not pick up bystander %v)", got, targetTime, bystanderTime)
	}
}

func TestInteg_SchemaCacheRepo_LastCachedAt_RejectsEmptyProjectID(t *testing.T) {
	r := NewSchemaCacheRepository(testDB)
	if _, err := r.LastCachedAt(context.Background(), ""); err == nil {
		t.Error("expected error for empty projectID, got nil")
	}
}

// --- ListTables ---

// seedCacheRowsWithKeys writes raw rows with explicit schema_key
// values so the ListTables test can verify distinct + sort semantics
// against a known input set (not the strconv-formatted defaults
// seedCacheRows produces).
func seedCacheRowsWithKeys(t *testing.T, projectID, hash string, keys []string) {
	t.Helper()
	col := testDB.Collection("project_schema_cache")
	now := time.Now().UTC()
	docs := make([]interface{}, 0, len(keys))
	for _, k := range keys {
		docs = append(docs, bson.M{
			"project_id":     projectID,
			"warehouse_hash": hash,
			"schema_key":     k,
			"schema":         bson.M{"name": "t"},
			"cached_at":      now,
		})
	}
	if _, err := col.InsertMany(context.Background(), docs); err != nil {
		t.Fatalf("seed keys: %v", err)
	}
}

func TestInteg_SchemaCacheRepo_ListTables_DistinctAndSorted(t *testing.T) {
	ctx := context.Background()
	r := NewSchemaCacheRepository(testDB)

	projectID := "proj-list-tables-distinct"
	t.Cleanup(func() { _ = r.Invalidate(ctx, projectID) })

	// Two warehouse_hash generations — same project — and a duplicate
	// key across them. ListTables must dedup on schema_key and return
	// values sorted ascending.
	seedCacheRowsWithKeys(t, projectID, "h1", []string{"dbo.orders", "dbo.customers"})
	seedCacheRowsWithKeys(t, projectID, "h2", []string{"dbo.orders", "dbo.products", "demo.events"})

	got, err := r.ListTables(ctx, projectID)
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	want := []string{"dbo.customers", "dbo.orders", "dbo.products", "demo.events"}
	if len(got) != len(want) {
		t.Fatalf("ListTables = %v, want %v", got, want)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("ListTables[%d] = %q, want %q (got=%v)", i, got[i], v, got)
		}
	}
}

func TestInteg_SchemaCacheRepo_ListTables_EmptyCacheReturnsEmptySlice(t *testing.T) {
	ctx := context.Background()
	r := NewSchemaCacheRepository(testDB)

	got, err := r.ListTables(ctx, "proj-list-tables-empty")
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	// JSON marshaling depends on this being an allocated slice (so it
	// renders as `[]`) rather than nil (`null`). Assert both length and
	// nil-ness.
	if got == nil {
		t.Fatal("ListTables returned nil; want an empty allocated slice so JSON renders as []")
	}
	if len(got) != 0 {
		t.Errorf("ListTables = %v, want empty", got)
	}
}

func TestInteg_SchemaCacheRepo_ListTables_IsolatedAcrossProjects(t *testing.T) {
	ctx := context.Background()
	r := NewSchemaCacheRepository(testDB)

	target := "proj-list-tables-target"
	bystander := "proj-list-tables-bystander"
	t.Cleanup(func() {
		_ = r.Invalidate(ctx, target)
		_ = r.Invalidate(ctx, bystander)
	})

	seedCacheRowsWithKeys(t, target, "h", []string{"a.x", "a.y"})
	seedCacheRowsWithKeys(t, bystander, "h", []string{"b.shouldnt_appear"})

	got, err := r.ListTables(ctx, target)
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	if len(got) != 2 || got[0] != "a.x" || got[1] != "a.y" {
		t.Errorf("ListTables(target) = %v, want [a.x a.y] — must not include bystander rows", got)
	}
}

func TestInteg_SchemaCacheRepo_ListTables_RejectsEmptyProjectID(t *testing.T) {
	r := NewSchemaCacheRepository(testDB)
	if _, err := r.ListTables(context.Background(), ""); err == nil {
		t.Error("expected error for empty projectID, got nil")
	}
}
