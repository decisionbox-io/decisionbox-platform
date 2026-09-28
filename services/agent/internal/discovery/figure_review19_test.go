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
