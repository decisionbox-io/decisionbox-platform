package discovery

import (
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// superstoreStep4 is the real result of the step the "only loss-making top-10
// revenue line" claim cited: all 17 sub-categories with sales and profit,
// ordered by profit ascending, exactly as the model received them.
func superstoreStep4() StepRows {
	return StepRows{Rows: []map[string]any{
		{"category": "Furniture", "sub_category": "Tables", "sales": 208020.182, "profit": -17753.2061},
		{"category": "Furniture", "sub_category": "Bookcases", "sales": 115361.20429999995, "profit": -3632.073600000004},
		{"category": "Office Supplies", "sub_category": "Supplies", "sales": 46725.49800000004, "profit": -1171.3945000000006},
		{"category": "Office Supplies", "sub_category": "Fasteners", "sales": 8532.239999999994, "profit": 2428.6358000000014},
		{"category": "Technology", "sub_category": "Machines", "sales": 189925.03100000008, "profit": 3461.9768999999915},
		{"category": "Office Supplies", "sub_category": "Labels", "sales": 12695.042000000003, "profit": 5572.778000000004},
		{"category": "Office Supplies", "sub_category": "Art", "sales": 27659.013999999996, "profit": 6653.196200000007},
		{"category": "Office Supplies", "sub_category": "Envelopes", "sales": 16528.362000000005, "profit": 6988.0246999999945},
		{"category": "Furniture", "sub_category": "Furnishings", "sales": 95598.1259999999, "profit": 13891.743},
		{"category": "Office Supplies", "sub_category": "Appliances", "sales": 108213.18499999995, "profit": 18329.484399999983},
		{"category": "Office Supplies", "sub_category": "Storage", "sales": 224644.55400000035, "profit": 21285.111499999974},
		{"category": "Furniture", "sub_category": "Chairs", "sales": 335768.2490000009, "profit": 27223.532300000043},
		{"category": "Office Supplies", "sub_category": "Binders", "sales": 207354.8810000005, "profit": 31426.10030000002},
		{"category": "Office Supplies", "sub_category": "Paper", "sales": 79540.5380000001, "profit": 34511.507000000005},
		{"category": "Technology", "sub_category": "Accessories", "sales": 167380.31799999988, "profit": 41936.63569999998},
		{"category": "Technology", "sub_category": "Phones", "sales": 331842.6400000002, "profit": 45050.82649999996},
		{"category": "Technology", "sub_category": "Copiers", "sales": 150745.28999999998, "profit": 56093.9365},
	}}
}

// The claim that shipped. Two of the top ten by sales run a loss, not one:
// Tables at rank 4 and Bookcases at rank 9.
func TestEvaluate_TheShippedOnlyClaimFails(t *testing.T) {
	claim := models.QuantifierClaim{
		Claim:      "the only top-10 revenue line running a loss",
		Kind:       QuantifierOnly,
		Step:       4,
		Filter:     "profit < 0",
		TopN:       10,
		TopNColumn: "sales",
	}
	got := EvaluateQuantifierClaims([]models.QuantifierClaim{claim}, map[int]StepRows{4: superstoreStep4()})
	if len(got) != 1 {
		t.Fatalf("got %d verdicts, want 1", len(got))
	}
	v := got[0]
	if v.Status != QuantifierFails {
		t.Fatalf("status = %q, want %q (reason: %s)", v.Status, QuantifierFails, v.Reason)
	}
	// The refuting rows have to be named: a repair prompt that says "2 rows,
	// not 1" leaves the model to find them again, which is the step it got
	// wrong the first time.
	for _, want := range []string{"Tables", "Bookcases", "2 of 10"} {
		if !strings.Contains(v.Reason, want) {
			t.Errorf("reason %q does not mention %q", v.Reason, want)
		}
	}
	if strings.Contains(v.Reason, "Supplies") {
		t.Errorf("reason names Supplies, which is rank 13 by sales and outside the top 10: %q", v.Reason)
	}
}

// The same claim scoped to the whole table is true of three rows, so it still
// fails -- but for a different count, which is what tells a reader the scope
// was the problem.
func TestEvaluate_OnlyClaimOverEveryRow(t *testing.T) {
	got := EvaluateQuantifierClaims([]models.QuantifierClaim{{
		Claim: "the only loss-making sub-category", Kind: QuantifierOnly, Step: 4, Filter: "profit < 0",
	}}, map[int]StepRows{4: superstoreStep4()})
	if got[0].Status != QuantifierFails {
		t.Fatalf("status = %q, want fails", got[0].Status)
	}
	if !strings.Contains(got[0].Reason, "3 of 17") {
		t.Errorf("reason = %q, want it to report 3 of 17", got[0].Reason)
	}
}

func TestEvaluate_OnlyClaimThatHolds(t *testing.T) {
	got := EvaluateQuantifierClaims([]models.QuantifierClaim{{
		Claim: "Tables is the only sub-category losing more than $10K",
		Kind:  QuantifierOnly, Step: 4, Filter: "profit < -10000",
	}}, map[int]StepRows{4: superstoreStep4()})
	if got[0].Status != QuantifierHolds {
		t.Errorf("status = %q, want holds (reason: %s)", got[0].Status, got[0].Reason)
	}
}

func TestEvaluate_Rank(t *testing.T) {
	steps := map[int]StepRows{4: superstoreStep4()}
	tests := []struct {
		name     string
		claim    models.QuantifierClaim
		want     QuantifierStatus
		inReason string
	}{
		{"Bookcases really is rank 9 by sales", models.QuantifierClaim{
			Kind: QuantifierRank, Step: 4, Column: "sales", Subject: "sub_category = 'Bookcases'", Rank: 9,
		}, QuantifierHolds, "rank 9"},
		{"Chairs is the largest by sales", models.QuantifierClaim{
			Kind: QuantifierRank, Step: 4, Column: "sales", Subject: "sub_category = 'Chairs'", Rank: 1,
		}, QuantifierHolds, "rank 1"},
		{"Copiers is NOT the second largest profit pool", models.QuantifierClaim{
			Kind: QuantifierRank, Step: 4, Column: "profit", Subject: "sub_category = 'Copiers'", Rank: 2,
		}, QuantifierFails, "is rank 1"},
		{"lowest profit, ascending", models.QuantifierClaim{
			Kind: QuantifierRank, Step: 4, Column: "profit", Subject: "sub_category = 'Tables'", Rank: 1, Order: "asc",
		}, QuantifierHolds, "rank 1"},
		{"a subject matching nothing is undecidable, not false", models.QuantifierClaim{
			Kind: QuantifierRank, Step: 4, Column: "sales", Subject: "sub_category = 'Nonesuch'", Rank: 1,
		}, QuantifierUndecidable, "selects 0 rows"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := EvaluateQuantifierClaims([]models.QuantifierClaim{tc.claim}, steps)[0]
			if v.Status != tc.want {
				t.Fatalf("status = %q, want %q (reason: %s)", v.Status, tc.want, v.Reason)
			}
			if !strings.Contains(v.Reason, tc.inReason) {
				t.Errorf("reason %q does not contain %q", v.Reason, tc.inReason)
			}
		})
	}
}

func TestEvaluate_Cardinality(t *testing.T) {
	steps := map[int]StepRows{4: superstoreStep4()}
	// The Superstore defect's shape: a count asserted from a capped list.
	v := EvaluateQuantifierClaims([]models.QuantifierClaim{{
		Claim: "12 products each loss-making", Kind: QuantifierCardinality, Step: 4, Filter: "profit < 0", Count: 12,
	}}, steps)[0]
	if v.Status != QuantifierFails || !strings.Contains(v.Reason, "not the 12") {
		t.Errorf("status = %q reason = %q, want fails naming 12", v.Status, v.Reason)
	}
	v = EvaluateQuantifierClaims([]models.QuantifierClaim{{
		Claim: "3 of 17 sub-categories run a loss", Kind: QuantifierCardinality, Step: 4, Filter: "profit < 0", Count: 3,
	}}, steps)[0]
	if v.Status != QuantifierHolds {
		t.Errorf("status = %q, want holds (reason: %s)", v.Status, v.Reason)
	}
}

func TestEvaluate_Monotonic(t *testing.T) {
	rising := StepRows{Rows: []map[string]any{{"y": 2023, "m": 28.06}, {"y": 2024, "m": 37.93}, {"y": 2025, "m": 35.77}, {"y": 2026, "m": 39.8}}}
	v := EvaluateQuantifierClaims([]models.QuantifierClaim{{
		Claim: "margin improved each year", Kind: QuantifierMonotonic, Step: 1, Column: "m", Trend: "increasing",
	}}, map[int]StepRows{1: rising})[0]
	if v.Status != QuantifierFails {
		t.Fatalf("status = %q, want fails (reason: %s)", v.Status, v.Reason)
	}
	// 37.93 -> 35.77 is where it breaks; the reason must say so.
	if !strings.Contains(v.Reason, "37.93") || !strings.Contains(v.Reason, "35.77") {
		t.Errorf("reason %q does not name the pair that breaks the trend", v.Reason)
	}
}

// A capped step cannot settle any of these, and saying so is not the same as
// saying the claim is false.
func TestEvaluate_CappedStepIsUndecidableNotFalse(t *testing.T) {
	capped := superstoreStep4()
	capped.Quality = []gowarehouse.QualityCaveat{gowarehouse.RowCapCaveat(10)}
	v := EvaluateQuantifierClaims([]models.QuantifierClaim{{
		Claim: "the only loss-making line", Kind: QuantifierOnly, Step: 4, Filter: "profit < 0",
	}}, map[int]StepRows{4: capped})[0]
	if v.Status != QuantifierUndecidable {
		t.Errorf("status = %q, want undecidable", v.Status)
	}
	if !strings.Contains(v.Reason, "capped") {
		t.Errorf("reason %q does not say the step was capped", v.Reason)
	}
}

// Everything the evaluator cannot read must come back undecidable. A checker
// that reports its own limits as refutations rejects sound claims.
func TestEvaluate_LimitsAreUndecidable(t *testing.T) {
	steps := map[int]StepRows{4: superstoreStep4()}
	tests := []struct {
		name  string
		claim models.QuantifierClaim
	}{
		{"step not in evidence", models.QuantifierClaim{Kind: QuantifierOnly, Step: 99, Filter: "profit < 0"}},
		{"column absent", models.QuantifierClaim{Kind: QuantifierOnly, Step: 4, Filter: "margin < 0"}},
		{"OR is not read", models.QuantifierClaim{Kind: QuantifierOnly, Step: 4, Filter: "profit < 0 OR sales > 1"}},
		{"not a simple term", models.QuantifierClaim{Kind: QuantifierOnly, Step: 4, Filter: "profit/sales < 0"}},
		{"empty filter", models.QuantifierClaim{Kind: QuantifierOnly, Step: 4}},
		{"top_n without a column", models.QuantifierClaim{Kind: QuantifierOnly, Step: 4, Filter: "profit < 0", TopN: 10}},
		{"unknown kind", models.QuantifierClaim{Kind: "vibes", Step: 4, Filter: "profit < 0"}},
		{"text ordering", models.QuantifierClaim{Kind: QuantifierOnly, Step: 4, Filter: "sub_category > 'M'"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := EvaluateQuantifierClaims([]models.QuantifierClaim{tc.claim}, steps)[0]
			if v.Status != QuantifierUndecidable {
				t.Errorf("status = %q, want undecidable (reason: %s)", v.Status, v.Reason)
			}
		})
	}
}

// The claim the evaluator exists for, declared exactly as a replay declared it:
// scoped to the top 10 by sales, against the step that returned exactly those
// 10 rows. The step is capped, but the cap is the claim's scope, so it is
// decidable -- and false, because Bookcases is inside the top 10 and loses
// money.
func TestEvaluate_ClaimScopedToACappedResultIsStillDecidable(t *testing.T) {
	top10 := StepRows{
		Rows:    superstoreTop10BySales(),
		Quality: []gowarehouse.QualityCaveat{gowarehouse.RowCapCaveat(10)},
	}
	v := EvaluateQuantifierClaims([]models.QuantifierClaim{{
		Claim: "Tables is the only loss-making sub-category among the 10 largest by sales",
		Kind:  QuantifierOnly, Step: 29, Filter: "profit < 0", TopN: 10, TopNColumn: "sales",
	}}, map[int]StepRows{29: top10})[0]
	if v.Status != QuantifierFails {
		t.Fatalf("status = %q, want fails (reason: %s)", v.Status, v.Reason)
	}
	if !strings.Contains(v.Reason, "Bookcases") {
		t.Errorf("reason %q does not name Bookcases", v.Reason)
	}
}

// A claim over a capped step that ranges beyond the returned rows is still
// refused: nothing here can speak for the rows the cap removed.
func TestEvaluate_UnscopedClaimOverACappedStepIsRefused(t *testing.T) {
	top10 := StepRows{
		Rows:    superstoreTop10BySales(),
		Quality: []gowarehouse.QualityCaveat{gowarehouse.RowCapCaveat(10)},
	}
	v := EvaluateQuantifierClaims([]models.QuantifierClaim{{
		Claim: "the only loss-making sub-category", Kind: QuantifierOnly, Step: 29, Filter: "profit < 0",
	}}, map[int]StepRows{29: top10})[0]
	if v.Status != QuantifierUndecidable {
		t.Errorf("status = %q, want undecidable", v.Status)
	}
}

// "Tables ran a loss in every year" is a universal, not a trend. Declared as
// monotonic it produced a confident wrong answer; declared as `all` it is
// evaluated as written.
func TestEvaluate_All(t *testing.T) {
	years := StepRows{Rows: []map[string]any{
		{"year": 2023, "profit": -2950.0}, {"year": 2024, "profit": -1204.0},
		{"year": 2025, "profit": -5507.0}, {"year": 2026, "profit": -8092.0},
	}}
	v := EvaluateQuantifierClaims([]models.QuantifierClaim{{
		Claim: "Tables ran a loss in every year", Kind: QuantifierAll, Step: 19, Filter: "profit < 0",
	}}, map[int]StepRows{19: years})[0]
	if v.Status != QuantifierHolds {
		t.Errorf("status = %q, want holds (reason: %s)", v.Status, v.Reason)
	}

	mixed := StepRows{Rows: []map[string]any{
		{"year": 2023, "sub": "Machines", "profit": 10648.0}, {"year": 2024, "sub": "Machines", "profit": 7940.0},
		{"year": 2025, "sub": "Machines", "profit": 1230.0}, {"year": 2026, "sub": "Machines", "profit": -2831.0},
	}}
	v = EvaluateQuantifierClaims([]models.QuantifierClaim{{
		Claim: "Machines was profitable every year", Kind: QuantifierAll, Step: 19, Filter: "profit > 0",
	}}, map[int]StepRows{19: mixed})[0]
	if v.Status != QuantifierFails {
		t.Fatalf("status = %q, want fails (reason: %s)", v.Status, v.Reason)
	}
	if !strings.Contains(v.Reason, "1 of 4") {
		t.Errorf("reason %q should say 1 of 4 rows fail", v.Reason)
	}
}

// The real top 10 by sales, which is what step 29 returned.
func superstoreTop10BySales() []map[string]any {
	return []map[string]any{
		{"sub_category": "Chairs", "sales": 335768.2490000009, "profit": 27223.532300000043},
		{"sub_category": "Phones", "sales": 331842.6400000002, "profit": 45050.82649999996},
		{"sub_category": "Storage", "sales": 224644.55400000035, "profit": 21285.111499999974},
		{"sub_category": "Tables", "sales": 208020.182, "profit": -17753.2061},
		{"sub_category": "Binders", "sales": 207354.8810000005, "profit": 31426.10030000002},
		{"sub_category": "Machines", "sales": 189925.03100000008, "profit": 3461.9768999999915},
		{"sub_category": "Accessories", "sales": 167380.31799999988, "profit": 41936.63569999998},
		{"sub_category": "Copiers", "sales": 150745.28999999998, "profit": 56093.9365},
		{"sub_category": "Bookcases", "sales": 115361.20429999995, "profit": -3632.073600000004},
		{"sub_category": "Appliances", "sales": 108213.18499999995, "profit": 18329.484399999983},
	}
}

// Step 19 of the Superstore audit run: four sub-categories over four years each.
// A claim about one of them is not a claim about the step.
func superstoreStep19() StepRows {
	return StepRows{Rows: []map[string]any{
		{"sub_category": "Binders", "yr": 2023, "profit": 5203.2512},
		{"sub_category": "Binders", "yr": 2024, "profit": 7633.3726},
		{"sub_category": "Binders", "yr": 2025, "profit": 10648.5871},
		{"sub_category": "Binders", "yr": 2026, "profit": 7940.8894},
		{"sub_category": "Bookcases", "yr": 2023, "profit": -346.2},
		{"sub_category": "Bookcases", "yr": 2024, "profit": -2755.2},
		{"sub_category": "Bookcases", "yr": 2025, "profit": 127.5},
		{"sub_category": "Bookcases", "yr": 2026, "profit": -658.2},
		{"sub_category": "Machines", "yr": 2023, "profit": 407.8},
		{"sub_category": "Machines", "yr": 2024, "profit": 2977.5},
		{"sub_category": "Machines", "yr": 2025, "profit": 2907.3},
		{"sub_category": "Machines", "yr": 2026, "profit": -2830.6},
		{"sub_category": "Tables", "yr": 2023, "profit": -3210.4},
		{"sub_category": "Tables", "yr": 2024, "profit": -3500.2},
		{"sub_category": "Tables", "yr": 2025, "profit": -2950.3},
		{"sub_category": "Tables", "yr": 2026, "profit": -8092.3},
	}}
}

// Both of these shipped in the frozen corpus, both are true, and both were
// reported refuted before `scope` existed -- the evaluator had no way to be told
// which series the sentence was about, so it answered a question about all four.
func TestEvaluate_ScopeNarrowsToTheSeriesTheSentenceIsAbout(t *testing.T) {
	steps := map[int]StepRows{19: superstoreStep19()}

	tables := models.QuantifierClaim{
		Claim: "Tables ran a loss in every year 2023-2026", Kind: QuantifierAll,
		Step: 19, Scope: "sub_category = 'Tables'", Filter: "profit < 0",
	}
	if got := evaluateQuantifierClaim(tables, steps); got.Status != QuantifierHolds {
		t.Errorf("Tables-every-year = %q (%s), want holds", got.Status, got.Reason)
	}

	// The same sentence with no scope asserts it of all four sub-categories,
	// which is false -- so the scope has to be the thing that decides it, not a
	// leniency applied either way.
	unscoped := tables
	unscoped.Scope = ""
	if got := evaluateQuantifierClaim(unscoped, steps); got.Status != QuantifierFails {
		t.Errorf("unscoped = %q (%s), want fails", got.Status, got.Reason)
	}

	// A year bound the sentence states belongs in the scope too.
	machines := models.QuantifierClaim{
		Claim: "Machines was profitable each year 2023-2025", Kind: QuantifierAll,
		Step: 19, Scope: "sub_category = 'Machines' AND yr <= 2025", Filter: "profit > 0",
	}
	if got := evaluateQuantifierClaim(machines, steps); got.Status != QuantifierHolds {
		t.Errorf("Machines-2023-2025 = %q (%s), want holds", got.Status, got.Reason)
	}
	// Extending it through 2026 makes it false, and must read as false.
	through2026 := machines
	through2026.Scope = "sub_category = 'Machines'"
	if got := evaluateQuantifierClaim(through2026, steps); got.Status != QuantifierFails {
		t.Errorf("Machines-through-2026 = %q (%s), want fails", got.Status, got.Reason)
	}
}

// A scope that matches nothing is the evaluator unable to find the claim's
// subject, not the claim being false.
func TestEvaluate_EmptyScopeIsUndecidable(t *testing.T) {
	c := models.QuantifierClaim{
		Claim: "Envelopes ran a loss every year", Kind: QuantifierAll,
		Step: 19, Scope: "sub_category = 'Envelopes'", Filter: "profit < 0",
	}
	got := evaluateQuantifierClaim(c, map[int]StepRows{19: superstoreStep19()})
	if got.Status != QuantifierUndecidable {
		t.Errorf("status = %q (%s), want undecidable", got.Status, got.Reason)
	}
}

// A capped step withholds rows that could match the scope, so a scope alone
// must not unlock a claim the cap makes unsettleable.
func TestEvaluate_ScopeDoesNotExcuseATruncatedStep(t *testing.T) {
	ev := superstoreStep19()
	ev.Quality = []gowarehouse.QualityCaveat{{Kind: gowarehouse.QualityTruncated}}
	c := models.QuantifierClaim{
		Claim: "Tables ran a loss in every year", Kind: QuantifierAll,
		Step: 19, Scope: "sub_category = 'Tables'", Filter: "profit < 0",
	}
	got := evaluateQuantifierClaim(c, map[int]StepRows{19: ev})
	if got.Status != QuantifierUndecidable {
		t.Errorf("status = %q (%s), want undecidable on a capped step", got.Status, got.Reason)
	}
}

// A rank claim over a step grouped by two keys needs both in its subject. The
// single-term wording in the contract produced exactly this undecidable during
// the E5 repair measurement: "Tables 2026 loss is the largest single-year loss"
// declared with subject `sub_category = 'Tables'`, which selects four rows.
func TestEvaluate_RankSubjectTakesAConjunction(t *testing.T) {
	steps := map[int]StepRows{19: superstoreStep19()}

	underSpecified := models.QuantifierClaim{
		Claim: "Tables 2026 loss is the largest single-year loss in the window", Kind: QuantifierRank,
		Step: 19, Column: "profit", Order: "asc", Subject: "sub_category = 'Tables'", Rank: 1,
	}
	if got := evaluateQuantifierClaim(underSpecified, steps); got.Status != QuantifierUndecidable {
		t.Errorf("under-specified subject = %q (%s), want undecidable", got.Status, got.Reason)
	}

	pinned := underSpecified
	pinned.Subject = "sub_category = 'Tables' AND yr = 2026"
	got := evaluateQuantifierClaim(pinned, steps)
	if got.Status != QuantifierHolds {
		t.Errorf("pinned subject = %q (%s), want holds", got.Status, got.Reason)
	}
}
