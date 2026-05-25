package verifier

import (
	"context"
	"fmt"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	agentmodels "github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
)

// Executor is the abstraction the Agent uses to execute its three
// non-terminal tools (lookup_schema, query_warehouse, read_step_rows).
// Defining it as an interface lets tests stub every tool without a
// warehouse or LLM, and lets production wire the real
// ai.SchemaProvider + queryexec.QueryExecutor + in-memory step
// snapshot.
//
// Errors are non-fatal: the agent loop logs them to
// recent_tool_errors and lets the model retry. Only the LLM transport
// short-circuits to unverifiable.
type Executor interface {
	LookupSchema(ctx context.Context, refs []string) (map[string]any, error)
	QueryWarehouse(ctx context.Context, sql string) (map[string]any, error)
	ReadStepRows(ctx context.Context, req StepRowsRequest) (map[string]any, error)
}

// toolStep is one entry in the agent's history. The Result map holds
// whatever the tool produced; on error, Error is non-empty and the
// next round's prompt surfaces it in recent_tool_errors.
type toolStep struct {
	Kind   ActionKind     `json:"kind"`
	Result map[string]any `json:"result,omitempty"`
	Error  string         `json:"error,omitempty"`
}

// DefaultExecutor wires the three production dependencies:
//   - schemaProv: ai.SchemaProvider — Qdrant-backed schema retrieval
//   - executor:   *queryexec.QueryExecutor — the same self-healing,
//                 filter-enforcing executor exploration uses
//   - stepByID:   in-memory snapshot of the discovery's exploration
//                 steps (passed from the orchestrator after Phase 3)
//
// The snapshot must be populated BEFORE the agent runs — that's why
// the validation phase lives at orchestrator.go:Phase 4.5, after
// exploration has finished and before persistSplitLogs.
type DefaultExecutor struct {
	SchemaProvider ai.SchemaProvider
	QueryExec      *queryexec.QueryExecutor
	StepByID       map[int]*agentmodels.ExplorationStep
	Cfg            BundleConfig

	// MaxReadStepRowsCall caps the per-call `limit` parameter the
	// agent may pass to `read_step_rows`. Zero falls back to a
	// conservative default of 200 — without a cap a single
	// `read_step_rows` call can dump an entire step's worth of rows
	// into the prompt history, blowing memory + token budget. Plan
	// §"Cost envelope" `VALIDATION_MAX_READ_STEP_ROWS`.
	MaxReadStepRowsCall int
}

// LookupSchema delegates to ai.SchemaProvider.Lookup. The agent's
// prompt accepts either "dataset.table" or "table"; SchemaProvider
// rehydrates the canonical qualified form.
//
// When no SchemaProvider is wired (e.g. the manual validate-doc path
// runs without Qdrant), this returns a non-fatal tool error rather
// than nil-derefencing — Codex prod-r3 P1. The verifier's tool-error
// handling renders the message back to the model, which then either
// asks for a different action or marks the dependent claim
// unverifiable. The agent loop never panics.
func (e *DefaultExecutor) LookupSchema(ctx context.Context, refs []string) (map[string]any, error) {
	if e.SchemaProvider == nil {
		return nil, fmt.Errorf("lookup_schema is not available for this run (no schema provider configured) — fall back to the catalog already attached to cited source steps, or mark the dependent claim unverifiable")
	}
	res, err := e.SchemaProvider.Lookup(ctx, refs)
	if err != nil {
		return nil, err
	}
	tables := make([]map[string]any, 0, len(res.Tables))
	for _, t := range res.Tables {
		cols := make([]map[string]any, 0, len(t.Columns))
		for _, c := range t.Columns {
			col := map[string]any{
				"name":     c.Name,
				"type":     c.Type,
				"nullable": c.Nullable,
			}
			if c.Category != "" {
				col["category"] = c.Category
			}
			cols = append(cols, col)
		}
		entry := map[string]any{
			"table":   t.Table,
			"columns": cols,
		}
		if t.RowCount >= 0 {
			entry["row_count"] = t.RowCount
		}
		if len(t.SampleRows) > 0 {
			entry["sample_rows"] = t.SampleRows
		}
		tables = append(tables, entry)
	}
	out := map[string]any{
		"tables":    tables,
		"not_found": res.NotFound,
	}
	if res.Truncated {
		out["truncated"] = true
	}
	return out, nil
}

// QueryWarehouse runs the agent-emitted SQL through queryexec.Execute,
// which (a) enforces the run-wide filter via verifyFilter, (b) self-
// heals via the SQL fixer on failure, (c) returns rows already
// normalised at the warehouse-driver layer. The MVP's row-level
// normaliseValue is reapplied here for safety on any int64-wrapping
// or per-cell-cap concerns that survive the driver layer.
func (e *DefaultExecutor) QueryWarehouse(ctx context.Context, sql string) (map[string]any, error) {
	res, err := e.QueryExec.Execute(ctx, sql, "validation")
	if err != nil {
		return nil, err
	}
	cap := e.Cfg.SampleRows
	if cap <= 0 {
		cap = 50
	}
	rows := res.Data
	truncated := false
	if len(rows) > cap {
		rows = rows[:cap]
		truncated = true
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, normaliseRow(r, e.Cfg.CellCharCap))
	}
	return map[string]any{
		"row_count": res.RowCount,
		"rows":      out,
		"truncated": truncated,
	}, nil
}

// ReadStepRows returns a slice from the in-memory step snapshot.
// Out-of-range offsets are returned as an empty truncated result so
// the agent can branch on `truncated: true` and mark the dependent
// claim unverifiable — plan §"Tool error handling" originally framed
// this as a tool error, but the practical contract used by the MVP
// (and reaffirmed here) is "empty rows + truncated=true" so the agent
// has a deterministic signal to fall back on. Codex MVP-r1 LOW noted
// the divergence; this is the documented production behaviour.
func (e *DefaultExecutor) ReadStepRows(ctx context.Context, req StepRowsRequest) (map[string]any, error) {
	s, ok := e.StepByID[req.StepID]
	if !ok {
		return nil, fmt.Errorf("step_id %d not found in this discovery's snapshot", req.StepID)
	}
	if req.Limit <= 0 {
		req.Limit = 50
	}
	// Clamp the agent-requested limit to MaxReadStepRowsCall — plan
	// §"Cost envelope" + Codex prod-r1 HIGH. The agent may still ask
	// for a larger number; we silently clamp and rely on the
	// returned `truncated: true` to signal further rows are
	// available.
	cap := e.MaxReadStepRowsCall
	if cap <= 0 {
		cap = 200
	}
	if req.Limit > cap {
		req.Limit = cap
	}
	if req.Offset < 0 {
		req.Offset = 0
	}
	total := len(s.QueryResult)
	if req.Offset >= total {
		return map[string]any{
			"step_id":   req.StepID,
			"row_count": total,
			"rows":      []map[string]any{},
			"truncated": true,
		}, nil
	}
	end := req.Offset + req.Limit
	if end > total {
		end = total
	}
	slice := s.QueryResult[req.Offset:end]
	out := make([]map[string]any, 0, len(slice))
	for _, r := range slice {
		out = append(out, normaliseRow(r, e.Cfg.CellCharCap))
	}
	return map[string]any{
		"step_id":   req.StepID,
		"row_count": total,
		"rows":      out,
		"truncated": end < total,
	}, nil
}
