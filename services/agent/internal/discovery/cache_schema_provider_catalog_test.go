package discovery

import (
	"context"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai/schema_retrieve"
)

// TestSearch_KeepsCatalogHits is the guard on the failure that makes a
// catalog-shaped datasource silently unroutable.
//
// The schemas map holds tables. A catalog source has none, so every one of its
// hits would fail a table-membership test — and the result is a datasource
// that indexes fine, searches fine, and returns nothing, forever, with no
// error anywhere. That is indistinguishable from the source having nothing
// relevant to say.
func TestSearch_KeepsCatalogHits(t *testing.T) {
	searcher := &fakeVectorSearcher{hits: []schema_retrieve.Hit{
		{Blurb: schema_retrieve.TableBlurb{Table: "sessions", Kind: "metric", Blurb: "Sessions.", WarehouseID: "ga"}, Score: 0.9},
		{Blurb: schema_retrieve.TableBlurb{Table: "country", Kind: "dimension", Blurb: "Country.", WarehouseID: "ga"}, Score: 0.8},
	}}
	p := providerWithSearcher(t, searcher, &fakeEmbedder{dim: 4})
	p.catalogRefs = refSet([]string{"sessions", "country"})

	hits, err := p.Search(context.Background(), "how did traffic convert", 10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2 — catalog items are not in the schemas map and must not be filtered against it", len(hits))
	}
	if hits[0].Table != "sessions" || hits[0].Kind != "metric" {
		t.Errorf("hits[0] = %+v, want the ref and kind carried through", hits[0])
	}
	if hits[1].Kind != "dimension" {
		t.Errorf("hits[1].Kind = %q, want dimension", hits[1].Kind)
	}
}

// TestSearch_StillFiltersUnknownTables is the other half, and the reason the
// filter exists: a table absent from the schemas map has been scoped out by a
// plugin (discovery scope, governance), and surfacing it would leak scope and
// then fail on lookup. Relaxing the filter for catalog items must not relax it
// for tables.
func TestSearch_StillFiltersUnknownTables(t *testing.T) {
	searcher := &fakeVectorSearcher{hits: []schema_retrieve.Hit{
		{Blurb: schema_retrieve.TableBlurb{Table: "events.users", Blurb: "Users."}, Score: 0.9},
		{Blurb: schema_retrieve.TableBlurb{Table: "events.secret_salaries", Blurb: "Scoped out."}, Score: 0.95},
	}}
	p := providerWithSearcher(t, searcher, &fakeEmbedder{dim: 4})

	hits, err := p.Search(context.Background(), "salaries", 10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) != 1 || hits[0].Table != "events.users" {
		t.Fatalf("hits = %+v, want only the in-scope table; a scoped-out table must stay hidden", hits)
	}
}

// TestSearch_MixedSourcesInOneProject is the shape a real anchored project
// takes: a warehouse and a catalog source indexed into the same collection.
// Both must survive — the table because it is in scope, the catalog item
// because the table filter does not apply to it.
func TestSearch_MixedSourcesInOneProject(t *testing.T) {
	searcher := &fakeVectorSearcher{hits: []schema_retrieve.Hit{
		{Blurb: schema_retrieve.TableBlurb{Table: "events.users", Blurb: "Users.", WarehouseID: "wh"}, Score: 0.9},
		{Blurb: schema_retrieve.TableBlurb{Table: "events.ghost", Blurb: "Scoped out.", WarehouseID: "wh"}, Score: 0.85},
		{Blurb: schema_retrieve.TableBlurb{Table: "sessions", Kind: "metric", Blurb: "Sessions.", WarehouseID: "ga"}, Score: 0.8},
	}}
	p := providerWithSearcher(t, searcher, &fakeEmbedder{dim: 4})
	p.catalogRefs = refSet([]string{"sessions"})

	hits, err := p.Search(context.Background(), "users and sessions", 10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2 (the in-scope table and the catalog item)", len(hits))
	}
	byRef := map[string]string{}
	for _, h := range hits {
		byRef[h.Table] = h.Datasource
	}
	if byRef["events.users"] != "wh" || byRef["sessions"] != "ga" {
		t.Errorf("hits = %+v, want each hit tagged with its owning datasource", hits)
	}
	if _, leaked := byRef["events.ghost"]; leaked {
		t.Error("a scoped-out table leaked through alongside the catalog items")
	}
}

// TestSearch_DropsStaleCatalogHits is the staleness guard, and the reason
// catalog hits are checked against the cached refs rather than waved through.
//
// The vector collection is shared across a project's datasources and is not
// self-cleaning: re-indexing clears only the warehouses it still indexes, so
// points belonging to a datasource that was removed — or to an item the source
// no longer offers — survive in it. The cached refs, keyed by the current
// config hash, are what says which of those are real.
func TestSearch_DropsStaleCatalogHits(t *testing.T) {
	searcher := &fakeVectorSearcher{hits: []schema_retrieve.Hit{
		{Blurb: schema_retrieve.TableBlurb{Table: "sessions", Kind: "metric", Blurb: "Sessions."}, Score: 0.9},
		{Blurb: schema_retrieve.TableBlurb{Table: "retiredMetric", Kind: "metric", Blurb: "Left over from a removed datasource."}, Score: 0.95},
	}}
	p := providerWithSearcher(t, searcher, &fakeEmbedder{dim: 4})
	p.catalogRefs = refSet([]string{"sessions"})

	hits, err := p.Search(context.Background(), "sessions", 10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) != 1 || hits[0].Table != "sessions" {
		t.Fatalf("hits = %+v, want only the ref the catalog still offers", hits)
	}
}

// TestSearch_DropsAllCatalogHitsWhenNoCatalogIsKnown pins the default. A
// datasource with no cached catalog is one nothing is known about, and
// trusting its points would surface items from whatever was indexed there
// previously.
func TestSearch_DropsAllCatalogHitsWhenNoCatalogIsKnown(t *testing.T) {
	searcher := &fakeVectorSearcher{hits: []schema_retrieve.Hit{
		{Blurb: schema_retrieve.TableBlurb{Table: "sessions", Kind: "metric", Blurb: "Sessions."}, Score: 0.9},
	}}
	p := providerWithSearcher(t, searcher, &fakeEmbedder{dim: 4})

	hits, err := p.Search(context.Background(), "sessions", 10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("hits = %+v, want none — nothing is known about this datasource's catalog", hits)
	}
}

// TestNewCacheSchemaProvider_AcceptsACatalogOnlySource: a catalog source has
// no tables, so requiring a schemas map would make it unrepresentable — which
// is what kept it out of schema search entirely.
func TestNewCacheSchemaProvider_AcceptsACatalogOnlySource(t *testing.T) {
	p, err := NewCacheSchemaProvider(CacheSchemaProviderOptions{
		ProjectID:   "p1",
		WarehouseID: "ga",
		CatalogRefs: []string{"sessions"},
	})
	if err != nil {
		t.Fatalf("a catalog-only source must be representable: %v", err)
	}
	if !p.catalogRefs["sessions"] {
		t.Error("the catalog refs were not retained")
	}

	// With neither, it is still a wiring bug worth surfacing loudly.
	if _, err := NewCacheSchemaProvider(CacheSchemaProviderOptions{ProjectID: "p1"}); err == nil {
		t.Error("a provider with neither schemas nor a catalog must be refused")
	}
}
