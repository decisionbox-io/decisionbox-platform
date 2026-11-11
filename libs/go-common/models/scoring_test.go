package models

import (
	"encoding/json"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

// storedScoring is one scored document's sub-document as the agent writes it.
func storedScoring() *Scoring {
	return &Scoring{
		ConfidenceBand:   ConfidenceBandMedium,
		ConfidenceBase:   0.80,
		ConfidenceScore:  0.65,
		ConfidenceReason: "Warehouse rows are consistent with the claims that were checked. Lowered because a number's own arithmetic does not reproduce it; and the text still carries a reference to a number that was never supplied.",
		ConfidenceDocks: []ConfidenceDock{
			{Code: "refuted_arithmetic", Label: "A number's own arithmetic does not reproduce it", Amount: 0.10},
			{Code: "unresolved_figure", Label: "The text still carries a reference to a number that was never supplied", Amount: 0.05},
		},
		EngineVersion: 1,
	}
}

// TestStandaloneDocsCarryScoringThroughBSON — both standalone collections are
// read directly (the insights list page and Ask read one, the recommendations
// page the other), so a field BSON drops here is a band those pages never see.
func TestStandaloneDocsCarryScoringThroughBSON(t *testing.T) {
	t.Run("insight", func(t *testing.T) {
		raw, err := bson.Marshal(&StandaloneInsight{ID: "i1", Confidence: 0.65, Scoring: storedScoring()})
		if err != nil {
			t.Fatal(err)
		}
		var got StandaloneInsight
		if err := bson.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.Scoring == nil {
			t.Fatal("scoring was dropped")
		}
		if got.Scoring.ConfidenceBand != ConfidenceBandMedium || got.Scoring.ConfidenceScore != 0.65 {
			t.Errorf("band / score dropped: %+v", got.Scoring)
		}
		if len(got.Scoring.ConfidenceDocks) != 2 || got.Scoring.ConfidenceDocks[1].Amount != 0.05 {
			t.Errorf("docks dropped: %+v", got.Scoring.ConfidenceDocks)
		}
		if got.Scoring.EngineVersion != 1 {
			t.Errorf("engine version dropped: %d", got.Scoring.EngineVersion)
		}
	})

	t.Run("recommendation", func(t *testing.T) {
		raw, err := bson.Marshal(&StandaloneRecommendation{ID: "r1", Scoring: storedScoring()})
		if err != nil {
			t.Fatal(err)
		}
		var got StandaloneRecommendation
		if err := bson.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.Scoring == nil || got.Scoring.ConfidenceBand != ConfidenceBandMedium {
			t.Errorf("scoring dropped or garbled: %+v", got.Scoring)
		}
	})
}

// TestAnUnscoredStandaloneDocDecodesToNil — the legacy marker, on the two
// collections the dashboard reads a document from directly.
func TestAnUnscoredStandaloneDocDecodesToNil(t *testing.T) {
	insightRaw, err := bson.Marshal(bson.M{"_id": "i1", "confidence": 0.93})
	if err != nil {
		t.Fatal(err)
	}
	var ins StandaloneInsight
	if err := bson.Unmarshal(insightRaw, &ins); err != nil {
		t.Fatal(err)
	}
	if ins.Scoring != nil {
		t.Errorf("an unscored insight decoded to %+v, want nil", ins.Scoring)
	}

	recRaw, err := bson.Marshal(bson.M{"_id": "r1", "confidence": 0.9})
	if err != nil {
		t.Fatal(err)
	}
	var rec StandaloneRecommendation
	if err := bson.Unmarshal(recRaw, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Scoring != nil {
		t.Errorf("an unscored recommendation decoded to %+v, want nil", rec.Scoring)
	}
}

// TestScoringOmitsTheUnwrittenHalfOnTheWire — the severity, weight and
// priority fields are declared and never written in this version, so a scored
// document must not ship a dozen zero-valued keys for them. They are omitempty
// precisely so declaring them early costs a client nothing.
func TestScoringOmitsTheUnwrittenHalfOnTheWire(t *testing.T) {
	out, err := json.Marshal(storedScoring())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"magnitude_score", "severity_band", "severity_score", "severity_driver",
		"area_weight", "weight_source", "priority_tier", "priority_score", "risk_flags",
	} {
		if strings.Contains(string(out), `"`+key+`"`) {
			t.Errorf("a scored document ships %q, which nothing writes in this version:\n%s", key, out)
		}
	}
	// The engine version is NOT omitempty: a reader needs it to know which
	// cutoffs scored the document, and version 0 is not a legal value anyway.
	if !strings.Contains(string(out), `"engine_version"`) {
		t.Errorf("engine_version was omitted:\n%s", out)
	}
}

// TestScoringUsesTheSameWireNameEverywhere — the dashboard reads the sub-
// document off two different endpoints, so one json name across all four
// persisted copies is the difference between one reader and four.
func TestScoringUsesTheSameWireNameEverywhere(t *testing.T) {
	ins, err := json.Marshal(&StandaloneInsight{ID: "i1", Scoring: storedScoring()})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := json.Marshal(&StandaloneRecommendation{ID: "r1", Scoring: storedScoring()})
	if err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string]string{"insight": string(ins), "recommendation": string(rec)} {
		if !strings.Contains(payload, `"evidence_scoring"`) {
			t.Errorf("the standalone %s does not ship evidence_scoring:\n%s", name, payload)
		}
		if strings.Contains(payload, `"scoring":`) {
			t.Errorf("the standalone %s ships a plain \"scoring\" key; the wire name is evidence_scoring:\n%s", name, payload)
		}
	}
}

// TestTheBandVocabularyIsTheFiveValues pins the vocabulary the dashboard
// switches on. A sixth band added without a label would render as nothing.
func TestTheBandVocabularyIsTheFiveValues(t *testing.T) {
	want := map[string]bool{
		"high": true, "medium": true, "low": true,
		"not_confirmed": true, "unverifiable": true,
	}
	got := []string{
		ConfidenceBandHigh, ConfidenceBandMedium, ConfidenceBandLow,
		ConfidenceBandNotConfirmed, ConfidenceBandUnverifiable,
	}
	if len(got) != len(want) {
		t.Fatalf("the vocabulary has %d values, want %d", len(got), len(want))
	}
	for _, band := range got {
		if !want[band] {
			t.Errorf("unexpected band value %q", band)
		}
		delete(want, band)
	}
	for band := range want {
		t.Errorf("the vocabulary no longer declares %q", band)
	}
}
