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
