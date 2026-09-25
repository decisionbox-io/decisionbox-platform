package models

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

// The API decodes a stored discovery into THIS struct, so a field missing here is
// a field BSON drops before any client sees it. All four audit fields were
// unreachable outside the agent until they were mirrored.
func TestInsight_CarriesTheEvidenceTrailThroughBSON(t *testing.T) {
	stored := bson.M{
		"id": "i1", "name": "n",
		"quality":             bson.A{bson.M{"kind": "truncated", "detail": "capped at 15"}},
		"quantifier_claims":   bson.A{bson.M{"claim": "c", "kind": "only", "step": 4}},
		"quantifier_verdicts": bson.A{bson.M{"claim": "c", "status": "fails", "reason": "2 of 10"}},
		"repair":              bson.M{"rounds": 1, "outcome": "repaired", "fixed": bson.A{"c"}},
	}
	raw, err := bson.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	var got Insight
	if err := bson.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Quality) != 1 || got.Quality[0].Detail != "capped at 15" {
		t.Errorf("quality dropped: %+v", got.Quality)
	}
	if len(got.QuantifierClaims) != 1 || got.QuantifierClaims[0].Step != 4 {
		t.Errorf("declared claims dropped: %+v", got.QuantifierClaims)
	}
	if len(got.QuantifierVerdicts) != 1 || got.QuantifierVerdicts[0].Status != "fails" {
		t.Errorf("verdicts dropped: %+v", got.QuantifierVerdicts)
	}
	if got.Repair == nil || got.Repair.Outcome != "repaired" || len(got.Repair.Fixed) != 1 {
		t.Errorf("repair record dropped: %+v", got.Repair)
	}
}

// The same gap one struct over: a repaired step's executed SQL, and the repair
// counters, must survive the decode a client's response is built from.
func TestExplorationStep_CarriesTheExecutedSQLThroughBSON(t *testing.T) {
	raw, err := bson.Marshal(bson.M{
		"step": 14, "action": "query_data",
		"query":          "SELECT * FROM `public.orders`",
		"query_executed": `SELECT * FROM "public"."orders"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got ExplorationStep
	if err := bson.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.QueryExecuted != `SELECT * FROM "public"."orders"` {
		t.Errorf("query_executed dropped: %q", got.QueryExecuted)
	}
	if got.EffectiveQuery() != got.QueryExecuted {
		t.Errorf("EffectiveQuery() = %q, want the statement that ran", got.EffectiveQuery())
	}
	// A step whose proposal ran unchanged reports the proposal.
	var clean ExplorationStep
	rawClean, _ := bson.Marshal(bson.M{"step": 3, "query": "SELECT 1"})
	if err := bson.Unmarshal(rawClean, &clean); err != nil {
		t.Fatal(err)
	}
	if clean.EffectiveQuery() != "SELECT 1" {
		t.Errorf("EffectiveQuery() = %q, want the proposal", clean.EffectiveQuery())
	}
}

func TestAnalysisStep_CarriesTheRepairCountersThroughBSON(t *testing.T) {
	raw, err := bson.Marshal(bson.M{
		"area_id": "revenue", "area_name": "Revenue",
		"insights_repaired": 2, "insights_claims_dropped": 1,
		"insights_unrepaired": 3, "analysis_repair_rounds": 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got AnalysisStep
	if err := bson.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.InsightsRepaired != 2 || got.InsightsClaimsDropped != 1 ||
		got.InsightsUnrepaired != 3 || got.AnalysisRepairRounds != 4 {
		t.Errorf("repair counters dropped: %+v", got)
	}
}
