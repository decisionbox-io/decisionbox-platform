package models

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRunStatusConstants(t *testing.T) {
	// Verify constants exist and have expected values
	if RunStatusPending != "pending" {
		t.Error("RunStatusPending should be 'pending'")
	}
	if RunStatusRunning != "running" {
		t.Error("RunStatusRunning should be 'running'")
	}
	if RunStatusCompleted != "completed" {
		t.Error("RunStatusCompleted should be 'completed'")
	}
	if RunStatusFailed != "failed" {
		t.Error("RunStatusFailed should be 'failed'")
	}
}

func TestPhaseConstants(t *testing.T) {
	phases := []string{PhaseInit, PhaseSchemaDiscovery, PhaseExploration, PhaseAnalysis,
		PhaseValidation, PhaseRecommendations, PhaseSaving, PhaseComplete}
	for _, p := range phases {
		if p == "" {
			t.Error("phase constant should not be empty")
		}
	}
}

func TestRunStepTypes(t *testing.T) {
	step := RunStep{
		Phase:       PhaseExploration,
		Type:        "query",
		Message:     "Step 1: checking retention",
		LLMThinking: "I need to check retention rates",
		Query:       "SELECT * FROM sessions",
		RowCount:    100,
	}

	if step.Phase != PhaseExploration {
		t.Error("Phase not set")
	}
	if step.RowCount != 100 {
		t.Error("RowCount not set")
	}
}

func TestRun_JSONRoundTrip(t *testing.T) {
	now := time.Now()
	completedAt := now.Add(5 * time.Minute)
	run := DiscoveryRun{
		ID:          "run-abc",
		ProjectID:   "proj-123",
		Status:      RunStatusCompleted,
		Phase:       PhaseComplete,
		PhaseDetail: "Discovery completed successfully",
		Progress:    100,
		StartedAt:   now,
		UpdatedAt:   completedAt,
		CompletedAt: &completedAt,
		Steps: []RunStep{
			{
				Phase:   PhaseExploration,
				StepNum: 1,
				Type:    "query",
				Message: "Step 1: checking retention",
				Query:   "SELECT COUNT(*) FROM sessions",
			},
			{
				Phase:   PhaseAnalysis,
				Type:    "insight",
				Message: "Found: High Churn (critical)",
			},
		},
		TotalQueries:      20,
		SuccessfulQueries: 18,
		FailedQueries:     2,
		InsightsFound:     5,
	}

	data, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var parsed DiscoveryRun
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if parsed.ID != "run-abc" {
		t.Errorf("ID = %q, want run-abc", parsed.ID)
	}
	if parsed.ProjectID != "proj-123" {
		t.Errorf("ProjectID = %q, want proj-123", parsed.ProjectID)
	}
	if parsed.Status != RunStatusCompleted {
		t.Errorf("Status = %q, want %q", parsed.Status, RunStatusCompleted)
	}
	if parsed.Phase != PhaseComplete {
		t.Errorf("Phase = %q, want %q", parsed.Phase, PhaseComplete)
	}
	if parsed.Progress != 100 {
		t.Errorf("Progress = %d, want 100", parsed.Progress)
	}
	if parsed.CompletedAt == nil {
		t.Fatal("CompletedAt should not be nil")
	}
	if len(parsed.Steps) != 2 {
		t.Errorf("Steps = %d, want 2", len(parsed.Steps))
	}
	if parsed.TotalQueries != 20 {
		t.Errorf("TotalQueries = %d, want 20", parsed.TotalQueries)
	}
	if parsed.SuccessfulQueries != 18 {
		t.Errorf("SuccessfulQueries = %d, want 18", parsed.SuccessfulQueries)
	}
	if parsed.FailedQueries != 2 {
		t.Errorf("FailedQueries = %d, want 2", parsed.FailedQueries)
	}
	if parsed.InsightsFound != 5 {
		t.Errorf("InsightsFound = %d, want 5", parsed.InsightsFound)
	}
}

func TestRunStep_AllFields(t *testing.T) {
	step := RunStep{
		Phase:           PhaseExploration,
		StepNum:         3,
		Timestamp:       time.Now(),
		Type:            "query",
		Message:         "Step 3: analyzing retention patterns",
		LLMThinking:     "I need to check 7-day retention rates",
		LLMQuery:        "Check retention cohorts",
		Query:           "SELECT DATE(created_at), COUNT(DISTINCT user_id) FROM sessions GROUP BY 1",
		QueryResult:     "15 rows returned",
		RowCount:        15,
		QueryTimeMs:     850,
		QueryFixed:      true,
		InsightName:     "",
		InsightSeverity: "",
		Error:           "",
		DurationMs:      1200,
	}

	if step.Phase != PhaseExploration {
		t.Errorf("Phase = %q, want %q", step.Phase, PhaseExploration)
	}
	if step.StepNum != 3 {
		t.Errorf("StepNum = %d, want 3", step.StepNum)
	}
	if step.Type != "query" {
		t.Errorf("Type = %q, want query", step.Type)
	}
	if step.LLMThinking == "" {
		t.Error("LLMThinking should be set")
	}
	if step.LLMQuery == "" {
		t.Error("LLMQuery should be set")
	}
	if step.Query == "" {
		t.Error("Query should be set")
	}
	if step.QueryResult == "" {
		t.Error("QueryResult should be set")
	}
	if step.RowCount != 15 {
		t.Errorf("RowCount = %d, want 15", step.RowCount)
	}
	if step.QueryTimeMs != 850 {
		t.Errorf("QueryTimeMs = %d, want 850", step.QueryTimeMs)
	}
	if !step.QueryFixed {
		t.Error("QueryFixed should be true")
	}
	if step.DurationMs != 1200 {
		t.Errorf("DurationMs = %d, want 1200", step.DurationMs)
	}
}

func TestRunStep_InsightType(t *testing.T) {
	step := RunStep{
		Phase:           PhaseAnalysis,
		Type:            "insight",
		Message:         "Found: High Churn at Level 45 (critical)",
		InsightName:     "High Churn at Level 45",
		InsightSeverity: "critical",
	}

	if step.Type != "insight" {
		t.Errorf("Type = %q, want insight", step.Type)
	}
	if step.InsightName != "High Churn at Level 45" {
		t.Errorf("InsightName = %q", step.InsightName)
	}
	if step.InsightSeverity != "critical" {
		t.Errorf("InsightSeverity = %q, want critical", step.InsightSeverity)
	}
}

func TestRunStep_ErrorType(t *testing.T) {
	step := RunStep{
		Phase:   PhaseExploration,
		Type:    "error",
		Message: "Query failed: syntax error",
		Error:   "syntax error near 'FROM'",
	}

	if step.Type != "error" {
		t.Errorf("Type = %q, want error", step.Type)
	}
	if step.Error == "" {
		t.Error("Error should be set")
	}
}

func TestDiscoveryRun_NilCompletedAt(t *testing.T) {
	run := DiscoveryRun{
		ID:        "run-123",
		Status:    RunStatusRunning,
		Phase:     PhaseExploration,
		StartedAt: time.Now(),
	}

	if run.CompletedAt != nil {
		t.Error("CompletedAt should be nil for running run")
	}

	data, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var parsed DiscoveryRun
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if parsed.CompletedAt != nil {
		t.Error("CompletedAt should remain nil after round-trip")
	}
}
