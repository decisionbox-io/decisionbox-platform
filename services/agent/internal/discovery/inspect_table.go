package discovery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
)

// MaxInspectTablesPerCall caps how many tables one inspect_table call
// may fetch, per PLAN-SCHEMA-RETRIEVAL.md §5: "hard limit 10 tables
// per call". Keeps a runaway LLM from exhausting warehouse quota by
// asking for hundreds of tables at once.
const MaxInspectTablesPerCall = 10

// InspectTableExecutor renders on-demand Level-2 schema detail: full
// column list + sample rows for a set of tables the LLM names in a
// conversation turn. Used as the target of the inspect_table tool on
// tool-capable models and as the handler for the equivalent JSON-
// protocol action on text-only models.
//
// This layer is intentionally thin. It doesn't cache — a second
// inspect_table call for the same table will re-pull samples. Caching
// would need to be request-scoped (so different /ask sessions don't
// collide) and the payoff is small compared to the simplicity cost.
type InspectTableExecutor struct {
	Warehouse gowarehouse.Provider
	// Executor runs the sample-row SELECT. Required so warehouse-specific
	// SQL dialect (through SampleQueryBuilder) and the SQL fixer are
	// inherited from the normal discovery path.
	Executor *queryexec.QueryExecutor
	// Filter is the project's warehouse filter clause (e.g. `WHERE
	// app_id = 'x'`). Empty when the project has no filter.
	Filter string
	// SampleLimit is the number of rows to fetch per table. 0 → 3.
	SampleLimit int
}

// Inspect renders Level-2 detail for each table in `refs`. Each ref is
// a "dataset.table" qualified name (same shape produced by the schema
// catalog). Missing tables are skipped with a one-line note — we never
// return a hard error for a single failing table, because a partial
// response is still useful.
//
// Returns the concatenated block plus a list of tables that couldn't
// be fetched (for the caller to log).
type InspectResult struct {
	Content        string
	Requested      []string
	Succeeded      []string
	Failed         map[string]string // table → reason
	Truncated      bool              // caller exceeded MaxInspectTablesPerCall
}

// Inspect fetches DDL + samples for the requested tables and returns
// a single block ready to paste into a prompt.
func (e *InspectTableExecutor) Inspect(ctx context.Context, refs []string) (*InspectResult, error) {
	if e.Warehouse == nil {
		return nil, errors.New("inspect_table: Warehouse is required")
	}
	if e.Executor == nil {
		return nil, errors.New("inspect_table: Executor is required")
	}
	sampleLimit := e.SampleLimit
	if sampleLimit <= 0 {
		sampleLimit = 3
	}

	// Dedup + cap.
	seen := map[string]struct{}{}
	unique := make([]string, 0, len(refs))
	for _, r := range refs {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		unique = append(unique, r)
	}
	sort.Strings(unique) // deterministic output

	truncated := false
	if len(unique) > MaxInspectTablesPerCall {
		unique = unique[:MaxInspectTablesPerCall]
		truncated = true
	}

	failed := map[string]string{}
	succeeded := make([]string, 0, len(unique))

	var b strings.Builder
	for i, ref := range unique {
		if i > 0 {
			b.WriteString("\n\n")
		}
		dataset, table := splitQualified(ref)
		if table == "" {
			failed[ref] = "missing table name"
			fmt.Fprintf(&b, "TABLE %s — inspect failed: missing table name", ref)
			continue
		}
		// Schema: full column list.
		schema, err := e.Warehouse.GetTableSchemaInDataset(ctx, dataset, table)
		if err != nil || schema == nil {
			reason := "get_schema failed"
			if err != nil {
				reason = err.Error()
			}
			failed[ref] = reason
			fmt.Fprintf(&b, "TABLE %s — inspect failed: %s", ref, reason)
			continue
		}
		fmt.Fprintf(&b, "TABLE %s (%d rows)\n  columns:\n", ref, schema.RowCount)
		for _, c := range schema.Columns {
			nullable := "NOT NULL"
			if c.Nullable {
				nullable = "NULL"
			}
			fmt.Fprintf(&b, "    - %s %s %s\n", c.Name, c.Type, nullable)
		}

		// Samples via executor, using the warehouse's own SampleQueryBuilder
		// when available — same path schema discovery uses at run start.
		var query string
		if builder, ok := e.Warehouse.(gowarehouse.SampleQueryBuilder); ok {
			query = builder.SampleQuery(dataset, table, e.Filter, sampleLimit)
		} else {
			query = fmt.Sprintf("SELECT * FROM `%s.%s` %s LIMIT %d", dataset, table, e.Filter, sampleLimit)
		}
		qr, qErr := e.Executor.Execute(ctx, query, "inspect_table sample "+ref)
		if qErr != nil {
			// Schema succeeded; sample failed — still valuable. Note it
			// but keep the column list visible.
			fmt.Fprintf(&b, "  samples: (sample fetch failed: %s)\n", qErr.Error())
		} else if len(qr.Data) == 0 {
			b.WriteString("  samples: (table is empty)\n")
		} else {
			b.WriteString("  samples:\n")
			for _, row := range qr.Data {
				b.WriteString("    ")
				b.WriteString(formatInspectRow(row))
				b.WriteByte('\n')
			}
		}

		succeeded = append(succeeded, ref)
	}

	return &InspectResult{
		Content:   strings.TrimRight(b.String(), "\n"),
		Requested: unique,
		Succeeded: succeeded,
		Failed:    failed,
		Truncated: truncated,
	}, nil
}

// splitQualified separates a "dataset.table" ref. Returns ("", table)
// when the ref has no dot — the warehouse provider treats the empty
// dataset as its default. A leading dot (".orphan") is treated as an
// unqualified table name, not a dataset-of-empty-name hint.
func splitQualified(ref string) (dataset, table string) {
	idx := strings.LastIndex(ref, ".")
	if idx < 0 {
		return "", ref
	}
	return ref[:idx], ref[idx+1:]
}

// formatInspectRow renders a sample row with stable, alphabetical key
// ordering so snapshot tests don't flake on Go map iteration order.
// Mirrors the schema_render helper but duplicated here to avoid a
// discovery → schema_render cycle.
func formatInspectRow(row map[string]interface{}) string {
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, row[k]))
	}
	return strings.Join(parts, ", ")
}
