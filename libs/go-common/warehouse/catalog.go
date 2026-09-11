package warehouse

import "context"

// Item kinds a CatalogSource may report. A source is free to use other
// labels; these are the ones the platform recognises and renders, and the
// empty kind is reserved for a table so every point written before catalogs
// existed keeps its meaning.
const (
	// ItemKindTable is the implicit kind of everything a table-shaped source
	// exposes. Never set explicitly — it is what an absent kind means.
	ItemKindTable = ""

	// ItemKindDimension is something results can be broken down by.
	ItemKindDimension = "dimension"

	// ItemKindMetric is a quantity results can report.
	ItemKindMetric = "metric"
)

// CatalogItem is one queryable thing a source offers, described well enough
// to be indexed for retrieval and named exactly as a query must name it.
//
// It is the cube analogue of a table: the unit a caller searches for and then
// writes into a query. A cube has no tables, so there is nothing to list and
// no columns to read — the catalog IS the schema.
type CatalogItem struct {
	// Ref is the name a query must use, verbatim. Not the display name: a
	// model that writes the human label instead of this produces a query the
	// source rejects.
	Ref string

	// Kind is what sort of thing this is — ItemKindDimension or
	// ItemKindMetric. It travels with the indexed point so a consumer can
	// tell a metric from something to group it by without re-reading the
	// source.
	Kind string

	// Text is the description to index. The source supplies it because the
	// source has it: a catalog endpoint that ships human names and prose
	// needs no model to write a description, which makes indexing cheaper
	// and idempotent rather than a per-run LLM cost.
	Text string

	// Unavailable, when non-empty, says why the connected credential cannot
	// query this item. Such an item is still catalogued — it exists, and a
	// caller asking why a value never appears deserves an answer — but it is
	// not indexed for retrieval, because surfacing something the credential
	// cannot read only produces queries that fail.
	Unavailable string
}

// Queryable reports whether the connected credential can actually use this
// item.
func (i CatalogItem) Queryable() bool { return i.Unavailable == "" }

// CatalogSource is an optional provider interface for a source whose
// queryable surface is a catalog of items rather than a set of tables.
//
// It exists because schema indexing otherwise begins by listing tables, and a
// cube-shaped source has none — so an index run over one fails outright
// rather than producing an empty or partial index. A provider that implements
// this is indexed from its catalog instead, and never asked to list tables.
//
// Implementing it is how a source says "my schema is not tables". A provider
// that does not implement it is unaffected in every respect.
type CatalogSource interface {
	// Catalog returns everything the source offers. One call returns the
	// whole surface — there is no per-item round trip to make and nothing to
	// page — so callers may treat it as a single cheap read and cache it.
	Catalog(ctx context.Context) ([]CatalogItem, error)
}

// AsCatalogSource returns p as a CatalogSource, and reports whether it is one.
// Callers branch on the second return to choose the catalog path over the
// table path.
func AsCatalogSource(p Provider) (CatalogSource, bool) {
	c, ok := p.(CatalogSource)
	return c, ok
}
