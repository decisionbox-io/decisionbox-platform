package main

import (
	"context"
	"strings"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
)

// warehouseSchemaProvider is a tiny ai.SchemaProvider that resolves
// lookups directly via warehouse.GetTableSchemaInDataset. Used by the
// MVP CLI in lieu of the production Qdrant-backed schema provider —
// validation correctness does not depend on Qdrant ranking; only
// `lookup_schema` resolution does, and warehouse direct calls are
// sufficient for replay testing. Production wires the real
// ai.SchemaProvider in the orchestrator.
type warehouseSchemaProvider struct {
	wh             gowarehouse.Provider
	defaultDataset string
}

// Lookup resolves each ref via the warehouse provider. Refs are
// accepted as "dataset.table" or bare "table" (the latter falls back
// to the project's primary dataset).
func (p *warehouseSchemaProvider) Lookup(ctx context.Context, refs []string) (ai.LookupResult, error) {
	out := ai.LookupResult{}
	for _, r := range refs {
		ds, tbl := splitRef(r, p.defaultDataset)
		sch, err := p.wh.GetTableSchemaInDataset(ctx, ds, tbl)
		if err != nil {
			out.NotFound = append(out.NotFound, r)
			continue
		}
		cols := make([]ai.LookupColumn, 0, len(sch.Columns))
		for _, c := range sch.Columns {
			cols = append(cols, ai.LookupColumn{
				Name:     c.Name,
				Type:     c.Type,
				Nullable: c.Nullable,
			})
		}
		out.Tables = append(out.Tables, ai.LookupTable{
			Table:    ds + "." + tbl,
			RowCount: sch.RowCount,
			Columns:  cols,
		})
	}
	return out, nil
}

// Search is a no-op in the MVP — the CLI doesn't exercise semantic
// schema search. Production wires Qdrant here.
func (p *warehouseSchemaProvider) Search(ctx context.Context, query string, k int) ([]ai.SearchHit, error) {
	return nil, nil
}

// splitRef accepts either "dataset.table" or bare "table".
func splitRef(ref, defaultDataset string) (dataset, table string) {
	ref = strings.TrimSpace(ref)
	ref = strings.Trim(ref, "`\"")
	if i := strings.Index(ref, "."); i > 0 {
		return ref[:i], ref[i+1:]
	}
	return defaultDataset, ref
}
