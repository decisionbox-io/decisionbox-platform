package discovery

import (
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// What a repaired insight may still lend to a recommendation.
//
// Repair edits prose. The figures it did not touch are still checked and still true, so
// withholding all of them punishes the whole insight for one edited sentence -- and a live
// run measured the cost: denied the figure it needed, the recommendation borrowed a
// verified-but-unrelated number from a neighbouring insight and left a second reference
// empty. So the rule is narrow: withhold only the figures whose rendered text the edit
// actually changed, which the repair record already knows.

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
