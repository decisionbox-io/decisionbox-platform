package discovery

import (
	"sort"
	"strings"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai/schema_render"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// SchemaContextBuilder renders the per-run "catalog" — a one-line-per-
// table directory the LLM gets in its system prompt. It deliberately
// does NOT pre-populate per-table column / sample detail (Level 1):
// the model fetches that on demand via the lookup_schema action,
// served by ai.SchemaProvider (CacheSchemaProvider in production).
//
// Why no upfront Level-1 dump?
//
// The previous flow injected ~40 tables of column lists + sample rows
// into the system prompt every turn, regardless of which tables the
// model actually used. On a real exploration run that scales to 100K+
// tokens of dead weight by step 90 — and is the dominant contributor
// to the 1M-token Bedrock ceiling we hit at step 98. Switching the
// retrieval surface from "push at boot" to "pull per turn" makes the
// per-step cost linear in tables-touched-per-step instead of
// tables-relevant-to-the-run.
//
// What stays in the catalog?
//
//   - All tables (subject to the schema_render budget; archive-shaped
//     names are dropped first when over budget).
//   - Per-table: column count, row count, optional 1–3 keyword hints
//     drawn from substring match against the analysis-area keywords.
//   - Catalog ordering is stable (alphabetical) so the prompt-cache
//     prefix can be reused across turns.
//
// Construct via a struct literal — Schemas is the only required field.
type SchemaContextBuilder struct {
	// Schemas is the full per-table metadata loaded from the schema
	// cache at run start. Used for catalog rendering and as the
	// source of truth for column counts / row counts.
	Schemas map[string]models.TableSchema
}

// Rendered is what BuildCatalog emits. The two telemetry fields are
// stamped onto discovery_runs so operators can see how big the boot
// context actually was for a given run.
type Rendered struct {
	Catalog        string
	CatalogTokens  int
	CatalogDropped int // archive-shaped + lowest-row-count entries removed by Compress
}

// BuildCatalog renders the catalog string for the run. The keyword
// list (typically the union of analysis-area keywords) is used to
// attach 1–3 hint tags per table line so the model can spot likely
// targets without burning a search_tables call.
//
// The function is pure (no I/O, no global state) so callers can
// snapshot-test the output deterministically. Heavy lifting lives in
// the schema_render package — this method is thin glue.
func (b *SchemaContextBuilder) BuildCatalog(keywords []string) *Rendered {
	entries := b.buildCatalog(keywords)
	rr := schema_render.Render(schema_render.RenderOptions{
		Catalog: entries,
	})
	return &Rendered{
		Catalog:        rr.Catalog,
		CatalogTokens:  rr.CatalogTokens,
		CatalogDropped: rr.CatalogDropped,
	}
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
		// Substring-match the table name against the lowercased keyword
		// set. Light touch — domain-pack keywords land verbatim on the
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

func (b *SchemaContextBuilder) sortedTableNames() []string {
	names := make([]string, 0, len(b.Schemas))
	for n := range b.Schemas {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
