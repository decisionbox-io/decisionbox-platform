package discovery

import (
	"reflect"
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/libs/go-common/scoring"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// The wiring between a run's PINNED severity weighting and the engine that
// reads it.
//
// The engine's own table tests cover the ladder. What they cannot cover is
// that this package hands it the right area's configuration -- and that it
// takes it from the pin rather than from the project, which is what makes a
// resumed run score with the weights it started with.

func weightPtr(v float64) *float64 { return &v }

// pinnedRun is a run whose first attempt recorded a taxonomy and two mapped
// areas.
func pinnedRun() *gomodels.RunScoringConfig {
	return &gomodels.RunScoringConfig{
		CategoryWeights: map[string]float64{"compliance": 3.0, "revenue_leak": 2.0},
		Areas: map[string]gomodels.RunAreaRisk{
			"consent": {RiskCategories: []string{"compliance", "data_quality"}},
			"billing": {RiskCategories: []string{"revenue_leak"}, SeverityWeight: weightPtr(1.5)},
		},
	}
}

// TestTheOrchestratorScoresAgainstTheRunsPinnedWeights -- the construction
// seam: the pinned weights become the calibration, and the pinned areas become
// the per-area lookup.
func TestTheOrchestratorScoresAgainstTheRunsPinnedWeights(t *testing.T) {
	pinned := pinnedRun()

	cfg := runScoringConfig(pinned)
	if cfg.Severity.CategoryWeights["compliance"] != 3.0 {
		t.Errorf("CategoryWeights = %v, want the pinned compliance weight", cfg.Severity.CategoryWeights)
	}
	// Everything else is still this build's calibration: the pin carries
	// weights, not a whole engine version.
	if cfg.Version != scoring.EngineVersion || cfg.Severity.DefaultWeight != scoring.DefaultConfig().Severity.DefaultWeight {
		t.Errorf("the pin replaced more than the weights: %+v", cfg.Severity)
	}

	risk := runAreaRisk(pinned)
	if len(risk) != 2 {
		t.Errorf("areaRisk = %v, want the two pinned areas", risk)
	}

	// A run that pinned nothing -- one older than the field -- scores exactly
	// as it did before any of this existed.
	bare := runScoringConfig(nil)
	if bare.Severity.CategoryWeights != nil {
		t.Errorf("an unpinned run resolved weights from somewhere: %v", bare.Severity.CategoryWeights)
	}
	if runAreaRisk(nil) != nil {
		t.Error("an unpinned run resolved area risk from somewhere")
	}
}

// TestAnInsightIsScoredAgainstItsOwnAreasConfiguration -- the lookup is by the
// insight's stored analysis area, so two areas scored in one call cannot be
// given each other's weights.
func TestAnInsightIsScoredAgainstItsOwnAreasConfiguration(t *testing.T) {
	plan := newCurrencyPlan(nil, nil)
	sc := scoringContext{
		cfg:      runScoringConfig(pinnedRun()),
		areaRisk: runAreaRisk(pinnedRun()),
		writer:   &usWriter,
	}

	// One share figure each, banding low on its own, so the weight is the
	// only thing that can separate them.
	share := func(id string) figurePair {
		return pairOf(holdingFigure(id, 1.2, models.UnitPercent, "", 3))
	}
	insights := []models.Insight{
		publishedInsight("i1", "consent", share("f1")),
		publishedInsight("i2", "billing", share("f1")),
		publishedInsight("i3", "engagement", share("f1")),
	}
	for i := range insights {
		insights[i].Validation = verdictOf(valmodels.StatusConfirmed)
	}
	sc.currencies = plan
	sc.stepByID = map[int]*models.ExplorationStep{3: {Step: 3, WarehouseID: "default"}}

	scoreInsights(insights, sc)

	// consent: the largest of its two categories, one of which is unweighted.
	consent := insights[0].Scoring
	if consent == nil {
		t.Fatal("the consent insight was not scored")
	}
	if consent.AreaWeight != 3.0 || consent.WeightSource.Category != "compliance" {
		t.Errorf("consent: weight %v from %+v, want 3.0 from compliance", consent.AreaWeight, consent.WeightSource)
	}
	if !reflect.DeepEqual(consent.RiskFlags, []string{"compliance", "data_quality"}) {
		t.Errorf("consent flags = %v, want both categories", consent.RiskFlags)
	}
	// 0.25 x 3.0 = 0.75, which is the high band -- a low-magnitude compliance
	// finding outranking a middling one elsewhere, through the real seam.
	if consent.SeverityBand != gomodels.SeverityBandHigh {
		t.Errorf("consent band = %q, want high", consent.SeverityBand)
	}

	// billing: the override wins over its category.
	billing := insights[1].Scoring
	if billing.AreaWeight != 1.5 || billing.WeightSource.Kind != gomodels.WeightSourceAreaOverride {
		t.Errorf("billing: weight %v from %+v, want 1.5 from the override", billing.AreaWeight, billing.WeightSource)
	}
	if !reflect.DeepEqual(billing.RiskFlags, []string{"revenue_leak"}) {
		t.Errorf("billing flags = %v, want [revenue_leak]", billing.RiskFlags)
	}

	// engagement: not in the pin at all, so the default and no flags. This is
	// also the case of an area the project gained after the run started.
	engagement := insights[2].Scoring
	if engagement.AreaWeight != 1.0 || engagement.WeightSource.Kind != gomodels.WeightSourceDefault {
		t.Errorf("engagement: weight %v from %+v, want the default", engagement.AreaWeight, engagement.WeightSource)
	}
	if engagement.RiskFlags != nil {
		t.Errorf("engagement flags = %v, want none", engagement.RiskFlags)
	}
	if engagement.SeverityBand != gomodels.SeverityBandLow {
		t.Errorf("engagement band = %q, want low", engagement.SeverityBand)
	}
}

// TestARecommendationCarriesTheFlagsOfTheInsightItAddresses -- through the
// real inheritance map this package builds, not the engine's own.
func TestARecommendationCarriesTheFlagsOfTheInsightItAddresses(t *testing.T) {
	cfg := runScoringConfig(pinnedRun())
	sc := scoringContext{
		cfg:        cfg,
		areaRisk:   runAreaRisk(pinnedRun()),
		currencies: newCurrencyPlan(nil, nil),
		stepByID:   map[int]*models.ExplorationStep{3: {Step: 3, WarehouseID: "default"}},
		writer:     &usWriter,
	}

	insights := []models.Insight{
		publishedInsight("i1", "consent", pairOf(holdingFigure("f1", 1.2, models.UnitPercent, "", 3))),
		publishedInsight("i2", "engagement", pairOf(holdingFigure("f1", 7.5, models.UnitPercent, "", 3))),
	}
	for i := range insights {
		insights[i].Validation = verdictOf(valmodels.StatusConfirmed)
	}
	scoreInsights(insights, sc)

	recs := []models.Recommendation{{
		ID: "r1", Title: "Fix consent capture",
		RelatedInsightIDs: []string{"i2", "i1"},
		Validation:        verdictOf(valmodels.StatusSupported),
	}}
	scoreRecommendations(recs, insights, cfg)

	got := recs[0].Scoring
	if got == nil {
		t.Fatal("the recommendation was not scored")
	}
	// It cites the engagement finding first, and inherits from the compliance
	// one anyway: inheritance is by the highest severity, not by citation
	// order, and that is what carries the flags.
	if !reflect.DeepEqual(got.RiskFlags, []string{"compliance", "data_quality"}) {
		t.Errorf("RiskFlags = %v, want the compliance insight's", got.RiskFlags)
	}
	if got.AreaWeight != 3.0 {
		t.Errorf("AreaWeight = %v, want the inherited 3.0", got.AreaWeight)
	}
}
