package discovery

import (
	"context"
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// A rewrite can stop a claim being refuted without making it true: `step` and
// `filter` are both authored, so pointing the claim at a step this insight does
// not cite makes the evaluator DECLINE instead of refuse. countRefuted reads that
// as progress because it counts only failures, and the claim would then be
// reported fixed with no predicate ever proven over the rows.
func TestRepair_RejectsARoundThatMakesTheClaimUndecidableInsteadOfTrue(t *testing.T) {
	// Same sentence, same claim text, but declared against step 99 — not cited.
	dodged := `{"insights":[{
		"name":"Furniture drags the top ten",
		"description":"` + shippedOnlyClaim + `. Chairs leads the category on volume.",
		"severity":"high","source_steps":[4],
		"quantifier_claims":[{"claim":"` + shippedOnlyClaim + `","kind":"only",
			"step":99,"filter":"profit < 0","top_n":10,"top_n_column":"sales"}]
	}]}`
	o, provider := newRepairOrchestrator(dodged, dodged)

	got, tally := repairOne(t, o, refutedInsight())

	if len(provider.Calls) != 2 {
		t.Fatalf("LLM calls = %d, want 2 (both rounds attempted and both rejected)", len(provider.Calls))
	}
	if len(got.Repair.Fixed) != 0 {
		t.Errorf("fixed = %v; an undecidable claim was never proven to hold", got.Repair.Fixed)
	}
	if got.Repair.Outcome == models.RepairRepaired {
		t.Errorf("outcome = %q; nothing was corrected", got.Repair.Outcome)
	}
	// The rounds were rejected, so the fallback removes the sentence instead.
	if got.Repair.Outcome != models.RepairClaimDropped {
		t.Errorf("outcome = %q, want %q", got.Repair.Outcome, models.RepairClaimDropped)
	}
	if insightMentions(got, shippedOnlyClaim) {
		t.Errorf("the refuted sentence survived: %q", got.Description)
	}
	if tally.claimsDropped != 1 {
		t.Errorf("tally = %+v, want the claim dropped", tally)
	}
}

// A declaration about nothing the document said is not a repair. The observed
// case: the model declared a rate claim, the cited rows carried no rate column so
// the predicate was a count proxy that failed at the boundary, the sentence was
// true and was never in the body, and the rewrite simply dropped the declaration.
// The prose came back byte-identical, and it was recorded as `repaired`.
func TestRepair_ADeclarationAboutNothingInTheProseIsWithdrawnNotRepaired(t *testing.T) {
	const body = "Chairs leads the category on volume."
	entry := models.Insight{
		ID: "insight-2", Name: "Furniture drags the top ten",
		Description: body, SourceSteps: []int{4}, Severity: "high",
		QuantifierClaims: []models.QuantifierClaim{{
			Claim: shippedOnlyClaim, Kind: QuantifierOnly,
			Step: 4, Filter: "profit < 0", TopN: 10, TopNColumn: "sales",
		}},
	}
	// The rewrite keeps the body and drops the declaration.
	withdrawn := `{"insights":[{
		"name":"Furniture drags the top ten",
		"description":"` + body + `",
		"severity":"high","source_steps":[4]
	}]}`
	o, _ := newRepairOrchestrator(withdrawn, withdrawn)

	got, tally := repairOne(t, o, entry)

	if got.Description != body {
		t.Errorf("description = %q, want the body unchanged", got.Description)
	}
	if len(got.Repair.Fixed) != 0 {
		t.Errorf("fixed = %v; no sentence was corrected", got.Repair.Fixed)
	}
	if len(got.Repair.Withdrawn) != 1 || got.Repair.Withdrawn[0] != shippedOnlyClaim {
		t.Errorf("withdrawn = %v, want the declaration that was about nothing", got.Repair.Withdrawn)
	}
	if got.Repair.Outcome != models.RepairWithdrawn {
		t.Errorf("outcome = %q, want %q", got.Repair.Outcome, models.RepairWithdrawn)
	}
	if tally.repaired != 0 {
		t.Errorf("tally.repaired = %d; a withdrawal must not count as a repair", tally.repaired)
	}
}

// The derived repair record must not be authorable. `evidence_repair` is an
// ordinary JSON tag, so a model that emits that key has its record decoded
// straight onto the insight — and an insight declaring no claims never reaches
// repair, so nothing would overwrite it.
func TestAttachQuantifierVerdicts_ClearsAModelAuthoredRepairRecord(t *testing.T) {
	authored := &models.InsightRepair{
		Rounds: 3, Outcome: models.RepairRepaired, Fixed: []string{"a claim I fixed myself"},
	}
	ins := []models.Insight{
		{ID: "no-claims", Name: "declares nothing", Repair: authored},
		{ID: "with-claims", Name: "declares something", SourceSteps: []int{4}, Repair: authored,
			QuantifierClaims: []models.QuantifierClaim{{
				Claim: "the only top-10 revenue line running a loss", Kind: QuantifierOnly,
				Step: 4, Filter: "profit < 0", TopN: 10, TopNColumn: "sales",
			}}},
	}
	attachQuantifierVerdicts(ins, step4ByID())
	for _, x := range ins {
		if x.Repair != nil {
			t.Errorf("%s kept a model-authored repair record: %+v", x.ID, x.Repair)
		}
	}
}

// scopedWithinResult decides whether a top-N claim over a CAPPED result may be
// evaluated at all. Counting rows does not settle it: a query capped with
// `ORDER BY p_brand LIMIT 25` returns 25 rows that are not the top 25 by revenue,
// and ranking them would refute a true claim from evidence that never held the
// answer.
func TestScopedWithinResult_RequiresTheRowsToShowTheClaimedOrder(t *testing.T) {
	desc := []map[string]any{{"b": "x", "rev": 30.0}, {"b": "y", "rev": 20.0}, {"b": "z", "rev": 10.0}}
	byName := []map[string]any{{"b": "x", "rev": 10.0}, {"b": "y", "rev": 30.0}, {"b": "z", "rev": 20.0}}
	claim := models.QuantifierClaim{TopN: 3, TopNColumn: "rev"}

	if !scopedWithinResult(claim, desc) {
		t.Error("a result already ordered by the claimed column must be in reach")
	}
	if scopedWithinResult(claim, byName) {
		t.Error("a capped result ordered by some other column must NOT be evaluated as a top-N")
	}
	if scopedWithinResult(models.QuantifierClaim{TopN: 4, TopNColumn: "rev"}, desc) {
		t.Error("a top-N larger than the rows returned cannot be contained by them")
	}
	if scopedWithinResult(models.QuantifierClaim{TopN: 3}, desc) {
		t.Error("a top-N with no column names no order to check")
	}
	if scopedWithinResult(models.QuantifierClaim{TopN: 3, TopNColumn: "missing"}, desc) {
		t.Error("a column the rows do not carry cannot confirm any order")
	}
	if scopedWithinResult(models.QuantifierClaim{TopN: 0, TopNColumn: "rev"}, desc) {
		t.Error("a scope claim over a capped result stays out of reach")
	}
}

// End to end: a capped step whose rows are not in the claimed order must yield
// undecidable, not a refutation.
func TestEvaluate_CappedStepInTheWrongOrderIsUndecidable(t *testing.T) {
	rows := []map[string]any{{"b": "x", "rev": 10.0}, {"b": "y", "rev": 30.0}, {"b": "z", "rev": 20.0}}
	ev := map[int]StepRows{4: {Rows: rows, Quality: []gowarehouse.QualityCaveat{gowarehouse.RowCapCaveat(3)}}}
	v := EvaluateQuantifierClaims([]models.QuantifierClaim{{
		Claim: "x leads the top 3 by revenue", Kind: QuantifierOnly,
		Step: 4, Filter: "rev > 25", TopN: 3, TopNColumn: "rev",
	}}, ev)
	if len(v) != 1 {
		t.Fatalf("got %d verdicts, want 1", len(v))
	}
	if v[0].Status != QuantifierUndecidable {
		t.Errorf("status = %q (%s), want undecidable: the cap did not order by the claimed column",
			v[0].Status, v[0].Reason)
	}
	if !strings.Contains(v[0].Reason, "capped") {
		t.Errorf("reason = %q, want it to name the cap", v[0].Reason)
	}
}

// Two refuted claims can share one sentence. The first removal takes the whole
// sentence, so the second finds nothing to change and dropClaimSentence reports
// false -- which would file a claim the reader can no longer see as unrepaired,
// escalating the outcome to its worst bucket and leaving a live failure recorded
// about text that is gone.
func TestRepair_BothClaimsInOneSentenceCountAsDropped(t *testing.T) {
	const second = "Chairs leads the category on volume"
	entry := models.Insight{
		ID: "insight-3", Name: "Furniture drags the top ten",
		Description: shippedOnlyClaim + " and " + second + ". Bookcases held flat all year.",
		SourceSteps: []int{4}, Severity: "high",
		QuantifierClaims: []models.QuantifierClaim{
			{Claim: shippedOnlyClaim, Kind: QuantifierOnly,
				Step: 4, Filter: "profit < 0", TopN: 10, TopNColumn: "sales"},
			// Also refuted over the same rows, and living in the same sentence.
			{Claim: second, Kind: QuantifierOnly,
				Step: 4, Filter: "sales > 1000000", TopN: 10, TopNColumn: "sales"},
		},
	}
	// No rounds: go straight to the removal pass, which is where this bites.
	t.Setenv(analysisRepairMaxRoundsEnv, "0")
	o, _ := newRepairOrchestrator()

	insights := []models.Insight{entry}
	attachQuantifierVerdicts(insights, step4ByID())
	if countRefuted(insights[0].QuantifierVerdicts) != 2 {
		t.Fatalf("precondition: want both claims refuted, got %d", countRefuted(insights[0].QuantifierVerdicts))
	}
	tally := o.repairRefutedInsights(context.Background(), "profitability", insights, step4ByID(), 8000)
	got := insights[0]

	if len(got.Repair.Unrepaired) != 0 {
		t.Errorf("unrepaired = %v; both claims went with the sentence the reader no longer sees", got.Repair.Unrepaired)
	}
	if len(got.Repair.Dropped) != 2 {
		t.Errorf("dropped = %v, want both claims", got.Repair.Dropped)
	}
	if got.Repair.Outcome != models.RepairClaimDropped {
		t.Errorf("outcome = %q, want %q", got.Repair.Outcome, models.RepairClaimDropped)
	}
	if tally.unrepaired != 0 {
		t.Errorf("tally.unrepaired = %d, want 0", tally.unrepaired)
	}
}

// The mechanical count substitution needs no model, so nothing reviews what it
// writes. Given a count corrected from 12 to 3, an unrelated "12-month decline"
// or "12% margin" elsewhere in the insight was rewritten too — fabricating text
// nobody wrote, and recording the insight as repaired while doing it.
func TestSubstituteCount_RefusesWhenTheNumeralCountsSomethingElse(t *testing.T) {
	for _, other := range []string{"12-month decline in Furniture", "12% average margin", "top 12 clerks"} {
		t.Run(other, func(t *testing.T) {
			ins := models.Insight{
				Name:        "Loss-making lines",
				Description: "12 sub-categories run a loss across the window.",
				Indicators:  []string{other},
			}
			c := models.QuantifierClaim{
				Claim: "12 sub-categories run a loss", Kind: QuantifierCardinality, Step: 4, Count: 12,
			}
			if substituteCount(&ins, &c, 12, 3) {
				t.Errorf("substitution accepted; it would rewrite %q", other)
			}
			if ins.Description != "12 sub-categories run a loss across the window." {
				t.Errorf("description was altered despite the refusal: %q", ins.Description)
			}
			if ins.Indicators[0] != other {
				t.Errorf("indicator was altered: %q", ins.Indicators[0])
			}
		})
	}
}

// The same numeral counting the same thing in two places is one quantity
// restated, and both must move together — the case the substitution exists for.
func TestSubstituteCount_StillPatchesEveryMentionOfTheSameQuantity(t *testing.T) {
	ins := models.Insight{
		Name:        "12 sub-categories run a loss",
		Description: "12 sub-categories run a loss across the window.",
		Indicators:  []string{"12 sub-categories below zero"},
	}
	c := models.QuantifierClaim{
		Claim: "12 sub-categories run a loss", Kind: QuantifierCardinality, Step: 4, Count: 12,
	}
	if !substituteCount(&ins, &c, 12, 3) {
		t.Fatal("substitution refused for one quantity restated three times")
	}
	for _, got := range []string{ins.Name, ins.Description, ins.Indicators[0], c.Claim} {
		if strings.Contains(got, "12") {
			t.Errorf("a mention was left stale: %q", got)
		}
	}
	if c.Count != 3 {
		t.Errorf("count = %d, want 3", c.Count)
	}
}

// A scope on top of a cap is out of reach whichever order they were applied in:
// the cap ran in the warehouse before any scope the claim names, so the rows in
// hand are the GLOBAL top N and the scoped rows among them are not the scoped
// top N.
func TestScopedWithinResult_RefusesAScopeOverACappedResult(t *testing.T) {
	desc := []map[string]any{{"r": "West", "rev": 30.0}, {"r": "East", "rev": 20.0}, {"r": "West", "rev": 10.0}}
	if !scopedWithinResult(models.QuantifierClaim{TopN: 3, TopNColumn: "rev"}, desc) {
		t.Fatal("precondition: an unscoped, correctly ordered top-N is in reach")
	}
	if scopedWithinResult(models.QuantifierClaim{TopN: 3, TopNColumn: "rev", Scope: "r = 'West'"}, desc) {
		t.Error("a scoped top-N over a capped result must stay undecidable")
	}
}
