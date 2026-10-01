package discovery

import (
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// The analysis prompt is handed a step's SQL beside the rows that step returned.
// When the fixer rewrote the statement, the proposal did not produce those rows
// -- the warehouse rejected it -- so shipping it pairs a statement with evidence
// it cannot explain. An observed run had this on 54 of 54 steps.
func TestRenderCompactedSteps_ShipsTheStatementThatRan(t *testing.T) {
	steps := []models.ExplorationStep{{
		Step:          7,
		Action:        "query_data",
		Query:         "SELECT APPROX_QUANTILES(gap, 4)[OFFSET(2)] FROM `public.orders`",
		QueryExecuted: `SELECT PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY gap) FROM "public"."orders"`,
		RowCount:      1,
		QueryResult:   []map[string]any{{"median": 84}},
	}}
	out := RenderCompactedSteps(steps)
	if !strings.Contains(out, "PERCENTILE_CONT") {
		t.Error("the prompt does not carry the statement that produced the rows")
	}
	if strings.Contains(out, "APPROX_QUANTILES") {
		t.Error("the prompt carries the rejected proposal, which never produced these rows")
	}
}

// A step whose proposal ran unchanged must render exactly as before, or this
// change would alter every prompt on every clean run.
func TestRenderCompactedSteps_UnrepairedStepIsUnchanged(t *testing.T) {
	steps := []models.ExplorationStep{{
		Step: 3, Action: "query_data", Query: "SELECT 1", RowCount: 1,
		QueryResult: []map[string]any{{"x": 1}},
	}}
	if !strings.Contains(RenderCompactedSteps(steps), `"query": "SELECT 1"`) {
		t.Error("an unrepaired step no longer renders its query")
	}
}
