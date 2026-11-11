package discovery

import (
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// The severity axis reaches the engine through two things this package
// resolves: the figures a document declared with the verdicts Go settled over
// them, and the currency those figures should be read in. Both are wiring the
// engine's own table tests cannot cover, and both are where an amount from one
// datasource would get banded against another's thresholds.

// holdingFigure is a figure and the verdict that vouches for it.
func holdingFigure(id string, value float64, unit, currency string, step int) (models.Figure, models.FigureVerdict) {
	return models.Figure{ID: id, Value: value, Unit: unit, Currency: currency, Step: step, Kind: models.FigureCell},
		models.FigureVerdict{ID: id, Display: id + "!", Status: models.FigureHolds}
}

// --- The home currency an insight's amounts are comparable in ---------------

// TestAnInsightsHomeCurrencyIsItsOwnDatasources -- severity compares amounts
// ACROSS an insight's figures to find the largest, so two numbers read in
// different currencies must never be ranked against each other.
func TestAnInsightsHomeCurrencyIsItsOwnDatasources(t *testing.T) {
	plan := newCurrencyPlan(profileWithCurrency("TRY"), []models.WarehouseConfig{
		datasource("default", ""),
		datasource("eu", "EUR"),
	})
	stepByID := map[int]*models.ExplorationStep{
		3: {Step: 3, WarehouseID: "default"},
		7: {Step: 7, WarehouseID: "eu"},
	}

	t.Run("one datasource", func(t *testing.T) {
		lira, _ := holdingFigure("f1", 2_400_000, models.UnitCurrency, "₺", 3)
		ins := models.Insight{Figures: []models.Figure{lira}}
		if got := plan.forInsight(ins, stepByID); got != "₺" {
			t.Errorf("home currency = %q, want the lira", got)
		}
	})

	t.Run("two datasources disagreeing", func(t *testing.T) {
		// An insight drawing amounts from both: there is no single currency
		// its amounts are comparable in, so there is no honest answer.
		lira, _ := holdingFigure("f1", 2_400_000, models.UnitCurrency, "₺", 3)
		euro, _ := holdingFigure("f2", 900_000, models.UnitCurrency, "€", 7)
		ins := models.Insight{Figures: []models.Figure{lira, euro}}
		if got := plan.forInsight(ins, stepByID); got != "" {
			t.Errorf("home currency = %q, want none: the amounts are in different currencies", got)
		}
	})

	t.Run("no currency figure falls back to run-wide agreement", func(t *testing.T) {
		// Percentages only. Nothing to read off the document, so the best
		// available answer is whatever every datasource agrees on.
		agreed := newCurrencyPlan(profileWithCurrency("TRY"), []models.WarehouseConfig{
			datasource("default", ""), datasource("second", "TRY"),
		})
		share, _ := holdingFigure("f1", 12, models.UnitPercent, "", 3)
		ins := models.Insight{Figures: []models.Figure{share}}
		if got := agreed.forInsight(ins, stepByID); got != "₺" {
			t.Errorf("home currency = %q, want the agreed lira", got)
		}
	})

	t.Run("a figure citing an unknown step is not read", func(t *testing.T) {
		// A figure naming a step that did not happen has no datasource to
		// read its currency from. It must not drag the answer to "" and so
		// disqualify the amounts that DO cite a real step.
		cited, _ := holdingFigure("f1", 2_400_000, models.UnitCurrency, "₺", 3)
		ghost, _ := holdingFigure("f2", 1, models.UnitCurrency, "$", 99)
		ins := models.Insight{Figures: []models.Figure{cited, ghost}}
		if got := plan.forInsight(ins, stepByID); got != "₺" {
			t.Errorf("home currency = %q, want the lira", got)
		}
	})
}

// --- The figures the engine is handed ---------------------------------------

// TestTheScorerBandsOnTheInsightsOwnCheckedFigures is the end-to-end wiring:
// the figure list, the verdicts over it and the resolved currency all reach the
// engine, and the band that comes back is the one those figures earn.
func TestTheScorerBandsOnTheInsightsOwnCheckedFigures(t *testing.T) {
	plan := projectCurrencyPlan("TRY")
	stepByID := map[int]*models.ExplorationStep{1: {Step: 1, WarehouseID: "default"}}

	amount, amountVerdict := holdingFigure("f1", 2_400_000, models.UnitCurrency, "₺", 1)
	share, shareVerdict := holdingFigure("f2", 34, models.UnitPercent, "", 1)
	refuted := models.Figure{ID: "f3", Value: 99, Unit: models.UnitPercent, Step: 1, Kind: models.FigureCell}
	refutedVerdict := models.FigureVerdict{ID: "f3", Display: "99%", Status: models.FigureFails}

	insights := []models.Insight{{
		ID: "i1", AnalysisArea: "revenue", Name: "n",
		Figures:        []models.Figure{amount, share, refuted},
		FigureVerdicts: []models.FigureVerdict{amountVerdict, shareVerdict, refutedVerdict},
		Validation:     verdictOf(valmodels.StatusConfirmed),
	}}
	scoreInsights(insights, testScoringCfg(), plan, stepByID)
	got := insights[0]

	// Amount bands ship unconfigured, so the band comes from the 34% share --
	// not from the refuted 99%, which is the whole claim this axis makes.
	if got.Scoring.SeverityDriver.Kind != gomodels.SeverityDriverShare {
		t.Errorf("driver kind = %q, want share", got.Scoring.SeverityDriver.Kind)
	}
	if got.Scoring.SeverityDriver.Figure.FigureID != "f2" {
		t.Errorf("banded on %q, want the checked 34%% share",
			got.Scoring.SeverityDriver.Figure.FigureID)
	}
	if got.Severity != gomodels.SeverityBandHigh {
		t.Errorf("severity = %q, want high", got.Severity)
	}
	// The grounded lira amount still reaches the reader.
	if got.Scoring.SeverityDriver.Amount == nil || got.Scoring.SeverityDriver.Amount.Currency != "₺" {
		t.Errorf("the lira amount is not shown: %+v", got.Scoring.SeverityDriver.Amount)
	}
	// High severity + a confirmed verdict is the top tier.
	if got.Scoring.PriorityTier != gomodels.PriorityTierCritical {
		t.Errorf("tier = %q, want critical", got.Scoring.PriorityTier)
	}
}

// TestAnAmountFromAnotherDatasourceDoesNotReachTheEngine -- the end-to-end
// form of the home-currency rule. An insight whose amounts span two currencies
// has none it can be banded in, and the amount must not be shown as "the
// amount at stake" either, because there is no single figure that is.
func TestAnAmountFromAnotherDatasourceDoesNotReachTheEngine(t *testing.T) {
	plan := newCurrencyPlan(profileWithCurrency("TRY"), []models.WarehouseConfig{
		datasource("default", ""), datasource("eu", "EUR"),
	})
	stepByID := map[int]*models.ExplorationStep{
		3: {Step: 3, WarehouseID: "default"},
		7: {Step: 7, WarehouseID: "eu"},
	}
	lira, liraVerdict := holdingFigure("f1", 2_400_000, models.UnitCurrency, "₺", 3)
	euro, euroVerdict := holdingFigure("f2", 900_000, models.UnitCurrency, "€", 7)

	insights := []models.Insight{{
		ID: "i1", AnalysisArea: "revenue", Name: "n",
		Figures:        []models.Figure{lira, euro},
		FigureVerdicts: []models.FigureVerdict{liraVerdict, euroVerdict},
		Validation:     verdictOf(valmodels.StatusConfirmed),
	}}
	scoreInsights(insights, testScoringCfg(), plan, stepByID)
	driver := insights[0].Scoring.SeverityDriver

	if driver.Kind != gomodels.SeverityDriverBaseline {
		t.Errorf("driver kind = %q, want baseline: neither amount is comparable", driver.Kind)
	}
	if driver.Amount != nil {
		t.Errorf("an amount was shown with no home currency to read it in: %+v", driver.Amount)
	}
	if insights[0].Severity == gomodels.SeverityBandLow {
		t.Error("an unquantifiable finding was banded low rather than on the baseline")
	}
}

// --- The on-demand re-validation path --------------------------------------

// TestARevalidatedInsightKeepsItsSeverityAndMovesItsPriority is the freeze at
// the seam. An operator clicking Run validation re-derives what the verdict
// decides; severity is magnitude x weight and a validation run changes
// neither, so a stored severity stays an immutable fact about the run that
// produced it.
func TestARevalidatedInsightKeepsItsSeverityAndMovesItsPriority(t *testing.T) {
	plan := projectCurrencyPlan("TRY")
	stepByID := map[int]*models.ExplorationStep{1: {Step: 1, WarehouseID: "default"}}
	share, shareVerdict := holdingFigure("f1", 34, models.UnitPercent, "", 1)

	insights := []models.Insight{{
		ID: "i1", AnalysisArea: "revenue", Name: "n",
		Figures:        []models.Figure{share},
		FigureVerdicts: []models.FigureVerdict{shareVerdict},
		Validation:     verdictOf(valmodels.StatusUnverifiable),
	}}
	scoreInsights(insights, testScoringCfg(), plan, stepByID)
	target := insights[0]
	if target.Scoring.PriorityTier != gomodels.PriorityTierInvestigate {
		t.Fatalf("fixture tier = %q, want investigate", target.Scoring.PriorityTier)
	}
	before := *target.Scoring

	// Re-validated, and this time it is confirmed. Note the figures are NOT
	// passed to the re-score path -- it has one stored document in hand and no
	// datasource context -- which is exactly why the severity half has to come
	// from the stored sub-document rather than be re-derived.
	target.Validation = verdictOf(valmodels.StatusConfirmed)
	rescoreTargetInsight(&target, testScoringCfg())

	if target.Severity != before.SeverityBand {
		t.Errorf("severity moved on a re-validation: %q -> %q", before.SeverityBand, target.Severity)
	}
	if target.RiskScore != before.SeverityScore {
		t.Errorf("risk_score moved on a re-validation: %v -> %v", before.SeverityScore, target.RiskScore)
	}
	if target.Scoring.MagnitudeScore != before.MagnitudeScore ||
		target.Scoring.SeverityReason != before.SeverityReason ||
		target.Scoring.SeverityDriver != before.SeverityDriver {
		t.Errorf("the severity half moved:\n got %+v\nwant %+v", *target.Scoring, before)
	}
	// Priority is the exception and must move: one of its two inputs did.
	if target.Scoring.PriorityTier != gomodels.PriorityTierCritical {
		t.Errorf("tier = %q, want critical after the verdict confirmed", target.Scoring.PriorityTier)
	}
	if target.Scoring.ConfidenceBand != gomodels.ConfidenceBandHigh {
		t.Errorf("confidence band = %q, want high", target.Scoring.ConfidenceBand)
	}
}

// TestARevalidatedLegacyInsightKeepsItsSeverityWord is the invariant that
// makes a vector-payload update unnecessary on this path.
//
// It is tempting to score a legacy document in full here, on the grounds that
// the one action whose purpose is to re-derive a grade should produce one.
// Two things rule it out, and the second is the load-bearing one:
//
//   - the severity it would get carries no information. This path has one
//     stored document in hand and no datasource context, so no amount can
//     qualify and the derivation lands on the area baseline -- replacing the
//     model's word with a band that says only "not quantified";
//   - `severity` is an indexed vector-payload field and the search handler
//     filters on it BEFORE hydrating rows. A row whose stored severity moved
//     while its indexed copy did not is a row that answers the wrong query:
//     still matching a `critical` search, and missing from a `medium` one.
//
// Severity not moving here is what keeps those two copies in agreement
// without a payload-update method on every vector-store provider.
func TestARevalidatedLegacyInsightKeepsItsSeverityWord(t *testing.T) {
	legacy := models.Insight{
		ID: "i1", AnalysisArea: "revenue", Name: "n",
		Severity:   "critical",
		RiskScore:  0.9,
		Validation: verdictOf(valmodels.StatusSupported),
	}
	rescoreTargetInsight(&legacy, testScoringCfg())

	if legacy.Severity != "critical" || legacy.RiskScore != 0.9 {
		t.Errorf("the legacy severity moved to %q/%v; the indexed copy would no longer agree",
			legacy.Severity, legacy.RiskScore)
	}
	// The confidence axis IS derived, which is what Run validation is for.
	if legacy.Scoring == nil || legacy.Scoring.ConfidenceBand != gomodels.ConfidenceBandHigh {
		t.Errorf("the confidence axis was not derived: %+v", legacy.Scoring)
	}
	// And no severity half is invented, so no tier is either -- the document
	// renders its legacy word and a dash for priority.
	if legacy.Scoring.SeverityBand != "" || legacy.Scoring.PriorityTier != "" {
		t.Errorf("a severity band or tier was invented: band=%q tier=%q",
			legacy.Scoring.SeverityBand, legacy.Scoring.PriorityTier)
	}
}

// TestARevalidatedLegacyRecommendationKeepsItsPriorityInteger -- the same, for
// the one mirrored field a recommendation carries. A legacy row's integer is
// the analysis model's own ranking, and zeroing it would destroy the only
// value the field has.
func TestARevalidatedLegacyRecommendationKeepsItsPriorityInteger(t *testing.T) {
	legacy := models.Recommendation{
		ID: "r1", Title: "t", Priority: 2, Severity: "critical",
		Validation: verdictOf(valmodels.StatusSupported),
	}
	rescoreTargetRecommendation(&legacy, testScoringCfg())

	if legacy.Priority != 2 {
		t.Errorf("priority = %d, want the stored 2 kept: nothing derived a tier to replace it", legacy.Priority)
	}
	if legacy.Severity != "critical" {
		t.Errorf("severity = %q, want the stored word kept", legacy.Severity)
	}
	if legacy.Scoring == nil || legacy.Scoring.ConfidenceBand == "" {
		t.Error("the confidence axis was not derived")
	}
}
