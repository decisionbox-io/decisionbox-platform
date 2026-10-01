package discovery

import (
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Session 14 shipped `{{f1}}` to a reader in a recommendation's TITLE, and the cause was two
// contract defects rather than a rendering bug. Both are text, so these tests pin the text:
// the behaviour they govern lives in a model, and the only other place the rule exists is the
// evaluator that was already refusing what the contract invited.

// TestRecommendationContract_DoesNotOfferAUnitTheResolverRefuses.
//
// resolveRef requires sameNotation between a restatement and the figure it names, so a `count`
// restating a `currency` figure is refused and its reference ships visible. The contract used
// to list `unit` among the things the model chooses, beside `scale` and `decimals` -- which the
// renderer really does leave to the model. Session 14's R6 took the invitation: it declared a
// count, aimed it at an amount, and the reader saw the marker.
func TestRecommendationContract_DoesNotOfferAUnitTheResolverRefuses(t *testing.T) {
	if strings.Contains(recommendationFigureContract, "`unit`, `scale` and `decimals` are yours") {
		t.Error("the contract still offers the model the unit, which resolveRef refuses: " +
			"a contract advertising an operation the evaluator does not implement is how " +
			"a visible {{f1}} reached a shipped title")
	}
	for clause, why := range map[string]string{
		"`unit` is the figure's, not yours": "the rule the resolver actually enforces",
		"cannot turn an amount into a count": "the specific restatement that was refused in " +
			"session 14, named so the model can recognise it",
		"ships its reference visible": "the consequence, which is what makes the rule worth obeying",
	} {
		if !strings.Contains(recommendationFigureContract, clause) {
			t.Errorf("the recommendation contract no longer says %q -- %s", clause, why)
		}
	}
}

// TestRecommendationContract_SaysWhatToDoWhenThereIsNoFigure.
//
// The measured behaviour of a model denied an id is not to fall back to prose: it is to find
// another id. That was already written down once -- withholding a repaired insight's figures
// made a recommendation borrow a verified-but-unrelated 24,999 -- and session 14 repeated it
// in a new way, aiming a reference at a figure of the wrong unit because the count it wanted
// had never been declared. "Do not retype a number" with no stated alternative is the gap.
func TestRecommendationContract_SaysWhatToDoWhenThereIsNoFigure(t *testing.T) {
	if !strings.Contains(recommendationFigureContract, "write it as ordinary text") {
		t.Error("the contract tells the model not to retype a number but never says what to " +
			"do when the number it needs is not a figure, which is when it invents a reference")
	}
	if !strings.Contains(recommendationFigureContract, "Never") ||
		!strings.Contains(recommendationFigureContract, "aim a reference at a different figure") {
		t.Error("the contract does not forbid aiming a reference at a different figure, which " +
			"is the move that put {{f1}} into a shipped title")
	}
}

// TestRecommendationFigures_ARestatementCannotChangeTheUnit is session 14's R6, and it is
// the behaviour the contract clause above now matches. Nothing tested it before.
//
// The insight's headline count (159 net-unprofitable customers) was never declared; its five
// figures are amounts and percentages. The recommendation wanted that count, declared one, and
// pointed it at the insight's `f1` -- an amount. Restating an amount as a count is not a
// formatting choice, so the resolver refuses; and because a missing value must never render as
// zero, the reference stays visible. The marker in a TITLE is the part still to fix, and it is
// pinned here so it cannot change silently.
func TestRecommendationFigures_ARestatementCannotChangeTheUnit(t *testing.T) {
	ins := models.Insight{
		ID:   "1144eb60-2031-4f95-a3f4-9667534c6d2a",
		Name: "159 customers are net unprofitable, losing {{f1}} on {{f2}} of sales",
		Figures: []models.Figure{
			{ID: "f1", Value: -71545, Unit: models.UnitCurrency, Step: 23, Kind: models.FigureCell, Column: "profit", Row: "status = unprofitable"},
			{ID: "f2", Value: 392727, Unit: models.UnitCurrency, Step: 23, Kind: models.FigureCell, Column: "sales", Row: "status = unprofitable"},
		},
		FigureVerdicts: []models.FigureVerdict{
			{ID: "f1", Status: models.FigureHolds, Claimed: -71545, Evaluated: -71545},
			{ID: "f2", Status: models.FigureHolds, Claimed: 392727, Evaluated: 392727},
		},
	}
	recs := []models.Recommendation{{
		Title:       "Fix discount discipline for the {{f1}} net-unprofitable customers losing {{f2}}",
		Description: "{{f1}} customers carry negative lifetime profit, together losing {{f2}}.",
		Figures: []models.Figure{
			{ID: "f1", Unit: models.UnitCount, Kind: models.FigureRefKind, Refs: []models.FigureRef{ref(ins.ID, "f1")}},
			{ID: "f2", Unit: models.UnitCurrency, Kind: models.FigureRefKind, Refs: []models.FigureRef{ref(ins.ID, "f1")}},
		},
	}}

	attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
	renderRecommendationFigures(recs)

	var count, amount models.FigureVerdict
	for _, v := range recs[0].FigureVerdicts {
		switch v.ID {
		case "f1":
			count = v
		case "f2":
			amount = v
		}
	}
	if count.Status != models.FigureUndecidable {
		t.Errorf("a count restating a currency figure got status %q, want undecidable: "+
			"changing the unit changes the quantity, and guessing which one the model meant is "+
			"how a fabricated number reaches the prose", count.Status)
	}
	if !strings.Contains(count.Reason, "unit") {
		t.Errorf("reason = %q, want it to name the unit mismatch so the record says why", count.Reason)
	}
	if amount.Status != models.FigureHolds {
		t.Errorf("the currency restatement of a currency figure got %q (%s), want holds -- "+
			"the refusal must be about the unit, not about restatements in general",
			amount.Status, amount.Reason)
	}

	// What the reader gets today: the amount rendered, the count still a marker. No zero.
	if !strings.Contains(recs[0].Title, "-$71,545") {
		t.Errorf("title = %q, want the resolvable reference rendered", recs[0].Title)
	}
	if !strings.Contains(recs[0].Title, "{{f1}}") {
		t.Errorf("title = %q, want the unresolvable reference left visible; a rendered 0 would "+
			"read as a finding", recs[0].Title)
	}
	for _, bad := range []string{"0 net-unprofitable", "$0"} {
		if strings.Contains(recs[0].Title, bad) {
			t.Errorf("title = %q, contains %q -- a missing value must never render as a number", recs[0].Title, bad)
		}
	}
}
