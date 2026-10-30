package render

import (
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// This block is the verifier's Layer 1: the SQL of the steps an insight cited,
// presented as authoritative column-grounding evidence. Presenting a statement
// the warehouse rejected makes the verifier check a claim against SQL that never
// ran -- and a repair can change what the answer means, so the difference is not
// cosmetic.
func TestRenderVerificationContext_UsesTheStatementThatRan(t *testing.T) {
	log := []models.ExplorationStep{{
		Step:          12,
		Action:        "query_data",
		QueryPurpose:  "median reorder gap",
		Query:         "SELECT APPROX_QUANTILES(gap, 4)[OFFSET(2)] AS med FROM `public.orders`",
		QueryExecuted: `SELECT PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY gap) AS med FROM "public"."orders"`,
		RowCount:      1,
		QueryResult:   []map[string]any{{"med": 84}},
	}}
	out := RenderVerificationContext(log, []int{12}, 10000)
	if !strings.Contains(out, "PERCENTILE_CONT") {
		t.Error("verifier context omits the statement that produced the evidence")
	}
	if strings.Contains(out, "APPROX_QUANTILES") {
		t.Error("verifier context presents the rejected proposal as grounding evidence")
	}
}

// A step is included only when it has SQL. The inclusion test must agree with
// the rendering: a step carrying only a repaired statement is still a query
// step, and dropping it would silently shrink the verifier's evidence.
func TestRenderVerificationContext_IncludesAStepKnownOnlyByItsRepairedSQL(t *testing.T) {
	log := []models.ExplorationStep{{
		Step: 4, Action: "query_data", QueryPurpose: "counts",
		QueryExecuted: `SELECT count(*) FROM "public"."orders"`,
		RowCount:      1, QueryResult: []map[string]any{{"n": 5}},
	}}
	out := RenderVerificationContext(log, []int{4}, 10000)
	if !strings.Contains(out, "count(*)") {
		t.Errorf("a step whose SQL is only in QueryExecuted was dropped; got:\n%s", out)
	}
}
