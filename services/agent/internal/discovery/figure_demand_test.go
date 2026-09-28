package discovery

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// demandInsight is one insight whose prose states three numerals and declares one, so
// two are undeclared. The figures are real cells of step 7.
func demandInsight() models.Insight {
	return models.Insight{
		Name:        "Buyer base: 99,996 of 150,000 customers ordered",
		Description: "Step 7 shows 50,004 never ordered, and 3,299 placed 2-5 orders.",
		SourceSteps: []int{7},
		FigureClaims: []models.FigureClaim{{
			Figure: "50,004", Value: 50004, Step: 7, Kind: models.FigureCell,
			Column: "customers", Row: "bucket = '0_never_ordered'",
		}},
	}
}

func TestDemand_ListsOnlyWhatIsUndeclared(t *testing.T) {
	ins := demandInsight()
	got := undeclaredNumerals(ins)

	has := func(v float64) bool {
		for _, g := range got {
			if sameFigureLoosely(g.value, v) {
				return true
			}
		}
		return false
	}
	for _, want := range []float64{99996, 150000, 3299} {
		if !has(want) {
			t.Errorf("%v is in the prose, undeclared, and was not asked about: %v", want, got)
		}
	}
	if has(50004) {
		t.Errorf("50,004 is declared and must not be asked about again: %v", got)
	}
	// Ascending, so the demand list is stable across runs rather than map-ordered.
	for i := 1; i < len(got); i++ {
		if got[i].value < got[i-1].value {
			t.Fatalf("demand list is not sorted: %v", got)
		}
	}
	// And each entry keeps the text the prose used, which is what the demand quotes.
	for _, g := range got {
		if g.raw == "" {
			t.Errorf("numeral %v carries no written text", g.value)
		}
	}
	for _, g := range got {
		if sameFigureLoosely(g.value, 3299) && g.raw != "3,299" {
			t.Errorf("3,299 was recorded as %q; the demand would ask about a form the prose does not use", g.raw)
		}
	}
}

func TestDemand_AlreadyLabelledIsNotAskedTwice(t *testing.T) {
	ins := demandInsight()
	ins.FigureCoverage = &models.FigureCoverage{Labels: []string{"3,299"}}
	for _, v := range undeclaredNumerals(ins) {
		if sameFigureLoosely(v.value, 3299) {
			t.Fatal("a numeral already dismissed as a label was asked about again")
		}
	}
}

// TestDemand_MergeTakesOnlyWhatWasAsked is the pass's central guard. A reply that can
// reach past the question is a pass that rewrites the record rather than adding to it.
func TestDemand_MergeTakesOnlyWhatWasAsked(t *testing.T) {
	base := func() []models.Insight { return []models.Insight{demandInsight()} }

	t.Run("a demanded figure is accepted", func(t *testing.T) {
		ins := base()
		wanted := map[int][]numeralHit{1: {{raw: "3299", value: 3299}, {raw: "99996", value: 99996}, {raw: "150000", value: 150000}}}
		explained, labelled := mergeDemandReply(ins, wanted, figureDemandReply{
			Declarations: []figureDemandDeclaration{{Insight: 1, Claim: models.FigureClaim{
				Figure: "3,299", Value: 3299, Step: 7, Kind: models.FigureCell,
				Column: "customers", Row: "bucket = '2_5_orders'",
			}}},
		})
		if explained != 1 || labelled != 0 {
			t.Fatalf("explained=%d labelled=%d, want 1 and 0", explained, labelled)
		}
		if len(ins[0].FigureClaims) != 2 {
			t.Fatalf("claims = %d, want the original plus one", len(ins[0].FigureClaims))
		}
	})

	t.Run("a figure that was not demanded is dropped", func(t *testing.T) {
		ins := base()
		wanted := map[int][]numeralHit{1: {{raw: "3299", value: 3299}}}
		explained, _ := mergeDemandReply(ins, wanted, figureDemandReply{
			Declarations: []figureDemandDeclaration{{Insight: 1, Claim: models.FigureClaim{
				Figure: "999,999", Value: 999999, Step: 7, Kind: models.FigureSum, Column: "customers",
			}}},
		})
		if explained != 0 || len(ins[0].FigureClaims) != 1 {
			t.Fatalf("a declaration for a figure nobody asked about was accepted (%d claims)", len(ins[0].FigureClaims))
		}
	})

	t.Run("an existing declaration is never replaced", func(t *testing.T) {
		ins := base()
		wanted := map[int][]numeralHit{1: {{raw: "50004", value: 50004}}} // pretend it was demanded
		explained, _ := mergeDemandReply(ins, wanted, figureDemandReply{
			Declarations: []figureDemandDeclaration{{Insight: 1, Claim: models.FigureClaim{
				Figure: "50,004", Value: 50004, Step: 99, Kind: models.FigureCount,
			}}},
		})
		if explained != 0 {
			t.Fatal("an already-declared figure was re-declared")
		}
		if ins[0].FigureClaims[0].Step != 7 {
			t.Fatalf("the original declaration was overwritten: step %d", ins[0].FigureClaims[0].Step)
		}
	})

	t.Run("an out-of-range insight index is ignored", func(t *testing.T) {
		ins := base()
		wanted := map[int][]numeralHit{1: {{raw: "3299", value: 3299}}}
		mergeDemandReply(ins, wanted, figureDemandReply{
			Declarations: []figureDemandDeclaration{
				{Insight: 0, Claim: models.FigureClaim{Figure: "3,299", Value: 3299, Step: 7, Kind: models.FigureCell}},
				{Insight: 9, Claim: models.FigureClaim{Figure: "3,299", Value: 3299, Step: 7, Kind: models.FigureCell}},
			},
		})
		if len(ins[0].FigureClaims) != 1 {
			t.Fatal("a declaration with a bogus insight index was applied")
		}
	})

	t.Run("a label is counted as a label and never as declared", func(t *testing.T) {
		ins := base()
		wanted := map[int][]numeralHit{1: {{raw: "3299", value: 3299}}}
		explained, labelled := mergeDemandReply(ins, wanted, figureDemandReply{
			NotMeasurements: []figureDemandLabel{{Insight: 1, Figure: "3,299", Why: "a bucket edge"}},
		})
		if explained != 0 || labelled != 1 {
			t.Fatalf("explained=%d labelled=%d, want 0 and 1", explained, labelled)
		}
		if len(ins[0].FigureClaims) != 1 {
			t.Fatal("a label added a declaration")
		}
		cov := figureCoverage(ins[0])
		if cov.Labelled != 1 {
			t.Fatalf("coverage labelled = %d, want 1", cov.Labelled)
		}
		if cov.Declared != 1 {
			t.Fatalf("coverage declared = %d, want 1 (the original only)", cov.Declared)
		}
	})

	t.Run("declared and dismissed in one reply keeps the declaration", func(t *testing.T) {
		ins := base()
		wanted := map[int][]numeralHit{1: {{raw: "3299", value: 3299}}}
		explained, labelled := mergeDemandReply(ins, wanted, figureDemandReply{
			Declarations: []figureDemandDeclaration{{Insight: 1, Claim: models.FigureClaim{
				Figure: "3,299", Value: 3299, Step: 7, Kind: models.FigureCell,
				Column: "customers", Row: "bucket = '2_5_orders'",
			}}},
			NotMeasurements: []figureDemandLabel{{Insight: 1, Figure: "3,299", Why: "actually a label"}},
		})
		if explained != 1 || labelled != 0 {
			t.Fatalf("explained=%d labelled=%d; the checkable statement must win", explained, labelled)
		}
	})
}

// TestDemand_CannotTouchTheProse is the safety property the whole pass rests on. The
// reply carries declarations, and there is no path by which it carries text -- so a
// model cannot answer "where did this number come from" by deleting the number.
func TestDemand_CannotTouchTheProse(t *testing.T) {
	ins := []models.Insight{demandInsight()}
	before := ins[0]

	mergeDemandReply(ins, map[int][]numeralHit{1: {{raw: "3299", value: 3299}, {raw: "99996", value: 99996}, {raw: "150000", value: 150000}}}, figureDemandReply{
		Declarations: []figureDemandDeclaration{{Insight: 1, Claim: models.FigureClaim{
			Figure: "3,299", Value: 3299, Step: 7, Kind: models.FigureCell,
			Column: "customers", Row: "bucket = '2_5_orders'",
		}}},
		NotMeasurements: []figureDemandLabel{{Insight: 1, Figure: "150000", Why: "the table's own total"}},
	})

	if ins[0].Name != before.Name {
		t.Errorf("name changed: %q -> %q", before.Name, ins[0].Name)
	}
	if ins[0].Description != before.Description {
		t.Errorf("description changed:\n  %q\n  %q", before.Description, ins[0].Description)
	}
	if len(ins[0].Indicators) != len(before.Indicators) {
		t.Error("indicators changed")
	}
}

// TestDemand_ReplyParse covers the shapes a reply arrives in, including the ones that
// must be rejected rather than half-read.
func TestDemand_ReplyParse(t *testing.T) {
	t.Run("a well-formed reply", func(t *testing.T) {
		r, err := parseFigureDemandReply(`{"declarations":[
			{"insight":1,"figure":"3,299","value":3299,"step":7,"kind":"cell","column":"customers","row":"bucket = '2_5_orders'"}],
			"not_measurements":[{"insight":1,"figure":"30","why":"bucket edge"}]}`)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Declarations) != 1 || r.Declarations[0].Claim.Column != "customers" {
			t.Fatalf("declarations = %+v", r.Declarations)
		}
		if r.Declarations[0].Insight != 1 {
			t.Fatalf("insight index lost: %+v", r.Declarations[0])
		}
		if len(r.NotMeasurements) != 1 || r.NotMeasurements[0].Figure != "30" {
			t.Fatalf("labels = %+v", r.NotMeasurements)
		}
	})

	t.Run("a fenced reply", func(t *testing.T) {
		r, err := parseFigureDemandReply("```json\n{\"declarations\":[],\"not_measurements\":[{\"insight\":2,\"figure\":\"15\",\"why\":\"top-N bound\"}]}\n```")
		if err != nil {
			t.Fatal(err)
		}
		if len(r.NotMeasurements) != 1 {
			t.Fatalf("labels = %+v", r.NotMeasurements)
		}
	})

	t.Run("a declaration missing its kind is dropped, not guessed", func(t *testing.T) {
		r, err := parseFigureDemandReply(`{"declarations":[{"insight":1,"figure":"3,299","value":3299,"step":7}],"not_measurements":[]}`)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Declarations) != 0 {
			t.Fatalf("a declaration with no kind was kept: %+v", r.Declarations)
		}
	})

	t.Run("a declaration with no insight index is dropped", func(t *testing.T) {
		r, err := parseFigureDemandReply(`{"declarations":[{"figure":"3,299","value":3299,"step":7,"kind":"cell"}],"not_measurements":[]}`)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Declarations) != 0 {
			t.Fatalf("a declaration with no insight index was kept: %+v", r.Declarations)
		}
	})

	t.Run("prose instead of JSON is an error", func(t *testing.T) {
		if _, err := parseFigureDemandReply("I could not account for those figures."); err == nil {
			t.Fatal("expected an error")
		}
	})
}

// TestDemand_PromptNamesTheFiguresAndFreezesTheProse asserts the prompt says the two
// things it has to. The instruction that the prose is fixed is the safety property in
// words; the figures being listed with their sentences is what makes the question
// answerable.
func TestDemand_PromptNamesTheFiguresAndFreezesTheProse(t *testing.T) {
	ins := []models.Insight{demandInsight()}
	steps := []models.ExplorationStep{{Step: 7, Action: "query_data", QueryResult: step7Rows()}}
	// Built by the real function, so the test cannot pass on a demand list the pass
	// would never produce.
	p := buildFigureDemandPrompt(ins, map[int][]numeralHit{1: undeclaredNumerals(ins[0])}, steps)

	// Named as the PROSE writes them, separators and all, not as bare floats.
	for _, want := range []string{"3,299", "99,996", "150,000"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt does not name the undeclared figure %s", want)
		}
	}
	if !strings.Contains(p, "The prose is fixed") {
		t.Error("prompt does not tell the model the prose cannot change")
	}
	if !strings.Contains(p, "not_measurements") {
		t.Error("prompt does not offer the dismissal path, so an honest gap has nowhere to go")
	}
	if strings.Contains(p, "Rewrite") {
		t.Error("prompt invites a rewrite")
	}
	// The sentence each figure sits in, so the model is not asked about a bare number.
	if !strings.Contains(p, "Step 7 shows 50,004 never ordered") {
		t.Error("prompt does not quote the prose the figures appear in")
	}
}

// TestDemand_CoverageAccountsForEveryNumeral is the readout the measurement depends on:
// written splits into declared, labelled and unexplained, and the residual is what a
// report has to lead with.
func TestDemand_CoverageAccountsForEveryNumeral(t *testing.T) {
	ins := demandInsight()
	ins.FigureCoverage = &models.FigureCoverage{Labels: []string{"150000"}}
	ins.FigureClaims = append(ins.FigureClaims, models.FigureClaim{
		Figure: "3,299", Value: 3299, Step: 7, Kind: models.FigureCell,
		Column: "customers", Row: "bucket = '2_5_orders'",
	})

	cov := figureCoverage(ins)
	if cov.Declared != 2 {
		t.Errorf("declared = %d, want 2", cov.Declared)
	}
	if cov.Labelled != 1 {
		t.Errorf("labelled = %d, want 1", cov.Labelled)
	}
	if cov.Declared+cov.Labelled+cov.Unexplained() != cov.Written {
		t.Errorf("the three buckets do not sum to written: %d + %d + %d != %d",
			cov.Declared, cov.Labelled, cov.Unexplained(), cov.Written)
	}
}

// TestDemand_ResettlePreservesLabelsButAttachDoesNot pins the trust boundary between
// the two passes. An authored label must not survive the first attach; a label Go
// collected must survive the re-settle.
func TestDemand_ResettlePreservesLabelsButAttachDoesNot(t *testing.T) {
	steps := stepIndex(7, step7Rows())

	t.Run("attach discards a label the model authored", func(t *testing.T) {
		ins := []models.Insight{demandInsight()}
		ins[0].FigureCoverage = &models.FigureCoverage{Labels: []string{"99996", "150000", "3299"}}
		attachFigureVerdicts(ins, steps)
		if len(ins[0].FigureCoverage.Labels) != 0 {
			t.Fatalf("model-authored labels survived the attach: %v", ins[0].FigureCoverage.Labels)
		}
	})

	t.Run("resettle keeps a label Go collected", func(t *testing.T) {
		ins := []models.Insight{demandInsight()}
		attachFigureVerdicts(ins, steps)
		mergeDemandReply(ins, map[int][]numeralHit{1: {{raw: "150000", value: 150000}}}, figureDemandReply{
			NotMeasurements: []figureDemandLabel{{Insight: 1, Figure: "150000", Why: "the table total"}},
		})
		resettleFigures(ins, steps)
		if ins[0].FigureCoverage.Labelled != 1 {
			t.Fatalf("labelled = %d after resettle, want 1 (labels %v)",
				ins[0].FigureCoverage.Labelled, ins[0].FigureCoverage.Labels)
		}
	})
}

// TestDemand_CapBoundsTheQuestion asserts the demand list is bounded. The extractor
// over-produces by design, so an area dense with bucket edges must not turn one call
// into a hundred questions.
func TestDemand_CapBoundsTheQuestion(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString(strconv.Itoa(1000+i*7) + " rows, ")
	}
	ins := []models.Insight{{Name: "Dense", Description: sb.String(), SourceSteps: []int{7}}}
	if n := len(undeclaredNumerals(ins[0])); n <= maxDemandedFigures {
		t.Fatalf("fixture produced only %d numerals; it must exceed the cap of %d to test it", n, maxDemandedFigures)
	}

	o := &Orchestrator{}
	// No AI client: the pass returns before calling out, and must report no demand
	// rather than a demand it never made.
	if got := o.demandFigureDeclarations(context.Background(), "area", ins, stepIndex(7, step7Rows()), 0); got.demanded != 0 {
		t.Fatalf("with no client the pass must not report a demand, got %d", got.demanded)
	}
}
