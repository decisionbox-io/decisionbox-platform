package models

import (
	"encoding/json"
	"testing"
	"time"
)

func TestInsightDefaults(t *testing.T) {
	insight := Insight{
		ID:           "test-1",
		AnalysisArea: "churn",
		Name:         "Test Pattern",
		Severity:     "high",
	}

	if insight.AffectedCount != 0 {
		t.Error("AffectedCount should default to 0")
	}
	if insight.RiskScore != 0 {
		t.Error("RiskScore should default to 0")
	}
	if insight.Metrics != nil {
		t.Error("Metrics should default to nil")
	}
}

func TestInsightWithMetrics(t *testing.T) {
	insight := Insight{
		ID:           "test-1",
		AnalysisArea: "churn",
		Name:         "High LTV Churn",
		Metrics: map[string]interface{}{
			"churn_rate":   0.68,
			"avg_ltv":     23.50,
			"avg_sessions": 12.5,
		},
		Indicators: []string{
			"Session drop: 12.5min to 4.2min",
			"Only 32% return after Day 1",
		},
	}

	if len(insight.Metrics) != 3 {
		t.Errorf("Metrics count = %d, want 3", len(insight.Metrics))
	}
	if insight.Metrics["churn_rate"] != 0.68 {
		t.Errorf("churn_rate = %v, want 0.68", insight.Metrics["churn_rate"])
	}
	if len(insight.Indicators) != 2 {
		t.Errorf("Indicators count = %d, want 2", len(insight.Indicators))
	}
}

func TestInsightValidation(t *testing.T) {
	insight := Insight{
		ID:            "test-1",
		AffectedCount: 500,
		Validation: &InsightValidation{
			Status:        "adjusted",
			VerifiedCount: 350,
			OriginalCount: 500,
			Reasoning:     "Verified count differs from claimed",
			ValidatedAt:   time.Now(),
		},
	}

	if insight.Validation == nil {
		t.Fatal("Validation should not be nil")
	}
	if insight.Validation.Status != "adjusted" {
		t.Errorf("Status = %q, want %q", insight.Validation.Status, "adjusted")
	}
	if insight.Validation.VerifiedCount != 350 {
		t.Errorf("VerifiedCount = %d, want 350", insight.Validation.VerifiedCount)
	}
}

func TestDiscoveryResultStructure(t *testing.T) {
	result := DiscoveryResult{
		ProjectID: "proj-123",
		Domain:    "gaming",
		Category:  "match3",
		Insights: []Insight{
			{ID: "1", AnalysisArea: "churn", Name: "Test"},
			{ID: "2", AnalysisArea: "levels", Name: "Test 2"},
		},
		Recommendations: []Recommendation{
			{ID: "r1", Title: "Fix Level 42", Priority: 5, RelatedInsightIDs: []string{"1", "2"}},
		},
		AnalysisLog: []AnalysisStep{
			{AreaID: "churn", Prompt: "analyze churn", Response: "{}"},
		},
	}

	if len(result.Insights) != 2 {
		t.Errorf("Insights = %d, want 2", len(result.Insights))
	}
	if len(result.Recommendations) != 1 {
		t.Errorf("Recommendations = %d, want 1", len(result.Recommendations))
	}
	if len(result.AnalysisLog) != 1 {
		t.Errorf("AnalysisLog = %d, want 1", len(result.AnalysisLog))
	}
}

func TestAnalysisStepCapture(t *testing.T) {
	step := AnalysisStep{
		AreaID:          "churn",
		AreaName:        "Churn Risks",
		RunAt:           time.Now(),
		Prompt:          "Analyze churn patterns...",
		Response:        `{"insights": []}`,
		TokensIn:        500,
		TokensOut:       200,
		DurationMs:      1500,
		RelevantQueries: 5,
		Insights:        []Insight{},
	}

	if step.Prompt == "" {
		t.Error("Prompt should be captured")
	}
	if step.Response == "" {
		t.Error("Response should be captured")
	}
	if step.TokensIn != 500 {
		t.Errorf("TokensIn = %d, want 500", step.TokensIn)
	}
}

func TestValidationResult(t *testing.T) {
	vr := ValidationResult{
		InsightID:     "test-1",
		AnalysisArea:  "churn",
		ClaimedCount:  2847,
		VerifiedCount: 2900,
		Status:        "confirmed",
		Reasoning:     "Within 20% tolerance",
		Query:         "SELECT COUNT(DISTINCT user_id) ...",
	}

	if vr.Status != "confirmed" {
		t.Errorf("Status = %q, want %q", vr.Status, "confirmed")
	}
}

func TestExplorationStepLLMDialog(t *testing.T) {
	step := ExplorationStep{
		Step:        1,
		Action:      "query_data",
		Thinking:    "Check retention rates",
		LLMRequest:  "Full prompt sent to LLM...",
		LLMResponse: `{"thinking": "...", "query": "SELECT ..."}`,
		TokensIn:    200,
		TokensOut:   150,
		DurationMs:  800,
	}

	if step.LLMRequest == "" {
		t.Error("LLMRequest should capture full prompt")
	}
	if step.LLMResponse == "" {
		t.Error("LLMResponse should capture full response")
	}
	if step.TokensIn == 0 {
		t.Error("TokensIn should be captured")
	}
}

func TestRecommendationRelatedInsights(t *testing.T) {
	rec := Recommendation{
		ID:                "r1",
		Title:             "Fix Level 42",
		Priority:          1,
		RelatedInsightIDs: []string{"insight-1", "insight-2"},
	}

	if len(rec.RelatedInsightIDs) != 2 {
		t.Errorf("RelatedInsightIDs = %d, want 2", len(rec.RelatedInsightIDs))
	}
	if rec.RelatedInsightIDs[0] != "insight-1" {
		t.Errorf("RelatedInsightIDs[0] = %q, want insight-1", rec.RelatedInsightIDs[0])
	}
}

func TestRecommendationRelatedInsightsEmpty(t *testing.T) {
	rec := Recommendation{
		ID:    "r1",
		Title: "Fix Level 42",
	}

	if rec.RelatedInsightIDs != nil {
		t.Error("RelatedInsightIDs should be nil when not set")
	}
}

func TestRecommendationJSON_WithRelatedInsights(t *testing.T) {
	input := `{
		"recommendations": [{
			"id": "r1",
			"title": "Fix Level 42",
			"category": "churn",
			"priority": 1,
			"related_insight_ids": ["insight-1", "insight-2", "insight-3"],
			"confidence": 0.85
		}]
	}`

	var result struct {
		Recommendations []Recommendation `json:"recommendations"`
	}
	if err := json.Unmarshal([]byte(input), &result); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if len(result.Recommendations) != 1 {
		t.Fatalf("Recommendations = %d, want 1", len(result.Recommendations))
	}
	rec := result.Recommendations[0]
	if len(rec.RelatedInsightIDs) != 3 {
		t.Errorf("RelatedInsightIDs = %d, want 3", len(rec.RelatedInsightIDs))
	}
	if rec.RelatedInsightIDs[0] != "insight-1" {
		t.Errorf("RelatedInsightIDs[0] = %q", rec.RelatedInsightIDs[0])
	}
}

func TestRecommendationJSON_WithoutRelatedInsights(t *testing.T) {
	input := `{
		"recommendations": [{
			"id": "r1",
			"title": "Fix Level 42",
			"priority": 1,
			"confidence": 0.85
		}]
	}`

	var result struct {
		Recommendations []Recommendation `json:"recommendations"`
	}
	if err := json.Unmarshal([]byte(input), &result); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	rec := result.Recommendations[0]
	if rec.RelatedInsightIDs != nil {
		t.Errorf("RelatedInsightIDs should be nil when not in JSON, got %v", rec.RelatedInsightIDs)
	}
}

func TestRecommendationJSON_EmptyRelatedInsights(t *testing.T) {
	input := `{
		"recommendations": [{
			"id": "r1",
			"title": "Fix",
			"related_insight_ids": [],
			"confidence": 0.85
		}]
	}`

	var result struct {
		Recommendations []Recommendation `json:"recommendations"`
	}
	if err := json.Unmarshal([]byte(input), &result); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	rec := result.Recommendations[0]
	if len(rec.RelatedInsightIDs) != 0 {
		t.Errorf("RelatedInsightIDs = %d, want 0", len(rec.RelatedInsightIDs))
	}
}

func TestRecommendationJSON_RoundTrip(t *testing.T) {
	rec := Recommendation{
		ID:                "r1",
		Title:             "Fix Level 42",
		Priority:          1,
		RelatedInsightIDs: []string{"i-1", "i-2"},
		Confidence:        0.85,
	}

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var parsed Recommendation
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if len(parsed.RelatedInsightIDs) != 2 {
		t.Errorf("RelatedInsightIDs = %d, want 2", len(parsed.RelatedInsightIDs))
	}
	if parsed.RelatedInsightIDs[0] != "i-1" || parsed.RelatedInsightIDs[1] != "i-2" {
		t.Errorf("RelatedInsightIDs = %v", parsed.RelatedInsightIDs)
	}
}

func TestImpactFieldsRestored(t *testing.T) {
	impact := Impact{
		Metric:               "retention_rate",
		EstimatedImprovement: "15-20%",
		Reasoning:            "Based on similar games",
		ReturnRate:           0.45,
		ConversionRate:       0.24,
		EstimatedValue:       42.50,
		TotalValue:           52675.00,
	}

	if impact.ReturnRate != 0.45 {
		t.Errorf("ReturnRate = %f, want 0.45", impact.ReturnRate)
	}
	if impact.TotalValue != 52675.00 {
		t.Errorf("TotalValue = %f, want 52675.00", impact.TotalValue)
	}
}

func TestDiscoveryResult_FailedRunType(t *testing.T) {
	result := DiscoveryResult{
		ProjectID: "proj-123",
		Domain:    "gaming",
		Category:  "match3",
		RunType:   "failed",
		Summary: Summary{
			Errors: []string{"churn: LLM timeout", "levels: parse error"},
		},
	}

	if result.RunType != "failed" {
		t.Errorf("RunType = %q, want failed", result.RunType)
	}
	if len(result.Summary.Errors) != 2 {
		t.Errorf("Errors = %d, want 2", len(result.Summary.Errors))
	}
	if len(result.Insights) != 0 {
		t.Errorf("Insights = %d, want 0 for failed run", len(result.Insights))
	}
	if len(result.Recommendations) != 0 {
		t.Errorf("Recommendations = %d, want 0 for failed run", len(result.Recommendations))
	}
}

func TestTableSchema_WithEmptyColumns(t *testing.T) {
	schema := TableSchema{
		TableName: "empty_table",
		RowCount:  0,
		Columns:   []ColumnInfo{},
	}

	if schema.TableName != "empty_table" {
		t.Errorf("TableName = %q, want empty_table", schema.TableName)
	}
	if schema.RowCount != 0 {
		t.Errorf("RowCount = %d, want 0", schema.RowCount)
	}
	if len(schema.Columns) != 0 {
		t.Errorf("Columns = %d, want 0", len(schema.Columns))
	}
	if schema.KeyColumns != nil {
		t.Error("KeyColumns should be nil when not set")
	}
	if schema.Metrics != nil {
		t.Error("Metrics should be nil when not set")
	}
	if schema.Dimensions != nil {
		t.Error("Dimensions should be nil when not set")
	}
	if schema.SampleData != nil {
		t.Error("SampleData should be nil when not set")
	}
}

func TestColumnInfo_AllFields(t *testing.T) {
	tests := []struct {
		name     string
		col      ColumnInfo
		wantType string
		wantCat  string
	}{
		{
			name:     "primary key column",
			col:      ColumnInfo{Name: "user_id", Type: "STRING", Nullable: false, Category: "primary_key"},
			wantType: "STRING",
			wantCat:  "primary_key",
		},
		{
			name:     "timestamp column",
			col:      ColumnInfo{Name: "created_at", Type: "TIMESTAMP", Nullable: false, Category: "time"},
			wantType: "TIMESTAMP",
			wantCat:  "time",
		},
		{
			name:     "metric column",
			col:      ColumnInfo{Name: "duration", Type: "INT64", Nullable: true, Category: "metric"},
			wantType: "INT64",
			wantCat:  "metric",
		},
		{
			name:     "dimension column",
			col:      ColumnInfo{Name: "country", Type: "STRING", Nullable: true, Category: "dimension"},
			wantType: "STRING",
			wantCat:  "dimension",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.col.Name == "" {
				t.Error("Name should not be empty")
			}
			if tt.col.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", tt.col.Type, tt.wantType)
			}
			if tt.col.Category != tt.wantCat {
				t.Errorf("Category = %q, want %q", tt.col.Category, tt.wantCat)
			}
		})
	}
}

func TestImpact_OptionalFields(t *testing.T) {
	// Impact with all optional fields populated
	impact := Impact{
		Metric:               "revenue",
		EstimatedImprovement: "25-30%",
		Reasoning:            "Based on segment analysis",
		ReturnRate:           0.65,
		ConversionRate:       0.12,
		EstimatedValue:       150.00,
		TotalValue:           75000.00,
	}

	if impact.ReturnRate != 0.65 {
		t.Errorf("ReturnRate = %f, want 0.65", impact.ReturnRate)
	}
	if impact.ConversionRate != 0.12 {
		t.Errorf("ConversionRate = %f, want 0.12", impact.ConversionRate)
	}
	if impact.EstimatedValue != 150.00 {
		t.Errorf("EstimatedValue = %f, want 150.00", impact.EstimatedValue)
	}

	// Impact with no optional fields (zero values)
	impactMinimal := Impact{
		Metric:               "retention_rate",
		EstimatedImprovement: "10%",
		Reasoning:            "simple estimate",
	}

	if impactMinimal.ReturnRate != 0 {
		t.Errorf("ReturnRate should default to 0, got %f", impactMinimal.ReturnRate)
	}
	if impactMinimal.ConversionRate != 0 {
		t.Errorf("ConversionRate should default to 0, got %f", impactMinimal.ConversionRate)
	}
	if impactMinimal.EstimatedValue != 0 {
		t.Errorf("EstimatedValue should default to 0, got %f", impactMinimal.EstimatedValue)
	}
	if impactMinimal.TotalValue != 0 {
		t.Errorf("TotalValue should default to 0, got %f", impactMinimal.TotalValue)
	}
}

func TestImpact_JSONRoundTrip(t *testing.T) {
	impact := Impact{
		Metric:               "revenue",
		EstimatedImprovement: "20%",
		Reasoning:            "test reasoning",
		ReturnRate:           0.5,
		ConversionRate:       0.3,
		EstimatedValue:       100.00,
		TotalValue:           50000.00,
	}

	data, err := json.Marshal(impact)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var parsed Impact
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if parsed.Metric != impact.Metric {
		t.Errorf("Metric = %q, want %q", parsed.Metric, impact.Metric)
	}
	if parsed.ReturnRate != impact.ReturnRate {
		t.Errorf("ReturnRate = %f, want %f", parsed.ReturnRate, impact.ReturnRate)
	}
	if parsed.ConversionRate != impact.ConversionRate {
		t.Errorf("ConversionRate = %f, want %f", parsed.ConversionRate, impact.ConversionRate)
	}
	if parsed.EstimatedValue != impact.EstimatedValue {
		t.Errorf("EstimatedValue = %f, want %f", parsed.EstimatedValue, impact.EstimatedValue)
	}
	if parsed.TotalValue != impact.TotalValue {
		t.Errorf("TotalValue = %f, want %f", parsed.TotalValue, impact.TotalValue)
	}
}

func TestTableSchema_AllFields(t *testing.T) {
	now := time.Now()
	schema := TableSchema{
		TableName: "sessions",
		RowCount:  50000,
		Columns: []ColumnInfo{
			{Name: "user_id", Type: "STRING"},
			{Name: "session_start", Type: "TIMESTAMP"},
		},
		KeyColumns: []string{"user_id"},
		Metrics:    []string{"duration", "event_count"},
		Dimensions: []string{"country", "platform"},
		SampleData: []map[string]interface{}{
			{"user_id": "u1", "duration": 300},
		},
		DiscoveredAt: now,
	}

	if schema.RowCount != 50000 {
		t.Errorf("RowCount = %d, want 50000", schema.RowCount)
	}
	if len(schema.KeyColumns) != 1 {
		t.Errorf("KeyColumns = %d, want 1", len(schema.KeyColumns))
	}
	if len(schema.Metrics) != 2 {
		t.Errorf("Metrics = %d, want 2", len(schema.Metrics))
	}
	if len(schema.Dimensions) != 2 {
		t.Errorf("Dimensions = %d, want 2", len(schema.Dimensions))
	}
	if len(schema.SampleData) != 1 {
		t.Errorf("SampleData = %d, want 1", len(schema.SampleData))
	}
	if schema.DiscoveredAt.IsZero() {
		t.Error("DiscoveredAt should not be zero")
	}
}

func TestSQLMetadata_Fields(t *testing.T) {
	now := time.Now()
	meta := SQLMetadata{
		Query:           "SELECT COUNT(*) FROM sessions",
		ExecutionTimeMs: 450,
		RowsReturned:    1,
		ExecutedAt:      now,
	}

	if meta.Query == "" {
		t.Error("Query should not be empty")
	}
	if meta.ExecutionTimeMs != 450 {
		t.Errorf("ExecutionTimeMs = %d, want 450", meta.ExecutionTimeMs)
	}
	if meta.RowsReturned != 1 {
		t.Errorf("RowsReturned = %d, want 1", meta.RowsReturned)
	}
}

func TestSummary_Fields(t *testing.T) {
	s := Summary{
		Date:                 time.Now(),
		Text:                 "Discovery found 5 insights",
		KeyFindings:          []string{"High churn at level 45", "Revenue drop"},
		TopRecommendations:   []string{"Send extra lives"},
		TotalInsights:        5,
		TotalRecommendations: 3,
		QueriesExecuted:      20,
		Errors:               []string{"levels: timeout"},
	}

	if s.TotalInsights != 5 {
		t.Errorf("TotalInsights = %d, want 5", s.TotalInsights)
	}
	if s.TotalRecommendations != 3 {
		t.Errorf("TotalRecommendations = %d, want 3", s.TotalRecommendations)
	}
	if s.QueriesExecuted != 20 {
		t.Errorf("QueriesExecuted = %d, want 20", s.QueriesExecuted)
	}
	if len(s.KeyFindings) != 2 {
		t.Errorf("KeyFindings = %d, want 2", len(s.KeyFindings))
	}
	if len(s.Errors) != 1 {
		t.Errorf("Errors = %d, want 1", len(s.Errors))
	}
}

func TestRecommendationStep_Fields(t *testing.T) {
	step := RecommendationStep{
		RunAt:        time.Now(),
		Prompt:       "Generate recommendations based on insights...",
		InsightCount: 5,
		Response:     `{"recommendations": []}`,
		TokensIn:     1000,
		TokensOut:    500,
		DurationMs:   2000,
	}

	if step.InsightCount != 5 {
		t.Errorf("InsightCount = %d, want 5", step.InsightCount)
	}
	if step.Prompt == "" {
		t.Error("Prompt should be captured")
	}
	if step.Response == "" {
		t.Error("Response should be captured")
	}
	if step.TokensIn != 1000 {
		t.Errorf("TokensIn = %d, want 1000", step.TokensIn)
	}
}

func TestDiscoveryResult_JSONRoundTrip(t *testing.T) {
	now := time.Now()
	result := DiscoveryResult{
		ProjectID:     "proj-123",
		Domain:        "gaming",
		Category:      "match3",
		DiscoveryDate: now,
		RunType:       "full",
		TotalSteps:    25,
		Insights: []Insight{
			{ID: "i-1", Name: "High Churn", AnalysisArea: "churn", Severity: "critical"},
		},
		Recommendations: []Recommendation{
			{ID: "r-1", Title: "Send Lives", Priority: 1, RelatedInsightIDs: []string{"i-1"}},
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var parsed DiscoveryResult
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if parsed.ProjectID != "proj-123" {
		t.Errorf("ProjectID = %q, want proj-123", parsed.ProjectID)
	}
	if parsed.RunType != "full" {
		t.Errorf("RunType = %q, want full", parsed.RunType)
	}
	if len(parsed.Insights) != 1 {
		t.Fatalf("Insights = %d, want 1", len(parsed.Insights))
	}
	if parsed.Insights[0].Severity != "critical" {
		t.Errorf("Insight severity = %q, want critical", parsed.Insights[0].Severity)
	}
	if len(parsed.Recommendations) != 1 {
		t.Fatalf("Recommendations = %d, want 1", len(parsed.Recommendations))
	}
	if parsed.Recommendations[0].RelatedInsightIDs[0] != "i-1" {
		t.Errorf("RelatedInsightIDs[0] = %q, want i-1", parsed.Recommendations[0].RelatedInsightIDs[0])
	}
}

func TestInsightValidation_JSONRoundTrip(t *testing.T) {
	iv := InsightValidation{
		Status:        "confirmed",
		VerifiedCount: 2800,
		OriginalCount: 2847,
		Query:         "SELECT COUNT(DISTINCT user_id) FROM sessions",
		Reasoning:     "Within tolerance",
		ValidatedAt:   time.Now(),
	}

	data, err := json.Marshal(iv)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var parsed InsightValidation
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if parsed.Status != "confirmed" {
		t.Errorf("Status = %q, want confirmed", parsed.Status)
	}
	if parsed.VerifiedCount != 2800 {
		t.Errorf("VerifiedCount = %d, want 2800", parsed.VerifiedCount)
	}
	if parsed.OriginalCount != 2847 {
		t.Errorf("OriginalCount = %d, want 2847", parsed.OriginalCount)
	}
}
