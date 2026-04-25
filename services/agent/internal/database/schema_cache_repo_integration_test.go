//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Integration tests for SchemaCacheRepository against a live Mongo
// testcontainer. Covers: cold lookup, round-trip, hash-mismatch miss,
// re-save overwrites stale rows, validation errors, invalidation, and
// the round-trip of rich TableSchema fields (columns + sample data).

const cacheTestProject = "proj-cache-integ-1"

func TestAgentInteg_SchemaCache_ColdLookup_Nil(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewSchemaCacheRepository(db)

	got, err := r.Find(ctx, cacheTestProject, "hash-a")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got != nil {
		t.Errorf("cold Find should return nil map, got %+v", got)
	}
}

func TestAgentInteg_SchemaCache_SaveFindRoundTrip(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewSchemaCacheRepository(db)

	input := map[string]models.TableSchema{
		"dbo.orders": {
			TableName: "dbo.orders",
			RowCount:  1000,
			Columns: []models.ColumnInfo{
				{Name: "id", Type: "INTEGER", Nullable: false, Category: "primary_key"},
				{Name: "created_at", Type: "TIMESTAMP", Nullable: false, Category: "time"},
			},
			KeyColumns: []string{"id"},
			Metrics:    []string{"total_amount"},
			Dimensions: []string{"status"},
			SampleData: []map[string]interface{}{
				{"id": int32(1), "status": "paid"},
			},
			DiscoveredAt: time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC),
		},
		"dbo.customers": {
			TableName:    "dbo.customers",
			RowCount:     50,
			Columns:      []models.ColumnInfo{{Name: "email", Type: "STRING"}},
			DiscoveredAt: time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC),
		},
	}

	if err := r.Save(ctx, cacheTestProject, "hash-b", input); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := r.Find(ctx, cacheTestProject, "hash-b")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Find returned %d entries, want 2", len(got))
	}
	orders, ok := got["dbo.orders"]
	if !ok {
		t.Fatal("missing dbo.orders")
	}
	if orders.RowCount != 1000 {
		t.Errorf("orders.RowCount = %d, want 1000", orders.RowCount)
	}
	if len(orders.Columns) != 2 {
		t.Errorf("orders.Columns len = %d, want 2", len(orders.Columns))
	}
	if len(orders.SampleData) != 1 {
		t.Errorf("orders.SampleData len = %d, want 1", len(orders.SampleData))
	}
}

func TestAgentInteg_SchemaCache_HashMismatch_MissesCache(t *testing.T) {
	// The key guard against stale cache reuse: Find({hash: X}) must
	// not return rows tagged with {hash: Y}, even if the project id
	// matches.
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewSchemaCacheRepository(db)

	if err := r.Save(ctx, cacheTestProject, "hash-original",
		map[string]models.TableSchema{"a.t": {TableName: "a.t"}},
	); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Find with a different hash — must miss.
	got, err := r.Find(ctx, cacheTestProject, "hash-different")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got != nil {
		t.Errorf("hash-mismatched Find returned %+v, want nil", got)
	}
}

func TestAgentInteg_SchemaCache_Resave_OverwritesPrior(t *testing.T) {
	// When the warehouse config changes, the next Save must flush
	// rows tagged with any prior hash — we'd otherwise grow without
	// bound as projects drift across dataset lists.
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewSchemaCacheRepository(db)

	if err := r.Save(ctx, cacheTestProject, "hash-v1",
		map[string]models.TableSchema{
			"dbo.old_a": {TableName: "dbo.old_a"},
			"dbo.old_b": {TableName: "dbo.old_b"},
		}); err != nil {
		t.Fatalf("Save v1: %v", err)
	}
	if err := r.Save(ctx, cacheTestProject, "hash-v2",
		map[string]models.TableSchema{"dbo.new": {TableName: "dbo.new"}},
	); err != nil {
		t.Fatalf("Save v2: %v", err)
	}

	// Old hash must now miss.
	if got, _ := r.Find(ctx, cacheTestProject, "hash-v1"); got != nil {
		t.Errorf("old-hash rows should be deleted, got %+v", got)
	}

	// New hash should return only the v2 rows.
	got, err := r.Find(ctx, cacheTestProject, "hash-v2")
	if err != nil {
		t.Fatalf("Find v2: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("Find v2 len = %d, want 1 (stale v1 rows leaked)", len(got))
	}
	if _, ok := got["dbo.new"]; !ok {
		t.Errorf("v2 Find missing dbo.new: got %+v", got)
	}
}

func TestAgentInteg_SchemaCache_Invalidate_DropsAllForProject(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewSchemaCacheRepository(db)

	if err := r.Save(ctx, cacheTestProject, "hash-1",
		map[string]models.TableSchema{"a.t": {TableName: "a.t"}},
	); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Independent project — must not be touched by Invalidate.
	if err := r.Save(ctx, "proj-other", "hash-other",
		map[string]models.TableSchema{"b.t": {TableName: "b.t"}},
	); err != nil {
		t.Fatalf("Save other: %v", err)
	}

	if err := r.Invalidate(ctx, cacheTestProject); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}

	if got, _ := r.Find(ctx, cacheTestProject, "hash-1"); got != nil {
		t.Errorf("Invalidate left rows behind: %+v", got)
	}
	if got, _ := r.Find(ctx, "proj-other", "hash-other"); len(got) != 1 {
		t.Errorf("Invalidate bled into other project: got %+v", got)
	}
}

func TestAgentInteg_SchemaCache_SaveEmpty_NoOp(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewSchemaCacheRepository(db)

	// Prime with a real row to make sure Save with empty schemas
	// doesn't accidentally delete it.
	if err := r.Save(ctx, cacheTestProject, "hash-real",
		map[string]models.TableSchema{"a.t": {TableName: "a.t"}},
	); err != nil {
		t.Fatalf("Save real: %v", err)
	}

	if err := r.Save(ctx, cacheTestProject, "hash-real", map[string]models.TableSchema{}); err != nil {
		t.Fatalf("Save empty: %v", err)
	}

	got, err := r.Find(ctx, cacheTestProject, "hash-real")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("empty Save wiped existing rows: len=%d", len(got))
	}
}

func TestAgentInteg_SchemaCache_ValidationPaths(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewSchemaCacheRepository(db)

	// Empty projectID / hash — every entry point should reject up-front.
	if _, err := r.Find(ctx, "", "h"); err == nil {
		t.Error("Find with empty projectID should error")
	}
	if _, err := r.Find(ctx, "p", ""); err == nil {
		t.Error("Find with empty hash should error")
	}
	if err := r.Save(ctx, "", "h", map[string]models.TableSchema{"x.y": {}}); err == nil {
		t.Error("Save with empty projectID should error")
	}
	if err := r.Save(ctx, "p", "", map[string]models.TableSchema{"x.y": {}}); err == nil {
		t.Error("Save with empty hash should error")
	}
	if err := r.Invalidate(ctx, ""); err == nil {
		t.Error("Invalidate with empty projectID should error")
	}
}

func TestAgentInteg_SchemaCache_ProjectIsolation(t *testing.T) {
	// Find for project A must never leak rows from project B even
	// when they share a hash string (e.g. both freshly indexed
	// warehouses happen to produce identical SHA-256 digests, which
	// is astronomically unlikely but let's not rely on that).
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewSchemaCacheRepository(db)

	shared := "hash-shared"

	if err := r.Save(ctx, "proj-A", shared,
		map[string]models.TableSchema{"a.one": {TableName: "a.one"}},
	); err != nil {
		t.Fatalf("Save A: %v", err)
	}
	if err := r.Save(ctx, "proj-B", shared,
		map[string]models.TableSchema{"b.one": {TableName: "b.one"}, "b.two": {TableName: "b.two"}},
	); err != nil {
		t.Fatalf("Save B: %v", err)
	}

	gotA, err := r.Find(ctx, "proj-A", shared)
	if err != nil {
		t.Fatalf("Find A: %v", err)
	}
	if len(gotA) != 1 {
		t.Errorf("Find A len = %d, want 1", len(gotA))
	}
	gotB, err := r.Find(ctx, "proj-B", shared)
	if err != nil {
		t.Fatalf("Find B: %v", err)
	}
	if len(gotB) != 2 {
		t.Errorf("Find B len = %d, want 2", len(gotB))
	}
}
