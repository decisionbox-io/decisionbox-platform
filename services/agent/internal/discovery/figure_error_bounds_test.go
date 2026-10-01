package discovery

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// The arithmetic's own error, and what it is allowed to conclude.
//
// Every figure here is compared by containment between two derived intervals: the precision
// the figure printed, and the bound the evaluation carries. These cases are the edges of
// that -- rounding at a boundary, an accumulated sum, overflow, subnormals, a denominator
// indistinguishable from zero. The rule they all enforce is the same one: where the
// arithmetic cannot settle a figure at the precision it claims, the answer is undecidable,
// never a refutation. Reporting the evaluator's own limit as the document's error is the
// failure this layer exists to avoid.

// TestFigures_UnrepairedInsightIsNotReRendered.
//
// figuresBrokenByRepair must be a no-op on the overwhelming majority of insights: repair
// runs only where a declared claim its own evidence contradicts. An insight that never
// entered repair is not asked the question at all, so prose edited by anything else --
// which is nothing today -- cannot start withholding figures as a side effect.
func TestFigures_UnrepairedInsightIsNotReRendered(t *testing.T) {
	ins := repairedQuartileInsight()
	ins.Repair = nil
	ins.Description = "Nothing like the template at all."

	if broken := figuresBrokenByRepair(ins); len(broken) != 0 {
		t.Errorf("withheld = %v on an insight that never entered repair", broken)
	}
}

// TestFigures_ClosenessIsNeverLooserThanTheStatedPrecision guards the cap.
//
// The allowance above grows with the magnitudes compared, and at 1e12 an unbounded one
// reaches a whole unit -- past the half-unit a figure written to no decimals claims. That
// would make the check looser than the precision the figure printed, which is the one
// property this whole layer rests on.
func TestFigures_ClosenessIsNeverLooserThanTheStatedPrecision(t *testing.T) {
	f := models.Figure{ID: "f1", Value: 1e12, Unit: models.UnitCount, Decimals: 0}
	// A full unit out. The interval is half a unit, so this must not hold however the
	// allowance is computed.
	if closeEnough(f, 1e12+1, 0) {
		t.Error("a figure written to whole units held against evidence a whole unit away")
	}
	if !closeEnough(f, 1e12, 0) {
		t.Error("a figure equal to its evidence did not hold")
	}
}

// TestFigures_HighPrecisionFigureEqualToItsEvidenceIsNotRefuted — r31 P2.
//
// Round 30 capped the closeness allowance at a hundredth of the interval, which was too
// tight at the top of float64's range. An unscaled cell of 100000000.0000045 written to six
// decimals prints "100000000.000005"; reading that print back lands 5.0664e-7 from the
// value it came from, and the figure claims 5e-7 -- so a figure exactly equal to its own
// evidence was refuted, and correction cannot rescue it because assigning the same number
// renders the same text. Six decimals at 1e8 is sixteen significant digits, past what a
// float64 holds, so the machine cannot tell these apart and must not claim to.
func TestFigures_HighPrecisionFigureEqualToItsEvidenceIsNotRefuted(t *testing.T) {
	const v = 100000000.0000045
	rows := []map[string]any{{"seg": "one", "amount": v}}
	f := models.Figure{
		ID: "f1", Value: v, Unit: models.UnitPlain, Decimals: 6,
		Step: 1, Kind: models.FigureCell, Column: "amount", Row: "seg = 'one'",
	}
	if v := oneVerdict(t, f, evidence(1, rows)); v.Status != models.FigureHolds {
		t.Errorf("status = %q (%s), want holds -- the figure states exactly its evidence",
			v.Status, v.Reason)
	}
}

// TestFigures_ReadFiguresAreHeldToExactlyTheirPrintedInterval.
//
// The counterpart to the case above, and the reason the allowance is per-kind rather than
// global. A cell and a count are read, not computed, so their bound is zero and they are
// held to exactly the interval they printed -- no looser than before any of this. A test
// here because a later widening of the bound would silently loosen every figure in the
// corpus, and that is the property the whole layer rests on.
func TestFigures_ReadFiguresAreHeldToExactlyTheirPrintedInterval(t *testing.T) {
	rows := []map[string]any{{"seg": "one", "amount": 100.006}}
	// Two decimals claims +/-0.005, and the evidence is 0.006 away.
	f := models.Figure{
		ID: "f1", Value: 100.0, Unit: models.UnitPlain, Decimals: 2,
		Step: 1, Kind: models.FigureCell, Column: "amount", Row: "seg = 'one'",
	}
	if v := oneVerdict(t, f, evidence(1, rows)); v.Status != models.FigureFails {
		t.Errorf("status = %q, want fails -- a cell is read, so it gets no error allowance", v.Status)
	}
	// And the same figure exactly on its interval still holds.
	rows[0]["amount"] = 100.005
	if v := oneVerdict(t, f, evidence(1, rows)); v.Status != models.FigureHolds {
		t.Errorf("status = %q, want holds -- the evidence is exactly on the interval", v.Status)
	}
}

// TestFigures_SpacingStaysFiniteAtTheTopOfTheRange — r32 P3.
//
// floatSpacing measured upwards, and one place above MaxFloat64 is infinity -- so the
// spacing was infinite, the allowance was infinite, and every comparison held. A figure
// claiming zero was certified against evidence of MaxFloat64. The non-finite guards
// elsewhere reject neither input, because both are finite numbers.
func TestFigures_SpacingStaysFiniteAtTheTopOfTheRange(t *testing.T) {
	if sp := floatSpacing(math.MaxFloat64); math.IsInf(sp, 0) || math.IsNaN(sp) || sp <= 0 {
		t.Errorf("floatSpacing(MaxFloat64) = %v, want a finite positive width", sp)
	}
	f := models.Figure{ID: "f1", Value: 0, Unit: models.UnitCount, Decimals: 0}
	if closeEnough(f, math.MaxFloat64, 0) {
		t.Error("a figure claiming zero held against evidence of MaxFloat64")
	}
	// The ordinary case is unchanged: spacing grows with magnitude and stays positive.
	for _, m := range []float64{1e-300, 1, 1e8, 1e12, 1e300} {
		if sp := floatSpacing(m); sp <= 0 || math.IsInf(sp, 0) {
			t.Errorf("floatSpacing(%g) = %v, want finite and positive", m, sp)
		}
	}
	if floatSpacing(0) != 0 {
		t.Error("floatSpacing(0) should be zero")
	}
}

// TestFigures_RatioOverAColumnTotalCarriesTheSumsRounding — r33 P2.
//
// A ratio with no `other` divides by the column total, and a total of a hundred rows carries
// the rounding of a hundred additions -- two orders of magnitude more than one double's own
// error. Round 32's bound read only the two operands' representation error, so it was short
// ninefold here: a numerator of 1.23455 over a hundred cells of 0.1 evaluates to
// 12.345500000000024, which refuted a figure printing "12.345%" and then rewrote it to
// "12.346%" over accumulation error alone.
func TestFigures_RatioOverAColumnTotalCarriesTheSumsRounding(t *testing.T) {
	rows := make([]map[string]any, 0, 101)
	rows = append(rows, map[string]any{"seg": "top", "grp": "whole", "amount": 1.23455})
	for i := 0; i < 100; i++ {
		rows = append(rows, map[string]any{"seg": fmt.Sprintf("r%d", i), "grp": "part", "amount": 0.1})
	}
	// Numerator one cell, denominator the total of a hundred tenths -- which a float64 adds
	// up to 9.999999999999998, not 10. So the ratio evaluates to 12.345500000000024 where
	// the figure states 12.3455, and the whole gap is the sum's accumulated rounding.
	f := models.Figure{
		ID: "f1", Value: 12.3455, Unit: models.UnitPercent, Decimals: 3,
		Step: 1, Kind: models.FigureRatio, Column: "amount",
		Row: "seg = 'top'", Scope: "grp = 'part'",
	}
	notRefutedAndNotRewritten(t, f, rows)
}

// TestFigures_SumCarriesItsAccumulatedRounding is the same property on the kind that
// accumulates it directly, rather than inheriting it through a denominator.
func TestFigures_SumCarriesItsAccumulatedRounding(t *testing.T) {
	rows := make([]map[string]any, 0, 100)
	for i := 0; i < 100; i++ {
		rows = append(rows, map[string]any{"amount": 0.1})
	}
	// A hundred tenths total 9.999999999999998, not 10.
	f := models.Figure{
		ID: "f1", Value: 10, Unit: models.UnitPlain, Decimals: 15,
		Step: 1, Kind: models.FigureSum, Column: "amount",
	}
	if v := oneVerdict(t, f, evidence(1, rows)); v.Status != models.FigureHolds {
		t.Errorf("status = %q (%s), want holds -- a total of a hundred tenths is 10 to any "+
			"precision a float64 has", v.Status, v.Reason)
	}
}

// TestFigures_AnErrorBoundThatOverflowsIsUndecidable — r33 P3.
//
// Two operands of 1e308 differ by zero, but adding their magnitudes overflows to infinity --
// and an infinite tolerance certifies every claim however wrong. That is the mirror of a
// false refutation and worse, because it comes back as `holds` and a reference to it inherits
// the certification. The arithmetic is ordered to stay finite, and anything that gets past
// that is undecidable, never a verdict.
func TestFigures_AnErrorBoundThatOverflowsIsUndecidable(t *testing.T) {
	// A diff of two enormous operands: finite inputs, finite answer, and a bound that must
	// not become infinite.
	rows := []map[string]any{
		{"seg": "a", "amount": 1e308},
		{"seg": "b", "amount": 1e308},
	}
	f := models.Figure{
		ID: "f1", Value: 1e300, Unit: models.UnitPlain, Decimals: 0,
		Step: 1, Kind: models.FigureDiff,
		Column: "amount", Row: "seg = 'a'", Other: "seg = 'b'",
	}
	// Refuted, and decidedly so. Merely "not holds" would also be satisfied by undecidable,
	// which is what an overflowing bound produces -- so this asserts the bound stayed finite
	// and the figure was actually judged, not that the guard caught it.
	if v := oneVerdict(t, f, evidence(1, rows)); v.Status != models.FigureFails {
		t.Errorf("status = %q (%s), want fails: the difference is zero and the figure claims "+
			"1e300, and the bound on two operands of 1e308 must stay finite", v.Status, v.Reason)
	}

	// And the case the guard is actually for: a finite answer with a bound that overflows.
	//
	// The denominator's magnitudes cancel, so a scope summing to 1e-5 carries rounding
	// proportional to 1e300. The quotient is then large enough that multiplying the two
	// overflows, while the quotient itself -- the answer -- is an ordinary 1e25.
	cancelling := []map[string]any{
		{"seg": "top", "grp": "whole", "amount": 1e20},
		{"seg": "a", "grp": "part", "amount": 1e300},
		{"seg": "b", "grp": "part", "amount": -1e300},
		{"seg": "c", "grp": "part", "amount": 1e-5},
	}
	ratio := models.Figure{
		ID: "f1", Value: 1e25, Unit: models.UnitPlain, Decimals: 0,
		Step: 1, Kind: models.FigureRatio, Column: "amount",
		Row: "seg = 'top'", Scope: "grp = 'part'",
	}
	if v := oneVerdict(t, ratio, evidence(1, cancelling)); v.Status != models.FigureUndecidable {
		t.Errorf("status = %q, want undecidable: the answer is finite but nothing here can "+
			"bound how far out it is, and an unlimited tolerance certifies anything", v.Status)
	}

	// A total that overflows outright was already undecidable on the answer alone; kept so a
	// change to the bound cannot turn it into a verdict.
	big := []map[string]any{{"amount": math.MaxFloat64}, {"amount": math.MaxFloat64}}
	sum := models.Figure{
		ID: "f1", Value: 1e300, Unit: models.UnitPlain, Decimals: 0,
		Step: 1, Kind: models.FigureSum, Column: "amount",
	}
	if v := oneVerdict(t, sum, evidence(1, big)); v.Status != models.FigureUndecidable {
		t.Errorf("status = %q, want undecidable for a total that overflows", v.Status)
	}
}

// TestFigures_AWideErrorBoundCertifiesNothing — r34 P2.
//
// Rounds 30 to 33 were all about a bound too NARROW, which refuted correct figures. This is
// the same bound too wide, which is worse: a thousand rows alternating 1e10 and -1e10 total
// exactly zero, and the worst-case bound on adding a thousand numbers that large is about
// 1.11. A figure claiming 1 to whole units sat inside that and was certified `holds`, where
// the true total refutes it -- and a recommendation referencing it would inherit the
// certification.
//
// The bound is not wrong; it is a worst case, and this column happens to cancel exactly.
// What is wrong is treating the whole uncertainty as acceptable error. A bound wider than
// the interval the figure printed means the arithmetic cannot resolve what the figure
// claims, so nothing can be established either way.
func TestFigures_AWideErrorBoundCertifiesNothing(t *testing.T) {
	rows := make([]map[string]any, 0, 1000)
	for i := 0; i < 1000; i++ {
		v := 1e10
		if i%2 == 1 {
			v = -1e10
		}
		rows = append(rows, map[string]any{"amount": v})
	}
	f := models.Figure{
		ID: "f1", Value: 1, Unit: models.UnitPlain, Decimals: 0,
		Step: 1, Kind: models.FigureSum, Column: "amount",
	}
	v := oneVerdict(t, f, evidence(1, rows))
	if v.Status == models.FigureHolds {
		t.Fatalf("status = holds: a claim of 1 was certified against a total of exactly zero")
	}
	if v.Status != models.FigureUndecidable {
		t.Errorf("status = %q, want undecidable -- the bound is a worst case, so the total "+
			"cannot be refuted at unit precision either", v.Status)
	}
	if !strings.Contains(v.Reason, "reaches outside") {
		t.Errorf("reason = %q, want it to say the uncertainty reaches outside what the figure claims", v.Reason)
	}

	// The reference index must not lend it, since nothing vouched for it.
	ins := models.Insight{
		ID: "11111111-2222-3333-4444-555555555555", Name: "totals",
		Figures: []models.Figure{f}, FigureVerdicts: []models.FigureVerdict{v},
	}
	recs := []models.Recommendation{{
		Description: "Act on the {{r1}} total.",
		Figures: []models.Figure{{
			ID: "r1", Unit: models.UnitPlain, Kind: models.FigureRefKind,
			Refs: []models.FigureRef{ref(ins.ID, "f1")},
		}},
	}}
	attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
	if rv := recs[0].FigureVerdicts[0]; rv.Status == models.FigureHolds {
		t.Errorf("a recommendation certified a reference to an unvouched figure: %+v", rv)
	}
}

// TestFigures_DisagreementIsStillReportedWhenTheBoundIsWide.
//
// The other half of the rule above, and the reason it applies only to agreement. A figure
// outside even the widened interval is wrong wherever in that interval the truth sits, so it
// is still refuted -- declining those too would hand the evaluator an excuse to settle
// nothing whenever the arithmetic is imprecise.
func TestFigures_DisagreementIsStillReportedWhenTheBoundIsWide(t *testing.T) {
	rows := make([]map[string]any, 0, 1000)
	for i := 0; i < 1000; i++ {
		v := 1e10
		if i%2 == 1 {
			v = -1e10
		}
		rows = append(rows, map[string]any{"amount": v})
	}
	// A claim far outside the uncertainty of about 1.11.
	f := models.Figure{
		ID: "f1", Value: 500, Unit: models.UnitPlain, Decimals: 0,
		Step: 1, Kind: models.FigureSum, Column: "amount",
	}
	if v := oneVerdict(t, f, evidence(1, rows)); v.Status != models.FigureFails {
		t.Errorf("status = %q (%s), want fails -- 500 is wrong wherever inside the bound the "+
			"true total sits", v.Status, v.Reason)
	}
}

// TestFigures_ADenominatorIndistinguishableFromZeroIsUndecidable — r36 P2.
//
// Exact zero was refused; a denominator whose own uncertainty reaches zero was not. A column
// of [1e9, 0.01, -1e9, -0.01] totals exactly zero but accumulates to -9.5e-9 with an
// uncertainty of 8.9e-7, so the division is undefined -- and the error bound could not say so,
// because it scales with the quotient and a zero numerator collapses it to nothing. An excess
// over that was certified as exactly -100%.
func TestFigures_ADenominatorIndistinguishableFromZeroIsUndecidable(t *testing.T) {
	rows := []map[string]any{
		{"seg": "top", "grp": "whole", "amount": 0.0},
		{"seg": "a", "grp": "part", "amount": 1e9},
		{"seg": "b", "grp": "part", "amount": 0.01},
		{"seg": "c", "grp": "part", "amount": -1e9},
		{"seg": "d", "grp": "part", "amount": -0.01},
	}
	f := models.Figure{
		ID: "f1", Value: -100, Unit: models.UnitPercent, Decimals: 0,
		Step: 1, Kind: models.FigureExcess, Column: "amount",
		Row: "seg = 'top'", Scope: "grp = 'part'",
	}
	v := oneVerdict(t, f, evidence(1, rows))
	if v.Status != models.FigureUndecidable {
		t.Fatalf("status = %q, want undecidable -- the denominator cannot be told from zero, so "+
			"the quotient is unbounded", v.Status)
	}
	if !strings.Contains(v.Reason, "zero") {
		t.Errorf("reason = %q, want it to name the denominator", v.Reason)
	}
	// An exact zero denominator is still refused, which is the case this subsumes.
	exact := []map[string]any{
		{"seg": "top", "grp": "whole", "amount": 5.0},
		{"seg": "a", "grp": "part", "amount": 0.0},
	}
	if v := oneVerdict(t, f, evidence(1, exact)); v.Status != models.FigureUndecidable {
		t.Errorf("status = %q, want undecidable for a denominator of exactly zero", v.Status)
	}
}

// TestFigures_AnOverflowingPercentReadingNeverReachesTheVerdict — r36 P3.
//
// A percentage figure is compared against three readings of its evidence, because a share
// column stores a fraction or a percentage and the figure cannot see which. For a value near
// the top of the float64 range the `got*100` reading overflows, and the infinity was written
// into Evaluated before the comparison rejected it -- so a verdict could ship carrying an
// infinity, which cannot be marshalled and takes the whole insights payload with it. The same
// failure round 24 fixed for the answer itself.
func TestFigures_AnOverflowingPercentReadingNeverReachesTheVerdict(t *testing.T) {
	rows := []map[string]any{{"seg": "one", "share": 1e307}}
	f := models.Figure{
		ID: "f1", Value: 1, Unit: models.UnitPercent, Decimals: 0,
		Step: 1, Kind: models.FigureCell, Column: "share", Row: "seg = 'one'",
	}
	v := oneVerdict(t, f, evidence(1, rows))
	if math.IsInf(v.Evaluated, 0) || math.IsNaN(v.Evaluated) {
		t.Fatalf("evaluated = %v, which cannot be marshalled to JSON", v.Evaluated)
	}
	ins := []models.Insight{{Name: "n", Figures: []models.Figure{f}, FigureVerdicts: []models.FigureVerdict{v}}}
	if _, err := json.Marshal(ins); err != nil {
		t.Errorf("the verdict left the insight unmarshalable: %v", err)
	}
}

// TestFigures_TheRecommenderIsOnlyShownFiguresItCanReference — r36 P2.
//
// The other half of "withheld at both ends or neither", on the path that has no repair. An id
// the reference grammar cannot express was advertised unchanged, and the contract tells the
// model to copy it verbatim -- so reference resolution refused it and a recommendation that
// followed the instruction exactly shipped "{{f1}}" rather than the value. A duplicated id and
// a figure with no readable value are unreferenceable for the same reason.
func TestFigures_TheRecommenderIsOnlyShownFiguresItCanReference(t *testing.T) {
	in := []models.Insight{{
		ID: "11111111-2222-3333-4444-555555555555", Name: "mixed",
		Figures: []models.Figure{
			{ID: "revenue-total", Value: 10, Unit: models.UnitCount}, // outside the id grammar
			{ID: "dup", Value: 20, Unit: models.UnitCount},
			{ID: "dup", Value: 21, Unit: models.UnitCount},
			{ID: "novalue", Unit: models.UnitCount, ValueMissing: true},
			{ID: "f1", Value: 50004, Unit: models.UnitCount}, // the only referenceable one
		},
	}}
	out := insightsForRecommenderPrompt(in)
	if len(out[0].Figures) != 1 || out[0].Figures[0].ID != "f1" {
		ids := make([]string, 0, len(out[0].Figures))
		for _, f := range out[0].Figures {
			ids = append(ids, f.ID)
		}
		t.Errorf("the prompt advertises %v, want f1 alone -- every other id is one reference "+
			"resolution refuses", ids)
	}
	// The stored insight is untouched, because it is what gets persisted.
	if len(in[0].Figures) != 5 {
		t.Error("the stored insight's figures were mutated")
	}
}

// TestFigures_SubnormalOperandsStillCarryRoundingError — r37 P3.
//
// eps times the magnitude is the relative error of a float64, and for a value near the bottom
// of the range that product underflows to exactly zero -- where the true error is larger
// rather than smaller, because a subnormal has fewer significand bits and an absolute spacing
// of 4.9e-324. Cells of "1.1e-320" and "1e-320" gave an excess a bound of zero, so a correct
// 10.00% was refuted and the correction gate rewrote it to 9.98% and certified that.
//
// Reached through string cells, which is the same path round 21 covered: drivers hand numerics
// back as text and the decoders coerce them.
func TestFigures_SubnormalOperandsStillCarryRoundingError(t *testing.T) {
	if got := roundingSlack(1.1e-320); got <= 0 {
		t.Fatalf("roundingSlack(1.1e-320) = %v, want a positive bound", got)
	}
	// Zero is floored too, which round 38 had to correct: "1e-324" is below the smallest
	// subnormal, so it PARSES to zero, and nothing here can tell that from an exact zero
	// because both arrive as the same float64. See TestFigures_AnOperandThatUnderflowedToZero.
	if got := roundingSlack(0); got != math.SmallestNonzeroFloat64 {
		t.Errorf("roundingSlack(0) = %v, want the floor -- a decimal that underflowed to zero "+
			"cannot be told from an exact one", got)
	}
	// A normal value is unaffected, which is every figure any run has produced.
	if got, want := roundingSlack(1000), floatEps*1000; got != want {
		t.Errorf("roundingSlack(1000) = %v, want %v -- the floor must not touch normal values", got, want)
	}

	rows := []map[string]any{
		{"seg": "high", "amount": "1.1e-320"},
		{"seg": "low", "amount": "1e-320"},
	}
	f := models.Figure{
		ID: "f1", Value: 10, Unit: models.UnitPercent, Decimals: 2,
		Step: 1, Kind: models.FigureExcess,
		Column: "amount", Row: "seg = 'high'", Other: "seg = 'low'",
	}
	v := oneVerdict(t, f, evidence(1, rows))
	if v.Status == models.FigureFails {
		t.Errorf("status = fails (%s): the operands carry more error than a normal value, not "+
			"none, so nothing here can refute 10.00%%", v.Reason)
	}
	insights := []models.Insight{{
		Name: "subnormal", Figures: []models.Figure{f}, SourceSteps: []int{1},
		FigureVerdicts: []models.FigureVerdict{v},
	}}
	if n := correctRefutedFigures("area", insights, stepIndex(1, rows)); n != 0 {
		t.Errorf("%d corrections rewrote a correct figure: %+v", n, insights[0].FigureCorrections)
	}
}

// TestFigures_AnOperandThatUnderflowedToZero — r38 P3.
//
// Round 37 floored the rounding bound for subnormal operands but exempted zero, on the
// grounds that an exact zero has no representation error. Review walked through the
// exception: "1e-324" is below the smallest subnormal, so asFloat parses it to zero, and a
// numerator that had underflowed was then treated as exact. An excess correctly claiming
// -99.90% evaluated to -100% with a bound of zero, was refuted, and the correction gate --
// acting inside 1% -- replaced the correct value with -100% and certified it.
//
// Nothing at this point can tell an exact zero from a decimal that vanished into one, since
// both arrive as the same float64. So the floor is unconditional, which closes the class
// rather than this one instance of it.
func TestFigures_AnOperandThatUnderflowedToZero(t *testing.T) {
	rows := []map[string]any{
		{"seg": "high", "amount": "1e-324"},
		{"seg": "low", "amount": "1e-321"},
	}
	f := models.Figure{
		ID: "f1", Value: -99.90, Unit: models.UnitPercent, Decimals: 2,
		Step: 1, Kind: models.FigureExcess,
		Column: "amount", Row: "seg = 'high'", Other: "seg = 'low'",
	}
	v := oneVerdict(t, f, evidence(1, rows))
	if v.Status == models.FigureFails {
		t.Errorf("status = fails (%s): the numerator underflowed, so it carries error rather "+
			"than none, and -99.90%% cannot be refuted here", v.Reason)
	}
	insights := []models.Insight{{
		Name: "underflow", Figures: []models.Figure{f}, SourceSteps: []int{1},
		FigureVerdicts: []models.FigureVerdict{v},
	}}
	if n := correctRefutedFigures("area", insights, stepIndex(1, rows)); n != 0 {
		t.Errorf("%d corrections rewrote a correct figure: %+v", n, insights[0].FigureCorrections)
	}

	// And the floor never changes a comparison at a magnitude anything real uses.
	normal := models.Figure{
		ID: "f1", Value: 100, Unit: models.UnitPlain, Decimals: 0,
		Step: 1, Kind: models.FigureCell, Column: "amount", Row: "seg = 'x'",
	}
	exact := []map[string]any{{"seg": "x", "amount": 100.6}}
	if v := oneVerdict(t, normal, evidence(1, exact)); v.Status != models.FigureFails {
		t.Errorf("status = %q, want fails -- a cell 0.6 from a figure claiming half a unit is "+
			"refuted, floor or no floor", v.Status)
	}
}
