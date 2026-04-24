package discovery

import (
	"context"
	"sort"
	"strings"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai/schema_render"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai/schema_retrieve"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// SchemaContextBuilder replaces the old `simplifySchemas`+JSON dump with
// the three-level shape described in PLAN-SCHEMA-RETRIEVAL.md §5:
//   Level 0 — catalog of every table (one line each)
//   Level 1 — top-K tables the retriever picked for the current task
//   Level 2 — inspect_table tool (registered in Phase B7 on tool-capable LLMs)
//
// BuildOnce renders the (catalog, retrieved) block once per run. Per-step
// retrieval lands later; for the initial rewire every prompt sees the
// same context, which still halves prompt size on small warehouses and
// unblocks ERP-scale warehouses where the full JSON wouldn't fit in a
// 1M-token window.
type SchemaContextBuilder struct {
	// Embedder is required to turn the search query into a vector.
	Embedder Embedder
	// Retriever is required for Level 1 retrieval. nil falls back to a
	// local heuristic over Schemas (keyword match on blurb-shaped
	// strings) — used by tests that don't spin up Qdrant.
	Retriever *schema_retrieve.Retriever
	// Schemas is the full per-table metadata discovered at run start.
	// Used for Level 0 rendering and for resolving Level 1 hits back to
	// full column lists + sample rows.
	Schemas map[string]models.TableSchema
}

// Rendered is what the builder emits: the two string blocks plus the
// telemetry fields the orchestrator stamps onto the discovery_run.
type Rendered struct {
	Catalog         string
	Retrieved       string
	CatalogTokens   int
	RetrievedTokens int
	CatalogDropped  int
	TopK            int
}

// BuildOnce renders a fresh (catalog, retrieved) block for the whole run.
// The query is derived from the analysis-area keywords + dataset name;
// retrieved tables are the union of the top-K vector hits and any tables
// whose name contains a domain-pack keyword (so obvious matches don't
// depend on the retriever).
//
// topK defaults to schema_render.DefaultRetrievalTopK when 0.
func (b *SchemaContextBuilder) BuildOnce(ctx context.Context, projectID, query string, topK int, keywords []string) (*Rendered, error) {
	if topK <= 0 {
		topK = schema_render.DefaultRetrievalTopK
	}
	catalog := b.buildCatalog(keywords)

	// Resolve Level 1 table list. Order: retriever hits first (preserves
	// relevance order), then any keyword-substring matches not already
	// included. We cap at topK so a keyword flood can't blow the budget.
	seen := map[string]struct{}{}
	picks := make([]string, 0, topK)

	if b.Retriever != nil && b.Embedder != nil && strings.TrimSpace(query) != "" {
		vec, err := b.Embedder.Embed(ctx, []string{query})
		if err == nil && len(vec) > 0 {
			hits, err := b.Retriever.Search(ctx, projectID, vec[0], schema_retrieve.SearchOpts{
				TopK:         topK,
				KeywordBoost: keywords,
				RowCountPrior: 0.05,
			})
			if err == nil {
				for _, h := range hits {
					if _, dup := seen[h.Blurb.Table]; dup {
						continue
					}
					seen[h.Blurb.Table] = struct{}{}
					picks = append(picks, h.Blurb.Table)
					if len(picks) >= topK {
						break
					}
				}
			}
		}
	}

	// Fallback / complement: keyword substring matches against the local
	// schema list. Useful even with Qdrant wired, because the spike
	// showed keyword boosts push obvious matches up further when the
	// vector score is close.
	for _, table := range b.sortedTableNames() {
		if len(picks) >= topK {
			break
		}
		if _, dup := seen[table]; dup {
			continue
		}
		lc := strings.ToLower(table)
		for _, kw := range keywords {
			if kw == "" {
				continue
			}
			if strings.Contains(lc, strings.ToLower(kw)) {
				picks = append(picks, table)
				seen[table] = struct{}{}
				break
			}
		}
	}

	retrieved := b.buildRetrieved(picks)
	rr := schema_render.Render(schema_render.RenderOptions{
		Catalog:   catalog,
		Retrieved: retrieved,
	})
	return &Rendered{
		Catalog:         rr.Catalog,
		Retrieved:       rr.Retrieved,
		CatalogTokens:   rr.CatalogTokens,
		RetrievedTokens: rr.RetrievedTokens,
		CatalogDropped:  rr.CatalogDropped,
		TopK:            len(picks),
	}, nil
}

func (b *SchemaContextBuilder) buildCatalog(keywords []string) []schema_render.CatalogEntry {
	names := b.sortedTableNames()
	out := make([]schema_render.CatalogEntry, 0, len(names))
	lcKeywords := make([]string, 0, len(keywords))
	for _, kw := range keywords {
		if kw != "" {
			lcKeywords = append(lcKeywords, strings.ToLower(kw))
		}
	}
	for _, name := range names {
		s := b.Schemas[name]
		entry := schema_render.CatalogEntry{
			Table:       name,
			ColumnCount: len(s.Columns),
			RowCount:    s.RowCount,
		}
		// Attach keyword hints by substring-matching the table name.
		// Light touch — domain-pack keywords dropped verbatim into the
		// catalog line when they appear in the table name.
		lc := strings.ToLower(name)
		for _, kw := range lcKeywords {
			if strings.Contains(lc, kw) {
				entry.Keywords = append(entry.Keywords, kw)
			}
			if len(entry.Keywords) >= 3 {
				break
			}
		}
		out = append(out, entry)
	}
	return out
}

func (b *SchemaContextBuilder) buildRetrieved(tables []string) []schema_render.TableDetail {
	out := make([]schema_render.TableDetail, 0, len(tables))
	for _, t := range tables {
		s, ok := b.Schemas[t]
		if !ok {
			continue
		}
		out = append(out, schema_render.TableDetail{
			Table:      t,
			Columns:    s.Columns,
			SampleRows: s.SampleData,
			RowCount:   s.RowCount,
		})
	}
	return out
}

func (b *SchemaContextBuilder) sortedTableNames() []string {
	names := make([]string, 0, len(b.Schemas))
	for n := range b.Schemas {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
