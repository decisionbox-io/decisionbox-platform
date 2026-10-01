package discovery

import (
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func TestDropSentence_RemovesOnlyTheEnclosingSentence(t *testing.T) {
	text := "Furniture is the weakest category. Tables is the only loss-making sub-category. " +
		"Chairs carries the volume."
	out, ok := dropSentence(text, "Tables is the only loss-making sub-category")
	if !ok {
		t.Fatalf("drop refused")
	}
	want := "Furniture is the weakest category. Chairs carries the volume."
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
}

// A decimal inside a neighbouring sentence must not be read as a sentence end,
// or the removal starts mid-sentence and leaves a fragment behind.
func TestDropSentence_DecimalIsNotASentenceBoundary(t *testing.T) {
	text := "Margin fell to 17.5% overall. Tables is the only loss-maker. Recovery is slow."
	out, ok := dropSentence(text, "Tables is the only loss-maker")
	if !ok {
		t.Fatalf("drop refused")
	}
	want := "Margin fell to 17.5% overall. Recovery is slow."
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
}

func TestDropSentence_RemovesAWholeMarkdownBullet(t *testing.T) {
	text := "Findings:\n\n- Chairs leads on volume\n- Tables is the only loss-maker\n- Paper is stable"
	out, ok := dropSentence(text, "Tables is the only loss-maker")
	if !ok {
		t.Fatalf("drop refused")
	}
	want := "Findings:\n\n- Chairs leads on volume\n- Paper is stable"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
}

// Removing the only sentence leaves an empty description, which is not a repair.
func TestDropSentence_RefusesWhenItWouldEmptyTheText(t *testing.T) {
	if _, ok := dropSentence("Tables is the only loss-maker.", "Tables is the only loss-maker"); ok {
		t.Errorf("dropped the entire text; want refusal")
	}
}

func TestDropSentence_AbsentNeedleIsNoChange(t *testing.T) {
	if _, ok := dropSentence("Chairs leads on volume.", "Tables is the only loss-maker"); ok {
		t.Errorf("reported a drop for a claim that is not in the text")
	}
}

// The headline is one clause; removing the claim from it leaves a fragment.
func TestDropClaimSentence_RefusesToTouchTheName(t *testing.T) {
	ins := models.Insight{
		Name:        "Tables is the only loss-making sub-category",
		Description: "Tables is the only loss-making sub-category. Chairs leads on volume.",
	}
	if dropClaimSentence(&ins, "Tables is the only loss-making sub-category") {
		t.Fatalf("dropped a claim that is the headline; want refusal")
	}
	if ins.Description == "Chairs leads on volume." {
		t.Errorf("edited the body after refusing: %q", ins.Description)
	}
}

// Description is the plain reduction of DescriptionMd. Removing the claim from
// one and leaving it in the other ships a document that contradicts itself.
func TestDropClaimSentence_RevertsWhenTheClaimSurvivesInMarkdown(t *testing.T) {
	claim := "Tables is the only loss-maker"
	ins := models.Insight{
		Name:          "Furniture drag",
		Description:   "Tables is the only loss-maker. Chairs leads.",
		DescriptionMd: "**" + claim + "**",
	}
	if dropClaimSentence(&ins, claim) {
		t.Fatalf("committed a half-edit; want refusal")
	}
	if ins.Description != "Tables is the only loss-maker. Chairs leads." {
		t.Errorf("body was edited despite the refusal: %q", ins.Description)
	}
}

func TestDropClaimSentence_RemovesTheIndicatorToo(t *testing.T) {
	claim := "Tables is the only loss-maker"
	ins := models.Insight{
		Name:        "Furniture drag",
		Description: "Tables is the only loss-maker. Chairs leads.",
		Indicators:  []string{claim, "Chairs leads on volume"},
	}
	if !dropClaimSentence(&ins, claim) {
		t.Fatalf("drop refused")
	}
	if len(ins.Indicators) != 1 || ins.Indicators[0] != "Chairs leads on volume" {
		t.Errorf("indicators = %v, want only the unrelated one", ins.Indicators)
	}
	if ins.Description != "Chairs leads." {
		t.Errorf("description = %q", ins.Description)
	}
}

func TestStandaloneNumber_DoesNotMatchInsideALongerNumber(t *testing.T) {
	for _, text := range []string{"112 products", "12.5% margin", "1.12 ratio", "1,120 units", "x_12_y"} {
		if at := standaloneNumber(text, "12"); len(at) != 0 {
			t.Errorf("standaloneNumber(%q, \"12\") = %v, want none", text, at)
		}
	}
	for _, text := range []string{"12 products", "(12)", "12% of lines", "top 12", "12"} {
		if at := standaloneNumber(text, "12"); len(at) != 1 {
			t.Errorf("standaloneNumber(%q, \"12\") = %v, want exactly one", text, at)
		}
	}
}

// The observed shape: the count appears once in the headline and once in the
// body, and both have to change together.
func TestSubstituteCount_PatchesHeadlineAndBody(t *testing.T) {
	ins := models.Insight{
		Name:        "12 Products Each Loss-Making",
		Description: "12 products run a loss across the range.",
		QuantifierClaims: []models.QuantifierClaim{{
			Claim: "12 products run a loss", Kind: QuantifierCardinality, Count: 12,
		}},
	}
	if !substituteCount(&ins, &ins.QuantifierClaims[0], 12, 302) {
		t.Fatalf("substitution refused")
	}
	if ins.Name != "302 Products Each Loss-Making" {
		t.Errorf("name = %q", ins.Name)
	}
	if ins.Description != "302 products run a loss across the range." {
		t.Errorf("description = %q", ins.Description)
	}
	if ins.QuantifierClaims[0].Count != 302 {
		t.Errorf("count = %d, want 302", ins.QuantifierClaims[0].Count)
	}
	if ins.QuantifierClaims[0].Claim != "302 products run a loss" {
		t.Errorf("claim = %q", ins.QuantifierClaims[0].Claim)
	}
}

// Two 12s in one sentence give no evidence about which one is the count.
func TestSubstituteCount_RefusesWhenAFieldIsAmbiguous(t *testing.T) {
	ins := models.Insight{
		Name:        "Loss-makers",
		Description: "12 of the 12 largest lines run a loss.",
		QuantifierClaims: []models.QuantifierClaim{{
			Claim: "12 lines run a loss", Kind: QuantifierCardinality, Count: 12,
		}},
	}
	if substituteCount(&ins, &ins.QuantifierClaims[0], 12, 3) {
		t.Fatalf("substituted into an ambiguous field; want refusal")
	}
	if ins.Description != "12 of the 12 largest lines run a loss." {
		t.Errorf("description was edited despite the refusal: %q", ins.Description)
	}
	if ins.QuantifierClaims[0].Count != 12 {
		t.Errorf("count was changed despite the refusal: %d", ins.QuantifierClaims[0].Count)
	}
}

// End to end over the real rows: the model said 3 of the top 10 run a loss; 2 do.
func TestSubstituteRefutedCounts_CorrectsFromTheRows(t *testing.T) {
	ins := models.Insight{
		Name:        "Top-ten drag",
		Description: "3 of the ten largest lines by sales run a loss.",
		SourceSteps: []int{4},
		QuantifierClaims: []models.QuantifierClaim{{
			Claim: "3 of the ten largest lines by sales run a loss", Kind: QuantifierCardinality,
			Step: 4, Filter: "profit < 0", TopN: 10, TopNColumn: "sales", Count: 3,
		}},
	}
	evidence := map[int]StepRows{4: superstoreStep4()}
	ins.QuantifierVerdicts = EvaluateQuantifierClaims(ins.QuantifierClaims, evidence)
	if ins.QuantifierVerdicts[0].Status != QuantifierFails {
		t.Fatalf("precondition: want a refuted claim, got %q", ins.QuantifierVerdicts[0].Status)
	}

	fixed := substituteRefutedCounts(&ins, evidence)
	if len(fixed) != 1 {
		t.Fatalf("fixed %d claims, want 1", len(fixed))
	}
	if ins.Description != "2 of the ten largest lines by sales run a loss." {
		t.Errorf("description = %q", ins.Description)
	}
	after := EvaluateQuantifierClaims(ins.QuantifierClaims, evidence)
	if after[0].Status != QuantifierHolds {
		t.Errorf("after substitution status = %q (%s), want holds", after[0].Status, after[0].Reason)
	}
}

// An "only" claim carries no numeral, so there is nothing to substitute and the
// mechanical pass must leave it for the model.
func TestSubstituteRefutedCounts_LeavesNonCardinalityAlone(t *testing.T) {
	ins := models.Insight{
		Name:        "Furniture drag",
		Description: "Tables is the only loss-making line in the top ten.",
		SourceSteps: []int{4},
		QuantifierClaims: []models.QuantifierClaim{{
			Claim: "Tables is the only loss-making line in the top ten", Kind: QuantifierOnly,
			Step: 4, Filter: "profit < 0", TopN: 10, TopNColumn: "sales",
		}},
	}
	evidence := map[int]StepRows{4: superstoreStep4()}
	ins.QuantifierVerdicts = EvaluateQuantifierClaims(ins.QuantifierClaims, evidence)
	before := ins.Description
	if fixed := substituteRefutedCounts(&ins, evidence); len(fixed) != 0 {
		t.Errorf("fixed %v, want none", fixed)
	}
	if ins.Description != before {
		t.Errorf("description changed: %q", ins.Description)
	}
}
