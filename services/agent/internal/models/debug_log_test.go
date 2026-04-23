package models

import (
	"testing"
	"time"
)

func TestNewDebugLog(t *testing.T) {
	log := NewDebugLog("app-123", "run-456", DebugLogTypeBigQuery, "query_executor", "execute_query")

	if log.AppID != "app-123" {
		t.Errorf("AppID = %q, want app-123", log.AppID)
	}
	if log.DiscoveryRunID != "run-456" {
		t.Errorf("DiscoveryRunID = %q, want run-456", log.DiscoveryRunID)
	}
	if log.LogType != DebugLogTypeBigQuery {
		t.Errorf("LogType = %q, want bigquery", log.LogType)
	}
	if log.Component != "query_executor" {
		t.Errorf("Component = %q, want query_executor", log.Component)
	}
	if log.Operation != "execute_query" {
		t.Errorf("Operation = %q, want execute_query", log.Operation)
	}
	if !log.Success {
		t.Error("Success should default to true")
	}
	if log.Timestamp.IsZero() {
		t.Error("Timestamp should be set")
	}
	if log.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
	if log.Metadata == nil {
		t.Error("Metadata should be initialized")
	}
}

func TestDebugLog_SetBigQueryDetails(t *testing.T) {
	log := NewDebugLog("app-123", "run-1", DebugLogTypeBigQuery, "executor", "query")

	results := []map[string]interface{}{
		{"user_id": "u1", "count": 10},
		{"user_id": "u2", "count": 20},
	}

	log.SetBigQueryDetails(
		"SELECT COUNT(*) FROM sessions WHERE app_id = 'test'",
		"count sessions",
		results,
		2,
		450,
	)

	if log.SQLQuery != "SELECT COUNT(*) FROM sessions WHERE app_id = 'test'" {
		t.Errorf("SQLQuery = %q", log.SQLQuery)
	}
	if log.QueryPurpose != "count sessions" {
		t.Errorf("QueryPurpose = %q", log.QueryPurpose)
	}
	if log.RowCount != 2 {
		t.Errorf("RowCount = %d, want 2", log.RowCount)
	}
	if log.DurationMs != 450 {
		t.Errorf("DurationMs = %d, want 450", log.DurationMs)
	}
	if len(log.QueryResults) != 2 {
		t.Errorf("QueryResults = %d, want 2", len(log.QueryResults))
	}
}

func TestDebugLog_SetBigQueryDetails_LargeResults(t *testing.T) {
	log := NewDebugLog("app-123", "run-1", DebugLogTypeBigQuery, "executor", "query")

	// Create 150 rows — should be capped at 100
	results := make([]map[string]interface{}, 150)
	for i := 0; i < 150; i++ {
		results[i] = map[string]interface{}{"row": i}
	}

	log.SetBigQueryDetails("SELECT *", "big query", results, 150, 1000)

	if len(log.QueryResults) != 100 {
		t.Errorf("QueryResults = %d, want 100 (capped)", len(log.QueryResults))
	}
}

func TestDebugLog_SetBigQueryDetails_EmptyResults(t *testing.T) {
	log := NewDebugLog("app-123", "run-1", DebugLogTypeBigQuery, "executor", "query")

	log.SetBigQueryDetails("SELECT 1", "test", nil, 0, 10)

	if log.QueryResults != nil {
		t.Error("QueryResults should be nil for empty results")
	}
	if log.RowCount != 0 {
		t.Errorf("RowCount = %d, want 0", log.RowCount)
	}
}

func TestDebugLog_SetLLMDetails(t *testing.T) {
	log := NewDebugLog("app-123", "run-1", DebugLogTypeLLM, "ai_client", "chat")

	log.SetLLMDetails(
		"claude-sonnet-4-20250514",
		"You are an analyst",
		"Analyze the data",
		`{"insights": []}`,
		500,
		200,
		1500,
	)

	if log.LLMModel != "claude-sonnet-4-20250514" {
		t.Errorf("LLMModel = %q", log.LLMModel)
	}
	if log.LLMSystemPrompt != "You are an analyst" {
		t.Errorf("LLMSystemPrompt = %q", log.LLMSystemPrompt)
	}
	if log.LLMPrompt != "Analyze the data" {
		t.Errorf("LLMPrompt = %q", log.LLMPrompt)
	}
	if log.LLMResponse != `{"insights": []}` {
		t.Errorf("LLMResponse = %q", log.LLMResponse)
	}
	if log.LLMInputTokens != 500 {
		t.Errorf("LLMInputTokens = %d, want 500", log.LLMInputTokens)
	}
	if log.LLMOutputTokens != 200 {
		t.Errorf("LLMOutputTokens = %d, want 200", log.LLMOutputTokens)
	}
	if log.DurationMs != 1500 {
		t.Errorf("DurationMs = %d, want 1500", log.DurationMs)
	}
}

func TestDebugLog_SetAnalysisDetails(t *testing.T) {
	log := NewDebugLog("app-123", "run-1", DebugLogTypeAnalysis, "orchestrator", "analyze")

	input := map[string]interface{}{"queries": 5}
	output := map[string]interface{}{"insights": 3}

	log.SetAnalysisDetails("churn", input, output, `{"insights": []}`)

	if log.AnalysisCategory != "churn" {
		t.Errorf("AnalysisCategory = %q, want churn", log.AnalysisCategory)
	}
	if log.AnalysisInput["queries"] != 5 {
		t.Errorf("AnalysisInput = %v", log.AnalysisInput)
	}
	if log.AnalysisOutput["insights"] != 3 {
		t.Errorf("AnalysisOutput = %v", log.AnalysisOutput)
	}
	if log.ExtractedJSON != `{"insights": []}` {
		t.Errorf("ExtractedJSON = %q", log.ExtractedJSON)
	}
}

func TestDebugLog_SetValidationDetails(t *testing.T) {
	log := NewDebugLog("app-123", "run-1", DebugLogTypeValidation, "validator", "validate")

	log.SetValidationDetails("affected_count", 2847, 2900, true, "Within tolerance")

	if log.ValidationField != "affected_count" {
		t.Errorf("ValidationField = %q", log.ValidationField)
	}
	if log.ValidationExpected != 2847 {
		t.Errorf("ValidationExpected = %v", log.ValidationExpected)
	}
	if log.ValidationActual != 2900 {
		t.Errorf("ValidationActual = %v", log.ValidationActual)
	}
	if !log.ValidationPassed {
		t.Error("ValidationPassed should be true")
	}
	if log.ValidationMessage != "Within tolerance" {
		t.Errorf("ValidationMessage = %q", log.ValidationMessage)
	}
}

func TestDebugLog_SetUserCountValidation(t *testing.T) {
	log := NewDebugLog("app-123", "run-1", DebugLogTypeValidation, "validator", "user_count")

	log.SetUserCountValidation("affected_count", 500, "insight", 10000)

	if log.UserCountField != "affected_count" {
		t.Errorf("UserCountField = %q", log.UserCountField)
	}
	if log.UserCountValue != 500 {
		t.Errorf("UserCountValue = %d, want 500", log.UserCountValue)
	}
	if log.UserCountSource != "insight" {
		t.Errorf("UserCountSource = %q", log.UserCountSource)
	}
	if log.TotalAppUsers != 10000 {
		t.Errorf("TotalAppUsers = %d, want 10000", log.TotalAppUsers)
	}
	if !log.UserCountIsValid {
		t.Error("UserCountIsValid should be true (500 <= 10000)")
	}
}

func TestDebugLog_SetUserCountValidation_Invalid(t *testing.T) {
	log := NewDebugLog("app-123", "run-1", DebugLogTypeValidation, "validator", "user_count")

	log.SetUserCountValidation("affected_count", 50000, "insight", 10000)

	if log.UserCountIsValid {
		t.Error("UserCountIsValid should be false (50000 > 10000)")
	}
}

func TestDebugLog_SetError(t *testing.T) {
	log := NewDebugLog("app-123", "run-1", DebugLogTypeBigQuery, "executor", "query")

	log.SetError("query timed out", "stack trace here")

	if log.Success {
		t.Error("Success should be false after SetError")
	}
	if log.ErrorMessage != "query timed out" {
		t.Errorf("ErrorMessage = %q", log.ErrorMessage)
	}
	if log.ErrorStack != "stack trace here" {
		t.Errorf("ErrorStack = %q", log.ErrorStack)
	}
}

func TestDebugLog_AddMetadata(t *testing.T) {
	log := NewDebugLog("app-123", "run-1", DebugLogTypeBigQuery, "executor", "query")

	log.AddMetadata("retry_count", 3)
	log.AddMetadata("dataset", "analytics_prod")

	if log.Metadata["retry_count"] != 3 {
		t.Errorf("retry_count = %v, want 3", log.Metadata["retry_count"])
	}
	if log.Metadata["dataset"] != "analytics_prod" {
		t.Errorf("dataset = %v", log.Metadata["dataset"])
	}
}

func TestDebugLog_AddMetadata_NilMap(t *testing.T) {
	// Test AddMetadata when Metadata is nil (shouldn't happen with NewDebugLog, but test the guard)
	log := &DebugLog{}
	log.AddMetadata("key", "value")

	if log.Metadata == nil {
		t.Fatal("Metadata should be initialized")
	}
	if log.Metadata["key"] != "value" {
		t.Errorf("key = %v, want value", log.Metadata["key"])
	}
}

func TestDebugLogType_Constants(t *testing.T) {
	types := []DebugLogType{
		DebugLogTypeBigQuery,
		DebugLogTypeLLM,
		DebugLogTypeAnalysis,
		DebugLogTypeValidation,
		DebugLogTypeExploration,
		DebugLogTypeRecommendation,
		DebugLogTypeOrchestrator,
	}

	for _, lt := range types {
		if lt == "" {
			t.Error("debug log type should not be empty")
		}
	}

	if DebugLogTypeBigQuery != "bigquery" {
		t.Errorf("DebugLogTypeBigQuery = %q", DebugLogTypeBigQuery)
	}
	if DebugLogTypeLLM != "llm" {
		t.Errorf("DebugLogTypeLLM = %q", DebugLogTypeLLM)
	}
}

func TestDebugLogFilter_Fields(t *testing.T) {
	filter := DebugLogFilter{
		AppID:          "app-123",
		DiscoveryRunID: "run-456",
		LogType:        DebugLogTypeBigQuery,
		Component:      "executor",
		Operation:      "query",
		Phase:          "exploration",
		SuccessOnly:    true,
		ErrorOnly:      false,
		StartTime:      time.Now().Add(-1 * time.Hour),
		EndTime:        time.Now(),
		Limit:          50,
	}

	if filter.AppID != "app-123" {
		t.Errorf("AppID = %q", filter.AppID)
	}
	if filter.Limit != 50 {
		t.Errorf("Limit = %d, want 50", filter.Limit)
	}
	if !filter.SuccessOnly {
		t.Error("SuccessOnly should be true")
	}
}

func TestDebugLogSummary_Fields(t *testing.T) {
	summary := DebugLogSummary{
		AppID:                       "app-123",
		DiscoveryRunID:              "run-456",
		StartTime:                   time.Now().Add(-5 * time.Minute),
		EndTime:                     time.Now(),
		TotalDurationMs:             300000,
		BigQueryLogCount:            20,
		LLMLogCount:              10,
		AnalysisLogCount:            5,
		ValidationLogCount:          8,
		ErrorCount:                  2,
		TotalQueriesExecuted:        25,
		TotalQueryTimeMs:            15000,
		FailedQueries:               3,
		FixedQueries:                2,
		TotalLLMCalls:            10,
		TotalLLMInputTokens:      50000,
		TotalLLMOutputTokens:     12000,
		UserCountValidations:        8,
		UserCountValidationsFailed:  1,
		Errors:                      []string{"query timeout", "parse error"},
	}

	if summary.TotalQueriesExecuted != 25 {
		t.Errorf("TotalQueriesExecuted = %d, want 25", summary.TotalQueriesExecuted)
	}
	if summary.ErrorCount != 2 {
		t.Errorf("ErrorCount = %d, want 2", summary.ErrorCount)
	}
	if summary.TotalLLMInputTokens != 50000 {
		t.Errorf("TotalLLMInputTokens = %d, want 50000", summary.TotalLLMInputTokens)
	}
	if len(summary.Errors) != 2 {
		t.Errorf("Errors = %d, want 2", len(summary.Errors))
	}
}
