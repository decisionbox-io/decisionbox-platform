//go:build integration

package database

import (
	"context"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// dims renders refs as dimension items, which is what these cases are about —
// the kind matters only to the tests that name it.
func dims(refs ...string) []gowarehouse.CatalogItem {
	out := make([]gowarehouse.CatalogItem, 0, len(refs))
	for _, r := range refs {
		out = append(out, gowarehouse.CatalogItem{Ref: r, Kind: gowarehouse.ItemKindDimension})
	}
	return out
}

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
	if err := r.SaveCatalog(ctx, catalogTestProject, "ga", "hash-a", dims(refs...)); err != nil {
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

	if err := r.SaveCatalog(ctx, catalogTestProject, "ga", "hash-a", dims("sessions")); err != nil {
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

// TestAgentInteg_CatalogCache_SaveOfEitherKindReplacesTheOther pins the model
// the cache actually has: a datasource holds tables OR a catalog, never both.
// An index run lists tables or reads a catalog, decided by whether the
// provider offers one — so a save of either kind is the datasource saying what
// it now is, and anything of the other kind left behind describes what it used
// to be.
//
// The concrete failure: a provider gains a catalog, the source itself
// unchanged so its config hash unchanged. Without this, Find keeps returning
// the tables it no longer has and every consumer goes on rendering and
// authorising them.
func TestAgentInteg_CatalogCache_SaveOfEitherKindReplacesTheOther(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewSchemaCacheRepository(db)

	t.Run("a catalog save retracts the tables", func(t *testing.T) {
		if err := r.Save(ctx, catalogTestProject, "moved", "hash-a", map[string]models.TableSchema{
			"sales.orders": {TableName: "sales.orders", RowCount: 10},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if err := r.SaveCatalog(ctx, catalogTestProject, "moved", "hash-a", dims("sessions")); err != nil {
			t.Fatalf("SaveCatalog: %v", err)
		}

		tables, err := r.Find(ctx, catalogTestProject, "moved", "hash-a")
		if err != nil {
			t.Fatalf("Find: %v", err)
		}
		if len(tables) != 0 {
			t.Errorf("tables = %+v, want none — this datasource is a catalog source now", tables)
		}
		refs, err := r.FindCatalog(ctx, catalogTestProject, "moved", "hash-a")
		if err != nil || len(refs) != 1 {
			t.Errorf("refs = %v, err = %v; want the new catalog", refs, err)
		}
	})

	t.Run("a table save retracts the catalog", func(t *testing.T) {
		if err := r.SaveCatalog(ctx, catalogTestProject, "moved-back", "hash-b", dims("sessions")); err != nil {
			t.Fatalf("SaveCatalog: %v", err)
		}
		if err := r.Save(ctx, catalogTestProject, "moved-back", "hash-b", map[string]models.TableSchema{
			"sales.orders": {TableName: "sales.orders", RowCount: 10},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}

		refs, err := r.FindCatalog(ctx, catalogTestProject, "moved-back", "hash-b")
		if err != nil {
			t.Fatalf("FindCatalog: %v", err)
		}
		if len(refs) != 0 {
			t.Errorf("refs = %v, want none — this datasource has tables now", refs)
		}
		tables, err := r.Find(ctx, catalogTestProject, "moved-back", "hash-b")
		if err != nil || len(tables) != 1 {
			t.Errorf("tables = %+v, err = %v; want the new tables", tables, err)
		}
	})
}

// TestAgentInteg_CatalogCache_ResaveReplaces: re-indexing replaces the catalog
// rather than accumulating, so an item the source stopped offering stops being
// trusted.
func TestAgentInteg_CatalogCache_ResaveReplaces(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewSchemaCacheRepository(db)

	if err := r.SaveCatalog(ctx, catalogTestProject, "ga", "hash-a", dims("sessions", "retiredMetric")); err != nil {
		t.Fatalf("SaveCatalog: %v", err)
	}
	if err := r.SaveCatalog(ctx, catalogTestProject, "ga", "hash-a", dims("sessions")); err != nil {
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

	if err := r.SaveCatalog(ctx, catalogTestProject, "ga", "hash-a", dims("sessions", "retiredMetric")); err != nil {
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

// A catalog names dimensions and metrics in one flat list, and a source can
// spell them alike — a GA4 custom metric and a custom event-scoped dimension
// are both customEvent:<name>. Anything asking "could this field identify a
// record?" therefore cannot read Refs: it would hand back a measure as though
// the question had been answered.
func TestAgentInteg_CatalogCache_KeepsDimensionsApartFromMetrics(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewSchemaCacheRepository(db)

	items := []gowarehouse.CatalogItem{
		{Ref: "sessionDefaultChannelGroup", Kind: gowarehouse.ItemKindDimension},
		{Ref: "customEvent:crm_id", Kind: gowarehouse.ItemKindDimension},
		{Ref: "sessions", Kind: gowarehouse.ItemKindMetric},
		{Ref: "customEvent:score", Kind: gowarehouse.ItemKindMetric},
	}
	if err := r.SaveCatalog(ctx, catalogTestProject, "ga", "hash-a", items); err != nil {
		t.Fatalf("SaveCatalog: %v", err)
	}

	// Everything is still searchable.
	refs, err := r.FindCatalog(ctx, catalogTestProject, "ga", "hash-a")
	if err != nil {
		t.Fatalf("FindCatalog: %v", err)
	}
	if len(refs) != 4 {
		t.Errorf("FindCatalog = %v, want all four items", refs)
	}

	got, err := r.FindCatalogDimensions(ctx, catalogTestProject, "ga", "hash-a")
	if err != nil {
		t.Fatalf("FindCatalogDimensions: %v", err)
	}
	want := []string{"sessionDefaultChannelGroup", "customEvent:crm_id"}
	if len(got) != len(want) {
		t.Fatalf("FindCatalogDimensions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("FindCatalogDimensions[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// A row written before dimension_refs existed knows nothing about kinds, and
// that is not the same as knowing there are no dimensions. Falling back to Refs
// would answer with metrics; answering nothing makes the datasource read as
// unindexed for this question until it is indexed again, which is the safe
// direction and the only honest one.
func TestAgentInteg_CatalogCache_ALegacyRowReportsNoDimensions(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewSchemaCacheRepository(db)

	if _, err := db.Collection(CollectionSchemaCache).InsertOne(ctx, CatalogCacheEntry{
		ProjectID:     catalogTestProject,
		WarehouseID:   "ga",
		WarehouseHash: "hash-legacy",
		EntryKind:     catalogEntryKind,
		Refs:          []string{"sessions", "customEvent:score"},
	}); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	got, err := r.FindCatalogDimensions(ctx, catalogTestProject, "ga", "hash-legacy")
	if err != nil {
		t.Fatalf("FindCatalogDimensions: %v", err)
	}
	if got != nil {
		t.Errorf("FindCatalogDimensions = %v, want nil for a row that never recorded kinds", got)
	}
}

// A catalog of nothing but metrics has no dimensions, which is a real answer
// and not the legacy one — but they read alike, and both are safe: the caller
// treats each as "nothing to bind on".
func TestAgentInteg_CatalogCache_AMetricOnlyCatalogYieldsNoDimensions(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewSchemaCacheRepository(db)

	if err := r.SaveCatalog(ctx, catalogTestProject, "ga", "hash-m", []gowarehouse.CatalogItem{
		{Ref: "sessions", Kind: gowarehouse.ItemKindMetric},
	}); err != nil {
		t.Fatalf("SaveCatalog: %v", err)
	}
	got, err := r.FindCatalogDimensions(ctx, catalogTestProject, "ga", "hash-m")
	if err != nil {
		t.Fatalf("FindCatalogDimensions: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("FindCatalogDimensions = %v, want none", got)
	}
}
