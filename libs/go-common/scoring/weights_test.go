package scoring

import (
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

// The configurable severity weight, and the business-risk flags that come from
// the same mapping.
//
// Pure functions, so the table tests here are the primary evidence for the
// whole feature: the precedence ladder, which category the breakdown names,
// the arithmetic the feature exists to produce, and every way a configuration
// can be wrong.

// weightedConfig is the shipped calibration with a taxonomy resolved into it.
//
// Built through ResolveSeverity rather than by writing CategoryWeights
// directly, so these tests exercise the same resolution the agent runs.
func weightedConfig(projectWeights map[string]float64) Config {
	cfg := DefaultConfig()
	cfg.Severity = ResolveSeverity(cfg.Severity, []gomodels.RiskCategory{
		{ID: "compliance", Label: "Compliance", Weight: 3.0},
		{ID: "revenue_leak", Label: "Revenue leakage", Weight: 2.0},
		{ID: "engagement", Label: "Engagement", Weight: 1.0},
	}, projectWeights)
	return cfg
}

// scoreArea scores a confirmed document with one qualifying share figure, in
// an area with the given categories and override. One helper, because the
// figure/verdict pair is the thing most likely to be got wrong and the
// severity half is what every case here is about.
func scoreArea(cfg Config, share float64, categories []string, override *float64) gomodels.Scoring {
	return Score(Input{
		Verdict: valmodels.StatusConfirmed,
		Figures: []gomodels.Figure{{ID: "f1", Value: share, Unit: gomodels.UnitPercent}},
		FigureVerdicts: []gomodels.FigureVerdict{{
			ID: "f1", Display: "share", Status: gomodels.FigureHolds,
			Claimed: share, Evaluated: share,
		}},
		AreaID:         "area",
		AreaCategories: categories,
		AreaOverride:   override,
		Config:         cfg,
	})
}

func ptr(v float64) *float64 { return &v }

// --- The precedence ladder -------------------------------------------------

// TestTheWeightLadder walks every rung and asserts both halves of the answer:
// the number, and what the breakdown will say set it.
func TestTheWeightLadder(t *testing.T) {
	cfg := weightedConfig(nil)

	cases := []struct {
		name       string
		categories []string
		override   *float64
		wantWeight float64
		wantKind   string
		wantCat    string
	}{
		{
			name: "no categories and no override: the engine default",
			// The shipped state, and the one that has to stay byte-identical.
			wantWeight: 1.0, wantKind: gomodels.WeightSourceDefault,
		},
		{
			name:       "one category",
			categories: []string{"compliance"},
			wantWeight: 3.0, wantKind: gomodels.WeightSourceCategory, wantCat: "compliance",
		},
		{
			name:       "two categories: the largest, not the product or the mean",
			categories: []string{"revenue_leak", "compliance"},
			// Product would be 6.0 and mean 2.5. Max is 3.0, and it names the
			// category it came from.
			wantWeight: 3.0, wantKind: gomodels.WeightSourceCategory, wantCat: "compliance",
		},
		{
			name:       "the override wins over the categories",
			categories: []string{"compliance"},
			override:   ptr(1.5),
			wantWeight: 1.5, wantKind: gomodels.WeightSourceAreaOverride,
		},
		{
			name:       "an override below its categories still wins",
			categories: []string{"compliance"},
			override:   ptr(0.5),
			// The point of an override: the one area a business weights
			// differently from the rest of its category.
			wantWeight: 0.5, wantKind: gomodels.WeightSourceAreaOverride,
		},
		{
			name:       "an override with no categories",
			override:   ptr(2.0),
			wantWeight: 2.0, wantKind: gomodels.WeightSourceAreaOverride,
		},
		{
			name:       "a category nothing weights falls back to the default",
			categories: []string{"security"},
			// A global category id the pack declares no weight for: it flags
			// the finding without weighting it.
			wantWeight: 1.0, wantKind: gomodels.WeightSourceDefault,
		},
		{
			name:       "a category nothing declares at all",
			categories: []string{"invented_by_hand"},
			wantWeight: 1.0, wantKind: gomodels.WeightSourceDefault,
		},
		{
			name:       "a weighted and an unweighted category together",
			categories: []string{"security", "revenue_leak"},
			wantWeight: 2.0, wantKind: gomodels.WeightSourceCategory, wantCat: "revenue_leak",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scoreArea(cfg, 24.66, tc.categories, tc.override)
			if got.AreaWeight != tc.wantWeight {
				t.Errorf("AreaWeight = %v, want %v", got.AreaWeight, tc.wantWeight)
			}
			if got.WeightSource == nil {
				t.Fatal("WeightSource is nil; a present weight with no source leaves a reader " +
					"unable to tell a deliberate 1.0 from an unwritten field")
			}
			if got.WeightSource.Kind != tc.wantKind {
				t.Errorf("WeightSource.Kind = %q, want %q", got.WeightSource.Kind, tc.wantKind)
			}
			if got.WeightSource.Category != tc.wantCat {
				t.Errorf("WeightSource.Category = %q, want %q", got.WeightSource.Category, tc.wantCat)
			}
		})
	}
}

// TestATieOnTheLargestWeightKeepsTheFirstDeclaredCategory -- the breakdown
// names a category, and it has to name the same one every time the document is
// re-scored. An unstable pick would be a stored explanation that changed its
// mind whenever validation was re-run.
func TestATieOnTheLargestWeightKeepsTheFirstDeclaredCategory(t *testing.T) {
	cfg := weightedConfig(map[string]float64{"revenue_leak": 3.0})

	first := scoreArea(cfg, 24.66, []string{"compliance", "revenue_leak"}, nil)
	second := scoreArea(cfg, 24.66, []string{"revenue_leak", "compliance"}, nil)

	if first.WeightSource.Category != "compliance" {
		t.Errorf("declared compliance first, got %q", first.WeightSource.Category)
	}
	if second.WeightSource.Category != "revenue_leak" {
		t.Errorf("declared revenue_leak first, got %q", second.WeightSource.Category)
	}
	// And scoring the same input twice gives the same answer, which is what
	// "stable across re-scores" actually means.
	for i := 0; i < 10; i++ {
		again := scoreArea(cfg, 24.66, []string{"compliance", "revenue_leak"}, nil)
		if again.WeightSource.Category != first.WeightSource.Category {
			t.Fatalf("re-score %d named %q, first named %q", i, again.WeightSource.Category, first.WeightSource.Category)
		}
	}
}

// TestAProjectWeightWinsOverThePacksDeclaredOne -- the project is the more
// specific statement, and it is how "compliance is three times anything else
// for us" is said once for a whole deployment.
func TestAProjectWeightWinsOverThePacksDeclaredOne(t *testing.T) {
	cfg := weightedConfig(map[string]float64{"compliance": 1.0})
	got := scoreArea(cfg, 24.66, []string{"compliance"}, nil)
	if got.AreaWeight != 1.0 {
		t.Errorf("AreaWeight = %v, want the project's 1.0 over the pack's 3.0", got.AreaWeight)
	}
	if got.WeightSource.Category != "compliance" {
		t.Errorf("WeightSource.Category = %q, want compliance — a project override is still the "+
			"category's weight, not a new kind of source", got.WeightSource.Category)
	}
}

// TestAProjectMayWeightACategoryNoPackDeclares -- a global category id with no
// pack-declared weight. This is the commercial case the whole axis exists for:
// a healthcare deployment weights `compliance` without its pack having to
// declare a taxonomy at all.
func TestAProjectMayWeightACategoryNoPackDeclares(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Severity = ResolveSeverity(cfg.Severity, nil, map[string]float64{"compliance": 3.0})

	got := scoreArea(cfg, 1.2, []string{"compliance"}, nil)
	if got.AreaWeight != 3.0 {
		t.Fatalf("AreaWeight = %v, want 3.0", got.AreaWeight)
	}
	if !reflect.DeepEqual(got.RiskFlags, []string{"compliance"}) {
		t.Errorf("RiskFlags = %v, want [compliance]", got.RiskFlags)
	}
}

// --- The arithmetic the feature exists to produce --------------------------

// TestALowMagnitudeInAWeightedCategoryOutranksAMediumOneInAnUnweightedArea is
// the acceptance criterion, as arithmetic.
//
// Severity is the thing the default list is ordered by, so "outranks" is a
// claim about two numbers and both bands. Asserted as both -- a test that
// checked only the scores could pass while the bands stayed equal, which is
// what a reader actually sees.
func TestALowMagnitudeInAWeightedCategoryOutranksAMediumOneInAnUnweightedArea(t *testing.T) {
	cfg := weightedConfig(nil)

	// 1.2% share -> the low magnitude, 0.25. In a 3.0 category: 0.75.
	small := scoreArea(cfg, 1.2, []string{"compliance"}, nil)
	// 7.5% share -> the medium magnitude, 0.60. Unweighted: 0.60.
	middling := scoreArea(cfg, 7.5, nil, nil)

	if small.MagnitudeScore != 0.25 {
		t.Fatalf("the small finding's magnitude = %v, want 0.25 (the fixture no longer bands low)", small.MagnitudeScore)
	}
	if middling.MagnitudeScore != 0.60 {
		t.Fatalf("the middling finding's magnitude = %v, want 0.60", middling.MagnitudeScore)
	}
	if small.SeverityScore != 0.75 {
		t.Errorf("the small finding's severity = %v, want 0.75 (0.25 x 3.0)", small.SeverityScore)
	}
	if small.SeverityScore <= middling.SeverityScore {
		t.Errorf("a low-magnitude finding in a 3.0 category (%v) does not outrank a medium-magnitude "+
			"one in a 1.0 area (%v) — which is the whole claim this axis makes",
			small.SeverityScore, middling.SeverityScore)
	}
	if small.SeverityBand != gomodels.SeverityBandHigh {
		t.Errorf("the small finding's band = %q, want high", small.SeverityBand)
	}
	if middling.SeverityBand != gomodels.SeverityBandMedium {
		t.Errorf("the middling finding's band = %q, want medium", middling.SeverityBand)
	}
}

// TestTheMagnitudeIsWeightFree -- the weight multiplies the SEVERITY and
// leaves the magnitude alone.
//
// Load-bearing beyond tidiness: the cross-run comparison in the ledger reads
// the magnitude precisely so that changing a weight cannot make every finding
// in a category look as though it got worse. A weight that leaked into the
// magnitude would reopen that.
func TestTheMagnitudeIsWeightFree(t *testing.T) {
	cfg := weightedConfig(nil)
	unweighted := scoreArea(cfg, 1.2, nil, nil)
	weighted := scoreArea(cfg, 1.2, []string{"compliance"}, nil)

	if unweighted.MagnitudeScore != weighted.MagnitudeScore {
		t.Errorf("the magnitude moved with the weight: %v unweighted vs %v weighted",
			unweighted.MagnitudeScore, weighted.MagnitudeScore)
	}
	if unweighted.SeverityScore == weighted.SeverityScore {
		t.Error("the severity did NOT move with the weight, so the weight is not being applied")
	}
}

// TestTheClampHoldsWithAWeightAbove1 -- risk_score carries the severity score
// and its [0, 1] contract is what every reader that prints it as a percentage
// relies on. A top-band magnitude in a 3.0 category is the case that would
// otherwise escape it.
func TestTheClampHoldsWithAWeightAbove1(t *testing.T) {
	cfg := weightedConfig(nil)
	got := scoreArea(cfg, 24.66, []string{"compliance"}, nil)

	if got.MagnitudeScore != 1.0 {
		t.Fatalf("magnitude = %v, want 1.0 (the fixture no longer bands high)", got.MagnitudeScore)
	}
	if got.SeverityScore != 1.0 {
		t.Errorf("severity = %v, want 1.0 — 1.00 x 3.0 must clamp, not report 3.0", got.SeverityScore)
	}
	if got.SeverityScore > 1.0 {
		t.Error("the [0,1] contract risk_score carries is broken")
	}
}

// --- Illegal weights ------------------------------------------------------

// TestAnIllegalWeightIsTreatedAsUnstated.
//
// Validation rejects these when a pack or a project is saved, so one reaching
// the engine was hand-edited into a document. Both wrong answers are worse
// than falling through: applying a zero deletes every finding in the area from
// the ranking, and clamping acts on a number nobody wrote.
func TestAnIllegalWeightIsTreatedAsUnstated(t *testing.T) {
	illegal := []float64{0, -1, MaxSeverityWeight + 0.0001, 100, math.NaN(), math.Inf(1), math.Inf(-1)}

	for _, w := range illegal {
		name := strconv.FormatFloat(w, 'g', -1, 64)
		t.Run("override "+name, func(t *testing.T) {
			cfg := weightedConfig(nil)
			// With categories present, an ignored override falls through to
			// THEM rather than to the default: the categories are what the
			// configuration said before the illegal value was written over
			// it.
			got := scoreArea(cfg, 24.66, []string{"revenue_leak"}, ptr(w))
			if got.AreaWeight != 2.0 || got.WeightSource.Kind != gomodels.WeightSourceCategory {
				t.Errorf("override %v: weight %v from %q, want 2.0 from the category",
					w, got.AreaWeight, got.WeightSource.Kind)
			}

			// With nothing else configured, the default.
			bare := scoreArea(cfg, 24.66, nil, ptr(w))
			if bare.AreaWeight != 1.0 || bare.WeightSource.Kind != gomodels.WeightSourceDefault {
				t.Errorf("override %v with no categories: weight %v from %q, want 1.0 from the default",
					w, bare.AreaWeight, bare.WeightSource.Kind)
			}
		})

		t.Run("category weight "+name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Severity = ResolveSeverity(cfg.Severity, []gomodels.RiskCategory{
				{ID: "compliance", Label: "Compliance", Weight: w},
			}, nil)
			got := scoreArea(cfg, 24.66, []string{"compliance"}, nil)
			if got.AreaWeight != 1.0 || got.WeightSource.Kind != gomodels.WeightSourceDefault {
				t.Errorf("category weight %v: weight %v from %q, want 1.0 from the default",
					w, got.AreaWeight, got.WeightSource.Kind)
			}
		})
	}
}

// TestResolveSeverityLeavesNothingBehindWhenThereIsNothingToResolve -- an
// empty resolution is nil rather than an empty map, so a project whose
// taxonomy is entirely broken is indistinguishable from one with no taxonomy,
// which is what it is.
func TestResolveSeverityLeavesNothingBehindWhenThereIsNothingToResolve(t *testing.T) {
	base := DefaultConfig().Severity

	for _, tc := range []struct {
		name     string
		taxonomy []gomodels.RiskCategory
		project  map[string]float64
	}{
		{name: "nothing at all"},
		{name: "a taxonomy of broken weights", taxonomy: []gomodels.RiskCategory{
			{ID: "compliance", Weight: 0},
			{ID: "security", Weight: -2},
		}},
		{name: "a category with no id", taxonomy: []gomodels.RiskCategory{{Weight: 2}}},
		{name: "a project weight with no id", project: map[string]float64{"": 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveSeverity(base, tc.taxonomy, tc.project)
			if got.CategoryWeights != nil {
				t.Errorf("CategoryWeights = %v, want nil", got.CategoryWeights)
			}
		})
	}
}

// TestResolveSeverityDoesNotWriteIntoItsInput -- Config carries a map, and a
// resolution that mutated the caller's copy would weight a second run with the
// first one's taxonomy.
func TestResolveSeverityDoesNotWriteIntoItsInput(t *testing.T) {
	base := DefaultConfig().Severity
	base.CategoryWeights = map[string]float64{"existing": 2.0}

	got := ResolveSeverity(base, []gomodels.RiskCategory{
		{ID: "compliance", Label: "Compliance", Weight: 3.0},
	}, nil)

	if len(base.CategoryWeights) != 1 || base.CategoryWeights["existing"] != 2.0 {
		t.Errorf("the input's map was modified: %v", base.CategoryWeights)
	}
	if _, resolved := got.CategoryWeights["existing"]; resolved {
		t.Error("the resolution carried a stale map's entry forward; it is a resolution, not a merge")
	}
}

// --- The flags ------------------------------------------------------------

// TestRiskFlagsAreAreaDerivedDeterministicAndMultiTag.
func TestRiskFlagsAreAreaDerivedDeterministicAndMultiTag(t *testing.T) {
	cfg := weightedConfig(nil)

	cases := []struct {
		name       string
		categories []string
		want       []string
	}{
		{name: "no categories: nil, not an empty list", categories: nil, want: nil},
		{name: "one", categories: []string{"compliance"}, want: []string{"compliance"}},
		{
			name:       "several, in the area's own declaration order",
			categories: []string{"revenue_leak", "compliance", "data_quality"},
			want:       []string{"revenue_leak", "compliance", "data_quality"},
		},
		{
			name:       "deduped",
			categories: []string{"compliance", "compliance"},
			want:       []string{"compliance"},
		},
		{
			name:       "an id nothing declares is dropped",
			categories: []string{"compliance", "invented_by_hand"},
			want:       []string{"compliance"},
		},
		{
			name:       "an area citing only unknown ids is not flagged",
			categories: []string{"invented_by_hand"},
			want:       nil,
		},
		{
			name:       "the global set is flaggable without being weighted",
			categories: []string{"security", "data_quality"},
			want:       []string{"security", "data_quality"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scoreArea(cfg, 24.66, tc.categories, nil)
			if !reflect.DeepEqual(got.RiskFlags, tc.want) {
				t.Errorf("RiskFlags = %#v, want %#v", got.RiskFlags, tc.want)
			}
		})
	}
}

// TestTheGlobalSetIsAvailableToAPackWithNoTaxonomy -- the acceptance criterion
// spelled out: no taxonomy, no resolved weights, and the cross-cutting flags
// still land.
func TestTheGlobalSetIsAvailableToAPackWithNoTaxonomy(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Severity = ResolveSeverity(cfg.Severity, nil, nil)
	if cfg.Severity.CategoryWeights != nil {
		t.Fatalf("CategoryWeights = %v, want nil for a pack with no taxonomy", cfg.Severity.CategoryWeights)
	}

	for _, id := range GlobalRiskCategories() {
		got := scoreArea(cfg, 24.66, []string{id}, nil)
		if !reflect.DeepEqual(got.RiskFlags, []string{id}) {
			t.Errorf("RiskFlags for %q = %v, want [%s]", id, got.RiskFlags, id)
		}
		// Flagged, not weighted: nothing declared a weight for it.
		if got.AreaWeight != 1.0 || got.WeightSource.Kind != gomodels.WeightSourceDefault {
			t.Errorf("%q: weight %v from %q, want the default", id, got.AreaWeight, got.WeightSource.Kind)
		}
	}
}

// TestTheModelCannotAddARiskFlag -- flags come from the area's categories and
// from no other input, so a document whose model volunteered one has nowhere
// to put it.
//
// Asserted two ways: the Input struct carries no flag-bearing field at all
// (checked by reflection, so adding one is a test failure rather than a silent
// new path), and an input with no AreaCategories produces no flags however
// much else it carries.
func TestTheModelCannotAddARiskFlag(t *testing.T) {
	rt := reflect.TypeOf(Input{})
	for i := 0; i < rt.NumField(); i++ {
		if name := rt.Field(i).Name; strings.Contains(strings.ToLower(name), "flag") {
			t.Errorf("Input.%s exists; risk flags must be derived from AreaCategories only, and a field "+
				"a caller can set is a field a model's output can reach", name)
		}
	}

	cfg := weightedConfig(nil)
	loaded := Score(Input{
		Verdict:            valmodels.StatusConfirmed,
		AreaID:             "compliance", // an area NAMED after a category
		QuantifierVerdicts: []gomodels.QuantifierVerdict{{Claim: "compliance", Status: gomodels.QuantifierHolds}},
		Config:             cfg,
	})
	if loaded.RiskFlags != nil {
		t.Errorf("RiskFlags = %v, want nil: the area maps to no category", loaded.RiskFlags)
	}
}

// TestARecommendationInheritsTheFlagsWithTheSeverityHalf -- an action about a
// compliance exposure is a compliance action. It has no analysis area of its
// own, which is the same reason its band is inherited rather than measured.
func TestARecommendationInheritsTheFlagsWithTheSeverityHalf(t *testing.T) {
	cfg := weightedConfig(nil)

	cited := []InheritedSeverity{
		{
			MagnitudeScore: 0.60, SeverityBand: gomodels.SeverityBandMedium, SeverityScore: 0.60,
			AreaWeight: 1.0, WeightSource: &gomodels.WeightSource{Kind: gomodels.WeightSourceDefault},
			RiskFlags: []string{"engagement"},
		},
		{
			MagnitudeScore: 0.25, SeverityBand: gomodels.SeverityBandHigh, SeverityScore: 0.75,
			AreaWeight: 3.0, WeightSource: &gomodels.WeightSource{
				Kind: gomodels.WeightSourceCategory, Category: "compliance",
			},
			RiskFlags: []string{"compliance", "data_quality"},
		},
	}

	got := ScoreRecommendation(Input{Verdict: valmodels.StatusSupported, Config: cfg}, cited)

	// The max-not-mean rule still decides which insight is inherited from,
	// with weights in play: the 0.75 compliance finding, not the 0.60 one.
	if got.SeverityScore != 0.75 {
		t.Errorf("SeverityScore = %v, want 0.75 (the highest cited)", got.SeverityScore)
	}
	if got.AreaWeight != 3.0 {
		t.Errorf("AreaWeight = %v, want 3.0", got.AreaWeight)
	}
	if !reflect.DeepEqual(got.RiskFlags, []string{"compliance", "data_quality"}) {
		t.Errorf("RiskFlags = %v, want the inherited [compliance data_quality]", got.RiskFlags)
	}

	// Nothing to inherit leaves the whole severity half -- flags included --
	// unset, rather than manufacturing one.
	none := ScoreRecommendation(Input{Verdict: valmodels.StatusSupported, Config: cfg}, nil)
	if none.RiskFlags != nil || none.SeverityBand != "" {
		t.Errorf("with nothing cited: RiskFlags = %v, band = %q, want neither", none.RiskFlags, none.SeverityBand)
	}
}

// TestRescoreConfidenceStillFreezesTheSeverityAxis -- including the two fields
// the weight adds to it.
//
// The on-demand "Run validation" path re-derives the confidence axis of a
// stored document. Re-deriving severity there would rewrite a past run under
// today's weights, and a stored severity is an immutable fact about the run
// that produced it.
func TestRescoreConfidenceStillFreezesTheSeverityAxis(t *testing.T) {
	prior := gomodels.Scoring{
		ConfidenceBand: gomodels.ConfidenceBandLow, ConfidenceScore: 0.30,
		MagnitudeScore: 0.25, SeverityBand: gomodels.SeverityBandHigh, SeverityScore: 0.75,
		AreaWeight: 3.0,
		WeightSource: &gomodels.WeightSource{
			Kind: gomodels.WeightSourceCategory, Category: "compliance",
		},
		RiskFlags:     []string{"compliance"},
		EngineVersion: EngineVersion,
	}

	// Today's configuration weights compliance at 1.0 instead, and the area
	// now maps to nothing.
	cfg := DefaultConfig()
	cfg.Severity = ResolveSeverity(cfg.Severity, nil, map[string]float64{"compliance": 1.0})
	got := RescoreConfidence(prior, Input{Verdict: valmodels.StatusConfirmed, Config: cfg})

	if got.AreaWeight != 3.0 {
		t.Errorf("AreaWeight = %v, want the stored 3.0", got.AreaWeight)
	}
	if got.WeightSource == nil || got.WeightSource.Category != "compliance" {
		t.Errorf("WeightSource = %+v, want the stored compliance category", got.WeightSource)
	}
	if !reflect.DeepEqual(got.RiskFlags, []string{"compliance"}) {
		t.Errorf("RiskFlags = %v, want the stored [compliance]", got.RiskFlags)
	}
	if got.SeverityScore != 0.75 || got.SeverityBand != gomodels.SeverityBandHigh {
		t.Errorf("the severity half moved: %v / %q", got.SeverityScore, got.SeverityBand)
	}
	// The confidence half did move, and so did the tier it feeds.
	if got.ConfidenceBand != gomodels.ConfidenceBandHigh {
		t.Errorf("ConfidenceBand = %q, want high", got.ConfidenceBand)
	}
	if got.PriorityTier != gomodels.PriorityTierCritical {
		t.Errorf("PriorityTier = %q, want critical", got.PriorityTier)
	}
}
