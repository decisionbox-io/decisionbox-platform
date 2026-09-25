package verifier

import (
	"testing"

	agentmodels "github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// The bundle is the verifier's evidence: a step's SQL beside the rows it
// returned. A repaired or re-quoted step keeps the model's REJECTED proposal in
// Query, so pairing that with rows it never produced grounds the verifier on a
// statement the warehouse refused.
func TestDigestStep_CarriesTheStatementThatRan(t *testing.T) {
	step := &agentmodels.ExplorationStep{
		Step:          9,
		Action:        "query_data",
		Query:         "SELECT APPROX_QUANTILES(gap, 4)[OFFSET(2)] FROM `public.orders`",
		QueryExecuted: `SELECT PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY gap) FROM "public"."orders"`,
		QueryResult:   []map[string]any{{"median": 84}},
	}
	got := digestStep(step, DefaultBundleConfig())
	if got.SQL != step.QueryExecuted {
		t.Errorf("bundle SQL = %q, want the statement that produced the rows", got.SQL)
	}
}

// A step whose proposal ran unchanged must be unaffected, or this touches every
// bundle on every clean run.
func TestDigestStep_UnrepairedStepIsUnchanged(t *testing.T) {
	step := &agentmodels.ExplorationStep{
		Step: 3, Action: "query_data", Query: "SELECT 1",
		QueryResult: []map[string]any{{"x": 1}},
	}
	if got := digestStep(step, DefaultBundleConfig()); got.SQL != "SELECT 1" {
		t.Errorf("bundle SQL = %q, want the proposal that ran", got.SQL)
	}
}
