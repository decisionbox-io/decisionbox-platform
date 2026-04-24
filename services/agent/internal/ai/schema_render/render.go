// Package schema_render composes the three-level schema context that
// replaces the full-schema JSON dump every LLM prompt used to carry.
// See PLAN-SCHEMA-RETRIEVAL.md §5 for the design.
//
//   - Level 0 — Catalog. One line per table (name, column count, row count,
//     1–3 hint keywords, optional join edge). Always included. Budgeted.
//   - Level 1 — Retrieved details. Full column list + types + 3 sample rows
//     for the top-K tables a retriever picked for the current task.
//   - Level 2 — inspect_table tool. Registered on tool-capable LLMs so the
//     agent can pull any Level-0 table it wants full details on.
//
// Render() composes Level 0 + Level 1 into a single string that prompts
// splice in at {{SCHEMA_CATALOG}} + {{SCHEMA_RETRIEVED}}. It enforces the
// CATALOG_BUDGET by dropping archive-shaped tables first, then the lowest
// row-counts — documented-limit for >20K-table warehouses (non-goal §2).
package schema_render

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// DefaultCatalogBudgetTokens is the Level 0 token ceiling. Env-overridable
// via SCHEMA_CATALOG_BUDGET on the agent. 150K leaves headroom for Level 1
// + system prompt + dialogue history inside a 1M-token window.
const DefaultCatalogBudgetTokens = 150_000

// DefaultRetrievalTopK is the default Level 1 size for tool-capable models.
// Spike showed top-5 → R@5=0.90 on FINPORT; 40 gives a comfortable margin
// while staying well under the CATALOG_BUDGET ceiling.
const DefaultRetrievalTopK = 40

// DefaultRetrievalTopKNoTool is the default for tool-disabled models —
// larger because the model can't compensate with inspect_table.
const DefaultRetrievalTopKNoTool = 60

// CatalogEntry is one row of Level 0 — what the LLM sees for every table
// in the warehouse. Keep the fields the renderer actually emits; anything
// else belongs on Level 1.
type CatalogEntry struct {
	Table       string
	ColumnCount int
	RowCount    int64
	Keywords    []string // 1–3 domain-pack keywords the blurb matched
	JoinsTo     []string // FK-shaped table names; truncated to keep the line short
}

// TableDetail is one Level-1 table: full columns + a handful of sample rows.
// Sample rows are formatted as key=value pairs so prompt size stays bounded
// regardless of column count (wide tables like audit logs would blow up a
// JSON blob).
type TableDetail struct {
	Table      string
	Columns    []models.ColumnInfo
	SampleRows []map[string]interface{}
	RowCount   int64
}

// Render emits the Level 0 + Level 1 block the prompt templates splice in.
// Catalog entries beyond the token budget are dropped by Compress (archive-
// shaped names first, then lowest-row-count) — see §5.3.
//
// counter is a token estimator. Nil counter falls back to the
// char-divided-by-four approximation with a 25% safety margin.
type RenderOptions struct {
	Catalog     []CatalogEntry
	Retrieved   []TableDetail
	Budget      int       // 0 → DefaultCatalogBudgetTokens
	Counter     TokenCounter // nil → CharCounter (safe for any model)
	MaxSampleRows int       // 0 → 3; caller can dial to 1 for small-context models
}

// Render produces the two-block string agents splice into prompts, along
// with the token statistics callers stamp onto discovery_runs.
type RenderResult struct {
	Catalog           string
	Retrieved         string
	CatalogTokens     int
	RetrievedTokens   int
	CatalogDropped    int // tables trimmed by Compress; 0 on small warehouses
	CatalogTablesUsed int // entries that actually made it into the catalog block
}

// Render is the main entry point. It is pure — no I/O, no logging, no
// global state — so callers can snapshot-test the output deterministically.
func Render(opts RenderOptions) RenderResult {
	budget := opts.Budget
	if budget <= 0 {
		budget = DefaultCatalogBudgetTokens
	}
	counter := opts.Counter
	if counter == nil {
		counter = CharCounter{}
	}
	sampleRows := opts.MaxSampleRows
	if sampleRows <= 0 {
		sampleRows = 3
	}

	catalog, dropped, used := compressCatalog(opts.Catalog, budget, counter)

	catBlock := renderCatalog(catalog)
	retBlock := renderRetrieved(opts.Retrieved, sampleRows)

	return RenderResult{
		Catalog:           catBlock,
		Retrieved:         retBlock,
		CatalogTokens:     counter.CountTokens(catBlock),
		RetrievedTokens:   counter.CountTokens(retBlock),
		CatalogDropped:    dropped,
		CatalogTablesUsed: used,
	}
}

// renderCatalog emits one "name | Xc | Yk/M rows | k1, k2 | -> ta, tb" line
// per table. The column-count and row-count markers let the LLM spot large
// tables even when they didn't make it into Level 1.
func renderCatalog(entries []CatalogEntry) string {
	if len(entries) == 0 {
		return "(warehouse has no tables)"
	}
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Table)
		b.WriteString(" | ")
		fmt.Fprintf(&b, "%dc | %s rows", e.ColumnCount, formatRowCount(e.RowCount))
		if len(e.Keywords) > 0 {
			b.WriteString(" | ")
			b.WriteString(strings.Join(dedupe(e.Keywords, 3), ", "))
		}
		if len(e.JoinsTo) > 0 {
			b.WriteString(" | -> ")
			b.WriteString(strings.Join(dedupe(e.JoinsTo, 3), ", "))
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderRetrieved(details []TableDetail, maxSample int) string {
	if len(details) == 0 {
		return "(no tables retrieved for this step; use inspect_table if needed)"
	}
	var b strings.Builder
	for i, d := range details {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "TABLE %s (%s rows)\n", d.Table, formatRowCount(d.RowCount))
		if len(d.Columns) == 0 {
			b.WriteString("  (no column metadata available)\n")
		} else {
			b.WriteString("  columns:\n")
			for _, c := range d.Columns {
				nullable := "NOT NULL"
				if c.Nullable {
					nullable = "NULL"
				}
				fmt.Fprintf(&b, "    - %s %s %s", c.Name, c.Type, nullable)
				if c.Category != "" {
					fmt.Fprintf(&b, " [%s]", c.Category)
				}
				b.WriteByte('\n')
			}
		}
		if len(d.SampleRows) > 0 {
			shown := d.SampleRows
			if len(shown) > maxSample {
				shown = shown[:maxSample]
			}
			b.WriteString("  sample rows:\n")
			for _, row := range shown {
				b.WriteString("    ")
				b.WriteString(formatSampleRow(row))
				b.WriteByte('\n')
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// archiveNamePattern matches tables whose names scream "not for analysis":
// _YYYY, _LOG, _ARCHIVE, _BKP / _BACKUP, _TMP / _TEMP, _STG / _STAGING.
// These go first when the catalog overshoots the budget — their retrieval
// signal is typically noise anyway.
var archiveNamePattern = regexp.MustCompile(
	`(?i)(_[0-9]{4}$|_LOG$|_ARCHIVE$|_BKP$|_BACKUP$|_TMP$|_TEMP$|_STG$|_STAGING$)`,
)

// IsArchiveShaped reports whether a table name matches the archive regex.
// Exported so callers can reuse the same heuristic for filtering elsewhere
// (e.g. a "hide archive tables" toggle in the dashboard).
func IsArchiveShaped(name string) bool {
	return archiveNamePattern.MatchString(name)
}

// compressCatalog enforces the Level-0 token budget by dropping entries
// when the rendered output would exceed it. Compression policy (§5.3):
//  1. Drop archive-shaped names first.
//  2. If still over budget, drop the lowest-row-count tables.
// The ordering inside entries is NOT shuffled — stable across runs so the
// prompt-cache prefix (when we add caching later) stays stable.
func compressCatalog(entries []CatalogEntry, budget int, counter TokenCounter) ([]CatalogEntry, int, int) {
	if counter.CountTokens(renderCatalog(entries)) <= budget {
		return entries, 0, len(entries)
	}

	kept := make([]CatalogEntry, 0, len(entries))
	archive := make([]CatalogEntry, 0)
	for _, e := range entries {
		if IsArchiveShaped(e.Table) {
			archive = append(archive, e)
		} else {
			kept = append(kept, e)
		}
	}
	totalDropped := len(archive)
	if counter.CountTokens(renderCatalog(kept)) <= budget {
		return kept, totalDropped, len(kept)
	}

	// Still over budget — drop smallest tables first. We mutate a copy
	// sorted by row count asc, then keep the trailing slice that fits.
	byRows := append([]CatalogEntry(nil), kept...)
	sort.SliceStable(byRows, func(i, j int) bool {
		return byRows[i].RowCount < byRows[j].RowCount
	})

	// Linear scan: for each possible cut point, does the suffix fit?
	// O(n) because we decrement one entry at a time instead of re-rendering.
	for cut := 1; cut < len(byRows); cut++ {
		candidate := byRows[cut:]
		// Preserve original ordering for prompt-cache stability.
		original := preserveOrder(entries, candidate)
		if counter.CountTokens(renderCatalog(original)) <= budget {
			return original, totalDropped + cut, len(original)
		}
	}

	// Budget too tight — return an empty catalog. Pathological; we don't
	// want to silently emit garbage. The caller should raise the budget
	// or refuse to index.
	return nil, len(entries), 0
}

// preserveOrder returns `subset` sorted by the index each entry had in
// `original`. Used so catalog compression doesn't churn the prompt-cache
// prefix.
func preserveOrder(original []CatalogEntry, subset []CatalogEntry) []CatalogEntry {
	in := make(map[string]struct{}, len(subset))
	for _, e := range subset {
		in[e.Table] = struct{}{}
	}
	out := make([]CatalogEntry, 0, len(subset))
	for _, e := range original {
		if _, ok := in[e.Table]; ok {
			out = append(out, e)
		}
	}
	return out
}

// formatRowCount shortens the number to keep the catalog line scanner-
// friendly: 2.1M / 14.3K / 542. Keeps 1 decimal place when it adds signal.
func formatRowCount(n int64) string {
	switch {
	case n < 0:
		return "unknown"
	case n < 1_000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		v := float64(n) / 1_000
		if n%1_000 == 0 {
			return fmt.Sprintf("%.0fK", v)
		}
		return fmt.Sprintf("%.1fK", v)
	case n < 1_000_000_000:
		v := float64(n) / 1_000_000
		if n%1_000_000 == 0 {
			return fmt.Sprintf("%.0fM", v)
		}
		return fmt.Sprintf("%.1fM", v)
	default:
		v := float64(n) / 1_000_000_000
		if n%1_000_000_000 == 0 {
			return fmt.Sprintf("%.0fB", v)
		}
		return fmt.Sprintf("%.1fB", v)
	}
}

// formatSampleRow renders a row as "col1=val1, col2=val2" with string
// values truncated to avoid blowing up the prompt on wide text columns.
// Column iteration is stable (alphabetical) for deterministic snapshots.
func formatSampleRow(row map[string]interface{}) string {
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := row[k]
		parts = append(parts, fmt.Sprintf("%s=%s", k, truncateValue(v)))
	}
	return strings.Join(parts, ", ")
}

const maxSampleValueLen = 80 // characters, not tokens — prompt-size proxy

func truncateValue(v interface{}) string {
	if v == nil {
		return "NULL"
	}
	s := fmt.Sprintf("%v", v)
	// Collapse internal whitespace so a 3-line JSON blob isn't rendered as
	// three lines in the prompt.
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxSampleValueLen {
		return s[:maxSampleValueLen] + "…"
	}
	return s
}

func dedupe(values []string, max int) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
		if len(out) >= max {
			break
		}
	}
	return out
}
