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
