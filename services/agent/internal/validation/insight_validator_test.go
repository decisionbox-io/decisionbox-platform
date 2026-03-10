package validation

import (
	"context"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/config"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

func newTestInsightValidator(t *testing.T) (*InsightValidator, *testutil.MockWarehouseProvider, *testutil.MockLLMProvider) {
	t.Helper()

	llmProvider := testutil.NewMockLLMProvider()
	wh := testutil.NewMockWarehouseProvider("test_dataset")

	cfg := &config.Config{}
	cfg.LLM.Model = "test-model"
	aiClient, err := ai.New(cfg, llmProvider)
	if err != nil {
		t.Fatalf("failed to create AI client: %v", err)
	}

	v := NewInsightValidator(InsightValidatorOptions{
		AIClient:  aiClient,
		Warehouse: wh,
		Dataset:   "test_dataset",
	})

	return v, wh, llmProvider
}

func TestInsightValidatorConfirmed(t *testing.T) {
	v, wh, llmProvider := newTestInsightValidator(t)

	// LLM generates verification query
	llmProvider.DefaultResponse.Content = "SELECT COUNT(DISTINCT user_id) as count FROM `test_dataset.sessions`"

	// Warehouse returns count close to claimed
	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"count"},
		Rows:    []map[string]interface{}{{"count": int64(480)}}, // close to 500
	}

	insights := []models.Insight{
		{ID: "1", Name: "Churn Pattern", AffectedCount: 500, AnalysisArea: "churn"},
	}

	results := v.ValidateInsights(context.Background(), insights)

	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].Status != "confirmed" {
		t.Errorf("status = %q, want 'confirmed' (480 is within 20%% of 500)", results[0].Status)
	}
	if results[0].VerifiedCount != 480 {
		t.Errorf("verified = %d, want 480", results[0].VerifiedCount)
	}
	if results[0].Query == "" {
		t.Error("verification query should be captured")
	}
	if insights[0].Validation == nil {
		t.Error("insight Validation should be set")
	}
}

func TestInsightValidatorAdjusted(t *testing.T) {
	v, wh, llmProvider := newTestInsightValidator(t)

	llmProvider.DefaultResponse.Content = "SELECT COUNT(DISTINCT user_id) as count FROM `test_dataset.sessions`"

	// Warehouse returns count significantly different from claimed
	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"count"},
		Rows:    []map[string]interface{}{{"count": int64(200)}}, // very different from 500
	}

	insights := []models.Insight{
		{ID: "1", Name: "Test", AffectedCount: 500, AnalysisArea: "churn"},
	}

	results := v.ValidateInsights(context.Background(), insights)

	if results[0].Status != "adjusted" {
		t.Errorf("status = %q, want 'adjusted' (200 vs 500)", results[0].Status)
	}
}

func TestInsightValidatorRejected(t *testing.T) {
	v, wh, llmProvider := newTestInsightValidator(t)

	llmProvider.DefaultResponse.Content = "SELECT COUNT(DISTINCT user_id) as count FROM `test_dataset.sessions`"

	// Warehouse returns zero
	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"count"},
		Rows:    []map[string]interface{}{{"count": int64(0)}},
	}

	insights := []models.Insight{
		{ID: "1", Name: "Test", AffectedCount: 500, AnalysisArea: "churn"},
	}

	results := v.ValidateInsights(context.Background(), insights)

	if results[0].Status != "rejected" {
		t.Errorf("status = %q, want 'rejected' (0 results)", results[0].Status)
	}
}

func TestInsightValidatorQueryError(t *testing.T) {
	v, wh, llmProvider := newTestInsightValidator(t)

	llmProvider.DefaultResponse.Content = "SELECT COUNT(DISTINCT user_id) as count FROM `test_dataset.sessions`"

	// Warehouse returns error
	wh.QueryError = context.DeadlineExceeded

	insights := []models.Insight{
		{ID: "1", Name: "Test", AffectedCount: 500, AnalysisArea: "churn"},
	}

	results := v.ValidateInsights(context.Background(), insights)

	if results[0].Status != "error" {
		t.Errorf("status = %q, want 'error'", results[0].Status)
	}
	if results[0].QueryError == "" {
		t.Error("QueryError should be populated")
	}
}

func TestInsightValidatorLLMError(t *testing.T) {
	v, _, llmProvider := newTestInsightValidator(t)

	// LLM fails to generate query
	llmProvider.Error = context.DeadlineExceeded

	insights := []models.Insight{
		{ID: "1", Name: "Test", AffectedCount: 500, AnalysisArea: "churn"},
	}

	results := v.ValidateInsights(context.Background(), insights)

	if results[0].Status != "error" {
		t.Errorf("status = %q, want 'error'", results[0].Status)
	}
}

func TestExtractCount(t *testing.T) {
	v := &InsightValidator{}

	tests := []struct {
		name string
		rows []map[string]interface{}
		want int
	}{
		{"count field", []map[string]interface{}{{"count": int64(42)}}, 42},
		{"total field", []map[string]interface{}{{"total": int64(100)}}, 100},
		{"total_users field", []map[string]interface{}{{"total_users": float64(500)}}, 500},
		{"first numeric", []map[string]interface{}{{"x": int64(99)}}, 99},
		{"empty rows", []map[string]interface{}{}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &gowarehouse.QueryResult{Rows: tt.rows}
			got := v.extractCount(result)
			if got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestExtractCountNilResult(t *testing.T) {
	v := &InsightValidator{}
	if v.extractCount(nil) != 0 {
		t.Error("nil result should return 0")
	}
}

func TestToInt(t *testing.T) {
	tests := []struct {
		input interface{}
		want  int
	}{
		{int(42), 42},
		{int64(100), 100},
		{float64(99.7), 99},
		{int32(50), 50},
		{"string", 0},
		{nil, 0},
	}

	for _, tt := range tests {
		got := toInt(tt.input)
		if got != tt.want {
			t.Errorf("toInt(%v) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
