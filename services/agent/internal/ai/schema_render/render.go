// Package schema_render composes the catalog block the LLM sees in its
// system prompt: one line per table, with column count, row count, and
// optional 1–3 keyword hints.
//
// Per-table column lists and sample rows are NOT rendered up front.
// They arrive on demand via the lookup_schema / search_tables actions
// served by ai.SchemaProvider during the exploration loop. This package
// is purely about the boot catalog — the directory the LLM uses to pick
// which tables to fetch detail for.
//
// Render() emits the catalog string the orchestrator splices in at
// {{SCHEMA_INFO}}. It enforces a token budget by dropping archive-
// shaped tables first, then the lowest row-counts — documented limit
// for >20K-table warehouses.
package schema_render

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// DefaultCatalogBudgetTokens is the catalog token ceiling. Env-overridable
// via SCHEMA_CATALOG_BUDGET on the agent. 150K leaves comfortable
// headroom for base context + dialogue history inside a 1M-token window
// and is well within reach for typical 200K-context models.
const DefaultCatalogBudgetTokens = 150_000

// CatalogEntry is one row of the catalog — what the LLM sees for every
// table in the warehouse.
type CatalogEntry struct {
	Table       string
	ColumnCount int
	RowCount    int64
	Keywords    []string // 1–3 domain-pack keywords matched against the table name
	JoinsTo     []string // FK-shaped table names; truncated to keep the line short
}

// RenderOptions configures Render. Counter is optional — nil falls back
// to the chars/4 heuristic with a 25% safety margin (CharCounter
// default), which is on the safe side for OpenAI / Anthropic models.
type RenderOptions struct {
	Catalog []CatalogEntry
	Budget  int          // 0 → DefaultCatalogBudgetTokens
	Counter TokenCounter // nil → CharCounter (safe for any model)
}

// RenderResult is what Render returns. Telemetry fields go onto the
// run document so operators can see how big the boot context was.
type RenderResult struct {
	Catalog           string
	CatalogTokens     int
	CatalogDropped    int // entries dropped by Compress; 0 on small warehouses
	CatalogTablesUsed int // entries that made it into the catalog block
}

// Render is the main entry point. Pure — no I/O, no logging, no global
// state — so callers can snapshot-test the output deterministically.
func Render(opts RenderOptions) RenderResult {
	budget := opts.Budget
	if budget <= 0 {
		budget = DefaultCatalogBudgetTokens
	}
	counter := opts.Counter
	if counter == nil {
		counter = CharCounter{}
	}

	catalog, dropped, used := compressCatalog(opts.Catalog, budget, counter)
	catBlock := renderCatalog(catalog)

	return RenderResult{
		Catalog:           catBlock,
		CatalogTokens:     counter.CountTokens(catBlock),
		CatalogDropped:    dropped,
		CatalogTablesUsed: used,
	}
}

// renderCatalog emits one "name | Xc | Yk/M rows | k1, k2 | -> ta, tb"
// line per table. Column count and row count let the LLM spot large
// tables when picking targets for lookup_schema.
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

// archiveNamePattern matches tables whose names scream "not for analysis":
// _YYYY, _LOG, _ARCHIVE, _BKP / _BACKUP, _TMP / _TEMP, _STG / _STAGING.
// These go first when the catalog overshoots the budget — their
// retrieval signal is typically noise anyway.
var archiveNamePattern = regexp.MustCompile(
	`(?i)(_[0-9]{4}$|_LOG$|_ARCHIVE$|_BKP$|_BACKUP$|_TMP$|_TEMP$|_STG$|_STAGING$)`,
)

// IsArchiveShaped reports whether a table name matches the archive
// regex. Exported so callers can reuse the same heuristic elsewhere
// (e.g. a "hide archive tables" toggle in the dashboard).
func IsArchiveShaped(name string) bool {
	return archiveNamePattern.MatchString(name)
}

// compressCatalog enforces the catalog token budget by dropping entries
// when the rendered output exceeds it. Compression policy:
//
//  1. Drop archive-shaped names first.
//  2. If still over budget, drop the lowest-row-count tables.
//
// Original ordering is preserved for prompt-cache prefix stability.
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

	// Still over budget — drop smallest tables first. Mutate a copy
	// sorted by row count asc, then keep the trailing slice that fits.
	byRows := append([]CatalogEntry(nil), kept...)
	sort.SliceStable(byRows, func(i, j int) bool {
		return byRows[i].RowCount < byRows[j].RowCount
	})

	// Linear scan: for each cut point, does the suffix fit?
	for cut := 1; cut < len(byRows); cut++ {
		candidate := byRows[cut:]
		original := preserveOrder(entries, candidate)
		if counter.CountTokens(renderCatalog(original)) <= budget {
			return original, totalDropped + cut, len(original)
		}
	}

	// Budget too tight — return an empty catalog. Pathological; the
	// caller should raise the budget or refuse to index.
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
// friendly: 2.1M / 14.3K / 542. Keeps 1 decimal place when it adds
// signal. -1 (legacy "unknown" sentinel) renders as "unknown".
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

// dedupe returns the first up-to-`max` distinct non-empty entries from
// values, preserving order.
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
