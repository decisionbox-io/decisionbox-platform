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

		MagnitudeScore: 0.60,
		SeverityBand:   SeverityBandMedium,
		SeverityScore:  0.60,
		SeverityDriver: &SeverityDriver{
			Kind:       SeverityDriverShare,
			Quantified: true,
			Figure: &SeverityFigure{
				FigureID: "f1", Display: "12.40%", Value: 12.4,
				Unit: UnitPercent, Verdict: FigureHolds,
			},
			Amount: &SeverityFigure{
				FigureID: "f3", Display: "$2.40M", Value: 2_400_000,
				Unit: UnitCurrency, Currency: "$", Verdict: FigureHolds,
			},
		},
		SeverityReason: "Medium — banded on 12.40%, with $2.40M at stake. Both checked against warehouse rows.",
		AreaWeight:     1.0,
		WeightSource:   &WeightSource{Kind: WeightSourceDefault},

		PriorityTier:  PriorityTierHigh,
		PriorityScore: 1.1225,

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

// TestScoringShipsBothAxesAndOmitsOnlyWhatNothingWrites — the severity and
// priority halves are written now, so they have to reach a client; risk_flags
// is the one part nothing derives yet, and a scored document must not ship an
// empty key for it.
func TestScoringShipsBothAxesAndOmitsOnlyWhatNothingWrites(t *testing.T) {
	out, err := json.Marshal(storedScoring())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"magnitude_score", "severity_band", "severity_score", "severity_driver",
		"severity_reason", "area_weight", "weight_source", "priority_tier",
		"priority_score",
	} {
		if !strings.Contains(string(out), `"`+key+`"`) {
			t.Errorf("a scored document does not ship %q:\n%s", key, out)
		}
	}
	if strings.Contains(string(out), `"risk_flags"`) {
		t.Errorf("a scored document ships risk_flags, which nothing derives yet:\n%s", out)
	}
	// Both driver slots reach the client. The amount is the number a reader
	// cares about and it is NOT the figure that banded the magnitude here, so
	// a client that only received one of them would be shown the share and
	// not the money.
	for _, key := range []string{`"figure"`, `"amount"`, `"$2.40M"`, `"12.40%"`} {
		if !strings.Contains(string(out), key) {
			t.Errorf("the driver lost %s on the wire:\n%s", key, out)
		}
	}
	// The engine version is NOT omitempty: a reader needs it to know which
	// cutoffs scored the document, and version 0 is not a legal value anyway.
	if !strings.Contains(string(out), `"engine_version"`) {
		t.Errorf("engine_version was omitted:\n%s", out)
	}
}

// TestAnUnscoredSeverityHalfOmitsItsKeys — the other direction. A document an
// older engine scored on the confidence axis alone must not ship a severity
// band of "" and a priority score of 0, which a client would read as a real
// bottom-of-the-list grade rather than as "this was never graded".
func TestAnUnscoredSeverityHalfOmitsItsKeys(t *testing.T) {
	confidenceOnly := &Scoring{
		ConfidenceBand:  ConfidenceBandHigh,
		ConfidenceBase:  0.95,
		ConfidenceScore: 0.95,
		EngineVersion:   1,
	}
	out, err := json.Marshal(confidenceOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"magnitude_score", "severity_band", "severity_score", "severity_driver",
		"severity_reason", "area_weight", "weight_source", "priority_tier",
		"priority_score", "risk_flags",
	} {
		if strings.Contains(string(out), `"`+key+`"`) {
			t.Errorf("an unscored severity half shipped %q:\n%s", key, out)
		}
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
	assertVocabulary(t, "confidence band",
		[]string{
			ConfidenceBandHigh, ConfidenceBandMedium, ConfidenceBandLow,
			ConfidenceBandNotConfirmed, ConfidenceBandUnverifiable,
		},
		"high", "medium", "low", "not_confirmed", "unverifiable")
}

// TestTheSeverityVocabularyIsThreeValuesAndExcludesCritical is the vocabulary
// change, pinned.
//
// `critical` was the top severity word and is now the top PRIORITY tier, which
// is what the matrix calls it. The word MOVED rather than being duplicated:
// two vocabularies for one idea leave every filter and every counter having to
// decide which of them it meant. A document written before this carries
// `critical` in its own `severity` field and is rendered as a legacy value,
// never re-interpreted as a tier.
func TestTheSeverityVocabularyIsThreeValuesAndExcludesCritical(t *testing.T) {
	bands := []string{SeverityBandHigh, SeverityBandMedium, SeverityBandLow}
	assertVocabulary(t, "severity band", bands, "high", "medium", "low")
	for _, band := range bands {
		if band == PriorityTierCritical {
			t.Errorf("%q is both a severity band and the top priority tier; "+
				"the word was supposed to move, not be duplicated", band)
		}
	}
}

// TestThePriorityTierVocabularyIsFourValues — ordered critical > investigate >
// high > low, which is the order the sort score has to reproduce.
func TestThePriorityTierVocabularyIsFourValues(t *testing.T) {
	assertVocabulary(t, "priority tier",
		[]string{
			PriorityTierCritical, PriorityTierInvestigate,
			PriorityTierHigh, PriorityTierLow,
		},
		"critical", "investigate", "high", "low")
}

// TestTheDriverKindVocabularyIsThreeValues — what banded the magnitude, in
// precedence order, plus the baseline that means nothing did.
func TestTheDriverKindVocabularyIsThreeValues(t *testing.T) {
	assertVocabulary(t, "driver kind",
		[]string{SeverityDriverAmount, SeverityDriverShare, SeverityDriverBaseline},
		"amount", "share", "baseline")
}

// TestTheWeightSourceVocabularyIsThreeValues — only the default is reachable
// today; the other two are the words a configured taxonomy will set.
func TestTheWeightSourceVocabularyIsThreeValues(t *testing.T) {
	assertVocabulary(t, "weight source",
		[]string{WeightSourceDefault, WeightSourceCategory, WeightSourceAreaOverride},
		"default", "category", "area_override")
}

// assertVocabulary checks a constant set against the exact string values a
// reader on the other side of the wire switches on.
//
// Both directions, which is the point: a renamed constant breaks a dashboard
// that still matches the old string, and a dropped one leaves a reader with a
// case it will never hit.
func assertVocabulary(t *testing.T, name string, got []string, want ...string) {
	t.Helper()
	remaining := make(map[string]bool, len(want))
	for _, w := range want {
		remaining[w] = true
	}
	if len(got) != len(want) {
		t.Fatalf("the %s vocabulary has %d values, want %d", name, len(got), len(want))
	}
	for _, value := range got {
		if !remaining[value] {
			t.Errorf("unexpected %s value %q", name, value)
		}
		delete(remaining, value)
	}
	for value := range remaining {
		t.Errorf("the %s vocabulary no longer declares %q", name, value)
	}
}
