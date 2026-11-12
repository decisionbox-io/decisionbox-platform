package discovery

import (
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/scoring"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Two guards that exist because of defects found in review, and that nothing
// else exercises.
//
// Both are the kind that fails silently and in the wrong direction: one would
// let an ambiguous figure veto a comparable one, the other would destroy a
// legacy ranking. A guard against a silent failure is worth exactly as much
// as the test that proves it still guards.

// TestHoldingCurrencyFiguresExcludesEveryDeclarationOfARepeatedID -- a figure
// id declared twice has no single verdict, so no declaration of it may speak
// for the document's currency.
//
// The map keeps the LAST status while the loop sees the first, so without the
// duplicate check a refuted declaration could be read through a holding one's
// status. That is the shape of the defect this was added for: the figure that
// decides a comparison is not the figure whose verdict is consulted.
func TestHoldingCurrencyFiguresExcludesEveryDeclarationOfARepeatedID(t *testing.T) {
	figures := []models.Figure{
		{ID: "amount_a", Unit: models.UnitCurrency, Currency: "$"},
		{ID: "dup", Unit: models.UnitCurrency, Currency: "€"},
		{ID: "dup", Unit: models.UnitCurrency, Currency: "€"},
		{ID: "share", Unit: models.UnitPercent},
		{ID: "refuted", Unit: models.UnitCurrency, Currency: "£"},
	}
	verdicts := []models.FigureVerdict{
		{ID: "amount_a", Status: models.FigureHolds},
		{ID: "dup", Status: models.FigureFails},  // first declaration: refuted
		{ID: "dup", Status: models.FigureHolds},  // second: holds — the map keeps this
		{ID: "share", Status: models.FigureHolds},
		{ID: "refuted", Status: models.FigureFails},
	}

	got := holdingCurrencyFigures(figures, verdicts)

	if len(got) != 1 || got[0].ID != "amount_a" {
		ids := make([]string, 0, len(got))
		for _, f := range got {
			ids = append(ids, f.ID)
		}
		t.Fatalf("holding currency figures = %v, want only [amount_a]", ids)
	}
	// Stated separately so a regression says which rule broke: a percent
	// figure is not a currency, and a refuted currency does not hold.
	for _, f := range got {
		if f.Unit != models.UnitCurrency {
			t.Errorf("%s is not a currency figure", f.ID)
		}
		if f.ID == "dup" {
			t.Error("a repeated id must not be treated as holding")
		}
	}
}

// TestHoldingCurrencyFiguresOnAnEmptyDocument -- no figures and no verdicts is
// an ordinary case, not an error: an insight that states no number at all.
func TestHoldingCurrencyFiguresOnAnEmptyDocument(t *testing.T) {
	if got := holdingCurrencyFigures(nil, nil); len(got) != 0 {
		t.Errorf("got %d figures from an empty document", len(got))
	}
	// A figure with no verdict at all is not holding. Absence of a check is
	// not a pass.
	unchecked := []models.Figure{{ID: "a", Unit: models.UnitCurrency, Currency: "$"}}
	if got := holdingCurrencyFigures(unchecked, nil); len(got) != 0 {
		t.Errorf("an unchecked figure was treated as holding: %v", got)
	}
}

// TestRescoreTargetRecommendationKeepsALegacyRanking -- re-validating a
// recommendation the engine cannot grade must leave its integer priority
// alone.
//
// That integer is the analysis model's own ranking on a legacy row. Zeroing it
// -- which is what writing RecommendationPriority("") would do -- destroys the
// only ordering such a row has, and it would happen on a path the user
// triggered by asking for MORE information, not less.
func TestRescoreTargetRecommendationKeepsALegacyRanking(t *testing.T) {
	cfg := scoring.DefaultConfig()

	legacy := &models.Recommendation{
		Priority: 2,
		Scoring:  nil, // never scored: no severity half, so no tier is derivable
	}
	rescoreTargetRecommendation(legacy, cfg)

	if legacy.Priority != 2 {
		t.Errorf("Priority = %d, want the legacy 2 left alone", legacy.Priority)
	}
	if legacy.Scoring == nil {
		t.Fatal("the confidence half should still have been written")
	}
	if legacy.Scoring.PriorityTier != "" {
		t.Errorf("PriorityTier = %q, want \"\" on a document with no severity band", legacy.Scoring.PriorityTier)
	}
	// The confidence axis moved even though priority could not: that is the
	// whole point of the re-validate path.
	if legacy.Scoring.ConfidenceBand == "" {
		t.Error("ConfidenceBand was not written")
	}
	if legacy.Confidence != legacy.Scoring.ConfidenceScore {
		t.Errorf("mirrored Confidence = %v, scoring says %v", legacy.Confidence, legacy.Scoring.ConfidenceScore)
	}
}

// TestRescoreTargetRecommendationWritesTheTierWhenItHasOne -- the other half
// of the same guard: a recommendation carrying an inherited severity band does
// get its integer priority re-derived, because the verdict that selects the
// tier is exactly what just changed.
func TestRescoreTargetRecommendationWritesTheTierWhenItHasOne(t *testing.T) {
	cfg := scoring.DefaultConfig()

	graded := &models.Recommendation{
		Priority: 4,
		Scoring: &models.Scoring{
			SeverityBand:  models.SeverityBandHigh,
			SeverityScore: 1.0,
			EngineVersion: 1,
		},
	}
	rescoreTargetRecommendation(graded, cfg)

	if graded.Scoring.PriorityTier == "" {
		t.Fatal("a document with a severity band should have landed in a tier")
	}
	want := scoring.RecommendationPriority(graded.Scoring.PriorityTier)
	if graded.Priority != want {
		t.Errorf("Priority = %d, want %d for tier %q", graded.Priority, want, graded.Scoring.PriorityTier)
	}
	// The severity half is inherited and frozen: re-validation re-derives
	// confidence and the tier, never the magnitude.
	if graded.Scoring.SeverityBand != models.SeverityBandHigh || graded.Scoring.SeverityScore != 1.0 {
		t.Errorf("severity half moved: %+v", graded.Scoring)
	}
}
