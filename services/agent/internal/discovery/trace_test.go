package discovery

import (
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// The gate is the safety property of this file: with the variable unset, an
// operator's run must do none of this work. Asserted rather than assumed
// because every emitter reads it and a default of "on" would put a per-row log
// line into every production discovery.
func TestTraceEnabled_OffUnlessExplicitlyTrue(t *testing.T) {
	for _, v := range []string{"", " ", "0", "false", "no", "off", "yes", "TRACE", "2x"} {
		if traceEnabled(v) {
			t.Errorf("traceEnabled(%q) = true, want false", v)
		}
	}
	for _, v := range []string{"1", "true", "TRUE", "True", " true "} {
		if !traceEnabled(v) {
			t.Errorf("traceEnabled(%q) = false, want true", v)
		}
	}
}

// digestRowsShown is the one piece of arithmetic in the trace, and the number it
// produces is the one a reader uses to decide whether a population claim could
// have been grounded at all. An inline digest shows every row; a windowed one
// shows both ends and nothing between.
func TestDigestRowsShown(t *testing.T) {
	row := func(i int) map[string]any { return map[string]any{"i": i} }
	cases := []struct {
		name string
		in   *gomodels.CompactResult
		want int
	}{
		{"nil digest shows nothing", nil, 0},
		{
			"inline digest shows every row",
			&gomodels.CompactResult{RowCount: 3, AllRows: []map[string]any{row(1), row(2), row(3)}},
			3,
		},
		{
			"windowed digest shows head plus tail only",
			&gomodels.CompactResult{
				RowCount: 150,
				HeadRows: []map[string]any{row(1), row(2), row(3), row(4), row(5)},
				TailRows: []map[string]any{row(146), row(147), row(148), row(149), row(150)},
			},
			10,
		},
		{
			"head-only digest counts the head",
			&gomodels.CompactResult{RowCount: 7, HeadRows: []map[string]any{row(1), row(2)}},
			2,
		},
		{
			"inline wins when both are present",
			&gomodels.CompactResult{
				RowCount: 2,
				AllRows:  []map[string]any{row(1), row(2)},
				HeadRows: []map[string]any{row(1), row(2)},
			},
			2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := digestRowsShown(tc.in); got != tc.want {
				t.Errorf("digestRowsShown() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestOneLineAndClip(t *testing.T) {
	if got := oneLine("select *\n  from  t\nwhere x = 1"); got != "select * from t where x = 1" {
		t.Errorf("oneLine() = %q", got)
	}
	if got := clip("abcdef", 3); got != "abc…" {
		t.Errorf("clip() = %q, want %q", got, "abc…")
	}
	if got := clip("abc", 3); got != "abc" {
		t.Errorf("clip() should not mark an unclipped string, got %q", got)
	}
}

// The trace's content is the point, not just that it emits. These assert the
// fields each event carries, because the numbers in them are what the
// measurement write-ups relied on.

func TestQueryTraceFields(t *testing.T) {
	row := func(i int) map[string]any { return map[string]any{"i": i} }
	s := models.ExplorationStep{
		Step: 14, Action: "query_data", RowCount: 25, ExecutionTimeMs: 42,
		QueryPurpose:  "nations by revenue",
		Query:         "SELECT * FROM `public.nation`",
		QueryExecuted: `SELECT * FROM "public"."nation"`,
		Fixed:         true, FixAttempts: 1,
		Quality: []gowarehouse.QualityCaveat{gowarehouse.RowCapCaveat(25)},
		CompactResult: &gomodels.CompactResult{
			RowCount: 25,
			HeadRows: []map[string]any{row(1), row(2), row(3), row(4), row(5)},
			TailRows: []map[string]any{row(21), row(22), row(23), row(24), row(25)},
		},
	}
	f := queryTraceFields(s)
	if f["trace"] != "query" || f["step"] != 14 || f["rows"] != 25 {
		t.Errorf("identity fields wrong: %+v", f)
	}
	// The SQL that ran, and the proposal beside it only because they differ.
	if f["sql"] != `SELECT * FROM "public"."nation"` {
		t.Errorf("sql = %v, want the statement that ran", f["sql"])
	}
	if f["sql_proposed"] != "SELECT * FROM `public.nation`" {
		t.Errorf("sql_proposed = %v, want the model's rejected proposal", f["sql_proposed"])
	}
	// 25 rows returned, 10 reproduced: the precondition for a population claim
	// that the evidence cannot support.
	if f["digest_shows"] != 10 {
		t.Errorf("digest_shows = %v, want 10", f["digest_shows"])
	}
	if f["digest_inline"] != false {
		t.Errorf("digest_inline = %v, want false for a windowed digest", f["digest_inline"])
	}
	if cav, ok := f["quality_caveats"].([]string); !ok || len(cav) != 1 {
		t.Errorf("quality_caveats = %v, want the row-cap caveat", f["quality_caveats"])
	}
	if _, ok := f["error"]; ok {
		t.Errorf("error field present on a step that succeeded: %v", f["error"])
	}
}

func TestQueryTraceFields_UnrepairedStepOmitsTheProposal(t *testing.T) {
	f := queryTraceFields(models.ExplorationStep{
		Step: 3, Action: "query_data", Query: "SELECT 1", RowCount: 1, Error: "boom",
	})
	if _, ok := f["sql_proposed"]; ok {
		t.Error("sql_proposed must be omitted when the proposal is what ran")
	}
	if f["sql"] != "SELECT 1" {
		t.Errorf("sql = %v", f["sql"])
	}
	if f["error"] != "boom" {
		t.Errorf("error = %v, want the step's error", f["error"])
	}
	if _, ok := f["digest_shows"]; ok {
		t.Error("digest fields must be omitted when the step has no digest")
	}
}

func TestExposureTraceFields(t *testing.T) {
	row := func(i int) map[string]any { return map[string]any{"i": i} }
	// A partial view: 21 rows returned, 10 shown, and NO cap caveat, because the
	// query never capped itself -- the digest did. That is the gap E1 does not cover.
	partial := models.ExplorationStep{
		Step: 41, Action: "query_data", RowCount: 21,
		CompactResult: &gomodels.CompactResult{
			RowCount: 21,
			HeadRows: []map[string]any{row(1), row(2), row(3), row(4), row(5)},
			TailRows: []map[string]any{row(17), row(18), row(19), row(20), row(21)},
		},
	}
	f, ok := exposureTraceFields("revenue", partial)
	if !ok {
		t.Fatal("a query_data step with a digest must produce an exposure event")
	}
	if f["rows"] != 21 || f["shown"] != 10 || f["full"] != false {
		t.Errorf("exposure = rows %v shown %v full %v, want 21/10/false", f["rows"], f["shown"], f["full"])
	}
	if _, ok := f["quality_caveats"]; ok {
		t.Error("no caveat belongs here: the query did not cap itself, the digest did")
	}

	// A fully inlined result is fully exposed.
	full := models.ExplorationStep{
		Step: 5, Action: "query_data", RowCount: 2,
		CompactResult: &gomodels.CompactResult{RowCount: 2, AllRows: []map[string]any{row(1), row(2)}},
	}
	if f, ok := exposureTraceFields("revenue", full); !ok || f["full"] != true || f["shown"] != 2 {
		t.Errorf("a wholly inlined result must read full: %+v", f)
	}

	// Steps the event does not apply to.
	for _, s := range []models.ExplorationStep{
		{Step: 1, Action: "lookup_schema", CompactResult: &gomodels.CompactResult{}},
		{Step: 2, Action: "query_data"}, // no digest
	} {
		if _, ok := exposureTraceFields("revenue", s); ok {
			t.Errorf("step %d should produce no exposure event", s.Step)
		}
	}
}

func TestClaimTraceFields(t *testing.T) {
	ins := models.Insight{
		Name: "Tables drags the top ten",
		QuantifierClaims: []models.QuantifierClaim{
			{Claim: "c1", Kind: "only", Step: 4, Subject: "s", Scope: "sc"},
			{Claim: "c2", Kind: "rank", Step: 9},
		},
		QuantifierVerdicts: []models.QuantifierVerdict{
			{Claim: "c1", Status: QuantifierFails, Reason: "2 of 10 rows, not 1"},
		},
	}
	f0 := claimTraceFields("profitability", ins, 0)
	if f0["trace"] != "claim" || f0["kind"] != "only" || f0["step"] != 4 {
		t.Errorf("claim 0 fields wrong: %+v", f0)
	}
	if f0["verdict"] != QuantifierFails || f0["reason"] != "2 of 10 rows, not 1" {
		t.Errorf("claim 0 verdict = %v / %v", f0["verdict"], f0["reason"])
	}
	// A claim with no verdict beside it must say so rather than be dropped.
	f1 := claimTraceFields("profitability", ins, 1)
	if f1["verdict"] != "not-evaluated" {
		t.Errorf("claim 1 verdict = %v, want not-evaluated", f1["verdict"])
	}
	if _, ok := f1["reason"]; ok {
		t.Error("an unevaluated claim has no reason to report")
	}
}

func TestInsightTraceFields(t *testing.T) {
	ins := models.Insight{
		ID: "i1", Name: "Tables drags the top ten", AnalysisArea: "profitability",
		Severity: "high", Confidence: 0.9, SourceSteps: []int{4, 30},
		Indicators: []string{"a", "b"},
		QuantifierVerdicts: []models.QuantifierVerdict{
			{Claim: "a", Status: QuantifierHolds},
			{Claim: "b", Status: QuantifierFails},
			{Claim: "c", Status: QuantifierUndecidable},
		},
		Repair: &models.InsightRepair{
			Rounds: 1, Outcome: models.RepairWithdrawn,
			Withdrawn: []string{"a declaration about nothing"},
		},
	}
	f := insightTraceFields("profitability", ins)
	if f["holds"] != 1 || f["fails"] != 1 || f["undecidable"] != 1 {
		t.Errorf("verdict tally = %v/%v/%v, want 1/1/1", f["holds"], f["fails"], f["undecidable"])
	}
	if f["claims"] != 0 {
		t.Errorf("claims = %v, want 0 declared on this fixture", f["claims"])
	}
	if f["repair_outcome"] != models.RepairWithdrawn {
		t.Errorf("repair_outcome = %v, want withdrawn", f["repair_outcome"])
	}
	if w, ok := f["repair_withdrawn"].([]string); !ok || len(w) != 1 {
		t.Errorf("repair_withdrawn = %v, want the withdrawn declaration", f["repair_withdrawn"])
	}
	if f["indicators"] != 2 || f["severity"] != "high" {
		t.Errorf("descriptive fields wrong: %+v", f)
	}
}

// With the gate off none of this work happens, so the emitters must be safe to
// call on anything the pipeline can hand them.
func TestTraceEmitters_AreSafeOnEmptyInput(t *testing.T) {
	traceExplorationStep(models.ExplorationStep{})
	traceExposure("", nil)
	traceClaims("", models.Insight{})
	traceInsight("", models.Insight{})

	// And with it on, including the drifted claims/verdicts case.
	orig := traceOn
	traceOn = true
	defer func() { traceOn = orig }()
	traceExplorationStep(models.ExplorationStep{Step: 1, Action: "query_data"})
	traceExposure("area", []models.ExplorationStep{{Step: 1, Action: "query_data"}})
	traceClaims("area", models.Insight{
		QuantifierClaims: []models.QuantifierClaim{{Claim: "x"}, {Claim: "y"}},
	})
	traceInsight("area", models.Insight{Repair: &models.InsightRepair{}})
}
