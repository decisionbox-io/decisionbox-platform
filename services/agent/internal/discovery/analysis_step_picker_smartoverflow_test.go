package discovery

import (
	"context"
	"fmt"
	"strings"
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func genRows(n int) []map[string]any {
	rows := make([]map[string]any, n)
	for i := range rows {
		rows[i] = map[string]any{"n": i, "label": fmt.Sprintf("row-%04d", i)}
	}
	return rows
}

func makeStepWithResult(n int, query, purpose string, rows []map[string]any) models.ExplorationStep {
	s := makeStep(n, query, purpose)
	s.QueryResult = rows
	s.RowCount = len(rows)
	cr := gomodels.BuildCompactResult(rows)
	s.CompactResult = &cr
	return s
}

// TestSmartOverflow_DropsDuplicateNotLowestScore is the core R5 behaviour: when
// over budget, a near-duplicate of a higher-scored survivor is dropped in
// preference to the unique lowest-scored step. Steps 1 and 2 examine the same
// tables for the same purpose (redundant); step 3 is unique but lowest-scored.
// Classic trim would drop step 3; smart overflow drops step 2 and keeps 3.
func TestSmartOverflow_DropsDuplicateNotLowestScore(t *testing.T) {
	area := AnalysisArea{ID: "x", Name: "X"}
	steps := []models.ExplorationStep{
		makeStep(1, "SELECT * FROM churn_events", "churn cohort trend"),
		makeStep(2, "SELECT * FROM churn_events WHERE dt > 0", "churn cohort trend"),
		makeStep(3, "SELECT * FROM revenue", "revenue by segment"),
	}
	hits := []RunStepIndexHit{{Step: 1, Score: 0.9}, {Step: 2, Score: 0.8}, {Step: 3, Score: 0.7}}
	estimate := func(s []models.ExplorationStep) int { return len(s) * 100 } // 3 steps over, 2 fit
	picker := &AnalysisStepPicker{
		Search:               searchFn(hits, nil),
		EstimateRenderedSize: estimate,
		BudgetTokens:         50, // 200 chars / 4
		SmartOverflowEnabled: true,
	}
	res, err := picker.Pick(context.Background(), area, steps)
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if len(res.Picked) != 2 {
		t.Fatalf("picked = %d, want 2", len(res.Picked))
	}
	kept := map[int]bool{res.Picked[0].Step.Step: true, res.Picked[1].Step.Step: true}
	if !kept[1] || !kept[3] {
		t.Errorf("smart overflow must keep the unique step 3 and drop the duplicate step 2; kept %v", kept)
	}
	if len(res.Dropped) != 1 || res.Dropped[0].StepNumber != 2 {
		t.Errorf("dropped = %+v, want [step 2]", res.Dropped)
	}
	if len(res.AlsoExamined) != 1 || !strings.Contains(res.AlsoExamined[0], "churn cohort trend") {
		t.Errorf("AlsoExamined = %v, want the dropped step's purpose", res.AlsoExamined)
	}
}

// TestSmartOverflow_Off_DropsLowestScore proves the toggle gates the behaviour:
// with smart overflow OFF, the same input drops the lowest-scored step (3), not
// the duplicate — exactly as before.
func TestSmartOverflow_Off_DropsLowestScore(t *testing.T) {
	area := AnalysisArea{ID: "x", Name: "X"}
	steps := []models.ExplorationStep{
		makeStep(1, "SELECT * FROM churn_events", "churn cohort trend"),
		makeStep(2, "SELECT * FROM churn_events WHERE dt > 0", "churn cohort trend"),
		makeStep(3, "SELECT * FROM revenue", "revenue by segment"),
	}
	hits := []RunStepIndexHit{{Step: 1, Score: 0.9}, {Step: 2, Score: 0.8}, {Step: 3, Score: 0.7}}
	picker := &AnalysisStepPicker{
		Search:               searchFn(hits, nil),
		EstimateRenderedSize: func(s []models.ExplorationStep) int { return len(s) * 100 },
		BudgetTokens:         50,
		SmartOverflowEnabled: false,
	}
	res, _ := picker.Pick(context.Background(), area, steps)
	if len(res.Dropped) != 1 || res.Dropped[0].StepNumber != 3 {
		t.Errorf("classic trim must drop lowest-scored step 3, got %+v", res.Dropped)
	}
	if len(res.AlsoExamined) != 0 {
		t.Errorf("classic trim must not emit a breadcrumb, got %v", res.AlsoExamined)
	}
}

// TestSmartOverflow_FitsWithoutTrim_NoBreadcrumb is the big-model no-op: when
// evidence fits, smart overflow changes nothing — no drops, no breadcrumb, no
// re-compaction.
func TestSmartOverflow_FitsWithoutTrim_NoBreadcrumb(t *testing.T) {
	area := AnalysisArea{ID: "x", Name: "X"}
	rows := genRows(30)
	steps := []models.ExplorationStep{
		makeStepWithResult(1, "SELECT * FROM a", "p1", rows),
		makeStepWithResult(2, "SELECT * FROM b", "p2", rows),
	}
	hits := []RunStepIndexHit{{Step: 1, Score: 0.9}, {Step: 2, Score: 0.8}}
	picker := &AnalysisStepPicker{
		Search:               searchFn(hits, nil),
		EstimateRenderedSize: EstimateCompactedRenderedSize,
		BudgetTokens:         1_000_000, // everything fits
		SmartOverflowEnabled: true,
	}
	res, _ := picker.Pick(context.Background(), area, steps)
	if len(res.Picked) != 2 || len(res.Dropped) != 0 || len(res.AlsoExamined) != 0 {
		t.Fatalf("fitting run must be untouched: picked=%d dropped=%d crumbs=%d",
			len(res.Picked), len(res.Dropped), len(res.AlsoExamined))
	}
	// The digest must remain the default 5-row head (not re-compacted).
	for _, p := range res.Picked {
		if got := len(p.Step.CompactResult.HeadRows); got != gomodels.HeadTailRowCount {
			t.Errorf("digest head rows = %d, want default %d (no re-compaction when it fits)", got, gomodels.HeadTailRowCount)
		}
	}
}

// TestSmartOverflow_RecompactsToFitWithoutDropping is the R6 behaviour: when the
// default digests overflow but the tighter digests fit, survivors are
// re-compacted and NO step is dropped.
func TestSmartOverflow_RecompactsToFitWithoutDropping(t *testing.T) {
	area := AnalysisArea{ID: "x", Name: "X"}
	rows := genRows(40)
	steps := []models.ExplorationStep{
		makeStepWithResult(1, "SELECT * FROM a", "p1", rows),
		makeStepWithResult(2, "SELECT * FROM b", "p2", rows),
	}
	hits := []RunStepIndexHit{{Step: 1, Score: 0.9}, {Step: 2, Score: 0.8}}

	// Measure the default vs tight rendered sizes so the budget can sit between
	// them without hardcoding byte counts.
	defaultTokens := EstimateCompactedRenderedSize(steps) / charsPerToken
	tightLimits := gomodels.CompactLimits{HeadTailRowCount: smartOverflowHeadTailRows, CompactInlineThreshold: smartOverflowInlineThreshold}
	tightSteps := make([]models.ExplorationStep, len(steps))
	for i, s := range steps {
		cr := gomodels.BuildCompactResultWithLimits(s.QueryResult, tightLimits)
		s.CompactResult = &cr
		tightSteps[i] = s
	}
	tightTokens := EstimateCompactedRenderedSize(tightSteps) / charsPerToken
	if !(tightTokens < defaultTokens) {
		t.Fatalf("test premise broken: tight (%d) must be smaller than default (%d)", tightTokens, defaultTokens)
	}
	budget := (tightTokens + defaultTokens) / 2 // fits tight, not default

	picker := &AnalysisStepPicker{
		Search:               searchFn(hits, nil),
		EstimateRenderedSize: EstimateCompactedRenderedSize,
		BudgetTokens:         budget,
		SmartOverflowEnabled: true,
	}
	res, _ := picker.Pick(context.Background(), area, steps)
	if len(res.Picked) != 2 {
		t.Fatalf("re-compaction should keep both steps, got %d picked (%d dropped)", len(res.Picked), len(res.Dropped))
	}
	for _, p := range res.Picked {
		if got := len(p.Step.CompactResult.HeadRows); got != smartOverflowHeadTailRows {
			t.Errorf("survivor digest head rows = %d, want tight %d", got, smartOverflowHeadTailRows)
		}
	}
}

// --- helper unit tests ---

func TestQueryTableSignature(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		{"SELECT * FROM churn_events", "churn_events"},
		{"select a,b FROM Sales.Orders o JOIN sales.customers c ON c.id=o.cid", "sales.customers,sales.orders"},
		{"SELECT 1", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := queryTableSignature(c.query); got != c.want {
			t.Errorf("queryTableSignature(%q) = %q, want %q", c.query, got, c.want)
		}
	}
}

func TestStepClusterKey_DuplicatesMatch(t *testing.T) {
	a := makeStep(1, "SELECT * FROM churn_events", "Churn cohort trend")
	b := makeStep(2, "SELECT x FROM churn_events WHERE dt>0", "churn cohort   trend") // same tables + normalized purpose
	c := makeStep(3, "SELECT * FROM revenue", "revenue by segment")
	if stepClusterKey(a) != stepClusterKey(b) {
		t.Errorf("near-duplicate steps must share a cluster key:\n a=%q\n b=%q", stepClusterKey(a), stepClusterKey(b))
	}
	if stepClusterKey(a) == stepClusterKey(c) {
		t.Errorf("distinct steps must not share a cluster key")
	}
}

func TestFormatAlsoExamined_DedupesAndCaps(t *testing.T) {
	if formatAlsoExamined(nil) != "" {
		t.Error("nil breadcrumbs must render empty")
	}
	out := formatAlsoExamined([]string{"a", "a", "b"})
	if !strings.Contains(out, "a") || !strings.Contains(out, "b") {
		t.Errorf("breadcrumb should include a and b: %q", out)
	}
	if strings.Count(out, "a;")+strings.Count(out, "a.") > 1 {
		t.Errorf("duplicate purposes must be de-duplicated: %q", out)
	}

	many := make([]string, 0, 30)
	for i := 0; i < 30; i++ {
		many = append(many, fmt.Sprintf("purpose-%d", i))
	}
	capped := formatAlsoExamined(many)
	if strings.Count(capped, "purpose-") > maxAlsoExaminedBreadcrumbs {
		t.Errorf("breadcrumb count exceeded cap %d: %q", maxAlsoExaminedBreadcrumbs, capped)
	}
}
