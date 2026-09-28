package discovery

import (
	"math"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Every fixture here is rows verbatim from a frozen corpus, and every figure the
// tests assert on is one hand adjudication settled against a DuckDB oracle built
// from the source .tbl bytes. Truth never comes from the rows the pipeline stored;
// the rows are the evidence the check runs over, and the expected answers were
// computed independently.

// step7Rows is the customer-order-count bucket step from run 3, verbatim. It is
// the evidence behind the run's most repeated false figure: three insights wrote
// "100,000 buyers" where the four ordering buckets sum to 99,996, and two wrote
// "150,004 customers" where all five sum to 150,000.
func step7Rows() []map[string]any {
	return []map[string]any{
		{"bucket": "0_never_ordered", "customers": 50004.0, "total_orders": 0.0, "avg_spent": 0.0},
		{"bucket": "16_plus", "customers": 44546.0, "total_orders": 936031.0, "avg_spent": 3174782.6282784087},
		{"bucket": "1_order", "customers": 17.0, "total_orders": 17.0, "avg_spent": 174746.96294117646},
		{"bucket": "2_5_orders", "customers": 3299.0, "total_orders": 14425.0, "avg_spent": 667436.0766899061},
		{"bucket": "6_15_orders", "customers": 52134.0, "total_orders": 549527.0, "avg_spent": 1595898.9751946905},
	}
}

// step5Rows is the return-flag step from run 3, verbatim. Its insight was rejected
// by pipeline validation for writing 47.1% in the headline and 49.7% in the body
// where the fulfilled net total gives 49.343%.
func step5Rows() []map[string]any {
	return []map[string]any{
		{"l_returnflag": "N", "l_linestatus": "O", "lines": 3004998.0, "net_revenue": 109189591897.472},
		{"l_returnflag": "R", "l_linestatus": "F", "lines": 1478870.0, "net_revenue": 53741292684.604},
		{"l_returnflag": "A", "l_linestatus": "F", "lines": 1478493.0, "net_revenue": 53758257134.87},
		{"l_returnflag": "N", "l_linestatus": "F", "lines": 38854.0, "net_revenue": 1413082168.0541},
	}
}

// step28Rows is the 1997 material step from run 3, verbatim. Hand adjudication
// found every figure drawn from it true, so it is the fixture for asserting the
// checker stays quiet on sound prose.
func step28Rows() []map[string]any {
	return []map[string]any{
		{"material": "TIN", "lines": 182467.0, "net_rev": 6645321129.9441, "avg_retail": 1498.56},
		{"material": "BRASS", "lines": 182778.0, "net_rev": 6641816775.8653, "avg_retail": 1499.89},
		{"material": "COPPER", "lines": 182040.0, "net_rev": 6639848155.3871, "avg_retail": 1501.95},
		{"material": "NICKEL", "lines": 181949.0, "net_rev": 6595240011.8071, "avg_retail": 1497.47},
		{"material": "STEEL", "lines": 182161.0, "net_rev": 6594526319.0763, "avg_retail": 1498.58},
	}
}

func evidence(step int, rows []map[string]any) map[int]StepRows {
	return map[int]StepRows{step: {Rows: rows}}
}

func oneVerdict(t *testing.T, c models.FigureClaim, steps map[int]StepRows) models.FigureVerdict {
	t.Helper()
	v := EvaluateFigureClaims([]models.FigureClaim{c}, steps)
	if len(v) != 1 {
		t.Fatalf("got %d verdicts, want 1", len(v))
	}
	return v[0]
}

// TestFigure_RoundNumberSubstitutionIsRefuted is the red-proof for the falsehood
// that occurred most often in run 3. The model wrote a clean 100,000 in a sentence
// that spells out the subtraction producing it, three times, in three areas. The
// arithmetic it would have to declare gives 99,996.
func TestFigure_RoundNumberSubstitutionIsRefuted(t *testing.T) {
	v := oneVerdict(t, models.FigureClaim{
		Figure: "100,000", Value: 100000, Step: 7, Kind: models.FigureSum,
		Column: "customers", Scope: "bucket != '0_never_ordered'",
	}, evidence(7, step7Rows()))

	if v.Status != models.FigureFails {
		t.Fatalf("status = %q, want fails (reason %q)", v.Status, v.Reason)
	}
	if v.Evaluated != 99996 {
		t.Fatalf("evaluated = %v, want 99996", v.Evaluated)
	}
	if v.Claimed != 100000 {
		t.Fatalf("claimed = %v, want 100000", v.Claimed)
	}
}

// TestFigure_SumOfAnAlteredNumberIsRefuted covers the second occurrence: having
// written 100,000, the model added 50,004 to it and reported 150,004 as the size of
// the customer base, in a headline. All five buckets sum to 150,000.
func TestFigure_SumOfAnAlteredNumberIsRefuted(t *testing.T) {
	v := oneVerdict(t, models.FigureClaim{
		Figure: "150,004", Value: 150004, Step: 7, Kind: models.FigureSum, Column: "customers",
	}, evidence(7, step7Rows()))

	if v.Status != models.FigureFails {
		t.Fatalf("status = %q, want fails", v.Status)
	}
	if v.Evaluated != 150000 {
		t.Fatalf("evaluated = %v, want 150000", v.Evaluated)
	}
}

// TestFigure_WrongDenominatorShareIsRefuted is the red-proof for the one falsehood
// pipeline validation caught on its own. The headline wrote 47.1% and the body 49.7%
// for a share the rows put at 49.343%.
//
// Both are refuted, and the 49.7% case is the tighter of the two at 0.72% -- the
// smallest true error measured across all three corpora, and therefore the one that
// fixes figureTolerance. If the tolerance were widened past that, this test fails.
func TestFigure_WrongDenominatorShareIsRefuted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fig   string
		value float64
	}{
		{"headline 47.1%", "47.1%", 47.1},
		{"body 49.7%", "49.7%", 49.7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := oneVerdict(t, models.FigureClaim{
				Figure: tc.fig, Value: tc.value, Step: 5, Kind: models.FigureRatio, Pct: true,
				Column: "net_revenue", Row: "l_returnflag = 'R' AND l_linestatus = 'F'",
				Scope: "l_linestatus = 'F'",
			}, evidence(5, step5Rows()))

			if v.Status != models.FigureFails {
				t.Fatalf("status = %q, want fails (evaluated %v, reason %q)", v.Status, v.Evaluated, v.Reason)
			}
			if math.Abs(v.Evaluated-49.343) > 0.001 {
				t.Fatalf("evaluated = %v, want 49.343", v.Evaluated)
			}
		})
	}
}

// TestFigure_SoundProseHolds asserts the checker stays quiet on figures hand
// adjudication found true, including the rounding the prose necessarily does. Every
// expected value here was computed from the oracle, not from these rows.
func TestFigure_SoundProseHolds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		claim models.FigureClaim
		step  int
		rows  []map[string]any
	}{
		{
			// "$6.645B" against 6,645,321,129.94: four significant figures.
			name: "a cell rounded to four figures",
			claim: models.FigureClaim{Figure: "$6.645B", Value: 6645000000, Step: 28,
				Kind: models.FigureCell, Column: "net_rev", Row: "material = 'TIN'"},
			step: 28, rows: step28Rows(),
		},
		{
			// "~911K lines total" against the column sum, 911,395.
			name: "a column total written approximately",
			claim: models.FigureClaim{Figure: "~911K", Value: 911000, Step: 28,
				Kind: models.FigureSum, Column: "lines"},
			step: 28, rows: step28Rows(),
		},
		{
			// "TIN holds 20.1% of the queried 1997 total": 6.6453B over 33.1166B.
			name: "a share of a column total as a percentage",
			claim: models.FigureClaim{Figure: "20.1%", Value: 20.1, Step: 28, Kind: models.FigureRatio,
				Pct: true, Column: "net_rev", Row: "material = 'TIN'"},
			step: 28, rows: step28Rows(),
		},
		{
			name: "a row count",
			claim: models.FigureClaim{Figure: "five materials", Value: 5, Step: 28,
				Kind: models.FigureCount},
			step: 28, rows: step28Rows(),
		},
		{
			// "returned lines are 49.4% of fulfilled lines": 1,478,870 / 2,996,217.
			name: "the body figure that was right",
			claim: models.FigureClaim{Figure: "49.4%", Value: 49.4, Step: 5, Kind: models.FigureRatio,
				Pct: true, Column: "lines", Row: "l_returnflag = 'R' AND l_linestatus = 'F'",
				Scope: "l_linestatus = 'F'"},
			step: 5, rows: step5Rows(),
		},
		{
			name: "a scoped sum",
			claim: models.FigureClaim{Figure: "99,996", Value: 99996, Step: 7, Kind: models.FigureSum,
				Column: "customers", Scope: "bucket != '0_never_ordered'"},
			step: 7, rows: step7Rows(),
		},
		{
			// The gap between the largest and smallest material, 50,795,000 --
			// "$0.05B apart" in prose.
			name: "a difference of two cells",
			claim: models.FigureClaim{Figure: "$50.8M", Value: 50800000, Step: 28,
				Kind: models.FigureDiff, Column: "net_rev",
				Row: "material = 'TIN'", Other: "material = 'STEEL'"},
			step: 28, rows: step28Rows(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := oneVerdict(t, tc.claim, evidence(tc.step, tc.rows))
			if v.Status != models.FigureHolds {
				t.Fatalf("status = %q, want holds (evaluated %v, reason %q)", v.Status, v.Evaluated, v.Reason)
			}
		})
	}
}

// TestFigure_UndecidableNeverReadsAsRefuted is the guard the quantifier evaluator
// learned the hard way: on that corpus two of five refutations were true sentences
// the grammar could not express, so the checker was reporting its own reach as the
// model being wrong. Every shape here must decline, and none may say fails.
func TestFigure_UndecidableNeverReadsAsRefuted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		claim models.FigureClaim
		steps map[int]StepRows
	}{
		{
			name: "a step the insight did not cite",
			claim: models.FigureClaim{Figure: "1", Value: 1, Step: 99, Kind: models.FigureCell,
				Column: "customers", Row: "bucket = '1_order'"},
			steps: evidence(7, step7Rows()),
		},
		{
			name: "a column the rows do not carry",
			claim: models.FigureClaim{Figure: "1", Value: 1, Step: 7, Kind: models.FigureSum,
				Column: "margin"},
			steps: evidence(7, step7Rows()),
		},
		{
			name: "a row selector matching several rows",
			claim: models.FigureClaim{Figure: "1", Value: 1, Step: 5, Kind: models.FigureCell,
				Column: "lines", Row: "l_linestatus = 'F'"},
			steps: evidence(5, step5Rows()),
		},
		{
			name: "a row selector matching none",
			claim: models.FigureClaim{Figure: "1", Value: 1, Step: 28, Kind: models.FigureCell,
				Column: "net_rev", Row: "material = 'PLUTONIUM'"},
			steps: evidence(28, step28Rows()),
		},
		{
			name: "a multi-row step with no selector at all",
			claim: models.FigureClaim{Figure: "1", Value: 1, Step: 28, Kind: models.FigureCell,
				Column: "net_rev"},
			steps: evidence(28, step28Rows()),
		},
		{
			name: "a filter richer than the grammar reads",
			claim: models.FigureClaim{Figure: "1", Value: 1, Step: 28, Kind: models.FigureSum,
				Column: "net_rev", Scope: "material = 'TIN' OR material = 'STEEL'"},
			steps: evidence(28, step28Rows()),
		},
		{
			name: "a kind that does not exist",
			claim: models.FigureClaim{Figure: "1", Value: 1, Step: 28, Kind: "median",
				Column: "net_rev"},
			steps: evidence(28, step28Rows()),
		},
		{
			name:  "no kind at all",
			claim: models.FigureClaim{Figure: "1", Value: 1, Step: 28, Column: "net_rev"},
			steps: evidence(28, step28Rows()),
		},
		{
			name: "a scope selecting no rows",
			claim: models.FigureClaim{Figure: "1", Value: 1, Step: 28, Kind: models.FigureSum,
				Column: "net_rev", Scope: "lines > 999999"},
			steps: evidence(28, step28Rows()),
		},
		{
			name:  "a step with no rows",
			claim: models.FigureClaim{Figure: "1", Value: 1, Step: 3, Kind: models.FigureCount},
			steps: map[int]StepRows{3: {Rows: nil}},
		},
		{
			name: "a diff whose other operand names nothing",
			claim: models.FigureClaim{Figure: "1", Value: 1, Step: 28, Kind: models.FigureDiff,
				Column: "net_rev", Row: "material = 'TIN'", Other: "material = 'OSMIUM'"},
			steps: evidence(28, step28Rows()),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := oneVerdict(t, tc.claim, tc.steps)
			if v.Status != models.FigureUndecidable {
				t.Fatalf("status = %q, want undecidable (evaluated %v, reason %q)", v.Status, v.Evaluated, v.Reason)
			}
			if v.Reason == "" {
				t.Fatal("undecidable with no reason: the verdict has to say what it could not read")
			}
		})
	}
}

// TestFigure_StatedPrecisionSetsTheInterval is the table from figure.go's header,
// asserted. It is the whole basis of the layer: the same 0.004%-vs-0.005% pair that
// no relative band can separate, separated by how many places the prose wrote.
//
// Each row names the corpus figure it comes from. If any "holds" row flips, the
// check has started refuting honest rounding; if any "fails" row flips, it has
// started passing an observed corruption.
func TestFigure_StatedPrecisionSetsTheInterval(t *testing.T) {
	for _, tc := range []struct {
		name    string
		figure  string
		claimed float64
		cell    float64
		want    string
	}{
		{"100,000 for 99,996 (run 3, three insights)", "100,000", 100000, 99996, models.FigureFails},
		{"150,004 for 150,000 (run 3, a headline)", "150,004", 150004, 150000, models.FigureFails},
		{"49.7% for 49.343% (run 3, a body)", "49.7%", 49.7, 49.343, models.FigureFails},
		{"47.1% for 49.343% (run 3, a headline)", "47.1%", 47.1, 49.343, models.FigureFails},
		{"100,004 for 99,996 (run 1)", "100,004", 100004, 99996, models.FigureFails},
		{"267,065 for 266,465 (run 2)", "267,065", 267065, 266465, models.FigureFails},

		{"$6.645B for 6,645,321,130", "$6.645B", 6645000000, 6645321129.9441, models.FigureHolds},
		{"~911K for 911,395", "~911K", 911000, 911395, models.FigureHolds},
		{"20.1% for 20.066%", "20.1%", 20.1, 20.066, models.FigureHolds},
		{"49.4% for 49.358%", "49.4%", 49.4, 49.358, models.FigureHolds},
		{"$33.12B for 33,116,752,392", "$33.12B", 33120000000, 33116752392, models.FigureHolds},
		{"17.92% for 17.9196%", "17.92%", 17.92, 17.9196, models.FigureHolds},

		// The boundary itself, on a figure written to the units place.
		{"exactly half a unit low", "1,000", 1000, 999.5, models.FigureHolds},
		{"a hair past half a unit low", "1,000", 1000, 999.49, models.FigureFails},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := []map[string]any{{"v": tc.cell}}
			v := oneVerdict(t, models.FigureClaim{
				Figure: tc.figure, Value: tc.claimed, Step: 1, Kind: models.FigureCell, Column: "v",
			}, evidence(1, rows))
			if v.Status != tc.want {
				t.Fatalf("%s written for %v: status = %q, want %q (slack %v)",
					tc.figure, tc.cell, v.Status, tc.want, figureSlack(tc.figure, tc.claimed))
			}
		})
	}
}

// TestFigure_UnreadableFigureStringFallsBackLoosely covers the path where the
// figure text carries no numeral the parser reads -- "five materials", "a third".
// The interval cannot be read from the text, so it falls back to a relative band,
// which errs toward holds. Asserted because a silent fallback that errs the other
// way would refute figures for the parser's limits.
func TestFigure_UnreadableFigureStringFallsBackLoosely(t *testing.T) {
	rows := []map[string]any{{"v": 1000.0}}
	for _, tc := range []struct {
		figure  string
		claimed float64
		want    string
	}{
		{"five materials", 1000, models.FigureHolds},
		{"a third of the base", 1004, models.FigureHolds},
		{"most of them", 980, models.FigureFails},
	} {
		t.Run(tc.figure, func(t *testing.T) {
			if _, ok := statedPlace(tc.figure); ok {
				t.Fatalf("%q was parsed for a precision, so this case no longer tests the fallback", tc.figure)
			}
			v := oneVerdict(t, models.FigureClaim{
				Figure: tc.figure, Value: tc.claimed, Step: 1, Kind: models.FigureCell, Column: "v",
			}, evidence(1, rows))
			if v.Status != tc.want {
				t.Fatalf("status = %q, want %q", v.Status, tc.want)
			}
		})
	}
}
