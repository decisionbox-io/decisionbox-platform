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
		"figures": bson.A{bson.M{
			"id": "f1", "value": 100000.0, "unit": "count", "step": 7, "kind": "sum",
			"column": "customers"}},
		"figure_verdicts": bson.A{bson.M{
			"id": "f1", "display": "100,000", "step": 7, "kind": "sum", "status": "fails",
			"claimed": 100000.0, "evaluated": 99996.0}},
		"figure_corrections": bson.A{bson.M{
			"id": "f1", "from": 100000.0, "to": 99996.0, "text": "100,000 -> 99,996"}},
		"figure_template": bson.M{"name": "Buyer base {{f1}}", "description": "All {{f1}} ordered."},
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
	if len(got.Figures) != 1 || got.Figures[0].Column != "customers" {
		t.Errorf("figures dropped: %+v", got.Figures)
	}
	if len(got.FigureVerdicts) != 1 || got.FigureVerdicts[0].Evaluated != 99996 {
		t.Errorf("figure verdicts dropped: %+v", got.FigureVerdicts)
	}
	if len(got.FigureCorrections) != 1 || got.FigureCorrections[0].Text != "100,000 -> 99,996" {
		t.Errorf("figure corrections dropped: %+v", got.FigureCorrections)
	}
	if got.FigureTemplate == nil || got.FigureTemplate.Name != "Buyer base {{f1}}" {
		t.Errorf("figure template dropped: %+v", got.FigureTemplate)
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

// The scoring sub-document is the newest member of the same class, and it
// carries the one distinction the dashboard renders everything else off: a
// stored `scoring` means a derived band, its absence means a legacy
// model-assigned number. Both directions have to survive the decode.
func TestInsight_CarriesTheScoringSubDocumentThroughBSON(t *testing.T) {
	raw, err := bson.Marshal(bson.M{
		"id": "i1", "name": "n", "confidence": 0.7,
		"scoring": bson.M{
			"confidence_band":   "medium",
			"confidence_base":   0.8,
			"confidence_score":  0.7,
			"confidence_reason": "Warehouse rows are consistent with the claims that were checked.",
			"confidence_docks": bson.A{bson.M{
				"code": "refuted_arithmetic", "label": "A number's own arithmetic does not reproduce it",
				"amount": 0.1,
			}},
			"engine_version": 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got Insight
	if err := bson.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Scoring == nil {
		t.Fatal("the scoring sub-document was dropped on decode")
	}
	if got.Scoring.ConfidenceBand != "medium" || got.Scoring.EngineVersion != 1 {
		t.Errorf("band / version dropped: %+v", got.Scoring)
	}
	if got.Scoring.ConfidenceBase != 0.8 || got.Scoring.ConfidenceScore != 0.7 {
		t.Errorf("base / score dropped: %+v", got.Scoring)
	}
	if got.Scoring.ConfidenceReason == "" {
		t.Error("the reason was dropped; the dashboard renders the engine's words")
	}
	if len(got.Scoring.ConfidenceDocks) != 1 ||
		got.Scoring.ConfidenceDocks[0].Code != "refuted_arithmetic" ||
		got.Scoring.ConfidenceDocks[0].Amount != 0.1 ||
		got.Scoring.ConfidenceDocks[0].Label == "" {
		t.Errorf("the docks were dropped: %+v", got.Scoring.ConfidenceDocks)
	}
	// The severity half is declared and unwritten in this version; a document
	// that carries none of it must decode to the zero values rather than to
	// anything a reader could mistake for a derived severity.
	if got.Scoring.SeverityBand != "" || got.Scoring.SeverityScore != 0 || got.Scoring.SeverityDriver != nil {
		t.Errorf("the unwritten severity half decoded to something: %+v", got.Scoring)
	}
}

// A document stored before the scorer existed carries no `scoring` key at all,
// and that has to arrive as nil: it is the only signal the dashboard has to
// label the old model-assigned number as legacy instead of rendering it as a
// band.
func TestInsight_AnAbsentScoringDecodesToNil(t *testing.T) {
	raw, err := bson.Marshal(bson.M{"id": "i1", "name": "n", "confidence": 0.93})
	if err != nil {
		t.Fatal(err)
	}
	var got Insight
	if err := bson.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Scoring != nil {
		t.Errorf("an unscored document decoded to a non-nil scoring block: %+v", got.Scoring)
	}
	if got.Confidence != 0.93 {
		t.Errorf("confidence = %v, want the stored 0.93 — legacy documents are not rewritten", got.Confidence)
	}
}

// The recommendation half of the same pair.
func TestRecommendation_CarriesTheScoringSubDocumentThroughBSON(t *testing.T) {
	raw, err := bson.Marshal(bson.M{
		"id": "r1", "title": "t",
		"scoring": bson.M{"confidence_band": "not_confirmed", "confidence_score": 0.25, "engine_version": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got Recommendation
	if err := bson.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Scoring == nil || got.Scoring.ConfidenceBand != "not_confirmed" || got.Scoring.EngineVersion != 1 {
		t.Errorf("the scoring sub-document was dropped or garbled: %+v", got.Scoring)
	}

	var legacy Recommendation
	bare, err := bson.Marshal(bson.M{"id": "r1", "title": "t"})
	if err != nil {
		t.Fatal(err)
	}
	if err := bson.Unmarshal(bare, &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Scoring != nil {
		t.Errorf("an unscored recommendation decoded to a non-nil scoring block: %+v", legacy.Scoring)
	}
}
