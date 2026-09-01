//go:build integration

package database

import (
	"context"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

const catalogTestProject = "catalog-cache-proj"

// TestAgentInteg_CatalogCache_RoundTrip is the basic contract: what a catalog
// source offered at a given config hash comes back, and a different hash does
// not — so a config change invalidates by miss, exactly as the table cache does.
func TestAgentInteg_CatalogCache_RoundTrip(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewSchemaCacheRepository(db)

	refs := []string{"sessions", "activeUsers", "sessionDefaultChannelGroup"}
	if err := r.SaveCatalog(ctx, catalogTestProject, "ga", "hash-a", refs); err != nil {
		t.Fatalf("SaveCatalog: %v", err)
	}

	got, err := r.FindCatalog(ctx, catalogTestProject, "ga", "hash-a")
	if err != nil {
		t.Fatalf("FindCatalog: %v", err)
	}
	if len(got) != len(refs) {
		t.Fatalf("FindCatalog returned %d refs, want %d", len(got), len(refs))
	}

	// A config change moves the hash, and the old entry must not answer for it.
	stale, err := r.FindCatalog(ctx, catalogTestProject, "ga", "hash-b")
	if err != nil {
		t.Fatalf("FindCatalog(other hash): %v", err)
	}
	if stale != nil {
		t.Errorf("FindCatalog on a changed hash = %v, want nil", stale)
	}
}

// TestAgentInteg_CatalogCache_NotReadAsTableSchemas is the important one.
//
// Catalog entries share the schema-cache collection. If the table read does
// not exclude them, a catalog document decodes as a table entry under the
// empty key — and Find then returns a NON-EMPTY map. Every consumer reads a
// non-empty map as "this datasource has cached tables", so a source with no
// tables would be reported as having exactly one, blank, table: the source
// lying about its own shape, which is the failure this whole path exists to
// avoid.
func TestAgentInteg_CatalogCache_NotReadAsTableSchemas(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewSchemaCacheRepository(db)

	if err := r.SaveCatalog(ctx, catalogTestProject, "ga", "hash-a", []string{"sessions"}); err != nil {
		t.Fatalf("SaveCatalog: %v", err)
	}

	tables, err := r.Find(ctx, catalogTestProject, "ga", "hash-a")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(tables) != 0 {
		t.Fatalf("Find returned %d table schemas for a catalog-only datasource, want 0: %+v", len(tables), tables)
	}
}

// TestAgentInteg_CatalogCache_SurvivesATableSave covers the other direction of
// the same collision: saving tables must not delete the catalog entry beside
// them, or a source with both silently loses half its index.
func TestAgentInteg_CatalogCache_SurvivesATableSave(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewSchemaCacheRepository(db)

	if err := r.SaveCatalog(ctx, catalogTestProject, "mixed", "hash-a", []string{"sessions"}); err != nil {
		t.Fatalf("SaveCatalog: %v", err)
	}
	if err := r.Save(ctx, catalogTestProject, "mixed", "hash-a", map[string]models.TableSchema{
		"sales.orders": {TableName: "sales.orders", RowCount: 10},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	refs, err := r.FindCatalog(ctx, catalogTestProject, "mixed", "hash-a")
	if err != nil {
		t.Fatalf("FindCatalog: %v", err)
	}
	if len(refs) != 1 || refs[0] != "sessions" {
		t.Errorf("catalog refs = %v, want them to survive a table save", refs)
	}

	tables, err := r.Find(ctx, catalogTestProject, "mixed", "hash-a")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(tables) != 1 {
		t.Errorf("table schemas = %+v, want exactly the one saved (no catalog doc leaking in)", tables)
	}
}

// TestAgentInteg_CatalogCache_ResaveReplaces: re-indexing replaces the catalog
// rather than accumulating, so an item the source stopped offering stops being
// trusted.
func TestAgentInteg_CatalogCache_ResaveReplaces(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewSchemaCacheRepository(db)

	if err := r.SaveCatalog(ctx, catalogTestProject, "ga", "hash-a", []string{"sessions", "retiredMetric"}); err != nil {
		t.Fatalf("SaveCatalog: %v", err)
	}
	if err := r.SaveCatalog(ctx, catalogTestProject, "ga", "hash-a", []string{"sessions"}); err != nil {
		t.Fatalf("SaveCatalog (resave): %v", err)
	}

	refs, err := r.FindCatalog(ctx, catalogTestProject, "ga", "hash-a")
	if err != nil {
		t.Fatalf("FindCatalog: %v", err)
	}
	if len(refs) != 1 || refs[0] != "sessions" {
		t.Errorf("refs = %v, want only the current catalog; a dropped item must not linger", refs)
	}
}

// TestAgentInteg_CatalogCache_EmptySaveClearsPriorRefs is the stale-authority
// case. "This datasource now offers nothing" is a real statement, and treating
// it as a no-op leaves the previous list standing as the authority — so search
// keeps trusting vector points for items the source has since dropped, and the
// only thing that would ever correct it is a future non-empty save.
func TestAgentInteg_CatalogCache_EmptySaveClearsPriorRefs(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewSchemaCacheRepository(db)

	if err := r.SaveCatalog(ctx, catalogTestProject, "ga", "hash-a", []string{"sessions", "retiredMetric"}); err != nil {
		t.Fatalf("SaveCatalog: %v", err)
	}
	if err := r.SaveCatalog(ctx, catalogTestProject, "ga", "hash-a", nil); err != nil {
		t.Fatalf("SaveCatalog(empty): %v", err)
	}

	refs, err := r.FindCatalog(ctx, catalogTestProject, "ga", "hash-a")
	if err != nil {
		t.Fatalf("FindCatalog: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("refs = %v, want none — an empty save must retract the previous catalog, not be ignored", refs)
	}
}
