package discovery

import (
	"math"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Every fixture here is rows verbatim from a frozen corpus, and every figure the tests
// assert on is one hand adjudication settled against a DuckDB oracle built from the
// source .tbl bytes. Truth never comes from the rows the pipeline stored; the rows are
// the evidence the check runs over, and the expected answers were computed independently.

func step7Rows() []map[string]any {
	return []map[string]any{
		{"bucket": "0_never_ordered", "customers": 50004.0, "total_orders": 0.0, "avg_spent": 0.0},
		{"bucket": "16_plus", "customers": 44546.0, "total_orders": 936031.0, "avg_spent": 3174782.6282784087},
		{"bucket": "1_order", "customers": 17.0, "total_orders": 17.0, "avg_spent": 174746.96294117646},
		{"bucket": "2_5_orders", "customers": 3299.0, "total_orders": 14425.0, "avg_spent": 667436.0766899061},
		{"bucket": "6_15_orders", "customers": 52134.0, "total_orders": 549527.0, "avg_spent": 1595898.9751946905},
	}
}

func step5Rows() []map[string]any {
	return []map[string]any{
		{"l_returnflag": "N", "l_linestatus": "O", "lines": 3004998.0, "net_revenue": 109189591897.472},
		{"l_returnflag": "R", "l_linestatus": "F", "lines": 1478870.0, "net_revenue": 53741292684.604},
		{"l_returnflag": "A", "l_linestatus": "F", "lines": 1478493.0, "net_revenue": 53758257134.87},
		{"l_returnflag": "N", "l_linestatus": "F", "lines": 38854.0, "net_revenue": 1413082168.0541},
	}
}

func step28Rows() []map[string]any {
	return []map[string]any{
		{"material": "TIN", "lines": 182467.0, "net_rev": 6645321129.9441, "avg_retail": 1498.56},
		{"material": "BRASS", "lines": 182778.0, "net_rev": 6641816775.8653, "avg_retail": 1499.89},
		{"material": "COPPER", "lines": 182040.0, "net_rev": 6639848155.3871, "avg_retail": 1501.95},
		{"material": "NICKEL", "lines": 181949.0, "net_rev": 6595240011.8071, "avg_retail": 1497.47},
		{"material": "STEEL", "lines": 182161.0, "net_rev": 6594526319.0763, "avg_retail": 1498.58},
	}
}

// step45Rows stores a share as a FRACTION. Figures drawn from it are written as
// percentages, which is the storage convention the model cannot see from the prose.
func step45Rows() []map[string]any {
	return []map[string]any{
		{"loyalty_bucket": "11plus_brands", "avg_top_brand_share": 0.14139702489528622},
		{"loyalty_bucket": "4-10_brands", "avg_top_brand_share": 0.22262278177021078},
		{"loyalty_bucket": "1-3_brands", "avg_top_brand_share": 0.5816180685719439},
	}
}

func evidence(step int, rows []map[string]any) map[int]StepRows {
	return map[int]StepRows{step: {Rows: rows}}
}

func stepIndex(step int, rows []map[string]any) map[int]*models.ExplorationStep {
	return map[int]*models.ExplorationStep{
		step: {Step: step, Action: "query_data", QueryResult: rows},
	}
}

func oneVerdict(t *testing.T, f models.Figure, steps map[int]StepRows) models.FigureVerdict {
	t.Helper()
	v := EvaluateFigures([]models.Figure{f}, steps)
	if len(v) != 1 {
		t.Fatalf("got %d verdicts, want 1", len(v))
	}
	return v[0]
}

// TestFigure_RenderedFormIsWhatTheReaderSees pins the renderer, which is the piece that
// makes this design work: because Go prints the figure, Go knows to what precision it was
// printed, and nothing has to read a number back out of a string.
func TestFigure_RenderedFormIsWhatTheReaderSees(t *testing.T) {
	for _, tc := range []struct {
		name string
		fig  models.Figure
		want string
	}{
		{"a count", models.Figure{Value: 8668, Unit: models.UnitCount}, "8,668"},
		{"a large count", models.Figure{Value: 1500000, Unit: models.UnitCount}, "1,500,000"},
		{"currency at billions", models.Figure{Value: 8476238553, Unit: models.UnitCurrency, Scale: models.ScaleBillions, Decimals: 2}, "$8.48B"},
		{"currency at millions", models.Figure{Value: 317195404, Unit: models.UnitCurrency, Scale: models.ScaleMillions, Decimals: 0}, "$317M"},
		{"currency unscaled", models.Figure{Value: 151219.54, Unit: models.UnitCurrency, Decimals: 0}, "$151,220"},
		{"negative currency", models.Figure{Value: -17753, Unit: models.UnitCurrency, Decimals: 0}, "-$17,753"},
		{"a percentage", models.Figure{Value: 24.66, Unit: models.UnitPercent, Decimals: 2}, "24.66%"},
		{"a percentage to one place", models.Figure{Value: 49.343, Unit: models.UnitPercent, Decimals: 1}, "49.3%"},
		{"a multiple", models.Figure{Value: 1.0077, Unit: models.UnitMultiple, Decimals: 3}, "1.008x"},
		{"days", models.Figure{Value: 111.5, Unit: models.UnitDays, Decimals: 1}, "111.5 days"},
		{"approximate at thousands", models.Figure{Value: 911395, Unit: models.UnitCount, Scale: models.ScaleThousands, Decimals: 0, Approx: true}, "~911K"},
		{"plain", models.Figure{Value: 25.52, Unit: models.UnitPlain, Decimals: 2}, "25.52"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderFigure(tc.fig); got != tc.want {
				t.Fatalf("rendered %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFigure_IntervalComesFromTheFormatNotTheText is the property the whole layer rests
// on, and the one the previous design could not hold.
//
// Run 3 wrote 100,000 where the rows give 99,996 -- 0.004% apart, and false -- while the
// same documents wrote $6.645B for 6,645,321,130, which is 0.005% apart and true. No
// relative tolerance separates those. The number of places printed does, and here Go
// chose the places, so it is arithmetic rather than a parse.
func TestFigure_IntervalComesFromTheFormatNotTheText(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fig       models.Figure
		wantSlack float64
	}{
		{"a count to the unit", models.Figure{Value: 100000, Unit: models.UnitCount}, 0.5},
		{"three decimals at billions", models.Figure{Value: 6645000000, Unit: models.UnitCurrency, Scale: models.ScaleBillions, Decimals: 3}, 500000},
		{"two decimals at billions", models.Figure{Value: 34860000000, Unit: models.UnitCurrency, Scale: models.ScaleBillions, Decimals: 2}, 5000000},
		{"one decimal percent", models.Figure{Value: 49.7, Unit: models.UnitPercent, Decimals: 1}, 0.05},
		{"two decimal percent", models.Figure{Value: 24.66, Unit: models.UnitPercent, Decimals: 2}, 0.005},
		{"zero decimals at thousands", models.Figure{Value: 911000, Unit: models.UnitCount, Scale: models.ScaleThousands}, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := figureSlack(tc.fig); math.Abs(got-tc.wantSlack) > 1e-9 {
				t.Fatalf("slack %v, want %v", got, tc.wantSlack)
			}
		})
	}
}

// TestFigure_KnownFalseFiguresAreRefuted is the red-proof, carried over from the parsing
// design. Every case is a figure hand adjudication found false against the oracle, across
// all three corpora. If any of these flips to holds, the layer has stopped catching what
// it was built for.
func TestFigure_KnownFalseFiguresAreRefuted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fig   models.Figure
		step  int
		rows  []map[string]any
		wantX float64
	}{
		{
			// Run 3, three insights: "All 100,000 buyers", where the four ordering
			// buckets sum to 99,996 -- in a sentence that spells out the subtraction.
			name: "100,000 buyers for 99,996 (run 3)",
			fig: models.Figure{ID: "f1", Value: 100000, Unit: models.UnitCount, Step: 7,
				Kind: models.FigureSum, Column: "customers", Scope: "bucket != '0_never_ordered'"},
			step: 7, rows: step7Rows(), wantX: 99996,
		},
		{
			// Run 3, a headline: 50,004 + its own wrong 100,000.
			name: "150,004 customers for 150,000 (run 3)",
			fig: models.Figure{ID: "f1", Value: 150004, Unit: models.UnitCount, Step: 7,
				Kind: models.FigureSum, Column: "customers"},
			step: 7, rows: step7Rows(), wantX: 150000,
		},
		{
			// Run 3, rejected by validation: the headline said 47.1% and the body 49.7%
			// for a share the rows put at 49.343%.
			name: "47.1% returns share for 49.343% (run 3 headline)",
			fig: models.Figure{ID: "f1", Value: 47.1, Unit: models.UnitPercent, Decimals: 1, Step: 5,
				Kind: models.FigureRatio, Column: "net_revenue",
				Row: "l_returnflag = 'R' AND l_linestatus = 'F'", Scope: "l_linestatus = 'F'"},
			step: 5, rows: step5Rows(), wantX: 49.343,
		},
		{
			name: "49.7% returns share for 49.343% (run 3 body)",
			fig: models.Figure{ID: "f1", Value: 49.7, Unit: models.UnitPercent, Decimals: 1, Step: 5,
				Kind: models.FigureRatio, Column: "net_revenue",
				Row: "l_returnflag = 'R' AND l_linestatus = 'F'", Scope: "l_linestatus = 'F'"},
			step: 5, rows: step5Rows(), wantX: 49.343,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := oneVerdict(t, tc.fig, evidence(tc.step, tc.rows))
			if v.Status != models.FigureFails {
				t.Fatalf("status = %q, want fails (evaluated %v, reason %q)", v.Status, v.Evaluated, v.Reason)
			}
			if math.Abs(v.Evaluated-tc.wantX) > math.Max(math.Abs(tc.wantX)*1e-6, 0.001) {
				t.Fatalf("evaluated = %v, want %v", v.Evaluated, tc.wantX)
			}
		})
	}
}

// TestFigure_SoundFiguresHold asserts the check stays quiet on figures hand adjudication
// found true, including the rounding the prose necessarily does.
func TestFigure_SoundFiguresHold(t *testing.T) {
	for _, tc := range []struct {
		name string
		fig  models.Figure
		step int
		rows []map[string]any
	}{
		{
			name: "a cell rounded to four figures",
			fig: models.Figure{Value: 6645000000, Unit: models.UnitCurrency, Scale: models.ScaleBillions, Decimals: 3,
				Step: 28, Kind: models.FigureCell, Column: "net_rev", Row: "material = 'TIN'"},
			step: 28, rows: step28Rows(),
		},
		{
			name: "a column total written approximately",
			fig: models.Figure{Value: 911000, Unit: models.UnitCount, Scale: models.ScaleThousands, Approx: true,
				Step: 28, Kind: models.FigureSum, Column: "lines"},
			step: 28, rows: step28Rows(),
		},
		{
			name: "a share of a column total as a percentage",
			fig: models.Figure{Value: 20.1, Unit: models.UnitPercent, Decimals: 1,
				Step: 28, Kind: models.FigureRatio, Column: "net_rev", Row: "material = 'TIN'"},
			step: 28, rows: step28Rows(),
		},
		{
			name: "a row count",
			fig:  models.Figure{Value: 5, Unit: models.UnitCount, Step: 28, Kind: models.FigureCount},
			step: 28, rows: step28Rows(),
		},
		{
			name: "the body figure that was right",
			fig: models.Figure{Value: 49.4, Unit: models.UnitPercent, Decimals: 1, Step: 5,
				Kind: models.FigureRatio, Column: "lines",
				Row: "l_returnflag = 'R' AND l_linestatus = 'F'", Scope: "l_linestatus = 'F'"},
			step: 5, rows: step5Rows(),
		},
		{
			name: "a scoped sum",
			fig: models.Figure{Value: 99996, Unit: models.UnitCount, Step: 7, Kind: models.FigureSum,
				Column: "customers", Scope: "bucket != '0_never_ordered'"},
			step: 7, rows: step7Rows(),
		},
		{
			name: "a difference of two cells",
			fig: models.Figure{Value: 50800000, Unit: models.UnitCurrency, Scale: models.ScaleMillions, Decimals: 1,
				Step: 28, Kind: models.FigureDiff, Column: "net_rev",
				Row: "material = 'TIN'", Other: "material = 'STEEL'"},
			step: 28, rows: step28Rows(),
		},
		{
			// TIN over STEEL as a multiple: 1.0077.
			name: "a ratio of two cells",
			fig: models.Figure{Value: 1.008, Unit: models.UnitMultiple, Decimals: 3, Step: 28,
				Kind: models.FigureRatio, Column: "net_rev",
				Row: "material = 'TIN'", Other: "material = 'STEEL'"},
			step: 28, rows: step28Rows(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := oneVerdict(t, tc.fig, evidence(tc.step, tc.rows))
			if v.Status != models.FigureHolds {
				t.Fatalf("status = %q, want holds (evaluated %v, reason %q)", v.Status, v.Evaluated, v.Reason)
			}
		})
	}
}

// TestFigure_PercentStorageConventionIsAccepted covers the defect that produced three of
// five refutations in one replay: a share column stored as a fraction, correctly written
// as a percentage.
func TestFigure_PercentStorageConventionIsAccepted(t *testing.T) {
	for _, tc := range []struct {
		value  float64
		bucket string
	}{
		{14.14, "11plus_brands"},
		{22.26, "4-10_brands"},
		{58.16, "1-3_brands"},
	} {
		t.Run(tc.bucket, func(t *testing.T) {
			v := oneVerdict(t, models.Figure{
				Value: tc.value, Unit: models.UnitPercent, Decimals: 2, Step: 45,
				Kind: models.FigureCell, Column: "avg_top_brand_share",
				Row: "loyalty_bucket = '" + tc.bucket + "'",
			}, evidence(45, step45Rows()))
			if v.Status != models.FigureHolds {
				t.Fatalf("status = %q, want holds (evaluated %v)", v.Status, v.Evaluated)
			}
		})
	}

	t.Run("a column already in percent still holds", func(t *testing.T) {
		rows := []map[string]any{{"decile": 1.0, "pct_of_total": 18.38}}
		v := oneVerdict(t, models.Figure{
			Value: 18.38, Unit: models.UnitPercent, Decimals: 2, Step: 14,
			Kind: models.FigureCell, Column: "pct_of_total", Row: "decile = 1",
		}, evidence(14, rows))
		if v.Status != models.FigureHolds {
			t.Fatalf("status = %q, want holds", v.Status)
		}
	})

	t.Run("a percentage that is simply wrong is still refuted", func(t *testing.T) {
		v := oneVerdict(t, models.Figure{
			Value: 30.0, Unit: models.UnitPercent, Decimals: 1, Step: 45,
			Kind: models.FigureCell, Column: "avg_top_brand_share",
			Row: "loyalty_bucket = '11plus_brands'",
		}, evidence(45, step45Rows()))
		if v.Status != models.FigureFails {
			t.Fatalf("status = %q, want fails", v.Status)
		}
	})
}

// TestFigure_UndecidableNeverReadsAsRefuted is the guard the quantifier evaluator learned
// the hard way: on that corpus two of five refutations were true sentences the grammar
// could not express, so the checker was reporting its own reach as the model being wrong.
func TestFigure_UndecidableNeverReadsAsRefuted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fig   models.Figure
		steps map[int]StepRows
	}{
		{"a step the insight did not cite",
			models.Figure{Value: 1, Step: 99, Kind: models.FigureCell, Column: "customers", Row: "bucket = '1_order'"},
			evidence(7, step7Rows())},
		{"a column the rows do not carry",
			models.Figure{Value: 1, Step: 7, Kind: models.FigureSum, Column: "margin"},
			evidence(7, step7Rows())},
		{"a row selector matching several rows",
			models.Figure{Value: 1, Step: 5, Kind: models.FigureCell, Column: "lines", Row: "l_linestatus = 'F'"},
			evidence(5, step5Rows())},
		{"a row selector matching none",
			models.Figure{Value: 1, Step: 28, Kind: models.FigureCell, Column: "net_rev", Row: "material = 'PLUTONIUM'"},
			evidence(28, step28Rows())},
		{"a multi-row step with no selector at all",
			models.Figure{Value: 1, Step: 28, Kind: models.FigureCell, Column: "net_rev"},
			evidence(28, step28Rows())},
		{"a filter richer than the grammar reads",
			models.Figure{Value: 1, Step: 28, Kind: models.FigureSum, Column: "net_rev", Scope: "material = 'TIN' OR material = 'STEEL'"},
			evidence(28, step28Rows())},
		{"a kind that does not exist",
			models.Figure{Value: 1, Step: 28, Kind: "median", Column: "net_rev"},
			evidence(28, step28Rows())},
		{"no kind at all",
			models.Figure{Value: 1, Step: 28, Column: "net_rev"},
			evidence(28, step28Rows())},
		{"a scope selecting no rows",
			models.Figure{Value: 1, Step: 28, Kind: models.FigureSum, Column: "net_rev", Scope: "lines > 999999"},
			evidence(28, step28Rows())},
		{"a step with no rows",
			models.Figure{Value: 1, Step: 3, Kind: models.FigureCount},
			map[int]StepRows{3: {Rows: nil}}},
		{"a diff with only one operand",
			models.Figure{Value: 158, Step: 18, Kind: models.FigureDiff, Column: "total_parts", Row: "total_parts = 200000"},
			evidence(18, []map[string]any{{"total_parts": 200000.0, "ordered_parts": 199842.0}})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := oneVerdict(t, tc.fig, tc.steps)
			if v.Status != models.FigureUndecidable {
				t.Fatalf("status = %q, want undecidable (evaluated %v, reason %q)", v.Status, v.Evaluated, v.Reason)
			}
			if v.Reason == "" {
				t.Fatal("undecidable with no reason: the verdict has to say what it could not read")
			}
		})
	}
}

// TestFigure_CorrectionIsAFieldAssignment is the second thing the format buys. Under the
// parsing design a correction was string surgery across four kinds of field, with refusals
// for ambiguity -- and it still once rewrote the wrong numeral in a compound phrase and
// reported the insight as holding. Here it sets a value.
func TestFigure_CorrectionIsAFieldAssignment(t *testing.T) {
	ins := []models.Insight{{
		Name:        "Buyer base sized at {{f1}}",
		Description: "All {{f1}} buyers remain after removing those who never ordered.",
		Indicators:  []string{"{{f1}} buyers placed at least one order"},
		SourceSteps: []int{7},
		Figures: []models.Figure{{
			ID: "f1", Value: 100000, Unit: models.UnitCount, Step: 7,
			Kind: models.FigureSum, Column: "customers", Scope: "bucket != '0_never_ordered'",
		}},
	}}
	steps := stepIndex(7, step7Rows())

	attachFigureVerdicts(ins, steps)
	if countRefutedFigures(ins[0].FigureVerdicts) != 1 {
		t.Fatalf("expected one refutation, got %+v", ins[0].FigureVerdicts)
	}

	if n := correctRefutedFigures("retention", ins, steps); n != 1 {
		t.Fatalf("corrections = %d, want 1", n)
	}
	if ins[0].Figures[0].Value != 99996 {
		t.Fatalf("value = %v, want 99996", ins[0].Figures[0].Value)
	}
	if got := ins[0].FigureCorrections[0].Text; got != "100,000 -> 99,996" {
		t.Fatalf("correction text = %q", got)
	}
	if countRefutedFigures(ins[0].FigureVerdicts) != 0 {
		t.Error("the figure is still refuted after correction")
	}

	// And the correction reaches every sentence, because they all reference one figure.
	renderInsightFigures(ins)
	for _, field := range []string{ins[0].Name, ins[0].Description, ins[0].Indicators[0]} {
		if strings.Contains(field, "100,000") {
			t.Errorf("a field still says 100,000: %q", field)
		}
		if !strings.Contains(field, "99,996") {
			t.Errorf("a field was not corrected: %q", field)
		}
	}
}
