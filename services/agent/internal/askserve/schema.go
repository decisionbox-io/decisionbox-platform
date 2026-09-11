package askserve

import (
	"context"
	"fmt"
	"sort"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
)

// SchemaRouter is the project-wide, warehouse-aware schema knowledge a turn uses
// to discover and inspect tables WITHOUT opening a warehouse connection. It is
// built eagerly from every datasource's cached schema (Mongo) plus a shared
// Qdrant retriever, because schema knowledge is cheap and datasource-spanning
// while warehouse connections are expensive and per-datasource (those stay lazy
// on the ProjectRuntime).
//
// Two operations, mirroring the two schema tools:
//
//   - Lookup(datasourceID, tables): resolve columns/samples for tables WITHIN
//     one datasource. Scoped per datasource so a table name that exists in two
//     datasources (e.g. public.customers in both) never cross-contaminates.
//   - SearchAll(query): semantic search ACROSS all datasources, each hit tagged
//     with its owning datasource. This is how the model discovers which
//     datasource holds what before it targets one with query_data.
type SchemaRouter struct {
	// lookups holds one per-datasource lookup provider, keyed by datasource id.
	lookups map[string]ai.SchemaProvider
	// labels maps a datasource id to its human label for tagging search hits.
	labels map[string]string
	// primary is the datasource id Lookup falls back to when the caller names
	// none.
	primary string
	// span runs one unfiltered cross-datasource semantic search and returns
	// hits already tagged with their owning datasource. nil when no retriever /
	// embedder is wired (search_tables is then unavailable, lookup still works).
	span func(ctx context.Context, query string, k int) ([]TaggedHit, error)
}

// TaggedHit is one search_tables result, carrying which datasource owns the
// table so the model can then target it with query_data / lookup_schema.
type TaggedHit struct {
	DatasourceID    string
	DatasourceLabel string
	// Table is the reference a query must use to name this hit: a qualified
	// table for a table-shaped datasource, or the item's own name for a
	// catalog-shaped one. Kind says which.
	Table string
	// Kind is the sort of thing the hit describes. Empty means a table, which
	// is what every hit was before catalog sources existed.
	Kind     string
	Blurb    string
	RowCount int64
	Score    float64
}

// SchemaRouterOptions assembles a SchemaRouter. Lookups is the per-datasource
// lookup providers; Span is the cross-datasource searcher (may be nil).
type SchemaRouterOptions struct {
	Lookups map[string]ai.SchemaProvider
	Labels  map[string]string
	Primary string
	Span    func(ctx context.Context, query string, k int) ([]TaggedHit, error)
}

// NewSchemaRouter builds a SchemaRouter, or returns nil when no datasource has
// a lookup provider (nothing is indexed) — the loop treats a nil router as
// "schema tools unavailable" and degrades gracefully, exactly as before.
func NewSchemaRouter(opts SchemaRouterOptions) *SchemaRouter {
	if len(opts.Lookups) == 0 {
		return nil
	}
	return &SchemaRouter{
		lookups: opts.Lookups,
		labels:  opts.Labels,
		primary: opts.Primary,
		span:    opts.Span,
	}
}

// Lookup resolves the requested tables within one datasource. An empty
// datasourceID resolves to the primary. An unknown datasourceID is an error the
// loop surfaces to the model (so it re-issues against a real datasource) — it
// never silently falls back to another datasource's schema.
func (s *SchemaRouter) Lookup(ctx context.Context, datasourceID string, tables []string) (ai.LookupResult, error) {
	id := datasourceID
	if id == "" {
		id = s.primary
	}
	p, ok := s.lookups[id]
	if !ok {
		return ai.LookupResult{}, fmt.Errorf("datasource %q is not indexed for schema lookup (known: %v)", datasourceID, s.datasourceIDs())
	}
	return p.Lookup(ctx, tables)
}

// SearchAll runs a semantic search across every datasource and returns hits
// tagged with their owning datasource, best-first. Returns an "unavailable"
// error when no cross-datasource searcher is wired.
func (s *SchemaRouter) SearchAll(ctx context.Context, query string, k int) ([]TaggedHit, error) {
	if s.span == nil {
		return nil, fmt.Errorf("schema search not available: no semantic index configured")
	}
	return s.span(ctx, query, k)
}

// SearchOne runs a semantic search within a single datasource and tags the hits
// with it. Used on a single-datasource / pinned turn, where spanning would
// surface tables the turn cannot query. An empty datasourceID resolves to the
// primary.
func (s *SchemaRouter) SearchOne(ctx context.Context, datasourceID, query string, k int) ([]TaggedHit, error) {
	id := datasourceID
	if id == "" {
		id = s.primary
	}
	p, ok := s.lookups[id]
	if !ok {
		return nil, fmt.Errorf("datasource %q is not indexed for schema search (known: %v)", datasourceID, s.datasourceIDs())
	}
	hits, err := p.Search(ctx, query, k)
	if err != nil {
		return nil, err
	}
	out := make([]TaggedHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, TaggedHit{
			DatasourceID:    id,
			DatasourceLabel: s.labels[id],
			Table:           h.Table,
			// Carried, not dropped. A single-datasource or pinned turn renders
			// through the same formatter as a spanning one, so losing the kind
			// here shows metrics and dimensions to the model as bare table
			// names on exactly the turns where a catalog source is most likely
			// to be the one being asked about.
			Kind:     h.Kind,
			Blurb:    h.Blurb,
			RowCount: h.RowCount,
			Score:    h.Score,
		})
	}
	return out, nil
}

// datasourceIDs returns the indexed datasource ids in a stable order (for error
// messages the model reads).
func (s *SchemaRouter) datasourceIDs() []string {
	ids := make([]string, 0, len(s.lookups))
	for id := range s.lookups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
