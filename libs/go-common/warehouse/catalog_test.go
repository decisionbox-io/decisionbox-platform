package warehouse

import (
	"context"
	"testing"
)

// tableOnlyProvider is a provider that has tables and no catalog — every
// provider that existed before this seam.
type tableOnlyProvider struct{ Provider }

// catalogProvider additionally describes itself with a catalog.
type catalogProvider struct {
	Provider
	items []CatalogItem
}

func (c catalogProvider) Catalog(context.Context) ([]CatalogItem, error) { return c.items, nil }

// TestAsCatalogSource is the branch that decides whether an index run lists
// tables or reads a catalog. Getting it wrong in either direction breaks a
// whole source: a table provider misread as catalog-shaped indexes nothing,
// and a catalog provider misread as table-shaped fails its index run on the
// first table listing.
func TestAsCatalogSource(t *testing.T) {
	if _, ok := AsCatalogSource(tableOnlyProvider{}); ok {
		t.Error("a provider without a Catalog method must not be read as catalog-shaped")
	}

	want := []CatalogItem{{Ref: "sessions", Kind: ItemKindMetric, Text: "Sessions."}}
	got, ok := AsCatalogSource(catalogProvider{items: want})
	if !ok {
		t.Fatal("a provider with a Catalog method must be read as catalog-shaped")
	}
	items, err := got.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	if len(items) != 1 || items[0].Ref != "sessions" {
		t.Errorf("Catalog() = %+v, want the provider's own items", items)
	}
}

// TestCatalogItemQueryable pins the distinction the indexer filters on. An
// item the credential cannot read is catalogued but not indexed — indexing it
// would surface something to the model that only ever produces failing
// queries.
func TestCatalogItemQueryable(t *testing.T) {
	if !(CatalogItem{Ref: "sessions"}).Queryable() {
		t.Error("an item with no stated obstacle must be queryable")
	}
	if (CatalogItem{Ref: "totalRevenue", Unavailable: "NO_REVENUE_METRICS"}).Queryable() {
		t.Error("an item that states why it cannot be queried must not report as queryable")
	}
}

// TestItemKindTableIsTheZeroValue is load-bearing and easy to break. Every
// index point written before catalogs existed carries no kind, and they are
// all tables; if the table kind were a non-empty string those points would
// read as some other kind and the consumers that branch on it — including the
// filter that keeps scoped-out tables hidden — would treat them wrongly.
func TestItemKindTableIsTheZeroValue(t *testing.T) {
	if ItemKindTable != "" {
		t.Errorf("ItemKindTable = %q, want the empty string so legacy points stay tables", ItemKindTable)
	}
	if ItemKindDimension == "" || ItemKindMetric == "" {
		t.Error("a non-table kind must not be empty, or it is indistinguishable from a table")
	}
	if ItemKindDimension == ItemKindMetric {
		t.Error("dimension and metric must be distinguishable")
	}
}
