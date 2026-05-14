package validation

import (
	"context"
	"errors"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

var errSimulatedLLM = errors.New("simulated LLM failure")

// TestInsightValidator_TokenAccumulation_SingleShot verifies the single-shot
// (no SchemaProvider) path stamps the verification LLM call's tokens onto
// the returned ValidationResult and the InsightValidation embed.
func TestInsightValidator_TokenAccumulation_SingleShot(t *testing.T) {
	llm := testutil.NewMockLLMProvider()
	llm.ResponseQueue = []*gollm.ChatResponse{
		{
			Content: `{"query": "SELECT COUNT(*) AS count FROM test_dataset.events"}`,
			Usage:   gollm.Usage{InputTokens: 700, OutputTokens: 250},
		},
	}

	aiClient, err := ai.New(llm, "test-model")
	if err != nil {
		t.Fatalf("ai.New: %v", err)
	}
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	// LLM returns SELECT ... AS count; mock warehouse's default response
	// is keyed the same way the extractor reads.
	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"count"},
		Rows:    []map[string]interface{}{{"count": int64(42)}},
	}

	v := NewInsightValidator(InsightValidatorOptions{
		AIClient:  aiClient,
		Warehouse: wh,
		Dataset:   "test_dataset",
	})
	v.SetExplorationLog([]models.ExplorationStep{})

	insights := []models.Insight{
		{ID: "1", Name: "active users", AffectedCount: 42, AnalysisArea: "engagement"},
	}
	results := v.ValidateInsights(context.Background(), insights)

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].InputTokens != 700 || results[0].OutputTokens != 250 {
		t.Errorf("ValidationResult tokens = (%d, %d), want (700, 250)", results[0].InputTokens, results[0].OutputTokens)
	}
	// The InsightValidation embed on the insight must mirror the
	// ValidationResult's totals — consumers reading just the insight
	// (without joining to the validation collection) still see usage.
	if insights[0].Validation == nil {
		t.Fatal("InsightValidation embed should be populated")
	}
	if insights[0].Validation.InputTokens != 700 || insights[0].Validation.OutputTokens != 250 {
		t.Errorf("InsightValidation embed tokens = (%d, %d), want (700, 250)",
			insights[0].Validation.InputTokens, insights[0].Validation.OutputTokens)
	}
}

// TestInsightValidator_TokenAccumulation_LookupLoop_Sums verifies that the
// three-call lookup-loop path (initial verification, lookup-round, forced
// final round) collapses all per-call tokens onto a single ValidationResult
// — the §4.5 sum-within-doc rule.
func TestInsightValidator_TokenAccumulation_LookupLoop_Sums(t *testing.T) {
	llm := testutil.NewMockLLMProvider()
	// Fill the lookup budget with rounds that each report distinct token
	// counts, then a forced final round emits the query. Total inputs =
	// 100 + 200 + ... + (the forced round's 50); total outputs = 25 + 40
	// + ... .
	llm.ResponseQueue = []*gollm.ChatResponse{}
	expectedIn := 0
	expectedOut := 0
	for i := 0; i < MaxLookupsPerVerification; i++ {
		in := 100 * (i + 1)
		out := 25 * (i + 1)
		llm.ResponseQueue = append(llm.ResponseQueue, &gollm.ChatResponse{
			Content: `{"lookup_schema": ["test_dataset.events"]}`,
			Usage:   gollm.Usage{InputTokens: in, OutputTokens: out},
		})
		expectedIn += in
		expectedOut += out
	}
	// Forced final round — bare SQL accepted via bareSQLFallback.
	llm.ResponseQueue = append(llm.ResponseQueue, &gollm.ChatResponse{
		Content: "SELECT COUNT(*) AS count FROM test_dataset.events",
		Usage:   gollm.Usage{InputTokens: 50, OutputTokens: 17},
	})
	expectedIn += 50
	expectedOut += 17

	provider := &stubSchemaProvider{
		tables: map[string]ai.LookupTable{
			"test_dataset.events": {
				Table:    "test_dataset.events",
				RowCount: 1,
				Columns:  []ai.LookupColumn{{Name: "user_id", Type: "INT64"}},
			},
		},
	}
	exec := &captureExecutor{rows: []map[string]interface{}{{"count": int64(1)}}}
	v := newValidatorWithSchemaProvider(t, provider, llm, exec)
	insights := []models.Insight{
		{ID: "1", Name: "n", AffectedCount: 1, AnalysisArea: "x"},
	}
	results := v.ValidateInsights(context.Background(), insights)

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].InputTokens != expectedIn || results[0].OutputTokens != expectedOut {
		t.Errorf("ValidationResult tokens = (%d, %d), want (%d, %d) — all rounds should sum onto one home doc",
			results[0].InputTokens, results[0].OutputTokens, expectedIn, expectedOut)
	}
	if insights[0].Validation == nil {
		t.Fatal("InsightValidation embed should be populated even on lookup-budget path")
	}
	if insights[0].Validation.InputTokens != expectedIn || insights[0].Validation.OutputTokens != expectedOut {
		t.Errorf("InsightValidation embed tokens = (%d, %d), want (%d, %d)",
			insights[0].Validation.InputTokens, insights[0].Validation.OutputTokens, expectedIn, expectedOut)
	}
}

// TestInsightValidator_TokenAccumulation_LLMErrorPreservesSpent verifies that
// when the very first LLM call returns an error, the ValidationResult still
// carries zero tokens (nothing was spent that returned a usage record). This
// pins the defer-stamping contract: an error-return path doesn't leak stale
// or uninitialised token fields.
func TestInsightValidator_TokenAccumulation_LLMErrorPreservesSpent(t *testing.T) {
	llm := testutil.NewMockLLMProvider()
	// Return an LLM error on first call (no usage reported).
	llm.Error = errSimulatedLLM

	aiClient, err := ai.New(llm, "test-model")
	if err != nil {
		t.Fatalf("ai.New: %v", err)
	}
	v := NewInsightValidator(InsightValidatorOptions{
		AIClient:  aiClient,
		Warehouse: testutil.NewMockWarehouseProvider("test_dataset"),
		Dataset:   "test_dataset",
	})
	v.SetExplorationLog([]models.ExplorationStep{})

	insights := []models.Insight{
		{ID: "1", Name: "n", AffectedCount: 10, AnalysisArea: "x"},
	}
	results := v.ValidateInsights(context.Background(), insights)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Status != "error" {
		t.Errorf("status = %q, want error", results[0].Status)
	}
	if results[0].InputTokens != 0 || results[0].OutputTokens != 0 {
		t.Errorf("ValidationResult tokens after LLM-error = (%d, %d), want (0, 0)",
			results[0].InputTokens, results[0].OutputTokens)
	}
}
