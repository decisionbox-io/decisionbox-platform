package validation

import (
	"context"
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

func newTestInsightValidator(t *testing.T) (*InsightValidator, *testutil.MockWarehouseProvider, *testutil.MockLLMProvider) {
	t.Helper()

	llmProvider := testutil.NewMockLLMProvider()
	wh := testutil.NewMockWarehouseProvider("test_dataset")

	aiClient, err := ai.New(llmProvider, "test-model")
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

func TestInsightValidatorQueryCleanup_JsonCodeBlock(t *testing.T) {
	v, wh, llmProvider := newTestInsightValidator(t)

	// LLM wraps SQL in ```json block (the bug that caused "Unexpected keyword JSON")
	llmProvider.DefaultResponse.Content = "```json\nSELECT COUNT(DISTINCT user_id) AS count FROM `test_dataset.sessions`\n```"

	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"count"},
		Rows:    []map[string]interface{}{{"count": int64(100)}},
	}

	insights := []models.Insight{
		{ID: "1", Name: "Test", AffectedCount: 100, AnalysisArea: "churn"},
	}

	results := v.ValidateInsights(context.Background(), insights)

	if results[0].Status == "error" {
		t.Errorf("should not error — got: %s", results[0].QueryError)
	}
	if results[0].Query == "" {
		t.Error("query should be extracted from json code block")
	}
	if strings.Contains(results[0].Query, "json") {
		t.Errorf("query should not contain 'json' prefix: %q", results[0].Query)
	}
}

func TestInsightValidatorQueryCleanup_SqlCodeBlock(t *testing.T) {
	v, wh, llmProvider := newTestInsightValidator(t)

	llmProvider.DefaultResponse.Content = "```sql\nSELECT COUNT(DISTINCT user_id) AS count FROM `test_dataset.sessions`\n```"

	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"count"},
		Rows:    []map[string]interface{}{{"count": int64(100)}},
	}

	insights := []models.Insight{
		{ID: "1", Name: "Test", AffectedCount: 100, AnalysisArea: "churn"},
	}

	results := v.ValidateInsights(context.Background(), insights)

	if results[0].Status == "error" {
		t.Errorf("should not error — got: %s", results[0].QueryError)
	}
	if strings.Contains(results[0].Query, "sql") {
		t.Errorf("query should not contain 'sql' prefix: %q", results[0].Query)
	}
}

func TestInsightValidatorQueryCleanup_RawSQL(t *testing.T) {
	v, wh, llmProvider := newTestInsightValidator(t)

	llmProvider.DefaultResponse.Content = "SELECT COUNT(DISTINCT user_id) AS count FROM `test_dataset.sessions`"

	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"count"},
		Rows:    []map[string]interface{}{{"count": int64(100)}},
	}

	insights := []models.Insight{
		{ID: "1", Name: "Test", AffectedCount: 100, AnalysisArea: "churn"},
	}

	results := v.ValidateInsights(context.Background(), insights)

	if results[0].Status == "error" {
		t.Errorf("should not error — got: %s", results[0].QueryError)
	}
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

func TestExtractCountFromRows_CountField(t *testing.T) {
	rows := []map[string]interface{}{{"count": int64(42)}}
	got := extractCountFromRows(rows)
	if got != 42 {
		t.Errorf("got %d, want 42", got)
	}
}

func TestExtractCountFromRows_FallbackToFirstNumeric(t *testing.T) {
	rows := []map[string]interface{}{{"total_players": int64(99)}}
	got := extractCountFromRows(rows)
	if got != 99 {
		t.Errorf("got %d, want 99", got)
	}
}

func TestExtractCountFromRows_EmptyRows(t *testing.T) {
	got := extractCountFromRows(nil)
	if got != 0 {
		t.Errorf("got %d, want 0", got)
	}
	got = extractCountFromRows([]map[string]interface{}{})
	if got != 0 {
		t.Errorf("got %d, want 0 for empty slice", got)
	}
}

func TestExtractCountFromRows_ZeroCountField(t *testing.T) {
	// count=0 should return 0, not fall back to another field
	rows := []map[string]interface{}{{"count": int64(0), "other": int64(99)}}
	got := extractCountFromRows(rows)
	if got != 0 {
		t.Errorf("got %d, want 0 (count field is 0)", got)
	}
}

func TestInsightValidatorQueryCleanup_NestedCodeBlocks(t *testing.T) {
	v, wh, llmProvider := newTestInsightValidator(t)

	// Multiple code blocks — should extract content from first pair
	llmProvider.DefaultResponse.Content = "Here is the query:\n```sql\nSELECT COUNT(DISTINCT user_id) AS count FROM `test_dataset.sessions`\n```\nAnd another block:\n```\nignore this\n```"

	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"count"},
		Rows:    []map[string]interface{}{{"count": int64(50)}},
	}

	insights := []models.Insight{
		{ID: "1", Name: "Test", AffectedCount: 50, AnalysisArea: "churn"},
	}

	results := v.ValidateInsights(context.Background(), insights)
	if results[0].Status == "error" {
		t.Errorf("should not error — got: %s", results[0].QueryError)
	}
}

func TestInsightValidatorQueryCleanup_NoSELECT(t *testing.T) {
	v, _, llmProvider := newTestInsightValidator(t)

	// LLM returns non-SQL response
	llmProvider.DefaultResponse.Content = "I cannot generate a query for this insight."

	insights := []models.Insight{
		{ID: "1", Name: "Test", AffectedCount: 100, AnalysisArea: "churn"},
	}

	results := v.ValidateInsights(context.Background(), insights)
	if results[0].Status != "error" {
		t.Errorf("status = %q, want 'error' for non-SQL response", results[0].Status)
	}
}

func TestInsightValidator_ZeroAffectedCount(t *testing.T) {
	v, _, llmProvider := newTestInsightValidator(t)

	llmProvider.DefaultResponse.Content = "SELECT COUNT(DISTINCT user_id) AS count FROM `test_dataset.sessions`"

	insights := []models.Insight{
		{ID: "1", Name: "Test", AffectedCount: 0, AnalysisArea: "churn"},
	}

	results := v.ValidateInsights(context.Background(), insights)
	if results[0].Status != "confirmed" {
		t.Errorf("status = %q, want 'confirmed' (zero affected = nothing to verify)", results[0].Status)
	}
}

func TestInsightValidator_SetSchemaContext(t *testing.T) {
	v, _, llmProvider := newTestInsightValidator(t)

	schemaJSON := `{"sessions": {"columns": ["user_id", "created_at", "country"]}}`
	v.SetSchemaContext(schemaJSON)

	if v.schemaCtx != schemaJSON {
		t.Errorf("schemaCtx = %q", v.schemaCtx)
	}

	// Verify the schema context is included in verification query generation
	llmProvider.DefaultResponse.Content = "SELECT COUNT(DISTINCT user_id) AS count FROM `test_dataset.sessions`"

	// Check that the system prompt sent to LLM contains schema info
	insights := []models.Insight{
		{ID: "1", Name: "Test", AffectedCount: 0, AnalysisArea: "churn"},
	}
	v.ValidateInsights(context.Background(), insights)

	if len(llmProvider.Calls) == 0 {
		t.Fatal("LLM should have been called")
	}
	lastCall := llmProvider.Calls[len(llmProvider.Calls)-1]
	foundSchema := false
	for _, msg := range lastCall.Request.Messages {
		if strings.Contains(msg.Content, "user_id") && strings.Contains(msg.Content, "created_at") {
			foundSchema = true
			break
		}
	}
	if !foundSchema {
		t.Error("schema context should be included in the verification query prompt")
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
