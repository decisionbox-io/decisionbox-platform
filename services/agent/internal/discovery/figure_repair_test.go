package discovery

import (
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func stepIndex(step int, rows []map[string]any) map[int]*models.ExplorationStep {
	return map[int]*models.ExplorationStep{
		step: {Step: step, Action: "query_data", QueryResult: rows},
	}
}

// TestFigureRepair_CorrectsRunThreesRepeatedFalseFigure reconstructs the insight
// that carried run 3's most repeated falsehood and asserts the whole path end to
// end: the declaration is refuted, the numeral is swapped for the one the
// arithmetic produced, every field carrying it is updated together, and the
// re-settled verdict holds.
//
// The prose is the shipped text, shortened. "All 100,000 buyers (150,000 total
// customers minus 50,004 who never ordered)" states the subtraction that gives
// 99,996 and then writes 100,000.
func TestFigureRepair_CorrectsRunThreesRepeatedFalseFigure(t *testing.T) {
	ins := []models.Insight{{
		Name:        "Buyer base sized at 100,000 buyers",
		Description: "All 100,000 buyers remain after removing those who never ordered.",
		Indicators:  []string{"100,000 buyers placed at least one order"},
		SourceSteps: []int{7},
		FigureClaims: []models.FigureClaim{{
			Figure: "100,000 buyers", Value: 100000, Step: 7, Kind: models.FigureSum,
			Column: "customers", Scope: "bucket != '0_never_ordered'",
		}},
	}}
	steps := stepIndex(7, step7Rows())

	attachFigureVerdicts(ins, steps)
	if got := countRefutedFigures(ins[0].FigureVerdicts); got != 1 {
		t.Fatalf("refuted figures before repair = %d, want 1 (verdicts %+v)", got, ins[0].FigureVerdicts)
	}

	if n := repairRefutedFigures("retention", ins, steps); n != 1 {
		t.Fatalf("substitutions = %d, want 1", n)
	}

	for _, f := range []struct{ name, text string }{
		{"name", ins[0].Name},
		{"description", ins[0].Description},
		{"indicator", ins[0].Indicators[0]},
	} {
		if strings.Contains(f.text, "100,000") {
			t.Errorf("%s still says 100,000: %q", f.name, f.text)
		}
		if !strings.Contains(f.text, "99,996") {
			t.Errorf("%s was not corrected to 99,996: %q", f.name, f.text)
		}
	}
	if got := countRefutedFigures(ins[0].FigureVerdicts); got != 0 {
		t.Errorf("refuted figures after repair = %d, want 0", got)
	}
	if len(ins[0].FigureFixes) != 1 || ins[0].FigureFixes[0].Text != "100,000 -> 99,996" {
		t.Errorf("fix record = %+v, want one 100,000 -> 99,996", ins[0].FigureFixes)
	}
}

// TestFigureRepair_RendersAtTheWrittenPrecision covers the other observed shapes.
// A correction has to read like the sentence it lands in: a percentage written to
// one decimal place stays at one decimal place rather than becoming
// 49.342999999999996.
func TestFigureRepair_RendersAtTheWrittenPrecision(t *testing.T) {
	for _, tc := range []struct {
		name      string
		figure    string
		evaluated float64
		wantFrom  string
		wantTo    string
	}{
		{"a percentage to one place", "49.7%", 49.343, "49.7", "49.3"},
		{"a headline percentage", "47.1%", 49.343, "47.1", "49.3"},
		{"a grouped integer", "150,004", 150000, "150,004", "150,000"},
		{"an ungrouped integer", "267065", 266465, "267065", "266465"},
		{"a scaled figure keeps its scale", "$6.9B", 6645321129.9441, "6.9", "6.6"},
		{"a thousands-scaled figure", "911K", 899000, "911", "899"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from, to, ok := renderFigureSwap(tc.figure, tc.evaluated)
			if !ok {
				t.Fatalf("declined to render a swap for %q", tc.figure)
			}
			if from != tc.wantFrom || to != tc.wantTo {
				t.Fatalf("got %q -> %q, want %q -> %q", from, to, tc.wantFrom, tc.wantTo)
			}
		})
	}
}

// TestFigureRepair_DeclinesRatherThanGuess is the red-proof for the free-correction
// path, which is the one nothing reviews. Every case here must leave the prose
// untouched and the figure visibly refuted, because a mechanical swap that writes a
// new falsehood is worse than a refutation a reader can see.
func TestFigureRepair_DeclinesRatherThanGuess(t *testing.T) {
	base := func() models.Insight {
		return models.Insight{
			Name:        "Materials",
			Description: "TIN leads at $6.9B.",
			SourceSteps: []int{28},
			FigureClaims: []models.FigureClaim{{
				Figure: "$6.9B", Value: 6900000000, Step: 28, Kind: models.FigureCell,
				Column: "net_rev", Row: "material = 'TIN'",
			}},
		}
	}

	t.Run("a numeral too short to be distinctive", func(t *testing.T) {
		ins := []models.Insight{{
			Name:        "Five materials, 21 brands",
			Description: "The result holds 21 rows.",
			SourceSteps: []int{28},
			FigureClaims: []models.FigureClaim{{
				Figure: "21", Value: 21, Step: 28, Kind: models.FigureCount,
			}},
		}}
		steps := stepIndex(28, step28Rows())
		attachFigureVerdicts(ins, steps)
		if countRefutedFigures(ins[0].FigureVerdicts) != 1 {
			t.Fatalf("expected the count claim to be refuted, got %+v", ins[0].FigureVerdicts)
		}
		if n := repairRefutedFigures("product", ins, steps); n != 0 {
			t.Fatalf("substitutions = %d, want 0: a two-digit numeral is not safe to swap", n)
		}
		if countRefutedFigures(ins[0].FigureVerdicts) != 1 {
			t.Error("the figure should still be refuted and visible")
		}
	})

	t.Run("the same numeral twice in one field", func(t *testing.T) {
		ins := []models.Insight{base()}
		ins[0].Description = "TIN leads at $6.9B, and BRASS also reads $6.9B."
		steps := stepIndex(28, step28Rows())
		attachFigureVerdicts(ins, steps)
		if n := repairRefutedFigures("product", ins, steps); n != 0 {
			t.Fatalf("substitutions = %d, want 0: which 6.9 to swap is ambiguous", n)
		}
	})

	t.Run("the same numeral measuring something else", func(t *testing.T) {
		ins := []models.Insight{base()}
		ins[0].Indicators = []string{"Average discount 6.9% across the five"}
		steps := stepIndex(28, step28Rows())
		attachFigureVerdicts(ins, steps)
		if n := repairRefutedFigures("product", ins, steps); n != 0 {
			t.Fatalf("substitutions = %d, want 0: 6.9%% is a discount, not billions", n)
		}
		if !strings.Contains(ins[0].Indicators[0], "6.9%") {
			t.Errorf("the unrelated indicator was rewritten: %q", ins[0].Indicators[0])
		}
	})

	t.Run("affected_count carries the same number", func(t *testing.T) {
		ins := []models.Insight{{
			Name:          "Buyer base",
			Description:   "All 100,000 buyers ordered.",
			AffectedCount: 100000,
			SourceSteps:   []int{7},
			FigureClaims: []models.FigureClaim{{
				Figure: "100,000 buyers", Value: 100000, Step: 7, Kind: models.FigureSum,
				Column: "customers", Scope: "bucket != '0_never_ordered'",
			}},
		}}
		steps := stepIndex(7, step7Rows())
		attachFigureVerdicts(ins, steps)
		if n := repairRefutedFigures("retention", ins, steps); n != 0 {
			t.Fatalf("substitutions = %d, want 0: the prose would disagree with affected_count", n)
		}
	})

	t.Run("an undecidable figure is never substituted", func(t *testing.T) {
		ins := []models.Insight{base()}
		ins[0].FigureClaims[0].Row = "material = 'PLUTONIUM'"
		steps := stepIndex(28, step28Rows())
		attachFigureVerdicts(ins, steps)
		if ins[0].FigureVerdicts[0].Status != models.FigureUndecidable {
			t.Fatalf("status = %q, want undecidable", ins[0].FigureVerdicts[0].Status)
		}
		if n := repairRefutedFigures("product", ins, steps); n != 0 {
			t.Fatalf("substitutions = %d, want 0", n)
		}
		if !strings.Contains(ins[0].Description, "$6.9B") {
			t.Errorf("prose was edited on an undecidable verdict: %q", ins[0].Description)
		}
	})
}

// TestFigureCoverage_CountsWhatWentUnchecked is the counter that makes a null result
// tellable from an unreached one. The comparable layer looked perfect on a quarter
// of documents because those documents had declared nothing to be wrong about.
func TestFigureCoverage_CountsWhatWentUnchecked(t *testing.T) {
	t.Run("an insight declaring nothing", func(t *testing.T) {
		ins := []models.Insight{{
			Name:        "TIN leads at $6.645B of $33.12B",
			Description: "Five materials span $6.595B to $6.645B across 911,395 lines.",
			SourceSteps: []int{28},
		}}
		attachFigureVerdicts(ins, stepIndex(28, step28Rows()))
		cov := ins[0].FigureCoverage
		if cov == nil {
			t.Fatal("coverage was not recorded for an insight that declared nothing")
		}
		if cov.Written == 0 {
			t.Fatal("the prose carries figures and none was counted")
		}
		if cov.Declared != 0 {
			t.Errorf("declared = %d, want 0", cov.Declared)
		}
		if len(ins[0].FigureVerdicts) != 0 {
			t.Errorf("verdicts = %d, want none", len(ins[0].FigureVerdicts))
		}
	})

	t.Run("a declared figure counts even when it is refuted", func(t *testing.T) {
		ins := []models.Insight{{
			Name:        "Buyer base 100,000",
			Description: "All 100,000 buyers ordered.",
			SourceSteps: []int{7},
			FigureClaims: []models.FigureClaim{{
				Figure: "100,000", Value: 100000, Step: 7, Kind: models.FigureSum,
				Column: "customers", Scope: "bucket != '0_never_ordered'",
			}},
		}}
		attachFigureVerdicts(ins, stepIndex(7, step7Rows()))
		cov := ins[0].FigureCoverage
		if cov.Declared != 1 {
			t.Fatalf("declared = %d of %d written, want 1: a refuted figure is a declared figure",
				cov.Declared, cov.Written)
		}
	})

	t.Run("the model cannot author its own coverage or verdicts", func(t *testing.T) {
		ins := []models.Insight{{
			Name:           "Anything",
			Description:    "Holds 911,395 lines.",
			SourceSteps:    []int{28},
			FigureCoverage: &models.FigureCoverage{Written: 99, Declared: 99},
			FigureVerdicts: []models.FigureVerdict{{Figure: "invented", Status: models.FigureHolds}},
		}}
		attachFigureVerdicts(ins, stepIndex(28, step28Rows()))
		if len(ins[0].FigureVerdicts) != 0 {
			t.Errorf("a volunteered verdict survived: %+v", ins[0].FigureVerdicts)
		}
		if ins[0].FigureCoverage.Written == 99 {
			t.Error("a volunteered coverage count survived")
		}
	})
}

// TestWrittenNumerals_ExcludesWhatMeasuresNothing pins the extractor's exclusions.
// Its precision as a truth check was 7-14% and it drives nothing, but a coverage
// count that included every year and brand number would report coverage far below
// what the model actually declared.
func TestWrittenNumerals_ExcludesWhatMeasuresNothing(t *testing.T) {
	got := writtenNumerals(
		"Brand#35 led in 1997 with 37,533 lines over step 9, Q4 included, 5 materials, 20.1% share",
	)
	has := func(v float64) bool {
		for _, g := range got {
			if g == v {
				return true
			}
		}
		return false
	}
	for _, want := range []float64{37533, 20.1} {
		if !has(want) {
			t.Errorf("%v was not extracted from %v", want, got)
		}
	}
	for _, unwanted := range []struct {
		v   float64
		why string
	}{
		{35, "part of the identifier Brand#35"},
		{1997, "a year"},
		{9, "the step number it names"},
		{4, "part of the identifier Q4"},
		{5, "a bare count under thirteen"},
	} {
		if has(unwanted.v) {
			t.Errorf("%v was extracted but is %s: %v", unwanted.v, unwanted.why, got)
		}
	}
}
