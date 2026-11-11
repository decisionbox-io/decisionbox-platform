package models

import (
	"encoding/json"
	"reflect"
	"strings"
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
			"avg_ltv":      23.50,
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
	}

	if len(result.Insights) != 2 {
		t.Errorf("Insights = %d, want 2", len(result.Insights))
	}
	if len(result.Recommendations) != 1 {
		t.Errorf("Recommendations = %d, want 1", len(result.Recommendations))
	}
	// AnalysisLog used to be embedded on DiscoveryResult; it now lives in
	// the discovery_analysis_steps collection (DiscoveryLogRepository).
	// Per-step persistence is exercised by the integration test against
	// Mongo testcontainers in services/agent/internal/database.
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
		RunAt:                            time.Now(),
		Prompt:                           "Generate recommendations based on insights...",
		InsightCount:                     5,
		Response:                         `{"recommendations": []}`,
		TokensIn:                         1000,
		TokensOut:                        500,
		DurationMs:                       2000,
		RecommendationsDropped:           3,
		RecommendationsDroppedMissingIDs: 1,
		RecommendationsDroppedUnknownID:  2,
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
	if step.RecommendationsDropped != 3 {
		t.Errorf("RecommendationsDropped = %d, want 3", step.RecommendationsDropped)
	}
	if step.RecommendationsDroppedMissingIDs != 1 {
		t.Errorf("RecommendationsDroppedMissingIDs = %d, want 1", step.RecommendationsDroppedMissingIDs)
	}
	if step.RecommendationsDroppedUnknownID != 2 {
		t.Errorf("RecommendationsDroppedUnknownID = %d, want 2", step.RecommendationsDroppedUnknownID)
	}
}

func TestRecommendationStep_DroppedFieldsOmitEmpty(t *testing.T) {
	// On the happy path (no drops) the per-reason fields must not
	// pollute the persisted document. Round-trip through JSON and
	// verify the dropped fields are absent from the wire shape.
	step := RecommendationStep{
		RunAt:        time.Now(),
		Prompt:       "p",
		InsightCount: 1,
		Response:     `{}`,
	}
	data, err := json.Marshal(step)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	wire := string(data)
	for _, banned := range []string{
		`"recommendations_dropped"`,
		`"recommendations_dropped_missing_ids"`,
		`"recommendations_dropped_unknown_id"`,
	} {
		if strings.Contains(wire, banned) {
			t.Errorf("zero-drop step should omit %s, got %s", banned, wire)
		}
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
	// Severity is NOT asserted here any more, and that is the contract: the
	// field is derived, tagged json:"-", so a JSON round trip deliberately
	// drops it. BSON is unaffected -- which is what TestDiscoveryResultBSON
	// covers -- so a persisted insight still reads back with its band.
	if parsed.Insights[0].Severity != "" {
		t.Errorf("Insight severity = %q; a derived field must not decode from JSON",
			parsed.Insights[0].Severity)
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

// --- Impact tolerant JSON decoding (issue #342) ---

// TestImpactUnmarshalJSON_Object verifies the normal object form still decodes
// into all fields (the custom UnmarshalJSON must not regress the happy path).
func TestImpactUnmarshalJSON_Object(t *testing.T) {
	const in = `{"metric":"retention","estimated_improvement":"+15%","reasoning":"cohort uplift","estimated_value":1234.5}`
	var im Impact
	if err := json.Unmarshal([]byte(in), &im); err != nil {
		t.Fatalf("Unmarshal object: %v", err)
	}
	if im.Metric != "retention" || im.EstimatedImprovement != "+15%" || im.Reasoning != "cohort uplift" {
		t.Errorf("object fields not decoded: %+v", im)
	}
	if im.EstimatedValue != 1234.5 {
		t.Errorf("EstimatedValue = %v, want 1234.5", im.EstimatedValue)
	}
}

// TestImpactUnmarshalJSON_BareString is the core #342 fix: a prose string is
// coerced into Impact{Reasoning: <string>} instead of failing the decode.
func TestImpactUnmarshalJSON_BareString(t *testing.T) {
	const in = `"improves utilization accuracy and revenue forecasting"`
	var im Impact
	if err := json.Unmarshal([]byte(in), &im); err != nil {
		t.Fatalf("Unmarshal bare string must not error: %v", err)
	}
	if im.Reasoning != "improves utilization accuracy and revenue forecasting" {
		t.Errorf("Reasoning = %q, want the prose string", im.Reasoning)
	}
	if im.Metric != "" || im.EstimatedImprovement != "" {
		t.Errorf("other fields must stay empty on string coercion: %+v", im)
	}
}

// TestImpactUnmarshalJSON_Null leaves the zero value and does not error.
func TestImpactUnmarshalJSON_Null(t *testing.T) {
	var im = Impact{Metric: "preset"}
	if err := json.Unmarshal([]byte("null"), &im); err != nil {
		t.Fatalf("Unmarshal null: %v", err)
	}
	// json treats a top-level null as a no-op on the destination.
	if im.Metric != "preset" {
		t.Errorf("null must be a no-op, got %+v", im)
	}
}

// TestImpactUnmarshalJSON_WrongType errors so the enclosing recommendation is
// dropped per-item rather than silently accepted.
func TestImpactUnmarshalJSON_WrongType(t *testing.T) {
	for _, in := range []string{`42`, `[1,2,3]`, `true`} {
		var im Impact
		if err := json.Unmarshal([]byte(in), &im); err == nil {
			t.Errorf("Unmarshal(%s) into Impact: want error, got none (%+v)", in, im)
		}
	}
}

// TestRecommendationUnmarshalJSON_StringImpact is the end-to-end #342 case: a
// whole recommendation whose expected_impact is a prose string decodes cleanly.
func TestRecommendationUnmarshalJSON_StringImpact(t *testing.T) {
	const in = `{
		"title":"Right-size idle warehouses",
		"expected_impact":"improves utilization accuracy and revenue forecasting",
		"priority":1
	}`
	var rec Recommendation
	if err := json.Unmarshal([]byte(in), &rec); err != nil {
		t.Fatalf("recommendation with string expected_impact must decode: %v", err)
	}
	if rec.Title != "Right-size idle warehouses" {
		t.Errorf("scalar fields not decoded: %+v", rec)
	}
	// `priority` is derived now and tagged json:"-", so the key in the input
	// above is an unknown field the decoder ignores. That is the protection,
	// not a loss: the whole recommendation used to fail on an off-typed one.
	if rec.Priority != 0 {
		t.Errorf("priority = %d; a derived field must not decode from JSON", rec.Priority)
	}
	if rec.ExpectedImpact.Reasoning != "improves utilization accuracy and revenue forecasting" {
		t.Errorf("prose impact not coerced into Reasoning: %+v", rec.ExpectedImpact)
	}
}

// TestAModelAuthoredPriorityIsIgnoredRatherThanFatal is the inverse of the
// coercion this test used to be.
//
// `priority` arriving as a descriptive word used to drop the whole
// recommendation batch (issue #342), which is why a coercion read "high" as 2.
// The field is derived now, so the coercion went with the ask -- but the
// protection it provided has to survive, because a project snapshots its
// prompts at creation and an existing deployment keeps asking the model for a
// priority indefinitely. Under json:"-" every shape below is an unknown field:
// the key is ignored and the rest of the recommendation decodes.
func TestAModelAuthoredPriorityIsIgnoredRatherThanFatal(t *testing.T) {
	for _, raw := range []string{
		`3`, `"2"`, `"P1"`, `"critical"`, `"high"`, `"Medium"`, `"low"`,
		`"lowest"`, `"optional"`, `"nonsense"`, `null`, `[1]`, `{"tier":"critical"}`,
	} {
		in := `{"title":"t","priority":` + raw + `,"segment_size":42}`
		var rec Recommendation
		if err := json.Unmarshal([]byte(in), &rec); err != nil {
			t.Errorf("priority %s failed the whole recommendation: %v", raw, err)
			continue
		}
		if rec.Priority != 0 {
			t.Errorf("priority %s decoded to %d; the model must not author this field",
				raw, rec.Priority)
		}
		if rec.Title != "t" || rec.SegmentSize != 42 {
			t.Errorf("priority %s cost the rest of the recommendation: %+v", raw, rec)
		}
	}
}

// TestAModelAuthoredSeverityIsIgnoredRatherThanFatal -- the same, for the two
// insight fields that stopped being the model's.
func TestAModelAuthoredSeverityIsIgnoredRatherThanFatal(t *testing.T) {
	for _, raw := range []string{
		`"critical"`, `"high"`, `null`, `0.9`, `["high"]`, `{"band":"high"}`,
	} {
		in := `{"name":"Revenue drop","severity":` + raw + `,"affected_count":1200}`
		var ins Insight
		if err := json.Unmarshal([]byte(in), &ins); err != nil {
			t.Errorf("severity %s failed the whole insight: %v", raw, err)
			continue
		}
		if ins.Severity != "" {
			t.Errorf("severity %s decoded to %q; the model must not author this field", raw, ins.Severity)
		}
		if ins.Name != "Revenue drop" || ins.AffectedCount != 1200 {
			t.Errorf("severity %s cost the rest of the insight: %+v", raw, ins)
		}
	}
	for _, raw := range []string{`0.9`, `"0.9"`, `null`, `"high"`, `[0.9]`, `{"score":1}`} {
		in := `{"name":"Revenue drop","risk_score":` + raw + `,"affected_count":1200}`
		var ins Insight
		if err := json.Unmarshal([]byte(in), &ins); err != nil {
			t.Errorf("risk_score %s failed the whole insight: %v", raw, err)
			continue
		}
		if ins.RiskScore != 0 {
			t.Errorf("risk_score %s decoded to %v; the model must not author this field", raw, ins.RiskScore)
		}
		if ins.Name != "Revenue drop" || ins.AffectedCount != 1200 {
			t.Errorf("risk_score %s cost the rest of the insight: %+v", raw, ins)
		}
	}
}

// TestNumericFieldCoercion covers segment_size arriving as a numeric string
// (same class as priority — a stringified number must not drop the rec).
//
// `confidence` is no longer coerced because it is no longer read from model
// output; a string-typed one is covered by
// TestAStringTypedConfidenceIsIgnoredRatherThanFatal.
func TestNumericFieldCoercion(t *testing.T) {
	const in = `{"title":"t","segment_size":"1,311,488"}`
	var rec Recommendation
	if err := json.Unmarshal([]byte(in), &rec); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if rec.SegmentSize != 1311488 {
		t.Errorf("SegmentSize = %d, want 1311488", rec.SegmentSize)
	}
}

// TestNonFiniteNumericRejected ensures NaN/Inf strings don't poison a stored
// float (which would later break JSON serialization of the discovery).
//
// There is no float half left to run: `risk_score` and `confidence` were the
// two float fields a model wrote, and both are derived now and read from no
// model output at all. flexNumber's non-finite rejection still guards the
// integer coercions below and the figure decoder, which is where the remaining
// model-authored numbers arrive.
func TestNonFiniteNumericRejected(t *testing.T) {
	for _, in := range []string{`{"title":"t","segment_size":"Inf"}`, `{"title":"t","segment_size":"-Inf"}`} {
		var rec Recommendation
		if err := json.Unmarshal([]byte(in), &rec); err != nil {
			t.Fatalf("Unmarshal(%s): %v", in, err)
		}
		if rec.SegmentSize != 0 {
			t.Errorf("%s: non-finite value leaked (segment=%d)", in, rec.SegmentSize)
		}
	}
	for _, in := range []string{`{"name":"n","risk_score":"NaN"}`, `{"name":"n","risk_score":"Inf"}`} {
		var ins Insight
		if err := json.Unmarshal([]byte(in), &ins); err != nil {
			t.Fatalf("Unmarshal(%s): %v", in, err)
		}
		if ins.RiskScore != 0 {
			t.Errorf("%s: non-finite value leaked (risk_score=%v)", in, ins.RiskScore)
		}
	}
}

// --- Insight.UnmarshalJSON (small/open-model insight recovery) ---

// TestInsightUnmarshalJSON_GoldenEquality is the big-model no-op proof at the
// struct level: a fully populated, well-typed insight survives a Marshal →
// Unmarshal round trip byte-for-byte identical. The tolerant decoder must not
// change any value that was already the right type (as an Opus/GPT response
// is). If this ever fails, the coercion is altering good data.
//
// Confidence, Severity and RiskScore are deliberately absent from the
// fixture: all three are derived rather than authored and are tagged
// json:"-", so none of them is on this wire at all and a value set here could
// not come back. Their own behaviour is pinned by
// TestAStringTypedConfidenceIsIgnoredRatherThanFatal and
// TestAModelAuthoredSeverityIsIgnoredRatherThanFatal.
func TestInsightUnmarshalJSON_GoldenEquality(t *testing.T) {
	original := Insight{
		ID:            "11111111-2222-3333-4444-555555555555",
		AnalysisArea:  "churn",
		Name:          "High churn at level 45",
		Description:   "Players drop off at level 45.",
		DescriptionMd: "Players drop off at **level 45**.",
		AffectedCount: 2847,
		Metrics:       map[string]interface{}{"drop_rate": 0.67, "cohort": "week-3"},
		Indicators:    []string{"session drop", "revenue decline"},
		TargetSegment: "new players",
		SourceSteps:   []int{1, 3, 5},
		DiscoveredAt:  time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Insight
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(original, decoded) {
		t.Errorf("round trip changed a well-typed insight:\n orig = %+v\n got  = %+v", original, decoded)
	}
}

// TestInsightUnmarshalJSON_StringTypedScalars covers the dominant small/open
// model failure: numeric fields emitted as strings. Under strict decoding a
// single one of these failed the whole insight and zeroed the analysis area.
func TestInsightUnmarshalJSON_StringTypedScalars(t *testing.T) {
	const in = `{
		"name":"Revenue drop",
		"affected_count":"1,200"
	}`
	var ins Insight
	if err := json.Unmarshal([]byte(in), &ins); err != nil {
		t.Fatalf("string-typed scalars must decode: %v", err)
	}
	if ins.AffectedCount != 1200 {
		t.Errorf("AffectedCount = %d, want 1200", ins.AffectedCount)
	}
}

// TestAStringTypedConfidenceIsIgnoredRatherThanFatal is the regression guard
// for the whole reason the field is tagged json:"-" rather than merely losing
// its coercion.
//
// The number is derived now, so the schemas no longer ask for it — but a
// project snapshots its area prompts at creation and a pack's seeds are never
// overwritten on upgrade, so existing deployments keep asking the model for it
// forever. Had the field kept json:"confidence" with the coercion removed, a
// model answering "confidence": "0.9" would fail the whole insight decode and
// silently zero the area's insights: precisely the issue-#342 failure the
// coercion was added for, reintroduced by the fix for something else. Under
// json:"-" the key is an unknown field and encoding/json ignores it.
func TestAStringTypedConfidenceIsIgnoredRatherThanFatal(t *testing.T) {
	for _, raw := range []string{`"0.9"`, `0.9`, `"high"`, `null`, `{"band":"high"}`, `[0.9]`} {
		t.Run(raw, func(t *testing.T) {
			var ins Insight
			in := `{"name":"Revenue drop","affected_count":7,"confidence":` + raw + `}`
			if err := json.Unmarshal([]byte(in), &ins); err != nil {
				t.Fatalf("a volunteered confidence failed the whole insight decode: %v", err)
			}
			if ins.Name != "Revenue drop" || ins.AffectedCount != 7 {
				t.Errorf("the rest of the insight was lost: %+v", ins)
			}
			if ins.Confidence != 0 {
				t.Errorf("Confidence = %v; the model must not be able to author it", ins.Confidence)
			}

			var rec Recommendation
			recIn := `{"title":"t","category":"c","confidence":` + raw + `}`
			if err := json.Unmarshal([]byte(recIn), &rec); err != nil {
				t.Fatalf("a volunteered confidence failed the whole recommendation decode: %v", err)
			}
			if rec.Title != "t" || rec.Category != "c" {
				t.Errorf("the rest of the recommendation was lost: %+v", rec)
			}
			if rec.Confidence != 0 {
				t.Errorf("Confidence = %v; the model must not be able to author it", rec.Confidence)
			}
		})
	}
}

// TestAVolunteeredScoringBlockIsIgnored is the same defence one level up. A
// model told to stop writing `confidence` may reach for the sub-document
// instead, and a scalar `"scoring": "high"` under a tag the decoder reads would
// fail the decode and drop the insight — the hazard the evidence_* naming
// convention exists for.
func TestAVolunteeredScoringBlockIsIgnored(t *testing.T) {
	for _, raw := range []string{`"high"`, `0.9`, `{"confidence_band":"high"}`, `null`} {
		var ins Insight
		in := `{"name":"n","severity":"high","scoring":` + raw + `}`
		if err := json.Unmarshal([]byte(in), &ins); err != nil {
			t.Fatalf("scoring=%s failed the insight decode: %v", raw, err)
		}
		if ins.Scoring != nil {
			t.Errorf("scoring=%s was read into the document: %+v", raw, ins.Scoring)
		}
		if ins.Name != "n" {
			t.Errorf("scoring=%s cost the insight its prose: %+v", raw, ins)
		}
	}
}

// TestInsightUnmarshalJSON_SourceStepsFlexible covers source_steps arriving as
// an array of numeric strings, a single scalar, or a comma-joined string — all
// shapes small models produce instead of the []int the schema asks for.
func TestInsightUnmarshalJSON_SourceStepsFlexible(t *testing.T) {
	cases := []struct {
		in   string
		want []int
	}{
		{`[1,3,5]`, []int{1, 3, 5}},
		{`["1","3","5"]`, []int{1, 3, 5}},
		{`"1,2,3"`, []int{1, 2, 3}},
		{`7`, []int{7}},
		{`"9"`, []int{9}},
		{`null`, nil},
	}
	for _, c := range cases {
		in := `{"name":"n","source_steps":` + c.in + `}`
		var ins Insight
		if err := json.Unmarshal([]byte(in), &ins); err != nil {
			t.Fatalf("Unmarshal(source_steps=%s): %v", c.in, err)
		}
		if !reflect.DeepEqual(ins.SourceSteps, c.want) {
			t.Errorf("source_steps %s → %v, want %v", c.in, ins.SourceSteps, c.want)
		}
	}
}

// TestInsightUnmarshalJSON_IndicatorsScalar covers indicators arriving as a bare
// string (one signal) instead of a list, and a list with a stray number.
func TestInsightUnmarshalJSON_IndicatorsScalar(t *testing.T) {
	var single Insight
	if err := json.Unmarshal([]byte(`{"name":"n","indicators":"session drop"}`), &single); err != nil {
		t.Fatalf("scalar indicators must decode: %v", err)
	}
	if !reflect.DeepEqual(single.Indicators, []string{"session drop"}) {
		t.Errorf("scalar indicators → %v, want [\"session drop\"]", single.Indicators)
	}

	var mixed Insight
	if err := json.Unmarshal([]byte(`{"name":"n","indicators":["a",2]}`), &mixed); err != nil {
		t.Fatalf("mixed indicators must decode: %v", err)
	}
	if !reflect.DeepEqual(mixed.Indicators, []string{"a", "2"}) {
		t.Errorf("mixed indicators → %v, want [\"a\",\"2\"]", mixed.Indicators)
	}
}

// TestInsightUnmarshalJSON_NonFiniteRejected ensures a NaN/Inf risk_score or
// confidence string cannot poison a stored float (which would later break JSON
// serialization of the whole discovery). Same guard as the recommendation path.
func TestInsightUnmarshalJSON_NonFiniteRejected(t *testing.T) {
	for _, in := range []string{`{"name":"n","risk_score":"NaN"}`, `{"name":"n","confidence":"Inf"}`, `{"name":"n","risk_score":"-Inf"}`} {
		var ins Insight
		if err := json.Unmarshal([]byte(in), &ins); err != nil {
			t.Fatalf("Unmarshal(%s): %v", in, err)
		}
		if ins.RiskScore != 0 || ins.Confidence != 0 {
			t.Errorf("%s: non-finite value leaked (risk=%v confidence=%v)", in, ins.RiskScore, ins.Confidence)
		}
	}
}
