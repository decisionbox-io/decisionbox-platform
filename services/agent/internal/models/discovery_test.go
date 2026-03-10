package models

import (
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
			{ID: "r1", Title: "Fix Level 42", Priority: 5},
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
