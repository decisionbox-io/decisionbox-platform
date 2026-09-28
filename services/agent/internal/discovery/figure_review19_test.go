package discovery

// The five concerns codex review round 19 raised against the figure layer. Each was
// verified in the code before being accepted, and each is pinned here.

import (
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// TestFigures_QuantifierClaimTextIsRenderedToo — P1.
//
// The quantifier contract asks for the claim verbatim as written in the prose, and the
// figure contract has the prose carrying references, so a model obeying both writes
// "{{f1}} of customers" into the claim. Every consumer matches claim text against the
// rendered fields: insightMentions, dropClaimSentence, substituteRefutedCounts. An
// unrendered claim matches none of them, so a refuted sentence survives while its
// declaration is recorded as withdrawn -- a repair reporting a fix it did not make.
func TestFigures_QuantifierClaimTextIsRenderedToo(t *testing.T) {
	ins := []models.Insight{{
		Name:        "Zero-order accounts are {{f1}} of the base",
		Description: "{{f1}} of registered customers have never ordered.",
		Figures: []models.Figure{
			{ID: "f1", Value: 33.34, Unit: models.UnitPercent, Decimals: 2, Step: 6, Kind: models.FigureCell},
		},
		QuantifierClaims: []models.QuantifierClaim{
			{Claim: "{{f1}} of registered customers have never ordered.", Kind: "share", Step: 6},
		},
	}}

	renderInsightFigures(ins)

	got := ins[0].QuantifierClaims[0].Claim
	if strings.Contains(got, "{{") {
		t.Fatalf("claim text still carries a reference: %q", got)
	}
	if !strings.Contains(got, "33.34%") {
		t.Errorf("claim = %q, want the rendered figure", got)
	}
	// The whole point: the claim must be findable in the prose the reader sees, because
	// that is how every repair path locates the sentence it is about.
	if !insightMentions(ins[0], got) {
		t.Errorf("the rendered claim %q cannot be found in the rendered insight, so no repair path can act on it", got)
	}
	// And the authored form is kept, so it stays checkable that the number came from a
	// declaration rather than from the model typing it.
	if tpl := ins[0].FigureTemplate; tpl == nil || len(tpl.Claims) != 1 || !strings.Contains(tpl.Claims[0], "{{f1}}") {
		t.Errorf("the template did not keep the authored claim: %+v", ins[0].FigureTemplate)
	}
}

// TestFigures_RepairedInsightCannotLendItsFigures — P1.
//
// Repair runs after the figures are rendered and it edits text without touching any
// figure. So a figure can pass its own check, have its sentence repaired to a different
// number, and keep the old value -- and a reference with no declared value then adopts it,
// putting the corrected error into a second document.
func TestFigures_RepairedInsightCannotLendItsFigures(t *testing.T) {
	stale := models.Insight{
		ID:          "11111111-2222-3333-4444-555555555555",
		Description: "302 sub-categories are loss-making.", // repaired from 12
		Figures:     []models.Figure{{ID: "f1", Value: 12, Unit: models.UnitCount}},
		FigureVerdicts: []models.FigureVerdict{
			{ID: "f1", Status: models.FigureHolds, Claimed: 12, Evaluated: 12},
		},
		Repair: &models.InsightRepair{Rounds: 1, Outcome: "claim_fixed"},
	}
	recs := []models.Recommendation{{
		Description: "Address the {{f1}} loss-making sub-categories.",
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCount, Kind: models.FigureRefKind,
			Refs: []models.FigureRef{ref(stale.ID, "f1")},
		}},
	}}

	attachRecommendationFigureVerdicts(recs, []models.Insight{stale})
	renderRecommendationFigures(recs)

	v := recs[0].FigureVerdicts[0]
	if v.Status != models.FigureUndecidable {
		t.Fatalf("status = %q, want undecidable -- a repaired insight's figures no longer track its prose", v.Status)
	}
	if !strings.Contains(v.Reason, "repaired") {
		t.Errorf("reason = %q, want it to name the repair", v.Reason)
	}
	if len(recs[0].FigureCorrections) != 0 {
		t.Errorf("a stale figure must not be recorded as a correction: %v", recs[0].FigureCorrections)
	}
}

// TestFigures_ReferenceCannotClaimFinerPrecisionThanItsSource — P2.
//
// A `holds` verdict says the arithmetic landed inside the interval the insight PRINTED, and
// nothing narrower. "~911K" at the thousands scale vouches for the value to within 500, so
// a recommendation rewriting it unscaled as "911,000" would assert a half-unit on a number
// checked to five hundred.
func TestFigures_ReferenceCannotClaimFinerPrecisionThanItsSource(t *testing.T) {
	source := models.Insight{
		ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Figures: []models.Figure{
			{ID: "f1", Value: 911000, Unit: models.UnitCount, Scale: models.ScaleThousands, Decimals: 0, Approx: true},
		},
		FigureVerdicts: []models.FigureVerdict{{ID: "f1", Status: models.FigureHolds}},
	}

	// Finer than the source: unscaled, so a half-unit interval.
	fine := []models.Recommendation{{
		Description: "The catalogue holds {{f1}} lines.",
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCount, Kind: models.FigureRefKind,
			Refs: []models.FigureRef{ref(source.ID, "f1")},
		}},
	}}
	attachRecommendationFigureVerdicts(fine, []models.Insight{source})
	renderRecommendationFigures(fine)
	if v := fine[0].FigureVerdicts[0]; v.Status != models.FigureUndecidable {
		t.Errorf("status = %q, want undecidable: this restates to ±0.5 a figure checked to ±500", v.Status)
	}
	// The number still reaches the reader -- it is what the insight itself shows.
	if !strings.Contains(fine[0].Description, "911,000") {
		t.Errorf("description = %q, want the source's number carried", fine[0].Description)
	}

	// At the source's own precision it holds.
	same := []models.Recommendation{{
		Description: "The catalogue holds {{f1}} lines.",
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCount, Scale: models.ScaleThousands, Decimals: 0,
			Kind: models.FigureRefKind, Refs: []models.FigureRef{ref(source.ID, "f1")},
		}},
	}}
	attachRecommendationFigureVerdicts(same, []models.Insight{source})
	if v := same[0].FigureVerdicts[0]; v.Status != models.FigureHolds {
		t.Errorf("status = %q (%s), want holds at the source's own precision", v.Status, v.Reason)
	}
}

// TestFigures_PercentEvaluationIsRecordedInTheFiguresOwnTerms — P2.
//
// A share is stored either as a fraction or as a percentage and the figure cannot see
// which, so both readings are tried. Recording the raw value regardless left the correction
// gate comparing a claim of 14.15% against an evaluated 0.1414, calling them different
// quantities, and declining a correction to 14.14% that sat well inside its 1% limit.
func TestFigures_PercentEvaluationIsRecordedInTheFiguresOwnTerms(t *testing.T) {
	f := models.Figure{
		ID: "f1", Value: 14.15, Unit: models.UnitPercent, Decimals: 2,
		Step: 3, Kind: models.FigureCell, Column: "share", Row: "band = A",
	}
	rows := []map[string]any{{"band": "A", "share": 0.1414}}
	v := evaluateFigure(f, map[int]StepRows{3: {Rows: rows}})

	if v.Status != models.FigureFails {
		t.Fatalf("status = %q, want fails -- 14.15 is outside the ±0.005 it claims", v.Status)
	}
	if v.Evaluated < 14 || v.Evaluated > 15 {
		t.Fatalf("evaluated = %v, want the percentage reading (~14.14) rather than the raw fraction", v.Evaluated)
	}
	// And now the correction gate can see that these are two readings of one quantity.
	if !sameQuantity(v.Claimed, v.Evaluated) {
		t.Errorf("sameQuantity(%v, %v) = false, so the correction is declined over a unit mismatch", v.Claimed, v.Evaluated)
	}
}

// TestFigures_FilledInValueRefreshesTheVerdictDisplay — P2.
//
// On the contract-compliant path a recommendation figure omits its value, so the verdict's
// display is computed from a zero. Filling the value in without re-settling left the stored
// audit record reading `display: "0"` on a figure whose claimed and evaluated values were
// both 52134 -- and that is every compliant figure, since the contract is what tells the
// model to omit it.
func TestFigures_FilledInValueRefreshesTheVerdictDisplay(t *testing.T) {
	ins := bandInsight()
	recs := []models.Recommendation{{
		Description: "The band holds {{f1}} customers.",
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCount, Kind: models.FigureRefKind,
			Refs: []models.FigureRef{ref(ins.ID, "f1")},
		}},
	}}

	attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
	renderRecommendationFigures(recs)

	v := recs[0].FigureVerdicts[0]
	if v.Display == "0" || v.Display == "" {
		t.Fatalf("verdict display = %q, but the figure renders 52,134; the audit record contradicts the prose", v.Display)
	}
	if !strings.Contains(v.Display, "52,134") {
		t.Errorf("display = %q, want 52,134", v.Display)
	}
	if v.Status != models.FigureHolds {
		t.Errorf("status = %q, want holds", v.Status)
	}
}

// --- Review round 20. One of these is a defect in round 19's own fix, and one is a
// finding from round 19 I miscounted and did not fix, which round 20 raised again.

// TestFigures_RepairedInsightLendsNoNumberAtAll — round 20, on round 19's fix.
//
// That fix withheld `vouched` but left `found` true, so it withheld the verdict and handed
// over the number anyway: a figure still stating 12 behind a sentence repaired to 302 was
// filled into the recommendation and rendered as 12.
//
// The distinction it missed is between the two unvouched cases. A refuted figure Go declined
// to correct is still on the page, so restating it matches what the reader sees. A repaired
// one is not -- repair rewrote that sentence -- so there is no number to restate.
func TestFigures_RepairedInsightLendsNoNumberAtAll(t *testing.T) {
	stale := models.Insight{
		ID:          "11111111-2222-3333-4444-555555555555",
		Description: "302 sub-categories are loss-making.", // repaired from 12
		Figures:     []models.Figure{{ID: "f1", Value: 12, Unit: models.UnitCount}},
		FigureVerdicts: []models.FigureVerdict{
			{ID: "f1", Status: models.FigureHolds, Claimed: 12, Evaluated: 12},
		},
		Repair: &models.InsightRepair{Rounds: 1, Outcome: "claim_fixed"},
	}
	recs := []models.Recommendation{{
		Description: "Address the {{f1}} loss-making sub-categories.",
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCount, Kind: models.FigureRefKind,
			Refs: []models.FigureRef{ref(stale.ID, "f1")},
		}},
	}}

	attachRecommendationFigureVerdicts(recs, []models.Insight{stale})
	renderRecommendationFigures(recs)

	if strings.Contains(recs[0].Description, "12") {
		t.Fatalf("the repaired-away number reached a second document: %q", recs[0].Description)
	}
	v := recs[0].FigureVerdicts[0]
	if v.Resolved {
		t.Error("resolved = true, but a repaired insight has no number behind that reference")
	}
	if v.Status != models.FigureUndecidable {
		t.Errorf("status = %q, want undecidable", v.Status)
	}
	if recs[0].Figures[0].Value != 0 {
		t.Errorf("figure value = %v, want it left unfilled", recs[0].Figures[0].Value)
	}
}

// TestFigures_ToleranceIsCentredOnWhatWasPrinted — raised in round 19, missed, re-raised in
// round 20.
//
// The interval is half the last place printed, but it was measured from Value, which can
// hold more precision than the format shows. 100,490,000 at the millions scale with no
// decimals prints "$100M". Evidence of 100,510,000 sits 20,000 from the value and 510,000
// from what was printed — so the check passed while the sentence said $100M and the
// evidence said $101M.
func TestFigures_ToleranceIsCentredOnWhatWasPrinted(t *testing.T) {
	f := models.Figure{
		ID: "f1", Value: 100490000, Unit: models.UnitCurrency, Scale: models.ScaleMillions, Decimals: 0,
		Step: 1, Kind: models.FigureCell, Column: "net", Row: "region = A",
	}
	if got := renderFigure(f); got != "$100M" {
		t.Fatalf("renderFigure = %q, want $100M -- this test's premise is stale", got)
	}
	v := evaluateFigure(f, map[int]StepRows{1: {Rows: []map[string]any{{"region": "A", "net": 100510000.0}}}})
	if v.Status != models.FigureFails {
		t.Errorf("status = %q, want fails: the prose says $100M and the evidence rounds to $101M", v.Status)
	}

	// And the honest case still holds: evidence inside the printed interval.
	v = evaluateFigure(f, map[int]StepRows{1: {Rows: []map[string]any{{"region": "A", "net": 100010000.0}}}})
	if v.Status != models.FigureHolds {
		t.Errorf("status = %q (%s), want holds: $100M covers 100,010,000", v.Status, v.Reason)
	}
}

// TestFigures_SumOfEquallyCoarseOperandsIsStatableAtTheirPrecision.
//
// Round 20 asked for operand uncertainty to accumulate, and accumulating it into the
// precision guard broke a working case: two exact counts each carry the half-unit interval
// every unscaled whole number carries, and summed they made their own total unstatable at
// the precision both operands already had.
//
// The two questions are separate. Whether a figure is printed finer than any operand was
// verified to is about printed places, so it is a maximum. How far the computed total can
// sit from the evidence does accumulate, so it widens the agreement check instead.
func TestFigures_SumOfEquallyCoarseOperandsIsStatableAtTheirPrecision(t *testing.T) {
	ins := bandInsight() // two unscaled counts, 52134 and 43897
	recs := []models.Recommendation{{
		Description: "{{f1}} buyers in the two highest bands.",
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCount, Kind: models.FigureSum,
			Refs: []models.FigureRef{ref(ins.ID, "f1"), ref(ins.ID, "f2")},
		}},
	}}
	attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
	renderRecommendationFigures(recs)

	if v := recs[0].FigureVerdicts[0]; v.Status != models.FigureHolds {
		t.Errorf("status = %q (%s), want holds -- summing two counts must not make their total unstatable", v.Status, v.Reason)
	}
	if !strings.Contains(recs[0].Description, "96,031") {
		t.Errorf("description = %q, want the total", recs[0].Description)
	}

	// But a genuinely coarser source still blocks a finer restatement.
	coarse := models.Insight{
		ID:             "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Figures:        []models.Figure{{ID: "f1", Value: 911395, Unit: models.UnitCount, Scale: models.ScaleThousands, Decimals: 0}},
		FigureVerdicts: []models.FigureVerdict{{ID: "f1", Status: models.FigureHolds}},
	}
	fine := []models.Recommendation{{
		Description: "{{f1}} lines.",
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCount, Kind: models.FigureRefKind,
			Refs: []models.FigureRef{ref(coarse.ID, "f1")},
		}},
	}}
	attachRecommendationFigureVerdicts(fine, []models.Insight{coarse})
	if v := fine[0].FigureVerdicts[0]; v.Status != models.FigureUndecidable {
		t.Errorf("status = %q, want undecidable for a finer restatement of a coarser source", v.Status)
	}
}

// TestFigures_ModelAuthoredInsightTemplateIsCleared.
//
// An insight with no figures and no references is skipped by renderInsightFigures, so a
// model-authored `evidence_figure_template` survived persistence and API serialisation as
// the platform's own provenance record -- the model authoring the proof that its numbers
// came from declarations. The recommendation pass already cleared all three derived fields;
// the insight pass cleared two.
func TestFigures_ModelAuthoredInsightTemplateIsCleared(t *testing.T) {
	ins := []models.Insight{{
		Name:           "Revenue grew last year",
		FigureTemplate: &models.FigureTemplate{Name: "Revenue grew {{f1}} last year"},
		FigureVerdicts: []models.FigureVerdict{{ID: "f1", Status: models.FigureHolds}},
	}}
	attachFigureVerdicts(ins, map[int]*models.ExplorationStep{})
	if ins[0].FigureTemplate != nil {
		t.Errorf("a model-authored provenance record survived: %+v", ins[0].FigureTemplate)
	}
	if ins[0].FigureVerdicts != nil {
		t.Errorf("a model-authored verdict survived: %+v", ins[0].FigureVerdicts)
	}
}

// TestFigures_TallyCountsAnInsightWhoseEveryReferenceFailed.
//
// The orchestrator gated the figure tally on resolved-or-inlined, which discarded it in the
// one case it mattered most: an area where every reference failed to resolve reported
// FigureRefsUnresolved as zero, reading as nothing went wrong.
func TestFigures_TallyCountsAnInsightWhoseEveryReferenceFailed(t *testing.T) {
	ins := []models.Insight{{
		Name:        "Revenue reached {{f1}}",
		Description: "No figures were declared at all.",
	}}
	tally := renderInsightFigures(ins)
	if tally.unresolved != 1 {
		t.Errorf("tally.unresolved = %d, want 1", tally.unresolved)
	}
	if tally.resolved != 0 || tally.inlined != 0 {
		t.Fatalf("premise stale: resolved=%d inlined=%d; this is the combination the orchestrator's guard discarded",
			tally.resolved, tally.inlined)
	}
}
