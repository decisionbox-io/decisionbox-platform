package scoring

import (
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// epsilon is the tolerance for comparing two scores. Every number in the
// engine is a multiple of 0.01, so anything larger than float noise is a real
// disagreement.
const epsilon = 1e-9

func scored(in Input) gomodels.Scoring {
	if in.Config.Version == 0 {
		in.Config = DefaultConfig()
	}
	return Score(in)
}

func caveat(kind, detail string) gowarehouse.QualityCaveat {
	return gowarehouse.QualityCaveat{Kind: gowarehouse.QualityKind(kind), Detail: detail}
}

func figureVerdict(status string) gomodels.FigureVerdict {
	return gomodels.FigureVerdict{ID: "f1", Display: "$1.00", Status: status}
}

func quantifierVerdict(status string) gomodels.QuantifierVerdict {
	return gomodels.QuantifierVerdict{Claim: "the only one", Kind: "unique", Status: status}
}

func dockAmount(s gomodels.Scoring, code string) (float64, bool) {
	total, found := 0.0, false
	for _, d := range s.ConfidenceDocks {
		if d.Code == code {
			total += d.Amount
			found = true
		}
	}
	return total, found
}

// TestBaseAndBandForEveryVerdict walks the taxonomy exhaustively, plus the
// three values a stored document can carry that the taxonomy does not define:
// nothing at all, the two legacy statuses, and a string written by a newer
// agent. Every one has to land somewhere deliberate.
func TestBaseAndBandForEveryVerdict(t *testing.T) {
	for _, tc := range []struct {
		verdict valmodels.Status
		base    float64
		band    string
	}{
		{valmodels.StatusConfirmed, 0.95, gomodels.ConfidenceBandHigh},
		{valmodels.StatusSupported, 0.80, gomodels.ConfidenceBandHigh},
		{valmodels.StatusPartial, 0.55, gomodels.ConfidenceBandMedium},
		{valmodels.StatusRejected, 0.05, gomodels.ConfidenceBandLow},
		{valmodels.StatusUnverifiable, 0.20, gomodels.ConfidenceBandUnverifiable},
		{valmodels.StatusValidationDisabled, 0.25, gomodels.ConfidenceBandNotConfirmed},
		{valmodels.StatusSkippedBudgetCap, 0.25, gomodels.ConfidenceBandNotConfirmed},
		{"", 0.25, gomodels.ConfidenceBandNotConfirmed},
		// The two legacy statuses older documents still carry.
		{"unverified", 0.25, gomodels.ConfidenceBandNotConfirmed},
		{"error", 0.25, gomodels.ConfidenceBandNotConfirmed},
		// A value this build has never heard of.
		{"triple_confirmed_by_vibes", 0.25, gomodels.ConfidenceBandNotConfirmed},
	} {
		t.Run(string(tc.verdict), func(t *testing.T) {
			got := scored(Input{Verdict: tc.verdict})
			if math.Abs(got.ConfidenceBase-tc.base) > epsilon {
				t.Errorf("base = %v, want %v", got.ConfidenceBase, tc.base)
			}
			if math.Abs(got.ConfidenceScore-tc.base) > epsilon {
				t.Errorf("undocked score = %v, want the base %v", got.ConfidenceScore, tc.base)
			}
			if got.ConfidenceBand != tc.band {
				t.Errorf("band = %q, want %q", got.ConfidenceBand, tc.band)
			}
			if got.ConfidenceReason == "" {
				t.Error("no reason was stored; the dashboard renders the engine's words, not its own")
			}
			if len(got.ConfidenceDocks) != 0 {
				t.Errorf("an undocked document carries docks: %+v", got.ConfidenceDocks)
			}
		})
	}
}

// TestEveryKnownStatusHasItsOwnReason stops a status falling through to the
// generic "not checked" wording when the taxonomy says something more
// specific. A status added upstream fails here until someone writes its
// sentence.
func TestEveryKnownStatusHasItsOwnReason(t *testing.T) {
	all := []valmodels.Status{
		valmodels.StatusConfirmed, valmodels.StatusSupported, valmodels.StatusRejected,
		valmodels.StatusPartial, valmodels.StatusUnverifiable,
		valmodels.StatusValidationDisabled, valmodels.StatusSkippedBudgetCap,
	}
	seen := map[string]valmodels.Status{}
	for _, s := range all {
		if !s.IsKnown() {
			t.Fatalf("%q is not IsKnown; the taxonomy moved under this test", s)
		}
		reason := scored(Input{Verdict: s}).ConfidenceReason
		if reason == unscoredReason {
			t.Errorf("%q fell through to the unscored wording", s)
		}
		if prior, dup := seen[reason]; dup {
			t.Errorf("%q and %q share one reason: %q", prior, s, reason)
		}
		seen[reason] = s
	}
}

// TestNoDocumentIsEverStoredWithoutAReason — a band with no explanation beside
// it is the one shape this layer must not produce. Every verdict the shipped
// calibration prices has a sentence of its own, and a calibration that priced
// one without wording it still gets a sentence rather than an empty field.
func TestNoDocumentIsEverStoredWithoutAReason(t *testing.T) {
	cfg := DefaultConfig()
	for verdict := range cfg.Confidence.Bases {
		if got := reasons[verdict]; got == "" {
			t.Errorf("the calibration prices %q and the engine has no wording for it", verdict)
		}
	}

	custom := DefaultConfig()
	custom.Confidence.Bases["invented_by_a_later_version"] = 0.6
	got := Score(Input{Verdict: "invented_by_a_later_version", Config: custom})
	if got.ConfidenceReason == "" {
		t.Error("a priced-but-unworded verdict stored an empty reason")
	}
	if got.ConfidenceBase != 0.6 {
		t.Errorf("base = %v, want the calibration's 0.6", got.ConfidenceBase)
	}
	// And such a verdict is NOT treated as checked: the band vocabulary is
	// this build's, so an unrecognised status is never ranked by cutoff.
	if got.ConfidenceBand != gomodels.ConfidenceBandNotConfirmed {
		t.Errorf("band = %q, want not_confirmed for a status this build does not know", got.ConfidenceBand)
	}
}

// TestEachDockInIsolation fires one signal at a time against a base high
// enough that nothing is capped or floored, so the amount, code and label are
// the configured ones.
func TestEachDockInIsolation(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Input
		code string
		amt  float64
	}{
		{"degraded evidence", Input{QualityCaveats: []gowarehouse.QualityCaveat{caveat("withheld", "37 of 412 rows withheld")}}, DockDegradedEvidence, 0.10},
		{"refuted arithmetic", Input{FigureVerdicts: []gomodels.FigureVerdict{figureVerdict(gomodels.FigureFails)}}, DockRefutedArithmetic, 0.10},
		{"refuted quantifier", Input{QuantifierVerdicts: []gomodels.QuantifierVerdict{quantifierVerdict(gomodels.QuantifierFails)}}, DockRefutedQuantifier, 0.10},
		{"unresolved figure", Input{UnresolvedFigures: 1}, DockUnresolvedFigure, 0.05},
		{"sample ended early", Input{StepReadBeyondRetained: true}, DockSampleEndedEarly, 0.05},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			in.Verdict = valmodels.StatusConfirmed
			got := scored(in)
			if len(got.ConfidenceDocks) != 1 {
				t.Fatalf("docks = %+v, want exactly one", got.ConfidenceDocks)
			}
			dock := got.ConfidenceDocks[0]
			if dock.Code != tc.code {
				t.Errorf("code = %q, want %q", dock.Code, tc.code)
			}
			if math.Abs(dock.Amount-tc.amt) > epsilon {
				t.Errorf("amount = %v, want %v", dock.Amount, tc.amt)
			}
			if dock.Label == "" {
				t.Error("the dock carries no label")
			}
			if math.Abs(got.ConfidenceScore-(0.95-tc.amt)) > epsilon {
				t.Errorf("score = %v, want %v", got.ConfidenceScore, 0.95-tc.amt)
			}
		})
	}
}

// TestAHoldingVerdictTrailDocksNothing is the other half of the dock tests:
// the statuses that are NOT refutations must cost nothing. Undecidable in
// particular — that is the evaluator declining, and charging for it would
// price the checker's own limits.
func TestAHoldingVerdictTrailDocksNothing(t *testing.T) {
	got := scored(Input{
		Verdict: valmodels.StatusSupported,
		FigureVerdicts: []gomodels.FigureVerdict{
			figureVerdict(gomodels.FigureHolds), figureVerdict(gomodels.FigureUndecidable),
		},
		QuantifierVerdicts: []gomodels.QuantifierVerdict{
			quantifierVerdict(gomodels.QuantifierHolds), quantifierVerdict(gomodels.QuantifierUndecidable),
		},
		UnresolvedFigures: 0,
	})
	if len(got.ConfidenceDocks) != 0 {
		t.Errorf("docks = %+v, want none", got.ConfidenceDocks)
	}
	if math.Abs(got.ConfidenceScore-0.80) > epsilon {
		t.Errorf("score = %v, want the undocked base 0.8", got.ConfidenceScore)
	}
}

// TestQualityDocksAreCapped is the §4.5 cap: three distinct caveat kinds
// produce two entries totalling 0.20, not three totalling 0.30.
func TestQualityDocksAreCapped(t *testing.T) {
	got := scored(Input{
		Verdict: valmodels.StatusConfirmed,
		QualityCaveats: []gowarehouse.QualityCaveat{
			caveat("withheld", "a"), caveat("sampled", "b"), caveat("truncated", "c"),
		},
	})
	if len(got.ConfidenceDocks) != 2 {
		t.Fatalf("docks = %+v, want two entries (the cap is two amounts' worth)", got.ConfidenceDocks)
	}
	total, _ := dockAmount(got, DockDegradedEvidence)
	if math.Abs(total-0.20) > epsilon {
		t.Errorf("quality docks total %v, want the 0.2 cap", total)
	}
	if math.Abs(got.ConfidenceScore-0.75) > epsilon {
		t.Errorf("score = %v, want 0.75", got.ConfidenceScore)
	}
}

// TestDistinctKindCountingIgnoresDetail — two caveats of the same kind with
// different details are one fact about one kind of degradation.
func TestDistinctKindCountingIgnoresDetail(t *testing.T) {
	got := scored(Input{
		Verdict: valmodels.StatusConfirmed,
		QualityCaveats: []gowarehouse.QualityCaveat{
			caveat("withheld", "37 of 412 rows withheld"),
			caveat("withheld", "12 of 98 rows withheld"),
		},
	})
	if len(got.ConfidenceDocks) != 1 {
		t.Fatalf("docks = %+v, want one", got.ConfidenceDocks)
	}
	if math.Abs(got.ConfidenceScore-0.85) > epsilon {
		t.Errorf("score = %v, want 0.85", got.ConfidenceScore)
	}
}

// TestAnEmptyCaveatKindDocksNothing — a caveat with no kind carries no fact to
// charge for, and counting it would price a malformed record.
func TestAnEmptyCaveatKindDocksNothing(t *testing.T) {
	got := scored(Input{
		Verdict:        valmodels.StatusConfirmed,
		QualityCaveats: []gowarehouse.QualityCaveat{caveat("", "nothing said")},
	})
	if len(got.ConfidenceDocks) != 0 {
		t.Errorf("docks = %+v, want none", got.ConfidenceDocks)
	}
}

// TestTheTwoRefutationDocksShareACap — arithmetic is charged in full and the
// quantifier entry is the one trimmed, so the serious fact keeps its full
// price.
func TestTheTwoRefutationDocksShareACap(t *testing.T) {
	got := scored(Input{
		Verdict:            valmodels.StatusConfirmed,
		FigureVerdicts:     []gomodels.FigureVerdict{figureVerdict(gomodels.FigureFails)},
		QuantifierVerdicts: []gomodels.QuantifierVerdict{quantifierVerdict(gomodels.QuantifierFails)},
	})
	arith, ok := dockAmount(got, DockRefutedArithmetic)
	if !ok || math.Abs(arith-0.10) > epsilon {
		t.Errorf("arithmetic dock = %v (present=%v), want 0.1 in full", arith, ok)
	}
	quant, ok := dockAmount(got, DockRefutedQuantifier)
	if !ok || math.Abs(quant-0.05) > epsilon {
		t.Errorf("quantifier dock = %v (present=%v), want 0.05 — trimmed to the 0.15 cap", quant, ok)
	}
	if math.Abs(got.ConfidenceScore-0.80) > epsilon {
		t.Errorf("score = %v, want 0.8", got.ConfidenceScore)
	}
}

// TestTheFloorHoldsAndZeroedDocksAreDropped — a rejected document is already
// at the floor, so every dock is charged nothing and none is stored. Showing a
// deduction that changed nothing is a breakdown that does not add up.
func TestTheFloorHoldsAndZeroedDocksAreDropped(t *testing.T) {
	got := scored(Input{
		Verdict: valmodels.StatusRejected,
		QualityCaveats: []gowarehouse.QualityCaveat{
			caveat("withheld", "a"), caveat("sampled", "b"),
		},
		FigureVerdicts:         []gomodels.FigureVerdict{figureVerdict(gomodels.FigureFails)},
		QuantifierVerdicts:     []gomodels.QuantifierVerdict{quantifierVerdict(gomodels.QuantifierFails)},
		UnresolvedFigures:      3,
		StepReadBeyondRetained: true,
	})
	if math.Abs(got.ConfidenceScore-0.05) > epsilon {
		t.Errorf("score = %v, want exactly the floor 0.05", got.ConfidenceScore)
	}
	if len(got.ConfidenceDocks) != 0 {
		t.Errorf("docks = %+v, want none — every one was trimmed to nothing", got.ConfidenceDocks)
	}
	if got.ConfidenceReason != scored(Input{Verdict: valmodels.StatusRejected}).ConfidenceReason {
		t.Error("the reason names deductions that were not taken")
	}
}

// TestAPartialTrimmedByTheFloorStoresWhatWasCharged is the interesting floor
// case: the base is above the floor but the docks would take it below, so one
// dock is stored trimmed and the ones after it are dropped.
func TestAPartialTrimmedByTheFloorStoresWhatWasCharged(t *testing.T) {
	got := scored(Input{
		Verdict: valmodels.StatusUnverifiable,
		QualityCaveats: []gowarehouse.QualityCaveat{
			caveat("withheld", "a"), caveat("sampled", "b"),
		},
		FigureVerdicts: []gomodels.FigureVerdict{figureVerdict(gomodels.FigureFails)},
	})
	// Base 0.20, floor 0.05: 0.15 of room. Two quality docks take 0.10 and
	// 0.10, so the second is trimmed to 0.05 and the refutation is dropped.
	if math.Abs(got.ConfidenceScore-0.05) > epsilon {
		t.Errorf("score = %v, want the floor 0.05", got.ConfidenceScore)
	}
	if len(got.ConfidenceDocks) != 2 {
		t.Fatalf("docks = %+v, want two", got.ConfidenceDocks)
	}
	if math.Abs(got.ConfidenceDocks[0].Amount-0.10) > epsilon ||
		math.Abs(got.ConfidenceDocks[1].Amount-0.05) > epsilon {
		t.Errorf("amounts = %v / %v, want 0.1 then 0.05", got.ConfidenceDocks[0].Amount, got.ConfidenceDocks[1].Amount)
	}
	if _, ok := dockAmount(got, DockRefutedArithmetic); ok {
		t.Error("a dock charged nothing was stored anyway")
	}
	// And the band stays terminal however far the score fell.
	if got.ConfidenceBand != gomodels.ConfidenceBandUnverifiable {
		t.Errorf("band = %q, want unverifiable", got.ConfidenceBand)
	}
}

// TestTheDocksAlwaysSumToTheDropTheScoreTook is the invariant the breakdown
// rests on, over every verdict crossed with every subset of the five signals.
// §S11 renders base, each dock, and the final; if the arithmetic does not hold
// the reader is shown three numbers that disagree.
func TestTheDocksAlwaysSumToTheDropTheScoreTook(t *testing.T) {
	verdicts := []valmodels.Status{
		valmodels.StatusConfirmed, valmodels.StatusSupported, valmodels.StatusPartial,
		valmodels.StatusRejected, valmodels.StatusUnverifiable,
		valmodels.StatusValidationDisabled, valmodels.StatusSkippedBudgetCap, "",
	}
	cfg := DefaultConfig()
	cases := 0
	for _, verdict := range verdicts {
		// Five independent signals, and the quality one with one, two or
		// three distinct kinds: 4 * 2 * 2 * 2 * 2 shapes per verdict.
		for kinds := 0; kinds < 4; kinds++ {
			for mask := 0; mask < 16; mask++ {
				in := Input{Verdict: verdict, Config: cfg}
				for i := 0; i < kinds; i++ {
					in.QualityCaveats = append(in.QualityCaveats,
						caveat([]string{"withheld", "sampled", "truncated"}[i], "d"))
				}
				if mask&1 != 0 {
					in.FigureVerdicts = []gomodels.FigureVerdict{figureVerdict(gomodels.FigureFails)}
				}
				if mask&2 != 0 {
					in.QuantifierVerdicts = []gomodels.QuantifierVerdict{quantifierVerdict(gomodels.QuantifierFails)}
				}
				if mask&4 != 0 {
					in.UnresolvedFigures = 2
				}
				if mask&8 != 0 {
					in.StepReadBeyondRetained = true
				}
				got := Score(in)
				sum := 0.0
				for _, d := range got.ConfidenceDocks {
					if d.Amount <= 0 {
						t.Fatalf("verdict=%q kinds=%d mask=%d stored a dock charging %v", verdict, kinds, mask, d.Amount)
					}
					if d.Code == "" || d.Label == "" {
						t.Fatalf("verdict=%q kinds=%d mask=%d stored a dock with no code or label: %+v", verdict, kinds, mask, d)
					}
					sum += d.Amount
				}
				if math.Abs(got.ConfidenceBase-sum-got.ConfidenceScore) > epsilon {
					t.Errorf("verdict=%q kinds=%d mask=%d: base %v - docks %v = %v, but the score is %v",
						verdict, kinds, mask, got.ConfidenceBase, sum, got.ConfidenceBase-sum, got.ConfidenceScore)
				}
				if got.ConfidenceScore < cfg.Confidence.Floor-epsilon {
					t.Errorf("verdict=%q kinds=%d mask=%d: score %v is below the floor", verdict, kinds, mask, got.ConfidenceScore)
				}
				if got.ConfidenceScore > got.ConfidenceBase+epsilon {
					t.Errorf("verdict=%q kinds=%d mask=%d: score %v exceeds the base %v", verdict, kinds, mask, got.ConfidenceScore, got.ConfidenceBase)
				}
				cases++
			}
		}
	}
	if cases != len(verdicts)*4*16 {
		t.Fatalf("the matrix ran %d cases; the generator has stopped covering it", cases)
	}
}

// TestEveryReachableScoreIsExactAtTwoDecimals is the guard for a defect that
// no amount of epsilon-tolerant assertion would have caught.
//
// Every base, dock and cutoff sits on a 0.05 grid, so every score this engine
// can produce has an exact two-decimal decimal form. The score is persisted
// and served to API clients verbatim, so one that round-trips as
// 0.35000000000000003 is a 17-digit float in a payload and a trap for any
// client comparing it for equality.
//
// It happened: snapping with `math.Round(v/0.01) * 0.01` mangled 0.35, 0.70
// and 0.95, because 0.01 has no exact binary representation and multiplying
// by it reintroduces the error the rounding just removed. Every invariant
// still held to within 1e-16, which is exactly why the existing tests passed
// and a reader of the stored document saw it anyway.
func TestEveryReachableScoreIsExactAtTwoDecimals(t *testing.T) {
	exact := func(v float64) bool {
		// strconv's shortest representation is what encoding/json and the
		// BSON encoder emit, so this is the form a client actually receives.
		return strconv.FormatFloat(v, 'f', -1, 64) == strconv.FormatFloat(v, 'f', 2, 64) ||
			strconv.FormatFloat(v, 'f', -1, 64) == strings.TrimRight(strconv.FormatFloat(v, 'f', 2, 64), "0")
	}

	cfg := DefaultConfig()
	checked := 0
	verdicts := []valmodels.Status{
		valmodels.StatusConfirmed, valmodels.StatusSupported, valmodels.StatusPartial,
		valmodels.StatusRejected, valmodels.StatusUnverifiable,
		valmodels.StatusValidationDisabled, valmodels.StatusSkippedBudgetCap, "",
	}
	for _, verdict := range verdicts {
		for kinds := 0; kinds < 4; kinds++ {
			for mask := 0; mask < 16; mask++ {
				in := Input{Verdict: verdict, Config: cfg}
				for i := 0; i < kinds; i++ {
					in.QualityCaveats = append(in.QualityCaveats,
						caveat([]string{"withheld", "sampled", "truncated"}[i], "d"))
				}
				if mask&1 != 0 {
					in.FigureVerdicts = []gomodels.FigureVerdict{figureVerdict(gomodels.FigureFails)}
				}
				if mask&2 != 0 {
					in.QuantifierVerdicts = []gomodels.QuantifierVerdict{quantifierVerdict(gomodels.QuantifierFails)}
				}
				if mask&4 != 0 {
					in.UnresolvedFigures = 2
				}
				if mask&8 != 0 {
					in.StepReadBeyondRetained = true
				}
				got := Score(in)
				if !exact(got.ConfidenceScore) {
					t.Errorf("verdict=%q kinds=%d mask=%d: score serialises as %s, not an exact two-decimal value",
						verdict, kinds, mask, strconv.FormatFloat(got.ConfidenceScore, 'f', -1, 64))
				}
				if !exact(got.ConfidenceBase) {
					t.Errorf("verdict=%q: base serialises as %s", verdict,
						strconv.FormatFloat(got.ConfidenceBase, 'f', -1, 64))
				}
				for _, d := range got.ConfidenceDocks {
					if !exact(d.Amount) {
						t.Errorf("verdict=%q kinds=%d mask=%d: dock %s amount serialises as %s",
							verdict, kinds, mask, d.Code, strconv.FormatFloat(d.Amount, 'f', -1, 64))
					}
				}
				checked++
			}
		}
	}
	if checked != len(verdicts)*4*16 {
		t.Fatalf("checked %d cases; the generator has stopped covering the matrix", checked)
	}

	// And the snapping helper itself, over every score on the grid.
	for n := 1; n <= 19; n++ {
		if v := round(float64(n) * 0.05); !exact(v) {
			t.Errorf("round(%d*0.05) serialises as %s", n, strconv.FormatFloat(v, 'f', -1, 64))
		}
	}
}

// TestTheCutoffsAreInclusiveAtTheBoundary pins both edges. A supported
// document docked by exactly one 0.05 lands on 0.75 and must still be high;
// 0.40 must still be medium.
func TestTheCutoffsAreInclusiveAtTheBoundary(t *testing.T) {
	high := scored(Input{Verdict: valmodels.StatusSupported, UnresolvedFigures: 1})
	if math.Abs(high.ConfidenceScore-0.75) > epsilon {
		t.Fatalf("score = %v, want exactly the high cutoff 0.75", high.ConfidenceScore)
	}
	if high.ConfidenceBand != gomodels.ConfidenceBandHigh {
		t.Errorf("band at exactly 0.75 = %q, want high", high.ConfidenceBand)
	}

	// Partial 0.55 docked 0.10 (arithmetic) + 0.05 (unresolved) = 0.40.
	medium := scored(Input{
		Verdict:           valmodels.StatusPartial,
		FigureVerdicts:    []gomodels.FigureVerdict{figureVerdict(gomodels.FigureFails)},
		UnresolvedFigures: 1,
	})
	if math.Abs(medium.ConfidenceScore-0.40) > epsilon {
		t.Fatalf("score = %v, want exactly the medium cutoff 0.4", medium.ConfidenceScore)
	}
	if medium.ConfidenceBand != gomodels.ConfidenceBandMedium {
		t.Errorf("band at exactly 0.4 = %q, want medium", medium.ConfidenceBand)
	}

	// One more 0.05 and it is low.
	low := scored(Input{
		Verdict:                valmodels.StatusPartial,
		FigureVerdicts:         []gomodels.FigureVerdict{figureVerdict(gomodels.FigureFails)},
		UnresolvedFigures:      1,
		StepReadBeyondRetained: true,
	})
	if low.ConfidenceBand != gomodels.ConfidenceBandLow {
		t.Errorf("band at %v = %q, want low", low.ConfidenceScore, low.ConfidenceBand)
	}
}

// TestASupportedFindingIsOneDockFromMedium is the calibration claim stated as
// an assertion: supported is high, and one refuted figure moves it to medium.
func TestASupportedFindingIsOneDockFromMedium(t *testing.T) {
	clean := scored(Input{Verdict: valmodels.StatusSupported})
	if clean.ConfidenceBand != gomodels.ConfidenceBandHigh {
		t.Errorf("a clean supported finding is %q, want high", clean.ConfidenceBand)
	}
	docked := scored(Input{
		Verdict:        valmodels.StatusSupported,
		FigureVerdicts: []gomodels.FigureVerdict{figureVerdict(gomodels.FigureFails)},
	})
	if docked.ConfidenceBand != gomodels.ConfidenceBandMedium {
		t.Errorf("a supported finding with refuted arithmetic is %q, want medium", docked.ConfidenceBand)
	}
	if math.Abs(docked.ConfidenceScore-0.70) > epsilon {
		t.Errorf("score = %v, want 0.7", docked.ConfidenceScore)
	}
}

// TestTheTerminalBandsSurviveTheirOwnScores — both sit below the low cutoff
// and neither may be rendered as low confidence. "Nothing could check this" and
// "this was never checked" are different statements from "checked and weak".
func TestTheTerminalBandsSurviveTheirOwnScores(t *testing.T) {
	cfg := DefaultConfig().Confidence
	for _, tc := range []struct {
		verdict valmodels.Status
		band    string
	}{
		{valmodels.StatusUnverifiable, gomodels.ConfidenceBandUnverifiable},
		{valmodels.StatusValidationDisabled, gomodels.ConfidenceBandNotConfirmed},
		{valmodels.StatusSkippedBudgetCap, gomodels.ConfidenceBandNotConfirmed},
		{"", gomodels.ConfidenceBandNotConfirmed},
	} {
		got := scored(Input{Verdict: tc.verdict})
		if got.ConfidenceScore >= cfg.MediumAt {
			t.Fatalf("%q scores %v, which is not below the medium cutoff — this test no longer proves anything",
				tc.verdict, got.ConfidenceScore)
		}
		if got.ConfidenceBand != tc.band {
			t.Errorf("%q band = %q, want %q", tc.verdict, got.ConfidenceBand, tc.band)
		}
	}
}

// TestRefuterDisabledChangesNothing pins the invariant Input carries the flag
// for: Combine already accounts for the refuter's absence, so docking here
// would charge the same fact twice.
func TestRefuterDisabledChangesNothing(t *testing.T) {
	for _, verdict := range []valmodels.Status{
		valmodels.StatusConfirmed, valmodels.StatusSupported, valmodels.StatusPartial,
		valmodels.StatusRejected, valmodels.StatusUnverifiable,
	} {
		base := Input{Verdict: verdict, Config: DefaultConfig()}
		off := base
		off.RefuterDisabled = true
		if !reflect.DeepEqual(Score(base), Score(off)) {
			t.Errorf("%q: RefuterDisabled changed the score", verdict)
		}
	}
}

// TestScoreIsDeterministicAndOrderIndependent — the same input scored twice is
// identical, and caveats supplied in the reverse order produce the same docks.
// Both matter: a score that moved between two reads of one document would make
// the stored breakdown unreproducible.
func TestScoreIsDeterministicAndOrderIndependent(t *testing.T) {
	in := Input{
		Verdict: valmodels.StatusSupported,
		QualityCaveats: []gowarehouse.QualityCaveat{
			caveat("withheld", "a"), caveat("sampled", "b"),
		},
		FigureVerdicts: []gomodels.FigureVerdict{
			figureVerdict(gomodels.FigureHolds), figureVerdict(gomodels.FigureFails),
		},
		UnresolvedFigures: 1,
		Config:            DefaultConfig(),
	}
	first, second := Score(in), Score(in)
	if !reflect.DeepEqual(first, second) {
		t.Error("the same input scored twice gave two answers")
	}

	reversed := in
	reversed.QualityCaveats = []gowarehouse.QualityCaveat{caveat("sampled", "b"), caveat("withheld", "a")}
	reversed.FigureVerdicts = []gomodels.FigureVerdict{
		figureVerdict(gomodels.FigureFails), figureVerdict(gomodels.FigureHolds),
	}
	if !reflect.DeepEqual(first, Score(reversed)) {
		t.Error("supplying the evidence in a different order changed the score")
	}
}

// TestScoreDoesNotMutateItsInput — Input carries slices the caller owns and
// goes on using (the insight they came off is persisted straight after).
func TestScoreDoesNotMutateItsInput(t *testing.T) {
	in := Input{
		Verdict:            valmodels.StatusSupported,
		QualityCaveats:     []gowarehouse.QualityCaveat{caveat("withheld", "a")},
		FigureVerdicts:     []gomodels.FigureVerdict{figureVerdict(gomodels.FigureFails)},
		QuantifierVerdicts: []gomodels.QuantifierVerdict{quantifierVerdict(gomodels.QuantifierFails)},
		Config:             DefaultConfig(),
	}
	want := Input{
		Verdict:            valmodels.StatusSupported,
		QualityCaveats:     []gowarehouse.QualityCaveat{caveat("withheld", "a")},
		FigureVerdicts:     []gomodels.FigureVerdict{figureVerdict(gomodels.FigureFails)},
		QuantifierVerdicts: []gomodels.QuantifierVerdict{quantifierVerdict(gomodels.QuantifierFails)},
		Config:             DefaultConfig(),
	}
	_ = Score(in)
	if !reflect.DeepEqual(in, want) {
		t.Error("Score mutated its input")
	}
}

// TestAnUnsetConfigScoresAgainstTheDefault is the guard for a caller that
// built its orchestrator as a struct literal, or simply left the field unset.
//
// Scoring against a zero Config would be silently and uniformly wrong rather
// than loud: an empty base table prices every verdict at 0, and cutoffs of 0
// then put every document — rejected ones included — in the top band. That is
// the worst shape for the bug, because nothing about the output looks broken.
func TestAnUnsetConfigScoresAgainstTheDefault(t *testing.T) {
	for _, verdict := range []valmodels.Status{
		valmodels.StatusConfirmed, valmodels.StatusSupported, valmodels.StatusPartial,
		valmodels.StatusRejected, valmodels.StatusUnverifiable, "",
	} {
		bare := Score(Input{Verdict: verdict})
		explicit := Score(Input{Verdict: verdict, Config: DefaultConfig()})
		if !reflect.DeepEqual(bare, explicit) {
			t.Errorf("%q: an unset Config scored differently from the default:\n bare     = %+v\n explicit = %+v",
				verdict, bare, explicit)
		}
	}

	// Specifically: a rejected document does not come out in the top band.
	got := Score(Input{Verdict: valmodels.StatusRejected})
	if got.ConfidenceBand != gomodels.ConfidenceBandLow {
		t.Errorf("a rejected document with no Config landed in %q, want low", got.ConfidenceBand)
	}
	if got.EngineVersion != EngineVersion {
		t.Errorf("EngineVersion = %d, want %d", got.EngineVersion, EngineVersion)
	}

	// A Config carrying a real base table is used as given, even if its
	// Version is unset — the fallback must key on the calibration, not on the
	// version.
	custom := DefaultConfig()
	custom.Version = 0
	custom.Confidence.Bases[valmodels.StatusSupported] = 0.42
	if got := Score(Input{Verdict: valmodels.StatusSupported, Config: custom}); got.ConfidenceBase != 0.42 {
		t.Errorf("a versionless but populated Config was discarded: base = %v, want 0.42", got.ConfidenceBase)
	}
}

// TestScoreStampsTheEngineVersion — and a zero-valued Config still produces an
// explainable document rather than one stamped with version 0, which no
// ConfigForVersion could ever resolve.
func TestScoreStampsTheEngineVersion(t *testing.T) {
	if got := Score(Input{Verdict: valmodels.StatusSupported, Config: DefaultConfig()}).EngineVersion; got != EngineVersion {
		t.Errorf("EngineVersion = %d, want %d", got, EngineVersion)
	}
	if got := Score(Input{Verdict: valmodels.StatusSupported}).EngineVersion; got != EngineVersion {
		t.Errorf("a zero-valued Config stamped version %d, want %d", got, EngineVersion)
	}
}

// TestRiskFlagsStayUnwritten is what is left of the freeze the confidence-only
// engine kept over the whole severity half.
//
// Everything else in that half is written now. Flags are not, and the reason
// is not scheduling: a flag is derived from the analysis area's risk
// categories, no pack declares any, so there is nothing to derive one from.
// An empty list says that; a guessed one would be the model-authored label
// this engine exists to remove, arriving from a different direction.
func TestRiskFlagsStayUnwritten(t *testing.T) {
	got := scored(Input{
		Verdict:                valmodels.StatusSupported,
		QualityCaveats:         []gowarehouse.QualityCaveat{caveat("withheld", "a")},
		FigureVerdicts:         []gomodels.FigureVerdict{figureVerdict(gomodels.FigureFails)},
		UnresolvedFigures:      1,
		StepReadBeyondRetained: true,
		AreaID:                 "retention",
	})
	if len(got.RiskFlags) != 0 {
		t.Errorf("RiskFlags = %v, want none", got.RiskFlags)
	}
}

// TestTheReasonNamesWhatLoweredIt — a band a reader sees has to be legible
// beside its reason. A supported finding rendered Medium with only "rows are
// consistent with the claims checked" beside it reads like a broken label.
func TestTheReasonNamesWhatLoweredIt(t *testing.T) {
	cfg := DefaultConfig()
	plain := Score(Input{Verdict: valmodels.StatusSupported, Config: cfg}).ConfidenceReason

	one := Score(Input{
		Verdict:        valmodels.StatusSupported,
		FigureVerdicts: []gomodels.FigureVerdict{figureVerdict(gomodels.FigureFails)},
		Config:         cfg,
	})
	if one.ConfidenceReason == plain {
		t.Error("a docked document reports the undocked reason")
	}
	label := cfg.Confidence.Docks.RefutedArithmetic.Label
	if !contains(one.ConfidenceReason, lowerFirst(label)) {
		t.Errorf("the reason %q does not carry the dock's own wording %q", one.ConfidenceReason, label)
	}

	// Two docks of the SAME code say their sentence once, not twice.
	twoQuality := Score(Input{
		Verdict: valmodels.StatusConfirmed,
		QualityCaveats: []gowarehouse.QualityCaveat{
			caveat("withheld", "a"), caveat("sampled", "b"),
		},
		Config: cfg,
	})
	qLabel := lowerFirst(cfg.Confidence.Docks.DegradedEvidence.Label)
	if countOccurrences(twoQuality.ConfidenceReason, qLabel) != 1 {
		t.Errorf("the reason repeats one dock's sentence: %q", twoQuality.ConfidenceReason)
	}

	// Three distinct codes read as a list rather than as three sentences.
	three := Score(Input{
		Verdict:                valmodels.StatusConfirmed,
		FigureVerdicts:         []gomodels.FigureVerdict{figureVerdict(gomodels.FigureFails)},
		UnresolvedFigures:      1,
		StepReadBeyondRetained: true,
		Config:                 cfg,
	})
	if !contains(three.ConfidenceReason, "; and ") {
		t.Errorf("three docks did not render as a list: %q", three.ConfidenceReason)
	}
}

func contains(haystack, needle string) bool {
	return countOccurrences(haystack, needle) > 0
}

func countOccurrences(haystack, needle string) int {
	if needle == "" {
		return 0
	}
	n := 0
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			n++
		}
	}
	return n
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'A' && b[0] <= 'Z' {
		b[0] += 'a' - 'A'
	}
	return string(b)
}
