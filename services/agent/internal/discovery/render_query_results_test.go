package discovery

import (
	"encoding/json"
	"strings"
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func TestRender_SingleStepBelowInlineThreshold(t *testing.T) {
	rows := []map[string]any{{"x": 1}, {"x": 2}, {"x": 3}}
	cmp := gomodels.BuildCompactResult(rows)
	step := models.ExplorationStep{
		Step:          1,
		Action:        "query_data",
		Query:         "SELECT x FROM t",
		QueryPurpose:  "x check",
		RowCount:      3,
		QueryResult:   rows,
		CompactResult: &cmp,
	}

	out := RenderCompactedSteps([]models.ExplorationStep{step})

	if !strings.Contains(out, "all_rows") {
		t.Errorf("expected all_rows present below inline threshold, got %s", out)
	}
	if !strings.Contains(out, "head_rows") {
		t.Errorf("expected head_rows present always, got %s", out)
	}
	if strings.Contains(out, "tail_rows") {
		t.Errorf("tail_rows should be omitted below threshold, got %s", out)
	}
}

func TestRender_SingleStepAboveInlineThreshold(t *testing.T) {
	rows := make([]map[string]any, gomodels.CompactInlineThreshold+5)
	for i := range rows {
		rows[i] = map[string]any{"x": i}
	}
	cmp := gomodels.BuildCompactResult(rows)
	step := models.ExplorationStep{
		Step:          1,
		Action:        "query_data",
		Query:         "SELECT x FROM t",
		QueryPurpose:  "x check",
		RowCount:      len(rows),
		QueryResult:   rows,
		CompactResult: &cmp,
	}
	out := RenderCompactedSteps([]models.ExplorationStep{step})

	if strings.Contains(out, "all_rows") {
		t.Errorf("all_rows should be omitted above threshold, got %s", out)
	}
	if !strings.Contains(out, "head_rows") {
		t.Errorf("head_rows must be present, got %s", out)
	}
	if !strings.Contains(out, "tail_rows") {
		t.Errorf("tail_rows must be present above threshold, got %s", out)
	}
}

func TestRender_PreservesStepIdentity(t *testing.T) {
	cmp := gomodels.BuildCompactResult([]map[string]any{{"a": 1}})
	step := models.ExplorationStep{
		Step:          7,
		Query:         "SELECT a FROM t WHERE id = 42",
		QueryPurpose:  "investigate user 42",
		RowCount:      1,
		CompactResult: &cmp,
	}
	out := RenderCompactedSteps([]models.ExplorationStep{step})

	for _, want := range []string{
		`"step": 7`,
		"SELECT a FROM t WHERE id = 42",
		"investigate user 42",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered output missing %q\n%s", want, out)
		}
	}
}

func TestRender_Deterministic(t *testing.T) {
	cmp := gomodels.BuildCompactResult([]map[string]any{{"a": 1}, {"a": 2}})
	step := models.ExplorationStep{
		Step:          3,
		Query:         "SELECT a",
		QueryPurpose:  "p",
		RowCount:      2,
		CompactResult: &cmp,
	}
	first := RenderCompactedSteps([]models.ExplorationStep{step})
	second := RenderCompactedSteps([]models.ExplorationStep{step})
	if first != second {
		t.Errorf("output not deterministic\nfirst: %s\nsecond: %s", first, second)
	}
}

func TestRender_FallbackBuildsCompactWhenMissing(t *testing.T) {
	rows := []map[string]any{{"a": 1}, {"a": 2}}
	step := models.ExplorationStep{
		Step:         1,
		Query:        "SELECT a",
		QueryPurpose: "p",
		RowCount:     2,
		QueryResult:  rows,
		// CompactResult intentionally nil — legacy / restored runs.
	}
	out := RenderCompactedSteps([]models.ExplorationStep{step})

	// Must have a populated compact result, not a blank one.
	if !strings.Contains(out, `"row_count": 2`) {
		t.Errorf("fallback must populate row_count from QueryResult, got %s", out)
	}
	if !strings.Contains(out, "head_rows") {
		t.Errorf("fallback must include head_rows, got %s", out)
	}
}

func TestRender_GoldenSnapshot(t *testing.T) {
	cmp := gomodels.BuildCompactResult([]map[string]any{
		{"x": 1.0, "label": "a"},
		{"x": 2.0, "label": "b"},
	})
	step := models.ExplorationStep{
		Step:          1,
		Action:        "query_data",
		Query:         "SELECT x, label FROM t",
		QueryPurpose:  "smoke",
		RowCount:      2,
		CompactResult: &cmp,
	}
	out := RenderCompactedSteps([]models.ExplorationStep{step})

	// Validate the JSON parses.
	var roundtrip []map[string]any
	if err := json.Unmarshal([]byte(out), &roundtrip); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if len(roundtrip) != 1 {
		t.Fatalf("expected 1 step, got %d", len(roundtrip))
	}
	if got, _ := roundtrip[0]["step"].(float64); int(got) != 1 {
		t.Errorf("step: got %v want 1", roundtrip[0]["step"])
	}
	qr, ok := roundtrip[0]["query_result"].(map[string]any)
	if !ok {
		t.Fatalf("query_result not a map: %T", roundtrip[0]["query_result"])
	}
	if rc, _ := qr["row_count"].(float64); int(rc) != 2 {
		t.Errorf("query_result.row_count: got %v want 2", qr["row_count"])
	}
}

func TestEstimateCompactedRenderedSize_MatchesRender(t *testing.T) {
	cmp := gomodels.BuildCompactResult([]map[string]any{{"x": 1}})
	step := models.ExplorationStep{
		Step:          1,
		CompactResult: &cmp,
	}
	full := RenderCompactedSteps([]models.ExplorationStep{step})
	if EstimateCompactedRenderedSize([]models.ExplorationStep{step}) != len(full) {
		t.Errorf("estimator must equal len(render(...))")
	}
}

func TestRender_PreservesThinking(t *testing.T) {
	// Legacy MarshalIndent of the full ExplorationStep included the
	// `thinking` field; the renderer keeps that contract so existing
	// analysis prompts that may rely on it don't lose context.
	cmp := gomodels.BuildCompactResult([]map[string]any{{"x": 1}})
	step := models.ExplorationStep{
		Step:          1,
		Query:         "SELECT x",
		QueryPurpose:  "p",
		Thinking:      "exploring why x might be relevant",
		CompactResult: &cmp,
	}
	out := RenderCompactedSteps([]models.ExplorationStep{step})
	if !strings.Contains(out, "exploring why x might be relevant") {
		t.Errorf("rendered output must include step.Thinking, got %s", out)
	}
}

func TestRender_EmptySliceProducesEmptyArray(t *testing.T) {
	out := RenderCompactedSteps(nil)
	if out != "[]" {
		t.Errorf("empty input must render as []; got %q", out)
	}
}
