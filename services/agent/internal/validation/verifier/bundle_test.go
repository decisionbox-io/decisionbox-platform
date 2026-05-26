package verifier

import (
	"strings"
	"testing"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	agentmodels "github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// TestBuildInsightBundle_Basics — bundle captures the insight's
// headline, severity, source step IDs, and the step row sample.
func TestBuildInsightBundle_Basics(t *testing.T) {
	step := &agentmodels.ExplorationStep{
		Step:        12,
		Query:       "SELECT 1 AS x",
		Thinking:    "trying x",
		QueryResult: []map[string]any{{"x": int64(7)}},
	}
	ins := &agentmodels.Insight{
		ID:            "i1",
		Name:          "Headline goes here",
		Description:   "...",
		Severity:      "high",
		AffectedCount: 42,
		SourceSteps:   []int{12},
	}
	wh := WarehouseInfo{Dialect: "BigQuery Standard SQL", Dataset: "ds"}
	disc := DiscoveryContext{ProjectID: "p", RunID: "r", Language: "English"}
	b := BuildInsightBundle(ins, map[int]*agentmodels.ExplorationStep{12: step}, wh, disc, DefaultBundleConfig())

	if b.Doc.Kind != valmodels.DocInsight || b.Doc.ID != "i1" || b.Doc.Headline != "Headline goes here" {
		t.Errorf("Doc = %+v", b.Doc)
	}
	if len(b.SourceSteps) != 1 || b.SourceSteps[0].StepID != 12 {
		t.Errorf("source steps = %+v", b.SourceSteps)
	}
	if b.SourceSteps[0].FullRowCount != 1 {
		t.Errorf("FullRowCount = %d", b.SourceSteps[0].FullRowCount)
	}
}

// TestBuildInsightBundle_MissingStepSkipped — a referenced step that's
// not in the snapshot is silently skipped.
func TestBuildInsightBundle_MissingStepSkipped(t *testing.T) {
	ins := &agentmodels.Insight{ID: "i1", SourceSteps: []int{1, 2}}
	steps := map[int]*agentmodels.ExplorationStep{
		1: {Step: 1, QueryResult: []map[string]any{{"a": 1}}},
		// 2 missing
	}
	b := BuildInsightBundle(ins, steps, WarehouseInfo{}, DiscoveryContext{}, DefaultBundleConfig())
	if len(b.SourceSteps) != 1 || b.SourceSteps[0].StepID != 1 {
		t.Errorf("expected single source step #1, got %+v", b.SourceSteps)
	}
}

// TestBuildInsightBundle_SampleTruncation — when QueryResult exceeds
// the sample cap, only the first N rows are kept and Truncated is
// flipped.
func TestBuildInsightBundle_SampleTruncation(t *testing.T) {
	rows := make([]map[string]any, 100)
	for i := range rows {
		rows[i] = map[string]any{"i": i}
	}
	step := &agentmodels.ExplorationStep{Step: 5, QueryResult: rows}
	ins := &agentmodels.Insight{ID: "i1", SourceSteps: []int{5}}
	cfg := BundleConfig{SampleRows: 10, CellCharCap: 200, EstimateRatio: 3.5}
	b := BuildInsightBundle(ins, map[int]*agentmodels.ExplorationStep{5: step}, WarehouseInfo{}, DiscoveryContext{}, cfg)

	d := b.SourceSteps[0]
	if !d.Truncated || d.FullRowCount != 100 || len(d.SampleRows) != 10 {
		t.Errorf("trunc=%v full=%d sample=%d", d.Truncated, d.FullRowCount, len(d.SampleRows))
	}
}

// TestNormaliseValue_BQInt64 — top-level int64 wrap unwraps to a
// number; nested int64 wrap inside another map also unwraps before
// the parent is serialised.
func TestNormaliseValue_BQInt64(t *testing.T) {
	top := map[string]any{"low": int32(42), "high": 0, "unsigned": false}
	got := normaliseValue(top, 200)
	if v, ok := got.(int64); !ok || v != 42 {
		t.Errorf("top-level unwrap = %v (%T), want int64(42)", got, got)
	}
	// Nested int64-wrap inside another map.
	nested := map[string]any{
		"outer_field": map[string]any{
			"low":      int64(123),
			"high":     0,
			"unsigned": false,
		},
	}
	out := normaliseValue(nested, 200)
	// outer is a non-BQ map, so it should be JSON-serialised; the
	// inner wrap must be replaced before serialisation.
	s, ok := out.(string)
	if !ok {
		t.Fatalf("expected serialised string, got %T: %v", out, out)
	}
	if !strings.Contains(s, "123") || strings.Contains(s, `"low"`) {
		t.Errorf("nested unwrap leaked wrapper structure: %s", s)
	}
}

// TestCapCell — UTF-8 safe truncation.
func TestCapCell(t *testing.T) {
	in := strings.Repeat("ä", 100)
	out := capCell(in, 10)
	if out == in {
		t.Errorf("expected truncation")
	}
	if !strings.HasSuffix(out, "…") {
		t.Errorf("expected ellipsis suffix, got %q", out)
	}
	// no truncation when under cap
	if got := capCell("short", 10); got != "short" {
		t.Errorf("unexpected truncation: %q", got)
	}
}

// TestRenderForPrompt_TruncationNotice — renderForPrompt MUST
// surface SourceStepsTruncated so the model knows to mark dependent
// claims unverifiable.
func TestRenderForPrompt_TruncationNotice(t *testing.T) {
	b := Bundle{
		Doc:                  DocDigest{Kind: valmodels.DocRecommendation, Headline: "h"},
		SourceStepsTruncated: true,
		SourceStepsOmitted:   3,
	}
	rendered := b.renderForPrompt()
	if !strings.Contains(rendered, "EVIDENCE OMITTED") || !strings.Contains(rendered, "3 source step(s) were dropped") {
		t.Errorf("rendered bundle should announce truncation; got:\n%s", rendered)
	}

	bClean := Bundle{Doc: DocDigest{Headline: "h"}}
	if strings.Contains(bClean.renderForPrompt(), "EVIDENCE OMITTED") {
		t.Errorf("rendered bundle for non-truncated bundle should NOT carry the notice")
	}
}

// TestRenderForPrompt_PriorClaims — refuter bundle includes the
// verifier's claim set verbatim.
func TestRenderForPrompt_PriorClaims(t *testing.T) {
	b := Bundle{
		Doc:         DocDigest{Headline: "h"},
		PriorClaims: []string{"first", "second"},
	}
	rendered := b.renderForPrompt()
	if !strings.Contains(rendered, "PRIOR_CLAIMS") {
		t.Errorf("PRIOR_CLAIMS heading missing")
	}
	if !strings.Contains(rendered, "first") || !strings.Contains(rendered, "second") {
		t.Errorf("prior claims not in rendered bundle")
	}
}

// TestBuildRecommendationBundle_SourceStepUnion — dedups + caps via
// the token-budget.
func TestBuildRecommendationBundle_SourceStepUnion(t *testing.T) {
	step1 := &agentmodels.ExplorationStep{Step: 1, QueryResult: []map[string]any{{"a": 1}}}
	step2 := &agentmodels.ExplorationStep{Step: 2, QueryResult: []map[string]any{{"b": 2}}}
	steps := map[int]*agentmodels.ExplorationStep{1: step1, 2: step2}
	insightByID := map[string]*agentmodels.Insight{
		"ins1": {ID: "ins1", Name: "h", SourceSteps: []int{1, 2}},
		"ins2": {ID: "ins2", Name: "h2", SourceSteps: []int{2}}, // overlapping
	}
	rec := &agentmodels.Recommendation{
		ID:                "r1",
		Title:             "rec headline",
		RelatedInsightIDs: []string{"ins1", "ins2"},
		Priority:          1,
	}
	cfg := BundleConfig{SampleRows: 10, CellCharCap: 200, RecStepsTokenCap: 100000, EstimateRatio: 3.5}
	b := BuildRecommendationBundle(rec, insightByID, steps, WarehouseInfo{}, DiscoveryContext{}, cfg)
	if b.Doc.Kind != valmodels.DocRecommendation {
		t.Errorf("kind = %s", b.Doc.Kind)
	}
	if len(b.SourceSteps) != 2 {
		t.Errorf("expected 2 deduped steps, got %d", len(b.SourceSteps))
	}
	if b.Doc.Priority != "high" {
		t.Errorf("priority label = %q, want 'high'", b.Doc.Priority)
	}
}

// TestBuildRecommendationBundle_TokenBudgetCap — over-budget steps
// are omitted with SourceStepsTruncated set.
func TestBuildRecommendationBundle_TokenBudgetCap(t *testing.T) {
	// Make each step very large so even one exceeds a tiny cap.
	bigRow := map[string]any{"big": strings.Repeat("x", 1000)}
	rows := make([]map[string]any, 50)
	for i := range rows {
		rows[i] = bigRow
	}
	step := &agentmodels.ExplorationStep{Step: 1, QueryResult: rows}
	insightByID := map[string]*agentmodels.Insight{
		"ins1": {ID: "ins1", SourceSteps: []int{1, 2, 3}},
	}
	stepByID := map[int]*agentmodels.ExplorationStep{1: step, 2: step, 3: step}
	rec := &agentmodels.Recommendation{ID: "r", RelatedInsightIDs: []string{"ins1"}}
	cfg := BundleConfig{SampleRows: 50, CellCharCap: 1000, RecStepsTokenCap: 50, EstimateRatio: 3.5}
	b := BuildRecommendationBundle(rec, insightByID, stepByID, WarehouseInfo{}, DiscoveryContext{}, cfg)
	if !b.SourceStepsTruncated {
		t.Errorf("expected SourceStepsTruncated=true; got bundle with %d steps", len(b.SourceSteps))
	}
	if b.SourceStepsOmitted == 0 {
		t.Errorf("SourceStepsOmitted = 0; expected > 0")
	}
}
