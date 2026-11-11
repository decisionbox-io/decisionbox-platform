package discovery

import (
	"strings"
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
	return models.Figure{ID: id, Value: value, Unit: unit, Currency: currency, Decimals: 2, Step: step, Kind: models.FigureCell},
		models.FigureVerdict{ID: id, Display: id + "!", Status: models.FigureHolds}
}

// publishedInsight builds an insight the way a run does: the prose is
// AUTHORED with references and then rendered, so the figure/prose link the
// severity axis requires is real rather than hand-written.
//
// That link is the point of going through the renderer here. Severity may
// only band a magnitude off a number the insight states, and a fixture whose
// prose was typed by hand either accidentally matches what the renderer would
// have produced or silently fails the link and tests the baseline instead.
func publishedInsight(id, area string, pairs ...figurePair) models.Insight {
	ins := models.Insight{ID: id, AnalysisArea: area, Name: "Finding in " + area}
	var authored []string
	for _, p := range pairs {
		ins.Figures = append(ins.Figures, p.figure)
		ins.FigureVerdicts = append(ins.FigureVerdicts, p.verdict)
		authored = append(authored, "{{"+p.figure.ID+"}}")
	}
	ins.Description = "The finding states " + strings.Join(authored, " and ") + "."
	usWriter.renderInsightFigures([]models.Insight{ins})
	// renderInsightFigures takes a slice and edits in place; re-render into a
	// slice we keep so the template and the rendered prose both land.
	one := []models.Insight{ins}
	usWriter.renderInsightFigures(one)
	return one[0]
}

// figurePair keeps a figure with the verdict that settles it.
type figurePair struct {
	figure  models.Figure
	verdict models.FigureVerdict
}

func pairOf(f models.Figure, v models.FigureVerdict) figurePair {
	return figurePair{figure: f, verdict: v}
}

// --- The home currency an insight's amounts are comparable in ---------------

// TestAnInsightsHomeCurrencyIsItsOwnDatasources -- severity compares amounts
// ACROSS a figure set to find the largest, so two numbers read in different
// currencies must never be ranked against each other.
//
// Resolved over the set the caller passes rather than over the insight,
// because the severity axis passes only the figures the prose states: a
// declaration the insight never published must not decide that its amounts
// are incomparable.
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
		if got := plan.forFigures(ins.Figures, stepByID); got != "₺" {
			t.Errorf("home currency = %q, want the lira", got)
		}
	})

	t.Run("two datasources disagreeing", func(t *testing.T) {
		// An insight drawing amounts from both: there is no single currency
		// its amounts are comparable in, so there is no honest answer.
		lira, _ := holdingFigure("f1", 2_400_000, models.UnitCurrency, "₺", 3)
		euro, _ := holdingFigure("f2", 900_000, models.UnitCurrency, "€", 7)
		ins := models.Insight{Figures: []models.Figure{lira, euro}}
		if got := plan.forFigures(ins.Figures, stepByID); got != "" {
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
		if got := agreed.forFigures(ins.Figures, stepByID); got != "₺" {
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
		if got := plan.forFigures(ins.Figures, stepByID); got != "₺" {
			t.Errorf("home currency = %q, want the lira", got)
		}
	})
}

// --- The figures the engine is handed ---------------------------------------

// TestTheScorerBandsOnTheInsightsOwnCheckedFigures is the end-to-end wiring:
// the figure list, the verdicts over it and the resolved currency all reach
// the engine, and the band that comes back is the one those figures earn.
func TestTheScorerBandsOnTheInsightsOwnCheckedFigures(t *testing.T) {
	plan := projectCurrencyPlan("TRY")
	stepByID := map[int]*models.ExplorationStep{1: {Step: 1, WarehouseID: "default"}}
	sc := scoringContext{cfg: testScoringCfg(), currencies: plan, stepByID: stepByID, writer: &usWriter}

	amount, amountVerdict := holdingFigure("f1", 2_400_000, models.UnitCurrency, "₺", 1)
	share, shareVerdict := holdingFigure("f2", 34, models.UnitPercent, "", 1)
	refuted := models.Figure{ID: "f3", Value: 99, Unit: models.UnitPercent, Decimals: 2, Step: 1, Kind: models.FigureCell}
	refutedVerdict := models.FigureVerdict{ID: "f3", Display: "99.00%", Status: models.FigureFails}

	ins := publishedInsight("i1", "revenue",
		pairOf(amount, amountVerdict), pairOf(share, shareVerdict), pairOf(refuted, refutedVerdict))
	insights := []models.Insight{ins}
	insights[0].Validation = verdictOf(valmodels.StatusConfirmed)

	scoreInsights(insights, sc)
	got := insights[0]

	// Amount bands ship unconfigured, so the band comes from the 34% share --
	// not from the refuted 99%, which is the whole claim this axis makes.
	if got.Scoring.SeverityDriver.Kind != gomodels.SeverityDriverShare {
		t.Errorf("driver kind = %q, want share (driver %+v)",
			got.Scoring.SeverityDriver.Kind, got.Scoring.SeverityDriver)
	}
	if got.Scoring.SeverityDriver.Figure == nil || got.Scoring.SeverityDriver.Figure.FigureID != "f2" {
		t.Errorf("banded on %+v, want the checked 34%% share", got.Scoring.SeverityDriver.Figure)
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

// TestAnUndeclaredFigureCannotDriveSeverity -- a figure the insight declared
// and never published is not a number the finding states.
//
// A finding whose prose says 1.00% is a finding about 1.00%, whatever else it
// declared. Banding it high off an unreferenced 80% figure would rank it on a
// number no reader can see, which is indistinguishable from the outside from
// banding it on a refuted one.
func TestAnUndeclaredFigureCannotDriveSeverity(t *testing.T) {
	plan := projectCurrencyPlan("TRY")
	stepByID := map[int]*models.ExplorationStep{1: {Step: 1, WarehouseID: "default"}}
	sc := scoringContext{cfg: testScoringCfg(), currencies: plan, stepByID: stepByID, writer: &usWriter}

	published, publishedVerdict := holdingFigure("stated", 1, models.UnitPercent, "", 1)
	unused, unusedVerdict := holdingFigure("unstated", 80, models.UnitPercent, "", 1)

	// Only `stated` is referenced from the authored prose.
	ins := publishedInsight("i1", "revenue", pairOf(published, publishedVerdict))
	ins.Figures = append(ins.Figures, unused)
	ins.FigureVerdicts = append(ins.FigureVerdicts, unusedVerdict)
	ins.Validation = verdictOf(valmodels.StatusConfirmed)

	insights := []models.Insight{ins}
	scoreInsights(insights, sc)
	driver := insights[0].Scoring.SeverityDriver

	if driver.Figure != nil && driver.Figure.FigureID == "unstated" {
		t.Error("severity was banded off a figure the prose never states")
	}
	if insights[0].Severity == gomodels.SeverityBandHigh {
		t.Errorf("severity = high off an unpublished 80%% figure (driver %+v)", driver)
	}
	if driver.Figure == nil || driver.Figure.FigureID != "stated" {
		t.Errorf("driver = %+v, want the 1%% figure the prose states", driver.Figure)
	}
}

// TestAnAmountFromAnotherDatasourceDoesNotReachTheEngine -- the end-to-end
// form of the home-currency rule. An insight whose amounts span two
// currencies has none it can be banded in, and the amount must not be shown
// as "the amount at stake" either, because there is no single figure that is.
func TestAnAmountFromAnotherDatasourceDoesNotReachTheEngine(t *testing.T) {
	plan := newCurrencyPlan(profileWithCurrency("TRY"), []models.WarehouseConfig{
		datasource("default", ""), datasource("eu", "EUR"),
	})
	stepByID := map[int]*models.ExplorationStep{
		3: {Step: 3, WarehouseID: "default"},
		7: {Step: 7, WarehouseID: "eu"},
	}
	sc := scoringContext{cfg: testScoringCfg(), currencies: plan, stepByID: stepByID, writer: &usWriter}

	lira, liraVerdict := holdingFigure("f1", 2_400_000, models.UnitCurrency, "₺", 3)
	euro, euroVerdict := holdingFigure("f2", 900_000, models.UnitCurrency, "€", 7)

	ins := publishedInsight("i1", "revenue", pairOf(lira, liraVerdict), pairOf(euro, euroVerdict))
	ins.Validation = verdictOf(valmodels.StatusConfirmed)
	insights := []models.Insight{ins}

	scoreInsights(insights, sc)
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

// TestARemovedSentenceDoesNotDiscardASurvivingAmount -- the home currency is
// resolved over the SURVIVING figures, not over every declaration.
//
// Repair removing a euro-sourced indicator must not make a lira amount that
// is still on the page incomparable: the driver would lose the money at stake
// because of a sentence deleted somewhere else.
func TestARemovedSentenceDoesNotDiscardASurvivingAmount(t *testing.T) {
	plan := newCurrencyPlan(profileWithCurrency("TRY"), []models.WarehouseConfig{
		datasource("default", ""), datasource("eu", "EUR"),
	})
	stepByID := map[int]*models.ExplorationStep{
		3: {Step: 3, WarehouseID: "default"},
		7: {Step: 7, WarehouseID: "eu"},
	}
	sc := scoringContext{cfg: testScoringCfg(), currencies: plan, stepByID: stepByID, writer: &usWriter}

	lira, liraVerdict := holdingFigure("lira", 2_400_000, models.UnitCurrency, "₺", 3)
	euro, euroVerdict := holdingFigure("euro", 900_000, models.UnitCurrency, "€", 7)
	share, shareVerdict := holdingFigure("share", 34, models.UnitPercent, "", 3)

	ins := models.Insight{ID: "i1", AnalysisArea: "revenue", Name: "n"}
	ins.Figures = []models.Figure{lira, euro, share}
	ins.FigureVerdicts = []models.FigureVerdict{liraVerdict, euroVerdict, shareVerdict}
	ins.Description = "The finding states {{lira}} and {{share}}."
	ins.Indicators = []string{"A euro figure of {{euro}}"}
	one := []models.Insight{ins}
	usWriter.renderInsightFigures(one)
	ins = one[0]

	// Repair drops the euro indicator, exactly as it does: the text goes, the
	// figure and its holding verdict stay.
	ins.Indicators = nil
	ins.Repair = &models.InsightRepair{Rounds: 1, Outcome: models.RepairClaimDropped}
	ins.Validation = verdictOf(valmodels.StatusConfirmed)

	insights := []models.Insight{ins}
	scoreInsights(insights, sc)
	driver := insights[0].Scoring.SeverityDriver

	if driver.Amount == nil || driver.Amount.Currency != "₺" {
		t.Errorf("the surviving lira amount was discarded: %+v; a sentence removed elsewhere "+
			"must not make it incomparable", driver.Amount)
	}
	if driver.Figure == nil || driver.Figure.FigureID != "share" {
		t.Errorf("banded on %+v, want the surviving share", driver.Figure)
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

	// Through the renderer, so the figure is one the prose actually states --
	// which is what the severity axis requires.
	ins := publishedInsight("i1", "revenue", pairOf(share, shareVerdict))
	ins.Validation = verdictOf(valmodels.StatusUnverifiable)
	insights := []models.Insight{ins}
	scoreInsights(insights, scoringContext{
		cfg: testScoringCfg(), currencies: plan, stepByID: stepByID, writer: &usWriter,
	})
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

// --- Figures repair severed from the prose --------------------------------

// TestARepairedAwayFigureCannotDriveSeverity is the second thing a review
// found, and it is the subtler of the two because nothing about the document
// looks wrong.
//
// Repair edits TEXT and leaves figures alone: it substitutes a refuted numeral
// in place, or drops a sentence outright, and touches neither the figure nor
// its verdict. So an insight whose 80% indicator repair deleted still carries
// that figure, its value and its holding verdict -- and banding off it would
// rank the finding high on a number removed from the text that shipped, while
// the prose a reader sees says 1%.
//
// The audit trail already answers which figures those are; this asserts the
// severity axis asks it.
func TestARepairedAwayFigureCannotDriveSeverity(t *testing.T) {
	plan := projectCurrencyPlan("USD")
	stepByID := map[int]*models.ExplorationStep{7: {Step: 7, WarehouseID: "default"}}
	sc := scoringContext{
		cfg: testScoringCfg(), currencies: plan, stepByID: stepByID, writer: &usWriter,
	}

	// A big share and a small one, both checked and holding, both referenced
	// from the prose as authored.
	figures := []models.Figure{
		// Decimals: 2 because the shipped prose below is written the way the
		// renderer writes it. figuresBrokenByRepair decides by re-rendering
		// the template and comparing, so a fixture whose text disagrees with
		// its own figures reads as repaired everywhere.
		{ID: "big", Value: 80, Unit: models.UnitPercent, Decimals: 2, Step: 7, Kind: models.FigureCell,
			Column: "share", Row: "bucket = 'a'"},
		{ID: "small", Value: 1, Unit: models.UnitPercent, Decimals: 2, Step: 7, Kind: models.FigureCell,
			Column: "share", Row: "bucket = 'b'"},
	}
	verdicts := []models.FigureVerdict{
		{ID: "big", Display: "80.00%", Status: models.FigureHolds, Claimed: 80, Evaluated: 80},
		{ID: "small", Display: "1.00%", Status: models.FigureHolds, Claimed: 1, Evaluated: 1},
	}
	template := &models.FigureTemplate{
		Name:        "Concentration in one bucket",
		Description: "Only {{small}} of orders sit outside the top bucket.",
		Indicators:  []string{"The top bucket holds {{big}} of orders"},
	}

	t.Run("untouched by repair, the big share bands it", func(t *testing.T) {
		insights := []models.Insight{{
			ID: "i1", AnalysisArea: "revenue", Name: "Concentration in one bucket",
			Description:    "Only 1.00% of orders sit outside the top bucket.",
			Indicators:     []string{"The top bucket holds 80.00% of orders"},
			Figures:        figures,
			FigureVerdicts: verdicts,
			FigureTemplate: template,
			Validation:     verdictOf(valmodels.StatusConfirmed),
		}}
		scoreInsights(insights, sc)
		got := insights[0]
		if got.Scoring.SeverityDriver.Figure == nil || got.Scoring.SeverityDriver.Figure.FigureID != "big" {
			t.Fatalf("driver = %+v, want the 80%% share", got.Scoring.SeverityDriver.Figure)
		}
		if got.Severity != gomodels.SeverityBandHigh {
			t.Errorf("severity = %q, want high", got.Severity)
		}
	})

	t.Run("repair deleted the indicator carrying it, so it cannot", func(t *testing.T) {
		// Exactly what repair leaves behind: the indicator gone from the
		// shipped prose, the figure and its holding verdict still on the
		// document, and a Repair record saying a claim was dropped.
		insights := []models.Insight{{
			ID: "i1", AnalysisArea: "revenue", Name: "Concentration in one bucket",
			Description:    "Only 1.00% of orders sit outside the top bucket.",
			Indicators:     nil,
			Figures:        figures,
			FigureVerdicts: verdicts,
			FigureTemplate: template,
			Repair: &models.InsightRepair{
				Rounds: 1, Outcome: models.RepairClaimDropped,
				Dropped: []string{"The top bucket holds 80.00% of orders"},
			},
			Validation: verdictOf(valmodels.StatusConfirmed),
		}}
		scoreInsights(insights, sc)
		got := insights[0]

		driver := got.Scoring.SeverityDriver
		if driver.Figure != nil && driver.Figure.FigureID == "big" {
			t.Error("severity was banded off a figure whose sentence repair deleted; " +
				"the prose a reader sees says 1.00%")
		}
		if got.Severity == gomodels.SeverityBandHigh {
			t.Errorf("severity = high off a removed 80%% claim (driver %+v)", driver)
		}
		// The small share is still on the page, so it is still allowed to
		// band -- withholding everything would be the blanket rule this
		// predicate replaced.
		if driver.Figure == nil || driver.Figure.FigureID != "small" {
			t.Errorf("driver = %+v, want the 1%% share that survived", driver.Figure)
		}
		// And the verdicts are NOT filtered: a refuted figure still docks
		// confidence whether or not repair later rewrote its sentence,
		// because that deduction is about what the check found.
		if len(got.Scoring.ConfidenceDocks) != 0 {
			t.Logf("confidence docks: %+v", got.Scoring.ConfidenceDocks)
		}
	})
}
