package discovery

import (
	"math"
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// The `excess` kind: a quotient minus one, for a sentence that states a gap as a
// proportion of the amount it is measured against.
//
// It exists because the kinds that preceded it could not tell "109.6% OF the lowest band"
// from "109.6% MORE THAN the lowest band". Those are different numbers, and a live run
// shipped the second sentence behind a figure vouched as the first -- the ratio was right,
// the sentence was false, and the excess was 9.6%. A word like "more" is in neither the
// figure nor the evaluator, so the distinction has to be a kind the model declares.

// TestFigures_ExcessHoldsTheSpreadThatRatioRefuted is insight 4afb7e88 of the run.
//
// The sentence read "above the lowest by only 0.77%", the declaration said `ratio`, and
// the evaluator answered 1.0077 -- so a figure whose prose was correct was refuted for it.
// There was no kind for what the sentence claimed.
func TestFigures_ExcessHoldsTheSpreadThatRatioRefuted(t *testing.T) {
	f := models.Figure{
		ID: "f1", Value: 0.77, Unit: models.UnitPercent, Decimals: 2,
		Step: 17, Kind: models.FigureExcess,
		Column: "net_rev", Row: "material = 'TIN'", Other: "material = 'STEEL'",
	}
	v := oneVerdict(t, f, evidence(17, step17Rows()))
	if v.Status != models.FigureHolds {
		t.Fatalf("status = %q (%s), want holds -- 0.77%% is the excess the sentence claimed", v.Status, v.Reason)
	}
	// The same two cells as a ratio are the quotient, and the run refuted this figure for
	// exactly that. Kept here so the two kinds cannot quietly collapse into one.
	f.Kind = models.FigureRatio
	if v := oneVerdict(t, f, evidence(17, step17Rows())); v.Status != models.FigureFails {
		t.Errorf("as a ratio the same declaration gives %q, want fails -- the quotient is 100.77%%", v.Status)
	}
}

// TestFigures_ExcessRefutesAQuotientWrittenAsAnExcess is insight 92ef2802 of the run, and
// the falsehood that argued for this kind.
//
// The figure declared `ratio` on avg_ltv, quartile 4 over quartile 1. Go evaluated 109.5878,
// the figure stated 109.59, and it HELD -- while the sentence around it read "spend only
// about 109.6% more", where the true excess is 9.6%. A vouched figure behind a false
// sentence is the one outcome worse than a refutation, because nothing downstream has any
// signal at all.
//
// Go cannot read the word "more". What it can do is check the arithmetic the model names,
// so the sentence has to be able to name this one.
func TestFigures_ExcessRefutesAQuotientWrittenAsAnExcess(t *testing.T) {
	quotientAsExcess := models.Figure{
		ID: "f_ltv_lift", Value: 109.59, Unit: models.UnitPercent, Decimals: 1,
		Step: 29, Kind: models.FigureExcess,
		Column: "avg_ltv", Row: "first_val_quartile = 4", Other: "first_val_quartile = 1",
	}
	v := oneVerdict(t, quotientAsExcess, evidence(29, step29Rows()))
	if v.Status != models.FigureFails {
		t.Fatalf("status = %q, want fails -- 109.59 is the quotient, and this figure declares the excess", v.Status)
	}
	if got := v.Evaluated; got < 9.58 || got > 9.59 {
		t.Errorf("evaluated = %v, want ~9.5878 -- the excess is the quotient minus one", got)
	}

	// And the number the sentence should have carried holds.
	correct := quotientAsExcess
	correct.Value = 9.6
	if v := oneVerdict(t, correct, evidence(29, step29Rows())); v.Status != models.FigureHolds {
		t.Errorf("status = %q (%s), want holds -- 9.6%% is the excess at one decimal", v.Status, v.Reason)
	}
}

// TestFigures_ExcessOverAColumnTotalNeedsEveryRow.
//
// An excess with no `other` divides by the column total over scope, which folds the whole
// result. A capped result gives a partial denominator, and the truncation work established
// what that costs: the figure stating the true value is refuted for disagreeing with a
// partial one, and the correction gate -- which acts only inside 1%, exactly where a small
// truncation lands -- then writes the partial number over the correct one and certifies it.
func TestFigures_ExcessOverAColumnTotalNeedsEveryRow(t *testing.T) {
	f := models.Figure{
		ID: "f1", Value: -80, Unit: models.UnitPercent, Decimals: 0,
		Step: 17, Kind: models.FigureExcess, Column: "net_rev", Row: "material = 'TIN'",
	}
	steps := map[int]StepRows{17: {
		Rows:    step17Rows(),
		Quality: []gowarehouse.QualityCaveat{gowarehouse.RowCapCaveat(3)},
	}}
	v := oneVerdict(t, f, steps)
	if v.Status != models.FigureUndecidable {
		t.Fatalf("status = %q, want undecidable -- a capped result cannot settle a fold", v.Status)
	}
	// With `other` it names two rows instead, and a row is either in what came back or it
	// is not, which oneCell already reports.
	f.Other = "material = 'STEEL'"
	f.Value = 0.77
	f.Decimals = 2
	if v := oneVerdict(t, f, steps); v.Status != models.FigureHolds {
		t.Errorf("status = %q (%s), want holds -- cell over cell reads named rows, not the fold", v.Status, v.Reason)
	}
}

// TestFigures_SmallExcessSurvivesItsOwnArithmetic — r29 P2.
//
// a/b-1 rounds the quotient near 1 and then subtracts, which keeps the absolute error while
// the result shrinks. For 50.0025 over 50 that gave 0.004999999999988 against a figure
// stating 0.005 -- outside the interval "0.01%" claims, so a correct figure was refuted,
// and the correction gate reads a gap that small as the same quantity and rewrites the
// sentence to "0.00%". A right number becomes a wrong one with no model in the loop.
//
// (a-b)/b is the same value in exact arithmetic and stable here, because a-b is exact when
// the two are close.
func TestFigures_SmallExcessSurvivesItsOwnArithmetic(t *testing.T) {
	rows := []map[string]any{
		{"seg": "high", "spend": 50.0025},
		{"seg": "low", "spend": 50.0},
	}
	f := models.Figure{
		ID: "f1", Value: 0.005, Unit: models.UnitPercent, Decimals: 2,
		Step: 1, Kind: models.FigureExcess,
		Column: "spend", Row: "seg = 'high'", Other: "seg = 'low'",
	}
	notRefutedAndNotRewritten(t, f, rows)
}

// TestFigureContract_ExcessDoesNotCaptureAnAbsoluteDifference — r29 P2.
//
// The first wording said `excess` was the kind "whenever your sentence says one thing
// exceeds another by some amount", which also reads onto "spend is $3.50 higher" -- an
// absolute difference, where `excess` evaluates 0.035 and refutes an accurate sentence.
// That is the failure mode the contract's own doc comment names: a contract advertising an
// operation the evaluator does not implement generates false refutations.
func TestFigureContract_ExcessDoesNotCaptureAnAbsoluteDifference(t *testing.T) {
	if !strings.Contains(figureContract, "as a share of") &&
		!strings.Contains(figureContract, "proportion of") {
		t.Error("the contract does not say an excess is a share of the other amount, so a " +
			"sentence stating an absolute gap reads as an excess")
	}
	if !strings.Contains(figureContract, "own units") {
		t.Error("the contract does not send a gap stated in the column's own units to `diff`")
	}
	if !strings.Contains(figureContract, "percentage points") {
		t.Error("the contract does not name a percentage-point change, which is a `diff` on a " +
			"percent column and the likeliest thing to be mistaken for an excess")
	}
}

// TestFigures_ExcessOnARoundingBoundarySurvivesOperandError — r30 P2.
//
// (a-b)/b removed the cancellation in the subtraction but not the error the operands
// arrive with. 137.00685 is not that number in binary, and an excess divides the small
// difference by the large denominator, so the representation error is amplified by
// |num|/|num-den| -- about 2.7 million here. The result lands 1e-14 from 0.005, which for
// a figure printing "0.01%" is just past the half-hundredth it claims, so a figure equal
// to its evidence is refuted and then rewritten to "0.00%".
//
// The allowance now comes from the evaluator, which derives it from the operands rather
// than guessing it from the answer.
func TestFigures_ExcessOnARoundingBoundarySurvivesOperandError(t *testing.T) {
	rows := []map[string]any{
		{"seg": "high", "spend": 137.00685},
		{"seg": "low", "spend": 137.0},
	}
	f := models.Figure{
		ID: "f1", Value: 0.005, Unit: models.UnitPercent, Decimals: 2,
		Step: 1, Kind: models.FigureExcess,
		Column: "spend", Row: "seg = 'high'", Other: "seg = 'low'",
	}
	notRefutedAndNotRewritten(t, f, rows)
}

// TestFigures_TinyExcessIsNotRefutedByItsOwnOperands — r32 P2.
//
// The fourth case in a row where the allowance was short by a factor of a few. Cells of
// 100.0000015 and 100 give an excess of 1.5e-6; written to six decimals that prints
// "0.000002%" and claims 5e-7, and the evaluation lands 3.8e-16 past that. Every previous
// allowance was computed from the ANSWER, and the answer here is eight orders of magnitude
// smaller than the operands whose representation error it carries, so no expression in the
// answer's magnitude could ever have covered it.
//
// The allowance now comes from evalFigure, which has the operands: 2*eps*|a/b|, scaled with
// the percent conversion. That is a bound rather than a guess, and it covers all three
// earlier cases too.
func TestFigures_TinyExcessIsNotRefutedByItsOwnOperands(t *testing.T) {
	rows := []map[string]any{
		{"seg": "high", "spend": 100.0000015},
		{"seg": "low", "spend": 100.0},
	}
	f := models.Figure{
		ID: "f1", Value: 0.0000015, Unit: models.UnitPercent, Decimals: 6,
		Step: 1, Kind: models.FigureExcess,
		Column: "spend", Row: "seg = 'high'", Other: "seg = 'low'",
	}
	v := notRefutedAndNotRewritten(t, f, rows)

	// And the value RECORDED is the accurate one, which is what (a-b)/b now buys.
	//
	// The bound above is wide enough to absorb a/b-1's error too, so the verdict no longer
	// depends on which form is used -- but Evaluated is stored, served, and read by the
	// correction gate, so it should be the better number. (a-b)/b lands 3.8e-15 from the
	// truth here and a/b-1 lands 9.1e-15, and float64 arithmetic is exact and repeatable, so
	// a threshold between them is a stable test rather than a lucky one.
	if err := math.Abs(v.Evaluated - 0.0000015); err > 5e-15 {
		t.Errorf("evaluated = %.20g, off by %.3g -- want the numerically stable form, which "+
			"lands within 4e-15 where a/b-1 lands at 9e-15", v.Evaluated, err)
	}
}

// TestFigures_ExcessSurvivesAnOverflowingSubtraction — r39 P3.
//
// (a-b)/b is the accurate form and it has one blind spot: the difference can overflow where
// the answer does not. 1e308 against -1e308 is an excess of exactly -2, but 2e308 is not a
// float64, so the stable form gave infinity and a computable figure came back undecidable --
// a capability lost rather than a number got wrong, which is why this is the mildest finding
// in the series. a/b-1 has no intermediate that large and answers it.
func TestFigures_ExcessSurvivesAnOverflowingSubtraction(t *testing.T) {
	rows := []map[string]any{
		{"seg": "high", "amount": 1e308},
		{"seg": "low", "amount": -1e308},
	}
	f := models.Figure{
		ID: "f1", Value: -200, Unit: models.UnitPercent, Decimals: 0,
		Step: 1, Kind: models.FigureExcess,
		Column: "amount", Row: "seg = 'high'", Other: "seg = 'low'",
	}
	v := oneVerdict(t, f, evidence(1, rows))
	if v.Status != models.FigureHolds {
		t.Fatalf("status = %q (%s), want holds -- the excess is exactly -200%%", v.Status, v.Reason)
	}
	if math.IsInf(v.Evaluated, 0) || math.IsNaN(v.Evaluated) {
		t.Errorf("evaluated = %v, which cannot be marshalled to JSON", v.Evaluated)
	}
	// And the accurate form is still what ordinary operands get: the fallback must not become
	// the default. 50.0025 against 50 lands closer under (a-b)/b than under a/b-1.
	near := []map[string]any{
		{"seg": "high", "amount": 100.0000015},
		{"seg": "low", "amount": 100.0},
	}
	fine := models.Figure{
		ID: "f1", Value: 0.0000015, Unit: models.UnitPercent, Decimals: 6,
		Step: 1, Kind: models.FigureExcess,
		Column: "amount", Row: "seg = 'high'", Other: "seg = 'low'",
	}
	if got := oneVerdict(t, fine, evidence(1, near)).Evaluated; math.Abs(got-0.0000015) > 5e-15 {
		t.Errorf("evaluated = %.20g, off by %.3g -- the stable form must still be the default",
			got, math.Abs(got-0.0000015))
	}
}
