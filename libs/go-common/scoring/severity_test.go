package scoring

import (
	"strconv"
	"strings"
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

// fig builds a figure and the verdict that settles it, which is the pair the
// severity axis reads: the figure says what the number is and in what
// notation, the verdict says whether Go's own evaluation reproduced it.
// Neither struct carries both, and joining them is the thing most likely to be
// got wrong, so every case here goes through one helper.
func fig(id string, value float64, unit, currency, status string) (gomodels.Figure, gomodels.FigureVerdict) {
	return gomodels.Figure{ID: id, Value: value, Unit: unit, Currency: currency},
		gomodels.FigureVerdict{ID: id, Display: id + "-display", Status: status}
}

// figurePair keeps a figure with the verdict that settles it, so a case cannot
// declare one without the other.
type figurePair struct {
	figure  gomodels.Figure
	verdict gomodels.FigureVerdict
}

func pair(f gomodels.Figure, v gomodels.FigureVerdict) figurePair {
	return figurePair{figure: f, verdict: v}
}

// withFigures scores an insight carrying the given figure/verdict pairs, with
// a confirmed verdict so the confidence axis is out of the way.
func withFigures(t *testing.T, homeCurrency string, cfg Config, pairs ...figurePair) gomodels.Scoring {
	t.Helper()
	in := Input{Verdict: valmodels.StatusConfirmed, HomeCurrency: homeCurrency, Config: cfg}
	for _, p := range pairs {
		in.Figures = append(in.Figures, p.figure)
		in.FigureVerdicts = append(in.FigureVerdicts, p.verdict)
	}
	return Score(in)
}

// configWithAmountBands is the shipped calibration plus the amount bands no
// deployment configures yet.
//
// Every amount-banded case in this file runs against it, which is the only way
// that code path is exercised at all: the shipped config bands nothing by
// amount on purpose, so a test using the default would assert the fallback and
// silently cover nothing.
func configWithAmountBands() Config {
	cfg := DefaultConfig()
	cfg.Severity.AmountBands = []MagnitudeBand{
		{At: 1_000_000, Band: gomodels.SeverityBandHigh},
		{At: 100_000, Band: gomodels.SeverityBandMedium},
	}
	return cfg
}

// --- The magnitude ladder --------------------------------------------------

// TestAnAmountBandsTheMagnitudeOnlyWhenAmountBandsAreConfigured is the
// shipped-default behaviour and the single most surprising thing about this
// axis, so it is asserted in both directions.
//
// An absolute money cutoff is a property of a business, not of the engine, so
// the shipped calibration declares none. A deployment that has not configured
// any therefore bands on the share -- and still shows the amount, because the
// amount is the number a reader cares about and it IS grounded; what is
// missing is a threshold to rank it against.
func TestAnAmountBandsTheMagnitudeOnlyWhenAmountBandsAreConfigured(t *testing.T) {
	amount := pair(fig("f1", 2_400_000, gomodels.UnitCurrency, "$", gomodels.FigureHolds))
	share := pair(fig("f2", 4, gomodels.UnitPercent, "", gomodels.FigureHolds))

	t.Run("unconfigured: banded on share, amount still shown", func(t *testing.T) {
		got := withFigures(t, "$", DefaultConfig(), amount, share)
		if got.SeverityDriver.Kind != gomodels.SeverityDriverShare {
			t.Errorf("driver kind = %q, want share", got.SeverityDriver.Kind)
		}
		if got.SeverityDriver.Figure == nil || got.SeverityDriver.Figure.FigureID != "f2" {
			t.Errorf("the band should come from the share figure, got %+v", got.SeverityDriver.Figure)
		}
		if got.SeverityDriver.Amount == nil || got.SeverityDriver.Amount.FigureID != "f1" {
			t.Fatalf("the grounded amount is not shown: %+v", got.SeverityDriver.Amount)
		}
		if !got.SeverityDriver.Quantified {
			t.Error("Quantified = false, but the magnitude came from a checked share")
		}
		// 4% is below the 5% medium cutoff, so this is the low band -- which
		// is the point of the case: a $2.4M finding is not high severity just
		// because the number is large, until someone says what large is.
		if got.SeverityBand != gomodels.SeverityBandLow {
			t.Errorf("band = %q, want low (4%% share)", got.SeverityBand)
		}
	})

	t.Run("configured: the amount takes precedence", func(t *testing.T) {
		got := withFigures(t, "$", configWithAmountBands(), amount, share)
		if got.SeverityDriver.Kind != gomodels.SeverityDriverAmount {
			t.Errorf("driver kind = %q, want amount", got.SeverityDriver.Kind)
		}
		if got.SeverityDriver.Figure == nil || got.SeverityDriver.Figure.FigureID != "f1" {
			t.Errorf("the band should come from the amount, got %+v", got.SeverityDriver.Figure)
		}
		if got.SeverityDriver.Amount == nil || got.SeverityDriver.Amount.FigureID != "f1" {
			t.Error("Figure and Amount should name the same figure once amount bands apply")
		}
		if got.SeverityBand != gomodels.SeverityBandHigh {
			t.Errorf("band = %q, want high ($2.4M over the $1M cutoff)", got.SeverityBand)
		}
	})
}

// TestOnlyAHoldingFigureCanDriveTheMagnitude is the whole claim this axis
// makes. A figure the warehouse refuted, one the evaluator could not settle,
// and one with no verdict at all are the same thing here: a number that was
// not checked.
func TestOnlyAHoldingFigureCanDriveTheMagnitude(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
	}{
		{"refuted", gomodels.FigureFails},
		{"undecidable", gomodels.FigureUndecidable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := withFigures(t, "$", DefaultConfig(),
				pair(fig("f1", 40, gomodels.UnitPercent, "", tc.status)))
			if got.SeverityDriver.Kind != gomodels.SeverityDriverBaseline {
				t.Errorf("a %s figure drove the magnitude (kind=%q)", tc.name, got.SeverityDriver.Kind)
			}
		})
	}

	t.Run("no verdict at all", func(t *testing.T) {
		// A figure present with no matching verdict row: nothing evaluated it,
		// which is not the same as it having passed. The join has to miss.
		got := Score(Input{
			Verdict: valmodels.StatusConfirmed,
			Figures: []gomodels.Figure{{ID: "f1", Value: 40, Unit: gomodels.UnitPercent}},
			Config:  DefaultConfig(),
		})
		if got.SeverityDriver.Kind != gomodels.SeverityDriverBaseline {
			t.Errorf("an unverdicted figure drove the magnitude (kind=%q)", got.SeverityDriver.Kind)
		}
	})
}

// TestTheHomeCurrencyRuleExcludesAForeignAmount is the rule that keeps an
// amount read from one datasource from being banded against another's
// thresholds.
//
// A figure that STATES a currency is the one case the ingest-time settlement
// leaves alone, because on a multi-currency warehouse that statement is the
// only thing distinguishing a foreign amount from a local one. So comparing it
// here is comparing the model's own claim about which currency the row was in,
// which is exactly the claim that must not set a band.
func TestTheHomeCurrencyRuleExcludesAForeignAmount(t *testing.T) {
	cfg := configWithAmountBands()
	foreign := pair(fig("f1", 5_000_000, gomodels.UnitCurrency, "€", gomodels.FigureHolds))
	share := pair(fig("f2", 30, gomodels.UnitPercent, "", gomodels.FigureHolds))

	got := withFigures(t, "$", cfg, foreign, share)
	if got.SeverityDriver.Kind != gomodels.SeverityDriverShare {
		t.Errorf("a euro amount banded a dollar-denominated document (kind=%q)", got.SeverityDriver.Kind)
	}
	if got.SeverityDriver.Amount != nil {
		t.Errorf("a foreign amount was shown as the document's amount: %+v", got.SeverityDriver.Amount)
	}

	t.Run("no home currency excludes every amount", func(t *testing.T) {
		// Two datasources reporting in different currencies, so the caller
		// could not determine one. Refusing to guess is the behaviour.
		got := withFigures(t, "", cfg,
			pair(fig("f1", 5_000_000, gomodels.UnitCurrency, "$", gomodels.FigureHolds)), share)
		if got.SeverityDriver.Kind != gomodels.SeverityDriverShare {
			t.Errorf("an amount banded a document with no home currency (kind=%q)", got.SeverityDriver.Kind)
		}
		if got.SeverityDriver.Amount != nil {
			t.Errorf("an amount was shown with no home currency to read it in: %+v", got.SeverityDriver.Amount)
		}
	})
}

// TestTheBaselineIsNeverAutoLow is acceptance, stated as a test.
//
// A finding with no checked figure is UNMEASURED, not small. Banding it low
// would hide exactly the finding whose numbers a model failed to declare,
// which is the opposite of what grounding severity is for.
func TestTheBaselineIsNeverAutoLow(t *testing.T) {
	got := Score(Input{Verdict: valmodels.StatusConfirmed, Config: DefaultConfig()})

	if got.SeverityDriver.Kind != gomodels.SeverityDriverBaseline {
		t.Fatalf("driver kind = %q, want baseline", got.SeverityDriver.Kind)
	}
	if got.SeverityDriver.Quantified {
		t.Error("Quantified = true on a document with no figure")
	}
	if got.SeverityDriver.Figure != nil {
		t.Errorf("a baseline driver named a figure: %+v", got.SeverityDriver.Figure)
	}
	if got.SeverityBand == gomodels.SeverityBandLow {
		t.Error("an unquantified finding was banded low; the baseline exists to prevent exactly this")
	}
	if got.MagnitudeScore != DefaultConfig().Severity.BaselineMagnitude {
		t.Errorf("magnitude = %v, want the baseline %v", got.MagnitudeScore, DefaultConfig().Severity.BaselineMagnitude)
	}
	if got.SeverityReason == "" {
		t.Error("a band with no reason beside it is the one shape this layer must not produce")
	}
}

// TestAffectedCountCannotDriveSeverity is the named regression guard for the
// defect this epic exists to remove.
//
// `affected_count` is a model-authored integer and there is no population
// denominator anywhere in a discovery result to turn it into a share. The
// field is deliberately absent from Input, so this test is really asserting
// that nothing has been added to put it back -- an enormous one changes
// nothing about the band.
func TestAffectedCountCannotDriveSeverity(t *testing.T) {
	bare := Score(Input{Verdict: valmodels.StatusConfirmed, Config: DefaultConfig()})
	// The only way an affected count could reach this engine is as a figure,
	// and a count is not a currency or a percentage, so it bands nothing.
	count, countVerdict := fig("f1", 4_000_000, "count", "", gomodels.FigureHolds)
	withCount := Score(Input{
		Verdict:        valmodels.StatusConfirmed,
		Figures:        []gomodels.Figure{count},
		FigureVerdicts: []gomodels.FigureVerdict{countVerdict},
		Config:         DefaultConfig(),
	})
	if withCount.SeverityBand != bare.SeverityBand || withCount.MagnitudeScore != bare.MagnitudeScore {
		t.Errorf("a four-million row count moved severity: %q/%v vs %q/%v",
			withCount.SeverityBand, withCount.MagnitudeScore, bare.SeverityBand, bare.MagnitudeScore)
	}
	if withCount.SeverityDriver.Kind != gomodels.SeverityDriverBaseline {
		t.Errorf("a count drove the magnitude (kind=%q)", withCount.SeverityDriver.Kind)
	}
}

// TestTheLargestFigureWinsByAbsoluteValue -- a decline is as severe as a rise.
//
// Ranking by the signed value would rank every fall last, and `excess` and
// `diff` figures are negative whenever the thing being measured went down,
// which is most of what a discovery run finds.
func TestTheLargestFigureWinsByAbsoluteValue(t *testing.T) {
	got := withFigures(t, "$", DefaultConfig(),
		pair(fig("small", 6, gomodels.UnitPercent, "", gomodels.FigureHolds)),
		pair(fig("big-negative", -34, gomodels.UnitPercent, "", gomodels.FigureHolds)))

	if got.SeverityDriver.Figure.FigureID != "big-negative" {
		t.Errorf("driver = %q, want the 34%% fall", got.SeverityDriver.Figure.FigureID)
	}
	if got.SeverityBand != gomodels.SeverityBandHigh {
		t.Errorf("band = %q, want high (|-34%%| over the 20%% cutoff)", got.SeverityBand)
	}
}

// TestTiedFiguresPickDeterministically -- the driver has to be stable, because
// it is stored and because re-validating a document must not change which
// figure it says drove its severity.
func TestTiedFiguresPickDeterministically(t *testing.T) {
	for i := 0; i < 8; i++ {
		got := withFigures(t, "$", DefaultConfig(),
			pair(fig("first", 25, gomodels.UnitPercent, "", gomodels.FigureHolds)),
			pair(fig("second", -25, gomodels.UnitPercent, "", gomodels.FigureHolds)))
		if got.SeverityDriver.Figure.FigureID != "first" {
			t.Fatalf("run %d picked %q; ties must keep the first-declared figure",
				i, got.SeverityDriver.Figure.FigureID)
		}
	}
}

// TestAShareAboveOneHundredPercentBandsHigh -- a quotient declared as a
// percentage is a real shape (a 109.59% ratio), and the band lookup has to
// treat a value above the top cutoff as the top band rather than falling
// through it.
func TestAShareAboveOneHundredPercentBandsHigh(t *testing.T) {
	got := withFigures(t, "$", DefaultConfig(),
		pair(fig("f1", 109.59, gomodels.UnitPercent, "", gomodels.FigureHolds)))
	if got.SeverityBand != gomodels.SeverityBandHigh {
		t.Errorf("band = %q, want high", got.SeverityBand)
	}
}

// TestTheDriverQuotesTheRenderedFigure -- the number beside the band has to be
// character-for-character the number in the sentence, so Display comes off the
// verdict rather than being re-rendered here.
func TestTheDriverQuotesTheRenderedFigure(t *testing.T) {
	f, v := fig("f1", 24.66, gomodels.UnitPercent, "", gomodels.FigureHolds)
	v.Display = "24.66%"
	got := withFigures(t, "$", DefaultConfig(), pair(f, v))

	d := got.SeverityDriver.Figure
	if d.Display != "24.66%" {
		t.Errorf("Display = %q, want the verdict's own rendition", d.Display)
	}
	if d.Verdict != gomodels.FigureHolds {
		t.Errorf("Verdict = %q, want %q", d.Verdict, gomodels.FigureHolds)
	}
	if d.Unit != gomodels.UnitPercent || d.Value != 24.66 {
		t.Errorf("driver lost the figure's own value/unit: %+v", d)
	}
	if !strings.Contains(got.SeverityReason, "24.66%") {
		t.Errorf("the stored reason does not quote the driver: %q", got.SeverityReason)
	}
}

// --- Score, band and weight ------------------------------------------------

// TestTheSeverityScoreIsClamped -- risk_score carries this number and its
// [0, 1] contract is what every reader that prints it as a percentage relies
// on. A weight above 1 on a top-band magnitude is the case that escapes it.
func TestTheSeverityScoreIsClamped(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Severity.DefaultWeight = 3.0
	got := withFigures(t, "$", cfg,
		pair(fig("f1", 40, gomodels.UnitPercent, "", gomodels.FigureHolds)))

	if got.SeverityScore != 1.0 {
		t.Errorf("score = %v, want 1.0 (1.00 magnitude x 3.0 weight, clamped)", got.SeverityScore)
	}
	if got.MagnitudeScore != 1.0 {
		t.Errorf("magnitude = %v, want the unclamped weight-free 1.0", got.MagnitudeScore)
	}
}

// TestAZeroWeightCannotEraseAFinding -- a calibration with an unset default
// weight would otherwise multiply every magnitude to nothing and band every
// finding low, silently and in the direction that hides findings.
func TestAZeroWeightCannotEraseAFinding(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Severity.DefaultWeight = 0
	got := withFigures(t, "$", cfg,
		pair(fig("f1", 40, gomodels.UnitPercent, "", gomodels.FigureHolds)))

	if got.AreaWeight != 1.0 {
		t.Errorf("weight = %v, want the 1.0 fallback", got.AreaWeight)
	}
	if got.SeverityBand != gomodels.SeverityBandHigh {
		t.Errorf("band = %q, want high", got.SeverityBand)
	}
}

// TestTheWeightAndItsSourceAreBothWritten -- the breakdown renders which
// weight multiplied the magnitude and what set it, so an absent source beside
// a present weight would leave a reader unable to tell a deliberate 1.0 from
// an unwritten field.
func TestTheWeightAndItsSourceAreBothWritten(t *testing.T) {
	got := Score(Input{Verdict: valmodels.StatusConfirmed, AreaID: "retention", Config: DefaultConfig()})
	if got.AreaWeight != 1.0 {
		t.Errorf("AreaWeight = %v, want 1.0", got.AreaWeight)
	}
	if got.WeightSource == nil || got.WeightSource.Kind != gomodels.WeightSourceDefault {
		t.Errorf("WeightSource = %+v, want kind %q", got.WeightSource, gomodels.WeightSourceDefault)
	}
}

// TestTheSeverityCutoffsAreInclusive, matching the confidence axis -- a score
// at exactly the cutoff is in the higher band.
func TestTheSeverityCutoffsAreInclusive(t *testing.T) {
	cfg := DefaultConfig()
	for _, tc := range []struct {
		weight float64
		want   string
	}{
		// 1.00 magnitude x weight, so the weight IS the score.
		{cfg.Severity.HighAt, gomodels.SeverityBandHigh},
		{cfg.Severity.HighAt - 0.01, gomodels.SeverityBandMedium},
		{cfg.Severity.MediumAt, gomodels.SeverityBandMedium},
		{cfg.Severity.MediumAt - 0.01, gomodels.SeverityBandLow},
	} {
		c := DefaultConfig()
		c.Severity.DefaultWeight = tc.weight
		got := withFigures(t, "$", c,
			pair(fig("f1", 40, gomodels.UnitPercent, "", gomodels.FigureHolds)))
		if got.SeverityBand != tc.want {
			t.Errorf("score %v banded %q, want %q", got.SeverityScore, got.SeverityBand, tc.want)
		}
	}
}

// TestEveryReachableSeverityScoreIsExactAtTwoDecimals is the severity twin of
// the confidence guard: the score is persisted and served verbatim, so one
// that round-trips as 0.6000000000000001 is a 17-digit float in a payload.
func TestEveryReachableSeverityScoreIsExactAtTwoDecimals(t *testing.T) {
	exact := func(v float64) bool {
		return strconv.FormatFloat(v, 'f', -1, 64) == strconv.FormatFloat(v, 'f', 2, 64) ||
			strconv.FormatFloat(v, 'f', -1, 64) == strings.TrimRight(strings.TrimRight(strconv.FormatFloat(v, 'f', 2, 64), "0"), ".")
	}
	checked := 0
	for _, weight := range []float64{0.5, 1.0, 1.5, 2.0, 2.5, 3.0} {
		for _, share := range []float64{0, 1, 4.99, 5, 19.99, 20, 100} {
			cfg := configWithAmountBands()
			cfg.Severity.DefaultWeight = weight
			for _, amount := range []float64{0, 99_999, 100_000, 1_000_000, 2_400_000} {
				got := withFigures(t, "$", cfg,
					pair(fig("f1", amount, gomodels.UnitCurrency, "$", gomodels.FigureHolds)),
					pair(fig("f2", share, gomodels.UnitPercent, "", gomodels.FigureHolds)))
				if !exact(got.SeverityScore) {
					t.Errorf("weight=%v share=%v amount=%v: severity score serialises as %s",
						weight, share, amount, strconv.FormatFloat(got.SeverityScore, 'f', -1, 64))
				}
				if !exact(got.MagnitudeScore) {
					t.Errorf("weight=%v share=%v amount=%v: magnitude serialises as %s",
						weight, share, amount, strconv.FormatFloat(got.MagnitudeScore, 'f', -1, 64))
				}
				checked++
			}
		}
	}
	if checked != 6*7*5 {
		t.Fatalf("the generator covered %d cases, not the matrix it declares", checked)
	}
}

// --- Inheritance -----------------------------------------------------------

// TestInheritSeverityTakesTheMax -- a recommendation that addresses one severe
// finding and two mild ones is as important as the severe one. Averaging would
// rank it below a recommendation addressing a single medium finding, which
// inverts what severity is for.
func TestInheritSeverityTakesTheMax(t *testing.T) {
	got, ok := InheritSeverity([]InheritedSeverity{
		{SeverityBand: gomodels.SeverityBandLow, SeverityScore: 0.25, MagnitudeScore: 0.25},
		{SeverityBand: gomodels.SeverityBandHigh, SeverityScore: 1.0, MagnitudeScore: 1.0,
			SeverityReason: "High — driven by 34%"},
		{SeverityBand: gomodels.SeverityBandMedium, SeverityScore: 0.6, MagnitudeScore: 0.6},
	})
	if !ok {
		t.Fatal("inheritance found nothing to inherit from three scored insights")
	}
	if got.SeverityBand != gomodels.SeverityBandHigh || got.SeverityScore != 1.0 {
		t.Errorf("inherited %q/%v, want high/1.0", got.SeverityBand, got.SeverityScore)
	}
	if got.SeverityReason != "High — driven by 34%" {
		t.Errorf("the reason did not travel with the band: %q", got.SeverityReason)
	}
}

// TestInheritSeverityIsStableOnTies -- the inherited driver is stored, so the
// pick has to be the first in citation order rather than whichever the loop
// reached last.
func TestInheritSeverityIsStableOnTies(t *testing.T) {
	first := &gomodels.SeverityDriver{Kind: gomodels.SeverityDriverShare,
		Figure: &gomodels.SeverityFigure{FigureID: "first"}}
	second := &gomodels.SeverityDriver{Kind: gomodels.SeverityDriverShare,
		Figure: &gomodels.SeverityFigure{FigureID: "second"}}
	got, _ := InheritSeverity([]InheritedSeverity{
		{SeverityBand: gomodels.SeverityBandHigh, SeverityScore: 0.8, SeverityDriver: first},
		{SeverityBand: gomodels.SeverityBandHigh, SeverityScore: 0.8, SeverityDriver: second},
	})
	if got.SeverityDriver.Figure.FigureID != "first" {
		t.Errorf("inherited %q, want the first-cited insight's driver",
			got.SeverityDriver.Figure.FigureID)
	}
}

// TestInheritSeveritySkipsUnscoredInsights -- a recommendation citing a legacy
// insight and a scored one inherits from the scored one; one citing only
// legacy insights inherits nothing rather than a zero band.
func TestInheritSeveritySkipsUnscoredInsights(t *testing.T) {
	got, ok := InheritSeverity([]InheritedSeverity{
		{}, // legacy: no band
		{SeverityBand: gomodels.SeverityBandMedium, SeverityScore: 0.6},
	})
	if !ok || got.SeverityBand != gomodels.SeverityBandMedium {
		t.Errorf("got %q/%v ok=%v, want medium", got.SeverityBand, got.SeverityScore, ok)
	}

	if _, ok := InheritSeverity([]InheritedSeverity{{}, {}}); ok {
		t.Error("inheritance invented a band from two unscored insights")
	}
	if _, ok := InheritSeverity(nil); ok {
		t.Error("inheritance invented a band from no insights at all")
	}
}

// TestARecommendationNeverMeasuresItsOwnFigures is the double-counting guard.
//
// A recommendation's figures are `ref` and `sum` over figures its insights
// already declared, each of which Go checks and stamps `holds`. The insight
// path would happily band a magnitude off a number it had already banded
// once, and report the result as independent evidence.
func TestARecommendationNeverMeasuresItsOwnFigures(t *testing.T) {
	in := Input{
		Verdict: valmodels.StatusConfirmed,
		Figures: []gomodels.Figure{{ID: "r1", Value: 40, Unit: gomodels.UnitPercent}},
		FigureVerdicts: []gomodels.FigureVerdict{
			{ID: "r1", Display: "40%", Status: gomodels.FigureHolds},
		},
		HomeCurrency: "$",
		Config:       DefaultConfig(),
	}

	// Nothing cited: no severity half at all, even though its own figure
	// holds and would have banded high on the insight path.
	bare := ScoreRecommendation(in, nil)
	if bare.SeverityBand != "" || bare.SeverityScore != 0 || bare.SeverityDriver != nil {
		t.Errorf("a recommendation measured its own figures: %q/%v/%+v",
			bare.SeverityBand, bare.SeverityScore, bare.SeverityDriver)
	}
	if bare.PriorityTier != "" {
		t.Errorf("priority tier = %q on a recommendation with no inherited severity", bare.PriorityTier)
	}
	// The confidence axis is still its own and still derived.
	if bare.ConfidenceBand != gomodels.ConfidenceBandHigh {
		t.Errorf("confidence band = %q, want high from its own confirmed verdict", bare.ConfidenceBand)
	}

	// Citing a low-severity insight yields LOW, not the high its own 40%
	// figure would have produced.
	cited := ScoreRecommendation(in, []InheritedSeverity{
		{SeverityBand: gomodels.SeverityBandLow, SeverityScore: 0.25, MagnitudeScore: 0.25,
			AreaWeight: 1.0, SeverityReason: "Low — driven by 2%"},
	})
	if cited.SeverityBand != gomodels.SeverityBandLow {
		t.Errorf("inherited band = %q, want low", cited.SeverityBand)
	}
	if cited.SeverityReason != "Low — driven by 2%" {
		t.Errorf("reason = %q, want the cited insight's own", cited.SeverityReason)
	}
}
