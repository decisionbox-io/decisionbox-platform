package discovery

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// The cases here are the three falsehoods session 12 shipped, the first live run of the
// recommendation figure layer. Every fixture is rows and declarations verbatim from that
// run, and every expected answer was settled independently against a DuckDB oracle built
// from the source .tbl bytes -- never from the run's own evidence, which is the thing
// under test.

// step17Rows is the material revenue table behind the run's refuted ratio: TIN is the
// highest and STEEL the lowest, and the insight's sentence said TIN was "above the lowest
// by only 0.77%".
//
// Oracle: 6645321129.9441 / 6594526319.0763 = 1.0077026, so the quotient is 100.77% and
// the excess is 0.77%.
func step17Rows() []map[string]any {
	return []map[string]any{
		{"material": "TIN", "net_rev": 6645321129.9441},
		{"material": "BRASS", "net_rev": 6641816775.8653},
		{"material": "COPPER", "net_rev": 6639848155.3871},
		{"material": "NICKEL", "net_rev": 6595240011.8071},
		{"material": "STEEL", "net_rev": 6594526319.0763},
	}
}

// step29Rows is the first-order-value quartile table behind the run's vouched falsehood.
//
// Oracle: 2377760.789459178 / 2169731.793297332 = 1.0958777, so the quotient is 109.59%
// and the excess -- which is what "109.6% more" claims -- is 9.59%.
func step29Rows() []map[string]any {
	return []map[string]any{
		{"first_val_quartile": 1.0, "custs": 24999.0, "avg_ltv": 2169731.793297332},
		{"first_val_quartile": 2.0, "custs": 24999.0, "avg_ltv": 2234156.3164526583},
		{"first_val_quartile": 3.0, "custs": 24999.0, "avg_ltv": 2291886.300097204},
		{"first_val_quartile": 4.0, "custs": 24999.0, "avg_ltv": 2377760.789459178},
	}
}

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

// notRefutedAndNotRewritten is what a figure sitting exactly on a rounding boundary is
// entitled to, and it is less than a vouch.
//
// The four cases below all state their evidence exactly and all print to a precision whose
// last place is the very gap the arithmetic cannot resolve. Certifying them would mean
// accepting agreement on overlapping intervals, which is what shipped a claim of 1 against a
// true total of 0.25 -- so they come back undecidable, and that is the honest answer.
//
// What matters is what was actually harmful: they were REFUTED, and the correction gate then
// read the tiny gap as the same quantity and rewrote correct prose to a wrong number. Neither
// happens now. The figure ships as the model wrote it, which is right, and nothing claims it
// was checked.
//
// The cost is measured rather than assumed: over both saved runs, 127 figures, not one moved
// from holds to undecidable. No real figure has landed on such a boundary.
func notRefutedAndNotRewritten(t *testing.T, f models.Figure, rows []map[string]any) models.FigureVerdict {
	t.Helper()
	v := oneVerdict(t, f, evidence(1, rows))
	if v.Status == models.FigureFails {
		t.Fatalf("status = fails (%s): the figure states exactly its own evidence", v.Reason)
	}
	insights := []models.Insight{{
		Name: "boundary", Figures: []models.Figure{f}, SourceSteps: []int{1},
		FigureVerdicts: []models.FigureVerdict{v},
	}}
	if n := correctRefutedFigures("area", insights, stepIndex(1, rows)); n != 0 {
		t.Errorf("%d corrections rewrote a correct figure: %+v", n, insights[0].FigureCorrections)
	}
	return v
}

// --- The repaired insight, and the eight true figures the blanket rule withdrew.

// repairedQuartileInsight is insight 8ac98428 of the run, in the shape it shipped.
//
// Eight figures, every one held and every one confirmed by the oracle. Repair dropped one
// indicator -- "Average spend rises monotonically with order count", which carries no
// figure reference at all -- and the name, the description and the two surviving
// indicators are byte-identical to what the renderer wrote.
func repairedQuartileInsight() models.Insight {
	figures := []models.Figure{
		{ID: "f2", Value: 17, Unit: models.UnitCount, Step: 7, Kind: models.FigureCell, Column: "customers", Row: "bucket = '1_order'"},
		{ID: "f6", Value: 52134, Unit: models.UnitCount, Step: 7, Kind: models.FigureCell, Column: "customers", Row: "bucket = '6_15_orders'"},
	}
	verdicts := []models.FigureVerdict{
		{ID: "f2", Status: models.FigureHolds, Claimed: 17, Evaluated: 17},
		{ID: "f6", Status: models.FigureHolds, Claimed: 52134, Evaluated: 52134},
	}
	tpl := &models.FigureTemplate{
		Name:        "Single-order customers are {{f2}} of the base",
		Description: "The bulk of value sits in the {{f6}} customers with 6-15 orders.",
		Indicators: []string{
			"Single-order customers number only {{f2}} of the whole base",
			"Average spend rises monotonically with order count",
		},
		Claims: []string{"average spend rises monotonically with order count"},
	}
	return models.Insight{
		ID:   "8ac98428-ac87-48cd-ba58-de179b524c66",
		Name: "Single-order customers are 17 of the base",
		// The description and the surviving indicator as the renderer wrote them.
		Description: "The bulk of value sits in the 52,134 customers with 6-15 orders.",
		Indicators:  []string{"Single-order customers number only 17 of the whole base"},
		Figures:     figures, FigureVerdicts: verdicts, FigureTemplate: tpl,
		Repair: &models.InsightRepair{
			Rounds: 1, Outcome: models.RepairClaimDropped,
			Dropped: []string{"average spend rises monotonically with order count"},
		},
	}
}

// TestFigures_RepairLendsTheFiguresItsEditDidNotReach is recommendation R5 of the run.
//
// The shipped sentence read "the largest bucket, 24,999 customers, buys 11-20 times" where
// the true count is 48,062. The reference resolved to a real, checked figure in a different
// insight -- a quartile size -- so the verdict said `holds` behind a false sentence.
//
// The cause was upstream of the recommendation. Repair had dropped one indicator from the
// insight holding the right number, the blanket rule withdrew all eight of that insight's
// figures from the recommender, and a model told to reference what it is shown, shown no id
// for the number it needed, took a wrong one. Withholding a true number is not the safe
// direction when the alternative is the model inventing a reference.
func TestFigures_RepairLendsTheFiguresItsEditDidNotReach(t *testing.T) {
	ins := repairedQuartileInsight()

	if broken := figuresBrokenByRepair(ins); len(broken) != 0 {
		t.Fatalf("figures withheld = %v, want none -- the dropped indicator carried no reference", broken)
	}

	// Advertised to the recommender, because withheld at both ends or neither.
	out := insightsForRecommenderPrompt([]models.Insight{ins})
	if len(out[0].Figures) != 2 {
		t.Errorf("the prompt advertises %d of 2 figures", len(out[0].Figures))
	}

	// And referenceable, with the value the insight itself shows.
	recs := []models.Recommendation{{
		Description: "Deepen frequency for the {{f1}} customers in the 6-15 band.",
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCount, Kind: models.FigureRefKind,
			Refs: []models.FigureRef{ref(ins.ID, "f6")},
		}},
	}}
	attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
	renderRecommendationFigures(recs)

	if v := recs[0].FigureVerdicts[0]; v.Status != models.FigureHolds {
		t.Errorf("status = %q (%s), want holds -- f6 held and its sentence is untouched", v.Status, v.Reason)
	}
	if !strings.Contains(recs[0].Description, "52,134") {
		t.Errorf("description = %q, want the checked value rendered", recs[0].Description)
	}
}

// TestFigures_RepairStillWithholdsTheFigureWhoseSentenceItRewrote keeps the reason the
// blanket rule existed.
//
// Repair substitutes a refuted count in place, so a figure can keep the value 12 behind a
// sentence now reading 302. That figure has no number a reader can see and must not be
// lent -- while a figure in a field repair never touched still says what the prose says.
func TestFigures_RepairStillWithholdsTheFigureWhoseSentenceItRewrote(t *testing.T) {
	ins := repairedQuartileInsight()
	// The description edited the way substituteRefutedCounts edits it: the rendered
	// numeral replaced, the template left as authored.
	ins.Description = "The bulk of value sits in the 302 customers with 6-15 orders."

	broken := figuresBrokenByRepair(ins)
	if !broken["f6"] {
		t.Error("f6 is lent although the sentence carrying it was rewritten")
	}
	if broken["f2"] {
		t.Error("f2 was withheld although no field referencing it changed")
	}

	out := insightsForRecommenderPrompt([]models.Insight{ins})
	if len(out[0].Figures) != 1 || out[0].Figures[0].ID != "f2" {
		t.Errorf("the prompt advertises %+v, want f2 alone", out[0].Figures)
	}

	recs := []models.Recommendation{{
		Description: "Address the {{f1}} customers.",
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCount, Kind: models.FigureRefKind,
			Refs: []models.FigureRef{ref(ins.ID, "f6")},
		}},
	}}
	attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
	renderRecommendationFigures(recs)

	if strings.Contains(recs[0].Description, "52,134") {
		t.Fatalf("the repaired-away number reached a second document: %q", recs[0].Description)
	}
	v := recs[0].FigureVerdicts[0]
	if v.Status != models.FigureUndecidable || v.Resolved {
		t.Errorf("verdict = %q resolved=%v, want undecidable and unresolved", v.Status, v.Resolved)
	}
}

// TestFigures_RepairIndicatorRemovalIsNoticed.
//
// The removal of a whole indicator is the edit the measured case turned on, and there it
// carried no reference. The same edit on an indicator that DOES carry one must withhold
// that figure: the number is not wrong, but it is no longer anywhere a reader can see it,
// and a recommendation restating it would show a number its own source does not.
func TestFigures_RepairIndicatorRemovalIsNoticed(t *testing.T) {
	ins := repairedQuartileInsight()
	// The surviving indicator -- the one carrying {{f2}} -- removed as well.
	ins.Indicators = nil

	broken := figuresBrokenByRepair(ins)
	if !broken["f2"] {
		t.Error("f2 is lent although the only sentence carrying it was removed")
	}
	if broken["f6"] {
		t.Error("f6 was withheld although the description carrying it is untouched")
	}
}

// TestFigures_RepairNoticesAnIndicatorRewrittenInPlace.
//
// substituteRefutedCounts edits an indicator's rendered text through a pointer, so the
// field is neither absent nor identical -- it is the same sentence with a different
// number, which is the shape the blanket rule was originally added for.
func TestFigures_RepairNoticesAnIndicatorRewrittenInPlace(t *testing.T) {
	ins := repairedQuartileInsight()
	ins.Indicators = []string{"Single-order customers number only 302 of the whole base"}

	if broken := figuresBrokenByRepair(ins); !broken["f2"] {
		t.Errorf("withheld = %v, want f2 -- its sentence now states a different number", broken)
	}
}

// TestFigures_RepairLendsAFigureCarriedOnlyByASurvivingIndicator.
//
// The indicator path is the one survival check that is not a direct string comparison --
// it matches as a multiset, because repair removes an indicator and the rest shift up. A
// red-proof found nothing covering it: every other fixture here also mentions its figures
// in the name or the description, so the indicator could stop clearing figures and no test
// would notice.
func TestFigures_RepairLendsAFigureCarriedOnlyByASurvivingIndicator(t *testing.T) {
	ins := repairedQuartileInsight()
	// Strip f2's other mention, so the surviving indicator is the only thing carrying it.
	ins.FigureTemplate.Name = "Order frequency is concentrated in the middle bands"
	ins.Name = ins.FigureTemplate.Name

	if broken := figuresBrokenByRepair(ins); broken["f2"] {
		t.Errorf("withheld = %v, want f2 lent -- the indicator carrying it is untouched", broken)
	}
}

// TestFigures_RepairWithNoTemplateLendsNothing.
//
// The template is what makes the question answerable: it is the authored prose, so
// re-rendering it says what the renderer wrote and therefore what repair changed. Without
// it there is nothing to compare, and the answer that withholds is the one that cannot
// ship a stale number.
func TestFigures_RepairWithNoTemplateLendsNothing(t *testing.T) {
	ins := repairedQuartileInsight()
	ins.FigureTemplate = nil

	broken := figuresBrokenByRepair(ins)
	if !broken["f2"] || !broken["f6"] {
		t.Errorf("withheld = %v, want every figure -- there is no audit trail to ask", broken)
	}
	if out := insightsForRecommenderPrompt([]models.Insight{ins}); len(out[0].Figures) != 0 {
		t.Errorf("the prompt advertises %+v, want nothing", out[0].Figures)
	}
}

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

// --- Review round 29, on the three fixes above.

// TestFigures_RepairWithholdsAFigureTheProseNeverReferenced — r29 P2.
//
// The blanket rule's replacement had a hole in the direction the blanket rule existed to
// cover. A model may declare a figure and then type its number into the sentence instead
// of referencing it; renderInsightFigures still records a template, and that template
// carries no placeholder for it. Repair rewriting "12 sub-categories" to "302" then leaves
// the figure with no reference to find broken, and it went on being lent at 12 -- the exact
// defect, reached by the path that has no reference at all.
//
// So a surviving reference is required, not merely the absence of a broken one.
func TestFigures_RepairWithholdsAFigureTheProseNeverReferenced(t *testing.T) {
	ins := models.Insight{
		ID: "11111111-2222-3333-4444-555555555555",
		// Repaired from 12. The model typed the number rather than referencing f1.
		Description: "302 sub-categories are loss-making.",
		Figures:     []models.Figure{{ID: "f1", Value: 12, Unit: models.UnitCount}},
		FigureVerdicts: []models.FigureVerdict{
			{ID: "f1", Status: models.FigureHolds, Claimed: 12, Evaluated: 12},
		},
		FigureTemplate: &models.FigureTemplate{
			Description: "12 sub-categories are loss-making.",
		},
		Repair: &models.InsightRepair{Rounds: 1, Outcome: models.RepairRepaired},
	}

	if broken := figuresBrokenByRepair(ins); !broken["f1"] {
		t.Errorf("withheld = %v, want f1 -- nothing in the prose references it, so nothing "+
			"establishes the value survived the repair", broken)
	}
	if out := insightsForRecommenderPrompt([]models.Insight{ins}); len(out[0].Figures) != 0 {
		t.Errorf("the prompt advertises %+v, want nothing", out[0].Figures)
	}

	recs := []models.Recommendation{{
		Description: "Address the {{f1}} loss-making sub-categories.",
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCount, Kind: models.FigureRefKind,
			Refs: []models.FigureRef{ref(ins.ID, "f1")},
		}},
	}}
	attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
	renderRecommendationFigures(recs)
	if strings.Contains(recs[0].Description, "12") {
		t.Errorf("the repaired-away number reached a second document: %q", recs[0].Description)
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

// --- Review round 30, on round 29's own fixes.

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

// TestFigures_RepairWithholdsAFigureThatNeverRendered — r30 P2.
//
// A figure usableFigures excluded -- no value, a duplicated id -- leaves its reference
// unresolved in the prose, and re-rendering reproduces the same placeholder. So the field
// reads as untouched and the figure as surviving, when the prose never showed a number for
// it at all. The prompt would advertise an id that buildFigureRefIndex separately rejects,
// which breaks "withheld at both ends or neither" from the other side.
func TestFigures_RepairWithholdsAFigureThatNeverRendered(t *testing.T) {
	// A duplicated id: usableFigures drops both declarations, so "{{f1}}" ships visible.
	dup := models.Insight{
		ID:          "11111111-2222-3333-4444-555555555555",
		Description: "{{f1}} sub-categories are loss-making, up from 302.",
		Figures: []models.Figure{
			{ID: "f1", Value: 12, Unit: models.UnitCount},
			{ID: "f1", Value: 19, Unit: models.UnitCount},
		},
		FigureTemplate: &models.FigureTemplate{
			Description: "{{f1}} sub-categories are loss-making, up from 302.",
		},
		Repair: &models.InsightRepair{Rounds: 1, Outcome: models.RepairRepaired},
	}
	if broken := figuresBrokenByRepair(dup); !broken["f1"] {
		t.Errorf("withheld = %v, want f1 -- its id is ambiguous, so the prose never rendered it", broken)
	}
	if out := insightsForRecommenderPrompt([]models.Insight{dup}); len(out[0].Figures) != 0 {
		t.Errorf("the prompt advertises %+v, which the reference index rejects", out[0].Figures)
	}

	// And a figure the model declared with no readable value, which would serialise into
	// the prompt as `value: 0`.
	missing := dup
	missing.Figures = []models.Figure{{ID: "f1", Unit: models.UnitCount, ValueMissing: true}}
	if broken := figuresBrokenByRepair(missing); !broken["f1"] {
		t.Errorf("withheld = %v, want f1 -- it declares no value to lend", broken)
	}
	if out := insightsForRecommenderPrompt([]models.Insight{missing}); len(out[0].Figures) != 0 {
		t.Errorf("the prompt advertises %+v with no value behind it", out[0].Figures)
	}
}

// --- Review round 31, on round 30's own fix.

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

// TestFigures_RepairKeysWithheldFiguresByTheirDeclaredIDs — r31 P2.
//
// figuresBrokenByRepair trimmed ids when recording them and both callers look them up
// untrimmed, so an id of " f1 " was recorded under "f1", found under neither, and kept. A
// blank id was skipped outright, same result. Either way the prompt advertised a figure the
// renderer cannot resolve and reference resolution rejects, so the recommendation shipped a
// visible placeholder.
func TestFigures_RepairKeysWithheldFiguresByTheirDeclaredIDs(t *testing.T) {
	for _, id := range []string{" f1 ", "", "revenue-total"} {
		ins := models.Insight{
			ID:             "11111111-2222-3333-4444-555555555555",
			Description:    "302 sub-categories are loss-making.",
			Figures:        []models.Figure{{ID: id, Value: 12, Unit: models.UnitCount}},
			FigureTemplate: &models.FigureTemplate{Description: "12 sub-categories are loss-making."},
			Repair:         &models.InsightRepair{Rounds: 1, Outcome: models.RepairRepaired},
		}
		if broken := figuresBrokenByRepair(ins); !broken[id] {
			t.Errorf("id %q: withheld = %v, want it keyed by the id as declared", id, broken)
		}
		if out := insightsForRecommenderPrompt([]models.Insight{ins}); len(out[0].Figures) != 0 {
			t.Errorf("id %q: the prompt advertises %+v, which nothing can resolve", id, out[0].Figures)
		}
		// Same for the branch with no audit trail to consult.
		ins.FigureTemplate = nil
		if broken := figuresBrokenByRepair(ins); !broken[id] {
			t.Errorf("id %q with no template: withheld = %v, want every figure", id, broken)
		}
	}
}

// --- Review round 32. The last of four attempts at one number, and the one that stopped
// guessing it.

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

// --- Review round 33, on round 32's own bound.

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

// --- Review round 34. The error bound cuts both ways.

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

// --- Review round 36.

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

// --- Review round 37.

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
	if got := roundingSlack(0); got != 0 {
		t.Errorf("roundingSlack(0) = %v, want zero -- an exact zero has no representation error", got)
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

// --- The malformed reference.

// TestRecommendationFigures_AnEmptyReferenceIsTheRecommendationsFault is the second half
// of R5 of the run: the same recommendation emitted `refs: [{"figure": ""}]` for another
// figure and shipped "{{f1}}" twice.
//
// The marker shipping is correct -- there is no number, and a reader seeing "{{f1}}" knows
// something went wrong. What was wrong was the verdict: an empty id fell through to the
// figure lookup and reported "insight 8ac98428 declares no figure", blaming a document
// that declares eight for a reference that named none of them. A reader of the audit trail
// inherits whichever of those it says.
func TestRecommendationFigures_AnEmptyReferenceIsTheRecommendationsFault(t *testing.T) {
	ins := bandInsight()
	recs := []models.Recommendation{{
		Description: "Win back the {{f1}} buyers.",
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCount, Kind: models.FigureRefKind,
			Refs: []models.FigureRef{ref(ins.ID, "")},
		}},
	}}
	attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
	renderRecommendationFigures(recs)

	v := recs[0].FigureVerdicts[0]
	if v.Status != models.FigureUndecidable {
		t.Fatalf("status = %q, want undecidable", v.Status)
	}
	if strings.Contains(v.Reason, "declares no figure") {
		t.Errorf("reason = %q, which blames the insight for a reference that named no figure", v.Reason)
	}
	if !strings.Contains(v.Reason, "figure id") {
		t.Errorf("reason = %q, want it to say the reference names no usable figure id", v.Reason)
	}
	// The reference ships visible rather than as a number, which is unchanged.
	if !strings.Contains(recs[0].Description, "{{f1}}") {
		t.Errorf("description = %q, want the unresolved reference left visible", recs[0].Description)
	}
}

// TestRecommendationFigures_AReferenceNamingNoInsightSaysSo.
//
// The other half of a reference. An empty insight id used to fall through to the index
// lookup and report "insight  is not among the insights this recommendation was given",
// which reads as a citation that went stale rather than a reference that named nothing.
func TestRecommendationFigures_AReferenceNamingNoInsightSaysSo(t *testing.T) {
	recs := []models.Recommendation{{
		Description: "Win back the {{f1}} buyers.",
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCount, Kind: models.FigureRefKind,
			Refs: []models.FigureRef{ref("", "f1")},
		}},
	}}
	attachRecommendationFigureVerdicts(recs, []models.Insight{bandInsight()})

	v := recs[0].FigureVerdicts[0]
	if v.Status != models.FigureUndecidable {
		t.Fatalf("status = %q, want undecidable", v.Status)
	}
	if !strings.Contains(v.Reason, "names no insight") {
		t.Errorf("reason = %q, want it to say the reference names no insight", v.Reason)
	}
}

// TestRecommendationFigures_AnIdOutsideTheGrammarIsRefused.
//
// The renderer resolves `[A-Za-z][A-Za-z0-9_]*` and the evaluator requires it of a
// figure's own id. A reference is held to the same grammar, so the two halves of this
// layer cannot disagree about what an id is -- the defect that let `id: "revenue-total"`
// hold while its placeholder shipped.
func TestRecommendationFigures_AnIdOutsideTheGrammarIsRefused(t *testing.T) {
	ins := bandInsight()
	for _, bad := range []string{"revenue-total", "1f", " f1", "f1 "} {
		recs := []models.Recommendation{{
			Description: "Win back the {{f1}} buyers.",
			Figures: []models.Figure{{
				ID: "f1", Unit: models.UnitCount, Kind: models.FigureRefKind,
				Refs: []models.FigureRef{ref(ins.ID, bad)},
			}},
		}}
		attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
		v := recs[0].FigureVerdicts[0]
		if v.Status != models.FigureUndecidable || !strings.Contains(v.Reason, "figure id") {
			t.Errorf("reference to %q gave %q (%s), want undecidable naming the id grammar", bad, v.Status, v.Reason)
		}
	}
}
