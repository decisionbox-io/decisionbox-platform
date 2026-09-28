package discovery

// The five concerns codex review round 19 raised against the figure layer. Each was
// verified in the code before being accepted, and each is pinned here.

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"

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

// --- Review round 21.

// TestFigures_AggregateOverIncompleteEvidenceIsUndecidable — round 21, P1, and the one place
// in this layer where getting it wrong does active harm rather than failing to catch
// something.
//
// The quantifier evaluator has refused incomplete evidence since the truncation work. This
// path read the same StepRows and ignored the same caveats, so a `sum` over a capped result
// gave a partial total, the figure stating the TRUE total was refuted for disagreeing with
// it, and the correction gate -- which acts only inside 1%, exactly where a small truncation
// lands -- replaced the correct number with the partial one and re-settled it to `holds`.
func TestFigures_AggregateOverIncompleteEvidenceIsUndecidable(t *testing.T) {
	// Nine of ten rows came back; the tenth is the one the cap dropped.
	rows := make([]map[string]any, 0, 9)
	for i := 0; i < 9; i++ {
		rows = append(rows, map[string]any{"region": i, "net": 100.0})
	}
	truncated := map[int]StepRows{
		7: {Rows: rows, Quality: []gowarehouse.QualityCaveat{{Kind: gowarehouse.QualityTruncated}}},
	}

	// The model declared the true total of 1000, which no longer matches the 900 visible.
	sum := models.Figure{ID: "f1", Value: 1000, Unit: models.UnitCurrency, Step: 7, Kind: models.FigureSum, Column: "net"}
	v := evaluateFigure(sum, truncated)
	if v.Status != models.FigureUndecidable {
		t.Fatalf("status = %q, want undecidable -- a capped result cannot settle a total", v.Status)
	}

	// And nothing corrects it, so the true number survives.
	ins := []models.Insight{{Name: "n", Figures: []models.Figure{sum}, FigureVerdicts: []models.FigureVerdict{v}}}
	correctRefutedFigures("area", ins, map[int]*models.ExplorationStep{})
	if got := ins[0].Figures[0].Value; got != 1000 {
		t.Errorf("figure value = %v, want the declared 1000 left alone; a partial sum must never overwrite a total", got)
	}

	// A row-specific kind is unaffected: the row it names is either present or it is not.
	cell := models.Figure{ID: "f2", Value: 100, Unit: models.UnitCurrency, Step: 7,
		Kind: models.FigureCell, Column: "net", Row: "region = 3"}
	if got := evaluateFigure(cell, truncated); got.Status != models.FigureHolds {
		t.Errorf("cell status = %q (%s), want holds -- a named row does not need the whole result", got.Status, got.Reason)
	}
}

// TestFigures_CorrectionCarriesItsCardinalityDeclaration — round 21.
//
// A cardinality claim holds its number twice: in its text, which by now is a reference, and
// in its own Count field, which the quantifier evaluator reads. A figure corrected from 101
// to 100 left the text rendering 100 and Count saying 101, so the next pass refuted the
// sentence for disagreeing with a number it no longer contained -- and substituteRefutedCounts
// cannot undo that, because the numeral it would look for is already gone.
func TestFigures_CorrectionCarriesItsCardinalityDeclaration(t *testing.T) {
	step := &models.ExplorationStep{Step: 4, QueryResult: []map[string]any{}}
	for i := 0; i < 100; i++ {
		step.QueryResult = append(step.QueryResult, map[string]any{"id": i})
	}
	ins := []models.Insight{{
		Name:        "There are {{f1}} distinct parts",
		SourceSteps: []int{4},
		Figures:     []models.Figure{{ID: "f1", Value: 101, Unit: models.UnitCount, Step: 4, Kind: models.FigureCount}},
		QuantifierClaims: []models.QuantifierClaim{
			{Claim: "There are {{f1}} distinct parts", Kind: QuantifierCardinality, Step: 4, Count: 101},
		},
	}}
	byID := map[int]*models.ExplorationStep{4: step}

	attachFigureVerdicts(ins, byID)
	if n := correctRefutedFigures("area", ins, byID); n != 1 {
		t.Fatalf("corrections = %d, want 1 (101 -> 100); premise stale", n)
	}
	if got := ins[0].QuantifierClaims[0].Count; got != 100 {
		t.Errorf("claim Count = %d, want 100 -- the declaration must move with the figure it was written from", got)
	}
}

// TestFigures_RepairedInsightAdvertisesNoFiguresToTheRecommender — round 21, and a direct
// consequence of round 20's fix.
//
// buildFigureRefIndex refuses every figure of a repaired insight. Leaving them in the prompt
// advertised ids guaranteed to resolve to nothing, so a model following the contract exactly
// would ship "{{f1}}" in its prose. Withheld at both ends or neither.
func TestFigures_RepairedInsightAdvertisesNoFiguresToTheRecommender(t *testing.T) {
	in := []models.Insight{
		{
			ID: "repaired", Name: "302 sub-categories are loss-making",
			Figures: []models.Figure{{ID: "f1", Value: 12, Unit: models.UnitCount}},
			Repair:  &models.InsightRepair{Rounds: 1, Outcome: "claim_fixed"},
		},
		{
			ID: "clean", Name: "A sound finding",
			Figures: []models.Figure{{ID: "f1", Value: 50004, Unit: models.UnitCount}},
		},
	}
	out := insightsForRecommenderPrompt(in)

	if len(out[0].Figures) != 0 {
		t.Errorf("a repaired insight still advertises figures the index will reject: %+v", out[0].Figures)
	}
	if len(out[1].Figures) != 1 {
		t.Errorf("a clean insight lost its figures: %+v", out[1].Figures)
	}
	// The originals are untouched, because they are what gets stored.
	if len(in[0].Figures) != 1 {
		t.Error("the stored insight's figures were mutated")
	}
}

// TestFigures_NumbersWrittenAsStringsStillDecode — round 21.
//
// Small and open models emit numbers as strings, and the insight and recommendation decoders
// have coerced that since issue #342, where one off-typed field silently zeroed an area's
// findings. Figures nest inside those structs and were decoded strictly regardless, so one
// mistyped figure discarded the whole insight it belonged to.
func TestFigures_NumbersWrittenAsStringsStillDecode(t *testing.T) {
	const body = `{"name":"n","description":"d","figures":[
      {"id":"f1","value":"1,234.5","step":"48","decimals":"2","unit":"currency","kind":"cell"},
      {"id":"f2","value":100,"step":48,"kind":"sum"}]}`

	var ins models.Insight
	if err := json.Unmarshal([]byte(body), &ins); err != nil {
		t.Fatalf("a string-typed figure failed the whole insight: %v", err)
	}
	if len(ins.Figures) != 2 {
		t.Fatalf("figures = %d, want 2", len(ins.Figures))
	}
	if ins.Figures[0].Value != 1234.5 || ins.Figures[0].Step != 48 || ins.Figures[0].Decimals != 2 {
		t.Errorf("coerced figure = %+v, want value 1234.5 step 48 decimals 2", ins.Figures[0])
	}
	if ins.Figures[1].Value != 100 || ins.Figures[1].Step != 48 {
		t.Errorf("well-typed figure changed: %+v", ins.Figures[1])
	}
}

// --- Review round 22. All three follow from the round-20 and round-21 fixes.

// TestFigures_ReferenceIndexesWhatWasPrinted — round 22.
//
// `holds` compares the RENDERED value against the evidence, so that is what it vouches for.
// Indexing the raw value with the printed interval mixed two descriptions of one figure: two
// figures of 1490 at the thousands scale both print 1K and both hold against evidence of
// 510, and summing their raw values gives 2980 -- rendered 3K, against evidence totalling
// 1020, which is outside even the accumulated drift.
func TestFigures_ReferenceIndexesWhatWasPrinted(t *testing.T) {
	coarse := models.Insight{
		ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Figures: []models.Figure{
			{ID: "f1", Value: 1490, Unit: models.UnitCount, Scale: models.ScaleThousands, Decimals: 0},
			{ID: "f2", Value: 1490, Unit: models.UnitCount, Scale: models.ScaleThousands, Decimals: 0},
		},
		FigureVerdicts: []models.FigureVerdict{
			{ID: "f1", Status: models.FigureHolds},
			{ID: "f2", Status: models.FigureHolds},
		},
	}
	recs := []models.Recommendation{{
		Description: "Together they hold {{f1}}.",
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCount, Scale: models.ScaleThousands, Decimals: 0,
			Kind: models.FigureSum,
			Refs: []models.FigureRef{ref(coarse.ID, "f1"), ref(coarse.ID, "f2")},
		}},
	}}

	attachRecommendationFigureVerdicts(recs, []models.Insight{coarse})
	renderRecommendationFigures(recs)

	// 1000 + 1000, the two printed values, not 1490 + 1490.
	if got := recs[0].Figures[0].Value; got != 2000 {
		t.Errorf("sum = %v, want 2000 from the two printed values; the raw values give 2980", got)
	}
	if !strings.Contains(recs[0].Description, "2K") {
		t.Errorf("description = %q, want 2K", recs[0].Description)
	}
}

// TestFigures_CorrectionDeclinesWhenAffectedCountWouldDesync — round 22.
//
// substituteCount already declines exactly this, and says why: affected_count is an int no
// text pass touches, correcting the prose around it leaves the document disagreeing with its
// own structured field, and overwriting it is the other error because "entities affected"
// need not be the quantity a matching figure counts.
func TestFigures_CorrectionDeclinesWhenAffectedCountWouldDesync(t *testing.T) {
	step := &models.ExplorationStep{Step: 4, QueryResult: []map[string]any{}}
	for i := 0; i < 99996; i++ {
		step.QueryResult = append(step.QueryResult, map[string]any{"id": i})
	}
	byID := map[int]*models.ExplorationStep{4: step}

	ins := []models.Insight{{
		Name:          "All {{f1}} buyers",
		SourceSteps:   []int{4},
		AffectedCount: 100000,
		Figures:       []models.Figure{{ID: "f1", Value: 100000, Unit: models.UnitCount, Step: 4, Kind: models.FigureCount}},
	}}
	attachFigureVerdicts(ins, byID)
	if ins[0].FigureVerdicts[0].Status != models.FigureFails {
		t.Fatalf("premise stale: verdict = %q, want fails", ins[0].FigureVerdicts[0].Status)
	}
	if n := correctRefutedFigures("area", ins, byID); n != 0 {
		t.Errorf("corrections = %d, want 0 -- correcting here desyncs affected_count", n)
	}
	if ins[0].Figures[0].Value != 100000 || ins[0].AffectedCount != 100000 {
		t.Errorf("figure %v / affected_count %d -- the two must not be left disagreeing",
			ins[0].Figures[0].Value, ins[0].AffectedCount)
	}
}

// TestFigures_ClaimReconciliationAcceptsASpacedReference — round 22.
//
// The renderer accepts "{{ f1 }}"; the reconciliation matched only "{{f1}}". So a spaced
// reference rendered the corrected sentence while its declaration kept the old count, which
// is the very defect the reconciliation was added to prevent -- put back by matching the
// reference differently from the code that resolves it.
func TestFigures_ClaimReconciliationAcceptsASpacedReference(t *testing.T) {
	step := &models.ExplorationStep{Step: 4, QueryResult: []map[string]any{}}
	for i := 0; i < 100; i++ {
		step.QueryResult = append(step.QueryResult, map[string]any{"id": i})
	}
	byID := map[int]*models.ExplorationStep{4: step}

	ins := []models.Insight{{
		Name:        "There are {{ f1 }} distinct parts",
		SourceSteps: []int{4},
		Figures:     []models.Figure{{ID: "f1", Value: 101, Unit: models.UnitCount, Step: 4, Kind: models.FigureCount}},
		QuantifierClaims: []models.QuantifierClaim{
			{Claim: "There are {{ f1 }} distinct parts", Kind: QuantifierCardinality, Step: 4, Count: 101},
		},
	}}
	attachFigureVerdicts(ins, byID)
	if n := correctRefutedFigures("area", ins, byID); n != 1 {
		t.Fatalf("corrections = %d, want 1; premise stale", n)
	}
	if got := ins[0].QuantifierClaims[0].Count; got != 100 {
		t.Errorf("claim Count = %d, want 100 -- a spaced reference is the same reference", got)
	}
}

// --- Review round 23: three ways malformed model output could hurt the process rather than
// the reader. Late-round findings, and the right kind to be left with.

// TestFigures_NonFiniteValuesAreRejected — round 23.
//
// ParseFloat accepts "NaN" and "Inf", and a non-finite float cannot be marshalled to JSON --
// so one such figure made generateRecommendations emit an empty insights payload and made any
// API response carrying the persisted figure unencodable. flexNumber has rejected non-finite
// values on both paths since #342; the figure decoder did not.
func TestFigures_NonFiniteValuesAreRejected(t *testing.T) {
	for _, bad := range []string{`"NaN"`, `"Inf"`, `"-Inf"`, `"+inf"`} {
		body := `{"name":"n","figures":[{"id":"f1","value":` + bad + `,"step":1,"kind":"sum"}]}`
		var ins models.Insight
		if err := json.Unmarshal([]byte(body), &ins); err != nil {
			t.Fatalf("value %s failed the decode outright: %v", bad, err)
		}
		if got := ins.Figures[0].Value; got != 0 {
			t.Errorf("value %s decoded to %v, want 0", bad, got)
		}
		// And the whole insight must still marshal, which is the failure this prevents.
		if _, err := json.Marshal(ins); err != nil {
			t.Errorf("value %s left the insight unmarshalable: %v", bad, err)
		}
	}
}

// TestFigures_DecimalPrecisionIsBounded — round 23.
//
// Decimals comes from model output and FormatFloat allocates in proportion to it, so
// `"decimals": 1000000000` builds a gigabyte-scale string and the slack calculation loops a
// billion times, both before any evidence is read. Bounded at rendering rather than only at
// decode, so a figure read back from storage is bounded too.
func TestFigures_DecimalPrecisionIsBounded(t *testing.T) {
	huge := models.Figure{ID: "f1", Value: 1.5, Unit: models.UnitPlain, Decimals: 1000000000}
	if got := huge.Places(); got != models.MaxFigureDecimals {
		t.Fatalf("Places() = %d, want %d", got, models.MaxFigureDecimals)
	}
	// Both of these would hang or exhaust memory on the raw value.
	if got := renderFigure(huge); len(got) > 32 {
		t.Errorf("renderFigure produced %d characters", len(got))
	}
	if s := figureSlack(huge); s <= 0 {
		t.Errorf("figureSlack = %v, want a positive interval", s)
	}
	// Negative is clamped to zero, not passed to FormatFloat as "shortest unique".
	if got := (models.Figure{Value: 1.5, Decimals: -3}).Places(); got != 0 {
		t.Errorf("Places() = %d for negative decimals, want 0", got)
	}
}

// TestFigures_DuplicateIDActsOnNothing — round 23.
//
// A repeated id makes every lookup last-wins while the evaluator still produces a verdict per
// declaration, so the two get crossed: two figures both called f1, one refuted at 100 against
// 99 and one holding at 200, let the first verdict's numbers through the proximity gate and
// write 99 over the second figure.
func TestFigures_DuplicateIDActsOnNothing(t *testing.T) {
	step := &models.ExplorationStep{Step: 4, QueryResult: []map[string]any{}}
	for i := 0; i < 99; i++ {
		step.QueryResult = append(step.QueryResult, map[string]any{"id": i})
	}
	byID := map[int]*models.ExplorationStep{4: step}

	ins := []models.Insight{{
		Name:        "Both are {{f1}}",
		SourceSteps: []int{4},
		Figures: []models.Figure{
			{ID: "f1", Value: 100, Unit: models.UnitCount, Step: 4, Kind: models.FigureCount},
			{ID: "f1", Value: 200, Unit: models.UnitCount, Step: 4, Kind: models.FigureCount},
		},
	}}
	attachFigureVerdicts(ins, byID)
	if n := correctRefutedFigures("area", ins, byID); n != 0 {
		t.Errorf("corrections = %d, want 0 on an ambiguous id", n)
	}
	if ins[0].Figures[0].Value != 100 || ins[0].Figures[1].Value != 200 {
		t.Errorf("figures were crossed: %v and %v", ins[0].Figures[0].Value, ins[0].Figures[1].Value)
	}
	// Nor is it rendered: the reference ships visible rather than resolving to a guess.
	renderInsightFigures(ins)
	if !strings.Contains(ins[0].Name, "{{f1}}") {
		t.Errorf("name = %q, want the ambiguous reference left visible", ins[0].Name)
	}

	// And it cannot be referenced from a recommendation either.
	dupIns := models.Insight{
		ID: "11111111-2222-3333-4444-555555555555",
		Figures: []models.Figure{
			{ID: "f1", Value: 100, Unit: models.UnitCount},
			{ID: "f1", Value: 200, Unit: models.UnitCount},
		},
		FigureVerdicts: []models.FigureVerdict{{ID: "f1", Status: models.FigureHolds}},
	}
	recs := []models.Recommendation{{
		Description: "It holds {{f1}}.",
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCount, Kind: models.FigureRefKind,
			Refs: []models.FigureRef{ref(dupIns.ID, "f1")},
		}},
	}}
	attachRecommendationFigureVerdicts(recs, []models.Insight{dupIns})
	if v := recs[0].FigureVerdicts[0]; v.Status != models.FigureUndecidable || !strings.Contains(v.Reason, "more than once") {
		t.Errorf("verdict = %q / %q, want undecidable naming the duplication", v.Status, v.Reason)
	}
}

// --- Review round 24. Three of the five were this layer's own guards applied at one site
// and not all of them, which is why the fixes below are the guard becoming structural rather
// than one more call added by hand.

// TestFigures_DuplicateIDIsRefusedInRecommendationAdoptionToo — round 24.
//
// The duplicate guard went in at the correction pass, the reference index and the renderer,
// and was missed at recommendation adoption -- where it produced exactly the crossing it was
// meant to stop: two figures named f1 both omitting a value, the first resolving to 100 and
// the second referencing nothing, let the first verdict's number into the second declaration,
// and renderableFigures then dropped the zero-valued one so the duplication never reached the
// renderer that would have refused it.
func TestFigures_DuplicateIDIsRefusedInRecommendationAdoptionToo(t *testing.T) {
	ins := bandInsight()
	recs := []models.Recommendation{{
		Description: "It holds {{f1}}.",
		Figures: []models.Figure{
			{ID: "f1", Unit: models.UnitCount, Kind: models.FigureRefKind, Refs: []models.FigureRef{ref(ins.ID, "f1")}},
			{ID: "f1", Unit: models.UnitCount, Kind: models.FigureRefKind, Refs: []models.FigureRef{ref(ins.ID, "nope")}},
		},
	}}

	attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
	renderRecommendationFigures(recs)

	for i, f := range recs[0].Figures {
		if f.Value != 0 {
			t.Errorf("figure[%d] was filled with %v despite an ambiguous id", i, f.Value)
		}
	}
	if !strings.Contains(recs[0].Description, "{{f1}}") {
		t.Errorf("description = %q, want the ambiguous reference left visible", recs[0].Description)
	}
}

// TestFigures_TheGuardIsTheConstructor is the structural half: keying figures by id without
// the duplicate guard should not be something a new call site can do by forgetting.
func TestFigures_TheGuardIsTheConstructor(t *testing.T) {
	figures := []models.Figure{
		{ID: "f1", Value: 1}, {ID: "f1", Value: 2}, {ID: "f2", Value: 3}, {ID: "  ", Value: 4},
	}
	byID := figuresByID(figures)
	if _, ok := byID["f1"]; ok {
		t.Error("figuresByID kept a duplicated id")
	}
	if got, ok := byID["f2"]; !ok || got.Value != 3 {
		t.Errorf("figuresByID lost a unique id: %+v", byID)
	}
	idx := figureIndexByID(figures)
	if _, ok := idx["f1"]; ok {
		t.Error("figureIndexByID kept a duplicated id")
	}
	if got, ok := idx["f2"]; !ok || got != 2 {
		t.Errorf("figureIndexByID = %v, want f2 at 2", idx)
	}
}

// TestFigures_NonFiniteArithmeticIsUndecidable — round 24.
//
// A ratio over a zero column total is infinite and zero-over-zero is NaN. closeEnough returns
// false for both, so the figure was refuted for arithmetic that produced no answer -- and the
// non-finite value was written into a verdict that cannot be marshalled, which is how one
// figure empties an insights payload.
func TestFigures_NonFiniteArithmeticIsUndecidable(t *testing.T) {
	// The reachable path is a driver handing numerics back as text: ParseFloat accepts
	// "NaN" and "Infinity", and asFloat only screened the float64 case.
	for _, bad := range []any{"NaN", "Infinity", "-Inf", float32(math.NaN())} {
		if got, ok := asFloat(bad); ok {
			t.Errorf("asFloat(%v) = (%v, true), want rejected", bad, got)
		}
	}

	rows := []map[string]any{{"band": "A", "net": "NaN"}, {"band": "B", "net": "10"}}
	f := models.Figure{
		ID: "f1", Value: 50, Unit: models.UnitCurrency,
		Step: 2, Kind: models.FigureSum, Column: "net",
	}
	v := evaluateFigure(f, map[int]StepRows{2: {Rows: rows}})
	if v.Status == models.FigureFails {
		t.Errorf("status = fails, want undecidable: the arithmetic has no finite answer")
	}
	if math.IsNaN(v.Evaluated) || math.IsInf(v.Evaluated, 0) {
		t.Errorf("evaluated = %v, which cannot be marshalled to JSON", v.Evaluated)
	}
	ins := []models.Insight{{Name: "n", Figures: []models.Figure{f}, FigureVerdicts: []models.FigureVerdict{v}}}
	if _, err := json.Marshal(ins); err != nil {
		t.Errorf("the verdict left the insight unmarshalable: %v", err)
	}

	// And the guard in the evaluator covers what the coercion cannot: a total that
	// overflows from inputs that were each finite. Both layers are needed, and this is the
	// case that proves the second one is not dead code.
	huge := []map[string]any{
		{"band": "A", "net": math.MaxFloat64},
		{"band": "B", "net": math.MaxFloat64},
	}
	over := evaluateFigure(
		models.Figure{ID: "f1", Value: 1, Unit: models.UnitCurrency, Step: 2, Kind: models.FigureSum, Column: "net"},
		map[int]StepRows{2: {Rows: huge}})
	if over.Status != models.FigureUndecidable {
		t.Errorf("overflowing sum: status = %q, want undecidable", over.Status)
	}
	if math.IsInf(over.Evaluated, 0) {
		t.Errorf("overflowing sum stored %v in the verdict, which cannot be marshalled", over.Evaluated)
	}
}

// TestFigures_RepairIsNotOfferedAFiguresArray — round 24.
//
// rewriteInsight reuses the analysis response format, so describing `figures` there handed the
// repair prompt a facility the repair path cannot support: mergeRepairedInsight copies the
// rewritten prose and not its figures, and rendering has already finished by the time repair
// runs. A structured-output repair could return "{{f1}}" with a matching declaration, pass the
// quantifier checks, and ship the placeholder to a reader.
func TestFigures_RepairIsNotOfferedAFiguresArray(t *testing.T) {
	props := func(format *gollm.ResponseFormat) map[string]interface{} {
		p := format.Schema["properties"].(map[string]interface{})
		items := p["insights"].(map[string]interface{})["items"].(map[string]interface{})
		return items["properties"].(map[string]interface{})
	}
	if _, ok := props(insightRepairResponseFormat())["figures"]; ok {
		t.Error("the repair schema offers a figures array, which nothing downstream of repair renders")
	}
	// And the analysis schema still does, because that is where figures are authored.
	if _, ok := props(insightResponseFormat())["figures"]; !ok {
		t.Error("the analysis schema lost its figures array")
	}
	// The repair schema keeps everything else, so removing figures cannot have replaced it.
	for _, key := range []string{"name", "description", "indicators", "quantifier_claims"} {
		if _, ok := props(insightRepairResponseFormat())[key]; !ok {
			t.Errorf("the repair schema lost %q", key)
		}
	}
}

// TestFigures_AdoptionDeclinesWhenSegmentSizeWouldDesync — round 24.
//
// The same call substituteCount and the insight correction pass make: a correction that leaves
// a structured field contradicting the prose is declined rather than guessed at.
func TestFigures_AdoptionDeclinesWhenSegmentSizeWouldDesync(t *testing.T) {
	ins := bandInsight()
	recs := []models.Recommendation{{
		Title:       "Reach the {{f1}} buyers",
		SegmentSize: 96447,
		Figures: []models.Figure{{
			ID: "f1", Value: 96447, Unit: models.UnitCount, Kind: models.FigureSum,
			Refs: []models.FigureRef{ref(ins.ID, "f1"), ref(ins.ID, "f2")},
		}},
	}}

	attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
	if len(recs[0].FigureCorrections) != 0 {
		t.Errorf("adopted despite the segment_size desync: %v", recs[0].FigureCorrections)
	}
	if recs[0].Figures[0].Value != 96447 || recs[0].SegmentSize != 96447 {
		t.Errorf("figure %v / segment_size %d -- the two must not be left disagreeing",
			recs[0].Figures[0].Value, recs[0].SegmentSize)
	}
}
