package scoring

import (
	"reflect"
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

// Folding a deployment's own configuration into a calibration, and the money
// thresholds it is the only way to set.

// TestAnEmptyOverrideChangesNothing is the compatibility criterion at the
// fold, and it is the assertion the whole feature's "nothing changes until you
// configure something" claim rests on.
//
// DeepEqual on the whole calibration rather than a spot check of the two
// fields the override can touch: the point is not that those two are right,
// it is that NOTHING moved. A fold that reached for a map it should have left
// alone, or that replaced a nil slice with an empty one, would pass a spot
// check and fail here.
func TestAnEmptyOverrideChangesNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  SeverityConfig
	}{
		{"the shipped calibration", DefaultConfig().Severity},
		{"a calibration with pack weights", weightedConfig(map[string]float64{"compliance": 2}).Severity},
		{"a calibration with amount bands", configWithAmountBands().Severity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ApplyOverride(tc.cfg, gomodels.ScoringOverride{})
			if !reflect.DeepEqual(got, tc.cfg) {
				t.Errorf("the calibration moved under an empty override:\n got %+v\nwant %+v", got, tc.cfg)
			}
		})
	}
}

// TestTheOverrideWinsPerCategory. An override states a deployment's priorities,
// which are more specific than a pack's; but it states them one category at a
// time, so a deployment that cares about compliance does not have to restate a
// taxonomy it never looked at.
func TestTheOverrideWinsPerCategory(t *testing.T) {
	cfg := weightedConfig(map[string]float64{"compliance": 1.0, "churn": 2.0})

	got := ApplyOverride(cfg.Severity, gomodels.ScoringOverride{
		CategoryWeights: map[string]float64{"compliance": 3.0, "security": 1.5},
	})

	// weightedConfig's taxonomy is (compliance 3.0, revenue_leak 2.0,
	// engagement 1.0) with the project weights folded over it, so the pack's
	// compliance 3.0 is already overridden to 1.0 before this override sees
	// it -- which is what makes the first row a real precedence assertion
	// rather than a coincidence.
	want := map[string]float64{
		"compliance":   3.0, // the override's, over the project's 1.0
		"churn":        2.0, // the project's, untouched by this override
		"security":     1.5, // added by the override, declared by nothing
		"revenue_leak": 2.0, // the pack's, untouched
		"engagement":   1.0, // the pack's, untouched
	}
	if !reflect.DeepEqual(got.CategoryWeights, want) {
		t.Errorf("weights = %+v, want %+v", got.CategoryWeights, want)
	}
}

// TestTheFoldDoesNotWriteIntoTheCalibrationItWasGiven. Config carries a map,
// and a fold that wrote into it would mutate the calibration every other
// caller holds -- including the package-level default a second run would then
// resolve against.
func TestTheFoldDoesNotWriteIntoTheCalibrationItWasGiven(t *testing.T) {
	cfg := weightedConfig(map[string]float64{"compliance": 1.0})
	before := map[string]float64{}
	for k, v := range cfg.Severity.CategoryWeights {
		before[k] = v
	}

	_ = ApplyOverride(cfg.Severity, gomodels.ScoringOverride{
		CategoryWeights: map[string]float64{"compliance": 3.0, "security": 2.0},
	})

	if !reflect.DeepEqual(cfg.Severity.CategoryWeights, before) {
		t.Errorf("the input calibration was mutated: %+v, want %+v", cfg.Severity.CategoryWeights, before)
	}
}

// TestAnOverrideWithAmountBandsReplacesTheLadder. A band set is a ladder read
// in order, not a bag of independent facts, so merging two would produce a
// third ladder nobody wrote.
func TestAnOverrideWithAmountBandsReplacesTheLadder(t *testing.T) {
	cfg := configWithAmountBands().Severity

	got := ApplyOverride(cfg, gomodels.ScoringOverride{
		AmountBands:    []gomodels.MagnitudeBand{{At: 2_000_000, Band: gomodels.SeverityBandHigh}},
		AmountCurrency: "₺",
	})

	want := []gomodels.MagnitudeBand{{At: 2_000_000, Band: gomodels.SeverityBandHigh}}
	if !reflect.DeepEqual(got.AmountBands, want) {
		t.Errorf("bands = %+v, want exactly the override's %+v", got.AmountBands, want)
	}
	if got.AmountCurrency != "₺" {
		t.Errorf("currency = %q, want ₺", got.AmountCurrency)
	}
}

// --- The currency guard ----------------------------------------------------

// TestAThresholdOnlyBandsTheMoneyItWasWrittenFor is the reason the
// configuration carries a currency at all.
//
// A home currency is resolved per DOCUMENT, from the datasource whose step
// produced its figures, so one run holds documents in different money. A
// deployment that wrote "2,000,000 is high" about liras must not have that
// decide a dollar amount -- and the engine's answer is not to refuse the
// document but to band it on its share, which is what an unconfigured
// deployment does with every amount anyway.
func TestAThresholdOnlyBandsTheMoneyItWasWrittenFor(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Severity.AmountBands = []MagnitudeBand{{At: 2_000_000, Band: gomodels.SeverityBandHigh}}
	cfg.Severity.AmountCurrency = "₺"

	// A lira amount far past the threshold, and a small share beside it.
	liraAmount := pair(fig("a1", 9_000_000, gomodels.UnitCurrency, "₺", gomodels.FigureHolds))
	smallShare := pair(fig("f1", 1.2, gomodels.UnitPercent, "", gomodels.FigureHolds))

	t.Run("the currency matches", func(t *testing.T) {
		got := withFigures(t, "₺", cfg, liraAmount, smallShare)
		if got.SeverityDriver.Kind != gomodels.SeverityDriverAmount {
			t.Fatalf("driver kind = %q, want amount", got.SeverityDriver.Kind)
		}
		if got.SeverityBand != gomodels.SeverityBandHigh {
			t.Errorf("band = %q, want high off the amount", got.SeverityBand)
		}
	})

	t.Run("the document is in another currency", func(t *testing.T) {
		dollarAmount := pair(fig("a1", 9_000_000, gomodels.UnitCurrency, "$", gomodels.FigureHolds))
		got := withFigures(t, "$", cfg, dollarAmount, smallShare)
		if got.SeverityDriver.Kind != gomodels.SeverityDriverShare {
			t.Errorf("driver kind = %q, want share — a ₺ threshold must not band a $ amount", got.SeverityDriver.Kind)
		}
		if got.SeverityBand != gomodels.SeverityBandLow {
			t.Errorf("band = %q, want low off the 1.2%% share", got.SeverityBand)
		}
		// And the amount is still SHOWN. It is grounded and it is the number a
		// reader cares about; what is missing is a threshold written for it.
		if got.SeverityDriver.Amount == nil {
			t.Error("the amount was dropped from the driver")
		}
	})

	t.Run("thresholds with no currency apply to whatever the document is in", func(t *testing.T) {
		bare := DefaultConfig()
		bare.Severity.AmountBands = []MagnitudeBand{{At: 2_000_000, Band: gomodels.SeverityBandHigh}}
		got := withFigures(t, "$", bare, pair(fig("a1", 9_000_000, gomodels.UnitCurrency, "$", gomodels.FigureHolds)))
		if got.SeverityDriver.Kind != gomodels.SeverityDriverAmount {
			t.Errorf("driver kind = %q, want amount", got.SeverityDriver.Kind)
		}
	})
}

// TestAnAmountBandedDocumentRecordsTheShareItBeat.
//
// The rule both driver slots follow: the figure that banded the magnitude,
// plus every candidate it beat. Without this the settings preview could not
// re-band a document whose thresholds were later lowered or removed, because
// the share it would fall back to would be gone.
func TestAnAmountBandedDocumentRecordsTheShareItBeat(t *testing.T) {
	cfg := configWithAmountBands()
	amount := pair(fig("a1", 5_000_000, gomodels.UnitCurrency, "$", gomodels.FigureHolds))
	share := pair(fig("f1", 7.5, gomodels.UnitPercent, "", gomodels.FigureHolds))

	got := withFigures(t, "$", cfg, amount, share)

	if got.SeverityDriver.Kind != gomodels.SeverityDriverAmount {
		t.Fatalf("driver kind = %q, want amount", got.SeverityDriver.Kind)
	}
	if got.SeverityDriver.Share == nil {
		t.Fatal("the share the amount beat was not recorded")
	}
	if got.SeverityDriver.Share.Value != 7.5 {
		t.Errorf("recorded share = %v, want 7.5", got.SeverityDriver.Share.Value)
	}
}

// TestAShareBandedDocumentRecordsNoBeatenShare — the other half of the same
// rule, and the reason the golden corpus does not move: a share cannot beat
// itself, so every document an unconfigured deployment produces carries this
// field absent.
func TestAShareBandedDocumentRecordsNoBeatenShare(t *testing.T) {
	got := withFigures(t, "$", DefaultConfig(),
		pair(fig("a1", 5_000_000, gomodels.UnitCurrency, "$", gomodels.FigureHolds)),
		pair(fig("f1", 7.5, gomodels.UnitPercent, "", gomodels.FigureHolds)))

	if got.SeverityDriver.Kind != gomodels.SeverityDriverShare {
		t.Fatalf("driver kind = %q, want share", got.SeverityDriver.Kind)
	}
	if got.SeverityDriver.Share != nil {
		t.Errorf("Share = %+v, want nil on a share-banded document", got.SeverityDriver.Share)
	}
}

// TestTheOverrideCanTurnASmallFindingIntoACriticalOne is the commercial claim,
// end to end through the fold: a healthcare deployment weights compliance, and
// a small compliance finding outranks a middling engagement one.
func TestTheOverrideCanTurnASmallFindingIntoACriticalOne(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Severity = ApplyOverride(cfg.Severity, gomodels.ScoringOverride{
		CategoryWeights: map[string]float64{"compliance": 3.0},
	})

	smallCompliance := Score(Input{
		Verdict: valmodels.StatusConfirmed, AreaID: "pii_access",
		AreaCategories: []string{"compliance"},
		Figures:        []gomodels.Figure{{ID: "f1", Value: 1.2, Unit: gomodels.UnitPercent}},
		FigureVerdicts: []gomodels.FigureVerdict{{ID: "f1", Display: "1.20%", Status: gomodels.FigureHolds, Claimed: 1.2, Evaluated: 1.2}},
		Config:         cfg,
	})
	middlingEngagement := Score(Input{
		Verdict: valmodels.StatusConfirmed, AreaID: "engagement",
		Figures:        []gomodels.Figure{{ID: "f1", Value: 7.5, Unit: gomodels.UnitPercent}},
		FigureVerdicts: []gomodels.FigureVerdict{{ID: "f1", Display: "7.50%", Status: gomodels.FigureHolds, Claimed: 7.5, Evaluated: 7.5}},
		Config:         cfg,
	})

	if smallCompliance.SeverityBand != gomodels.SeverityBandHigh {
		t.Errorf("the compliance finding banded %q, want high (0.25 × 3 = 0.75)", smallCompliance.SeverityBand)
	}
	if middlingEngagement.SeverityBand != gomodels.SeverityBandMedium {
		t.Errorf("the engagement finding banded %q, want medium", middlingEngagement.SeverityBand)
	}
	if smallCompliance.PriorityScore <= middlingEngagement.PriorityScore {
		t.Errorf("priority %v did not outrank %v — the ordering is the whole claim",
			smallCompliance.PriorityScore, middlingEngagement.PriorityScore)
	}
	// And the breakdown says WHICH category did it, because a reader asked to
	// trust a reordering has to be able to see the reason for it.
	if got := smallCompliance.WeightSource; got == nil || got.Category != "compliance" {
		t.Errorf("weight source = %+v, want the compliance category", got)
	}
}
