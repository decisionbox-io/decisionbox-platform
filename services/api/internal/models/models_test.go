package models

import (
	"encoding/json"
	"testing"
	"time"
)

// --- Project ---

func TestProject_JSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	original := Project{
		ID:          "proj-123",
		Name:        "Test Project",
		Description: "A test project",
		Domain:      "gaming",
		Category:    "match3",
		Warehouse: WarehouseConfig{
			Provider:  "bigquery",
			ProjectID: "gcp-project",
			Datasets:  []string{"dataset1", "dataset2"},
			Location:  "US",
			Config:    map[string]string{"key": "value"},
		},
		LLM: LLMConfig{
			Provider: "claude",
			Model:    "claude-sonnet-4",
			Config:   map[string]string{"api_key": "sk-test"},
		},
		Schedule: ScheduleConfig{
			Enabled:  true,
			CronExpr: "0 6 * * *",
			MaxSteps: 100,
		},
		Profile: map[string]interface{}{
			"game_name": "TestGame",
		},
		Status:    "active",
		CreatedAt: now,
		UpdatedAt: now,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var decoded Project
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID = %q, want %q", decoded.ID, original.ID)
	}
	if decoded.Name != original.Name {
		t.Errorf("Name = %q, want %q", decoded.Name, original.Name)
	}
	if decoded.Description != original.Description {
		t.Errorf("Description = %q", decoded.Description)
	}
	if decoded.Domain != original.Domain {
		t.Errorf("Domain = %q", decoded.Domain)
	}
	if decoded.Category != original.Category {
		t.Errorf("Category = %q", decoded.Category)
	}
	if decoded.Status != original.Status {
		t.Errorf("Status = %q", decoded.Status)
	}
	if decoded.Warehouse.Provider != "bigquery" {
		t.Errorf("Warehouse.Provider = %q", decoded.Warehouse.Provider)
	}
	if decoded.Warehouse.ProjectID != "gcp-project" {
		t.Errorf("Warehouse.ProjectID = %q", decoded.Warehouse.ProjectID)
	}
	if len(decoded.Warehouse.Datasets) != 2 {
		t.Errorf("Warehouse.Datasets len = %d", len(decoded.Warehouse.Datasets))
	}
	if decoded.LLM.Provider != "claude" {
		t.Errorf("LLM.Provider = %q", decoded.LLM.Provider)
	}
	if decoded.LLM.Model != "claude-sonnet-4" {
		t.Errorf("LLM.Model = %q", decoded.LLM.Model)
	}
	if decoded.Schedule.Enabled != true {
		t.Error("Schedule.Enabled should be true")
	}
	if decoded.Schedule.CronExpr != "0 6 * * *" {
		t.Errorf("Schedule.CronExpr = %q", decoded.Schedule.CronExpr)
	}
	if decoded.Schedule.MaxSteps != 100 {
		t.Errorf("Schedule.MaxSteps = %d", decoded.Schedule.MaxSteps)
	}
}

func TestProject_JSONFieldTags(t *testing.T) {
	p := Project{
		ID:   "test-id",
		Name: "test",
	}
	data, _ := json.Marshal(p)
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)

	// Verify JSON field names match expected tags
	expectedFields := []string{"id", "name", "domain", "category", "warehouse", "llm", "schedule", "status", "created_at", "updated_at"}
	for _, f := range expectedFields {
		if _, ok := raw[f]; !ok {
			t.Errorf("missing JSON field %q in marshaled Project", f)
		}
	}
}

func TestProject_OmitEmpty(t *testing.T) {
	p := Project{
		ID:   "test-id",
		Name: "test",
	}
	data, _ := json.Marshal(p)
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)

	// description and profile should be omitted when empty
	if _, ok := raw["description"]; ok {
		t.Error("empty description should be omitted")
	}
	if _, ok := raw["profile"]; ok {
		t.Error("nil profile should be omitted")
	}
	if _, ok := raw["prompts"]; ok {
		t.Error("nil prompts should be omitted")
	}
	if _, ok := raw["last_run_at"]; ok {
		t.Error("nil last_run_at should be omitted")
	}
}

func TestProject_WithLastRun(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	p := Project{
		ID:            "proj-1",
		Name:          "Test",
		LastRunAt:     &now,
		LastRunStatus: "completed",
	}

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	var decoded Project
	json.Unmarshal(data, &decoded)

	if decoded.LastRunAt == nil {
		t.Fatal("LastRunAt should not be nil")
	}
	if decoded.LastRunStatus != "completed" {
		t.Errorf("LastRunStatus = %q", decoded.LastRunStatus)
	}
}

func TestProjectPrompts_JSONRoundTrip(t *testing.T) {
	prompts := ProjectPrompts{
		Exploration:     "Explore the data for patterns",
		Recommendations: "Generate recommendations",
		BaseContext:     "{{PROFILE}} context",
		AnalysisAreas: map[string]AnalysisAreaConfig{
			"churn": {
				Name:        "Churn Analysis",
				Description: "Analyze churn patterns",
				Keywords:    []string{"churn", "retention"},
				Prompt:      "Analyze churn",
				IsBase:      true,
				IsCustom:    false,
				Priority:    1,
				Enabled:     true,
			},
		},
	}

	data, err := json.Marshal(prompts)
	if err != nil {
		t.Fatal(err)
	}

	var decoded ProjectPrompts
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded.Exploration != prompts.Exploration {
		t.Errorf("Exploration = %q", decoded.Exploration)
	}
	if decoded.Recommendations != prompts.Recommendations {
		t.Errorf("Recommendations = %q", decoded.Recommendations)
	}
	if decoded.BaseContext != prompts.BaseContext {
		t.Errorf("BaseContext = %q", decoded.BaseContext)
	}
	area, ok := decoded.AnalysisAreas["churn"]
	if !ok {
		t.Fatal("missing churn area")
	}
	if area.Name != "Churn Analysis" {
		t.Errorf("area.Name = %q", area.Name)
	}
	if !area.IsBase {
		t.Error("area.IsBase should be true")
	}
	if area.Priority != 1 {
		t.Errorf("area.Priority = %d", area.Priority)
	}
	if len(area.Keywords) != 2 {
		t.Errorf("area.Keywords len = %d", len(area.Keywords))
	}
}

// --- DiscoveryResult ---

func TestDiscoveryResult_JSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	original := DiscoveryResult{
		ID:             "disc-1",
		ProjectID:      "proj-1",
		Domain:         "gaming",
		Category:       "match3",
		RunType:        "full",
		AreasRequested: []string{"churn", "monetization"},
		DiscoveryDate:  now,
		TotalSteps:     50,
		Duration:       120000,
		Insights: []Insight{
			{
				ID:            "ins-1",
				AnalysisArea:  "churn",
				Name:          "High D1 Churn",
				Description:   "Players leaving on day 1",
				Severity:      "high",
				AffectedCount: 1500,
				RiskScore:     0.85,
				Confidence:    0.92,
				Metrics: map[string]interface{}{
					"d1_churn_rate": 0.45,
				},
				Indicators:    []string{"low tutorial completion"},
				TargetSegment: "new_players",
				SourceSteps:   []int{3, 7, 12},
				DiscoveredAt:  now,
			},
		},
		Recommendations: []Recommendation{
			{
				ID:            "rec-1",
				Category:      "retention",
				Title:         "Improve Tutorial",
				Description:   "Revamp the tutorial flow",
				Priority:      1,
				TargetSegment: "new_players",
				SegmentSize:   1500,
				ExpectedImpact: Impact{
					Metric:               "d1_retention",
					EstimatedImprovement: "+15%",
					Reasoning:            "tutorial completion strongly correlates with retention",
				},
				Actions:           []string{"simplify tutorial", "add rewards"},
				RelatedInsightIDs: []string{"ins-1"},
				Confidence:        0.88,
			},
		},
		Summary: Summary{
			Date:                 now,
			Text:                 "Found significant churn patterns",
			KeyFindings:          []string{"High D1 churn", "Low tutorial completion"},
			TopRecommendations:   []string{"Improve tutorial"},
			TotalInsights:        1,
			TotalRecommendations: 1,
			QueriesExecuted:      50,
		},
		CreatedAt: now,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var decoded DiscoveryResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID = %q", decoded.ID)
	}
	if decoded.ProjectID != original.ProjectID {
		t.Errorf("ProjectID = %q", decoded.ProjectID)
	}
	if decoded.Domain != "gaming" {
		t.Errorf("Domain = %q", decoded.Domain)
	}
	if decoded.Category != "match3" {
		t.Errorf("Category = %q", decoded.Category)
	}
	if decoded.RunType != "full" {
		t.Errorf("RunType = %q", decoded.RunType)
	}
	if len(decoded.AreasRequested) != 2 {
		t.Errorf("AreasRequested len = %d", len(decoded.AreasRequested))
	}
	if decoded.TotalSteps != 50 {
		t.Errorf("TotalSteps = %d", decoded.TotalSteps)
	}
	if decoded.Duration != 120000 {
		t.Errorf("Duration = %d", decoded.Duration)
	}

	// Insights
	if len(decoded.Insights) != 1 {
		t.Fatalf("Insights len = %d", len(decoded.Insights))
	}
	ins := decoded.Insights[0]
	if ins.ID != "ins-1" {
		t.Errorf("Insight.ID = %q", ins.ID)
	}
	if ins.Severity != "high" {
		t.Errorf("Insight.Severity = %q", ins.Severity)
	}
	if ins.AffectedCount != 1500 {
		t.Errorf("Insight.AffectedCount = %d", ins.AffectedCount)
	}
	if ins.RiskScore != 0.85 {
		t.Errorf("Insight.RiskScore = %f", ins.RiskScore)
	}
	if len(ins.SourceSteps) != 3 {
		t.Errorf("Insight.SourceSteps len = %d", len(ins.SourceSteps))
	}

	// Recommendations
	if len(decoded.Recommendations) != 1 {
		t.Fatalf("Recommendations len = %d", len(decoded.Recommendations))
	}
	rec := decoded.Recommendations[0]
	if rec.ID != "rec-1" {
		t.Errorf("Recommendation.ID = %q", rec.ID)
	}
	if rec.Priority != 1 {
		t.Errorf("Recommendation.Priority = %d", rec.Priority)
	}
	if rec.ExpectedImpact.Metric != "d1_retention" {
		t.Errorf("Impact.Metric = %q", rec.ExpectedImpact.Metric)
	}
	if len(rec.RelatedInsightIDs) != 1 {
		t.Errorf("RelatedInsightIDs len = %d", len(rec.RelatedInsightIDs))
	}

	// Summary
	if decoded.Summary.TotalInsights != 1 {
		t.Errorf("Summary.TotalInsights = %d", decoded.Summary.TotalInsights)
	}
	if decoded.Summary.QueriesExecuted != 50 {
		t.Errorf("Summary.QueriesExecuted = %d", decoded.Summary.QueriesExecuted)
	}
}

func TestDiscoveryResult_WithLogs(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	result := DiscoveryResult{
		ID: "disc-logs",
		ExplorationLog: []ExplorationStep{
			{
				Step:         1,
				Timestamp:    now,
				Action:       "query",
				Thinking:     "checking user counts",
				QueryPurpose: "count users",
				Query:        "SELECT COUNT(*) FROM users",
				RowCount:     1,
				ExecutionMs:  45,
			},
		},
		AnalysisLog: []AnalysisStep{
			{
				AreaID:          "churn",
				AreaName:        "Churn Analysis",
				RunAt:           now,
				RelevantQueries: 5,
				TokensIn:        1000,
				TokensOut:       500,
				DurationMs:      3000,
				InsightCount:    2,
			},
		},
		ValidationLog: []ValidationLogEntry{
			{
				InsightID:     "ins-1",
				AnalysisArea:  "churn",
				ClaimedCount:  1500,
				VerifiedCount: 1480,
				Status:        "verified",
				Reasoning:     "count matches within tolerance",
				Query:         "SELECT COUNT(DISTINCT user_id) FROM churned",
				ValidatedAt:   now,
			},
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}

	var decoded DiscoveryResult
	json.Unmarshal(data, &decoded)

	if len(decoded.ExplorationLog) != 1 {
		t.Fatalf("ExplorationLog len = %d", len(decoded.ExplorationLog))
	}
	if decoded.ExplorationLog[0].Step != 1 {
		t.Errorf("ExplorationLog[0].Step = %d", decoded.ExplorationLog[0].Step)
	}
	if decoded.ExplorationLog[0].ExecutionMs != 45 {
		t.Errorf("ExplorationLog[0].ExecutionMs = %d", decoded.ExplorationLog[0].ExecutionMs)
	}

	if len(decoded.AnalysisLog) != 1 {
		t.Fatalf("AnalysisLog len = %d", len(decoded.AnalysisLog))
	}
	if decoded.AnalysisLog[0].TokensIn != 1000 {
		t.Errorf("AnalysisLog[0].TokensIn = %d", decoded.AnalysisLog[0].TokensIn)
	}

	if len(decoded.ValidationLog) != 1 {
		t.Fatalf("ValidationLog len = %d", len(decoded.ValidationLog))
	}
	if decoded.ValidationLog[0].Status != "verified" {
		t.Errorf("ValidationLog[0].Status = %q", decoded.ValidationLog[0].Status)
	}
}

func TestDiscoveryResult_OmitEmptyLogs(t *testing.T) {
	result := DiscoveryResult{
		ID:        "disc-no-logs",
		ProjectID: "proj-1",
	}

	data, _ := json.Marshal(result)
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)

	// Logs should be omitted when empty
	if _, ok := raw["exploration_log"]; ok {
		t.Error("nil exploration_log should be omitted")
	}
	if _, ok := raw["analysis_log"]; ok {
		t.Error("nil analysis_log should be omitted")
	}
	if _, ok := raw["validation_log"]; ok {
		t.Error("nil validation_log should be omitted")
	}
}

func TestInsightValidation_JSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	insight := Insight{
		ID:       "ins-v",
		Name:     "Validated insight",
		Severity: "medium",
		Validation: &InsightValidation{
			Status:        "verified",
			VerifiedCount: 100,
			OriginalCount: 105,
			Reasoning:     "counts match within tolerance",
			ValidatedAt:   now,
		},
	}

	data, _ := json.Marshal(insight)
	var decoded Insight
	json.Unmarshal(data, &decoded)

	if decoded.Validation == nil {
		t.Fatal("Validation should not be nil")
	}
	if decoded.Validation.Status != "verified" {
		t.Errorf("Validation.Status = %q", decoded.Validation.Status)
	}
	if decoded.Validation.VerifiedCount != 100 {
		t.Errorf("Validation.VerifiedCount = %d", decoded.Validation.VerifiedCount)
	}
}

func TestInsight_OmitEmptyValidation(t *testing.T) {
	insight := Insight{
		ID:       "ins-no-val",
		Name:     "No validation",
		Severity: "low",
	}

	data, _ := json.Marshal(insight)
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)

	if _, ok := raw["validation"]; ok {
		t.Error("nil validation should be omitted")
	}
}

// --- DiscoveryRun ---

func TestDiscoveryRun_JSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	completed := now.Add(10 * time.Minute)
	original := DiscoveryRun{
		ID:          "run-1",
		ProjectID:   "proj-1",
		Status:      "completed",
		Phase:       "done",
		PhaseDetail: "Completed successfully",
		Progress:    100,
		StartedAt:   now,
		UpdatedAt:   now,
		CompletedAt: &completed,
		Steps: []RunStep{
			{
				Phase:     "exploration",
				StepNum:   1,
				Timestamp: now,
				Type:      "query",
				Message:   "Executing schema query",
				Query:     "SELECT * FROM INFORMATION_SCHEMA.TABLES",
				RowCount:  10,
				QueryTimeMs: 150,
			},
			{
				Phase:           "analysis",
				StepNum:         2,
				Timestamp:       now,
				Type:            "insight",
				Message:         "Found churn pattern",
				InsightName:     "D1 Churn",
				InsightSeverity: "high",
				DurationMs:      3000,
			},
		},
		TotalQueries:      50,
		SuccessfulQueries: 48,
		FailedQueries:     2,
		InsightsFound:     5,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var decoded DiscoveryRun
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID = %q", decoded.ID)
	}
	if decoded.ProjectID != "proj-1" {
		t.Errorf("ProjectID = %q", decoded.ProjectID)
	}
	if decoded.Status != "completed" {
		t.Errorf("Status = %q", decoded.Status)
	}
	if decoded.Phase != "done" {
		t.Errorf("Phase = %q", decoded.Phase)
	}
	if decoded.Progress != 100 {
		t.Errorf("Progress = %d", decoded.Progress)
	}
	if decoded.CompletedAt == nil {
		t.Fatal("CompletedAt should not be nil")
	}
	if decoded.TotalQueries != 50 {
		t.Errorf("TotalQueries = %d", decoded.TotalQueries)
	}
	if decoded.SuccessfulQueries != 48 {
		t.Errorf("SuccessfulQueries = %d", decoded.SuccessfulQueries)
	}
	if decoded.FailedQueries != 2 {
		t.Errorf("FailedQueries = %d", decoded.FailedQueries)
	}
	if decoded.InsightsFound != 5 {
		t.Errorf("InsightsFound = %d", decoded.InsightsFound)
	}

	if len(decoded.Steps) != 2 {
		t.Fatalf("Steps len = %d", len(decoded.Steps))
	}
	if decoded.Steps[0].Query != "SELECT * FROM INFORMATION_SCHEMA.TABLES" {
		t.Errorf("Steps[0].Query = %q", decoded.Steps[0].Query)
	}
	if decoded.Steps[1].InsightName != "D1 Churn" {
		t.Errorf("Steps[1].InsightName = %q", decoded.Steps[1].InsightName)
	}
}

func TestDiscoveryRun_OmitEmptyCompletedAt(t *testing.T) {
	run := DiscoveryRun{
		ID:     "run-pending",
		Status: "pending",
	}

	data, _ := json.Marshal(run)
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)

	if _, ok := raw["completed_at"]; ok {
		t.Error("nil completed_at should be omitted")
	}
}

func TestRunStep_OmitEmpty(t *testing.T) {
	step := RunStep{
		Phase:   "exploration",
		Type:    "thinking",
		Message: "Planning next query",
	}

	data, _ := json.Marshal(step)
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)

	// Fields with omitempty should be absent when zero
	for _, field := range []string{"query", "query_result", "llm_thinking", "llm_query", "insight_name", "insight_severity", "error"} {
		if _, ok := raw[field]; ok {
			t.Errorf("%q should be omitted when empty", field)
		}
	}
}

// --- Feedback ---

func TestFeedback_JSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	original := Feedback{
		ID:          "fb-1",
		ProjectID:   "proj-1",
		DiscoveryID: "disc-1",
		TargetType:  "insight",
		TargetID:    "ins-1",
		Rating:      "like",
		Comment:     "Very useful insight",
		CreatedAt:   now,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}

	var decoded Feedback
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID = %q", decoded.ID)
	}
	if decoded.ProjectID != original.ProjectID {
		t.Errorf("ProjectID = %q", decoded.ProjectID)
	}
	if decoded.DiscoveryID != original.DiscoveryID {
		t.Errorf("DiscoveryID = %q", decoded.DiscoveryID)
	}
	if decoded.TargetType != "insight" {
		t.Errorf("TargetType = %q", decoded.TargetType)
	}
	if decoded.TargetID != "ins-1" {
		t.Errorf("TargetID = %q", decoded.TargetID)
	}
	if decoded.Rating != "like" {
		t.Errorf("Rating = %q", decoded.Rating)
	}
	if decoded.Comment != "Very useful insight" {
		t.Errorf("Comment = %q", decoded.Comment)
	}
}

func TestFeedback_OmitEmptyComment(t *testing.T) {
	fb := Feedback{
		ID:         "fb-2",
		TargetType: "recommendation",
		TargetID:   "rec-1",
		Rating:     "dislike",
	}

	data, _ := json.Marshal(fb)
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)

	if _, ok := raw["comment"]; ok {
		t.Error("empty comment should be omitted")
	}
}

// --- Pricing ---

func TestPricing_JSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	original := Pricing{
		ID: "pricing-1",
		LLM: map[string]map[string]TokenPrice{
			"claude": {
				"claude-sonnet-4": {InputPerMillion: 3.0, OutputPerMillion: 15.0},
				"claude-opus-4":   {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			},
			"openai": {
				"gpt-4o": {InputPerMillion: 2.5, OutputPerMillion: 10.0},
			},
		},
		Warehouse: map[string]WarehousePrice{
			"bigquery": {
				CostModel:           "per_byte_scanned",
				CostPerTBScannedUSD: 6.25,
			},
		},
		UpdatedAt: now,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}

	var decoded Pricing
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID = %q", decoded.ID)
	}

	// Check LLM pricing
	claude, ok := decoded.LLM["claude"]
	if !ok {
		t.Fatal("missing claude LLM pricing")
	}
	sonnet, ok := claude["claude-sonnet-4"]
	if !ok {
		t.Fatal("missing claude-sonnet-4 pricing")
	}
	if sonnet.InputPerMillion != 3.0 {
		t.Errorf("sonnet InputPerMillion = %f", sonnet.InputPerMillion)
	}
	if sonnet.OutputPerMillion != 15.0 {
		t.Errorf("sonnet OutputPerMillion = %f", sonnet.OutputPerMillion)
	}

	// Check warehouse pricing
	bq, ok := decoded.Warehouse["bigquery"]
	if !ok {
		t.Fatal("missing bigquery warehouse pricing")
	}
	if bq.CostModel != "per_byte_scanned" {
		t.Errorf("CostModel = %q", bq.CostModel)
	}
	if bq.CostPerTBScannedUSD != 6.25 {
		t.Errorf("CostPerTBScannedUSD = %f", bq.CostPerTBScannedUSD)
	}
}

func TestCostEstimate_JSONRoundTrip(t *testing.T) {
	original := CostEstimate{
		LLM: LLMCostEstimate{
			Provider:              "claude",
			Model:                 "claude-sonnet-4",
			EstimatedInputTokens:  50000,
			EstimatedOutputTokens: 20000,
			CostUSD:               0.45,
		},
		Warehouse: WarehouseCostEstimate{
			Provider:              "bigquery",
			EstimatedQueries:      50,
			EstimatedBytesScanned: 1073741824, // 1 GB
			CostUSD:               0.006,
		},
		TotalUSD: 0.456,
		Breakdown: CostBreakdown{
			Exploration:     0.15,
			Analysis:        0.20,
			Validation:      0.05,
			Recommendations: 0.056,
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}

	var decoded CostEstimate
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded.LLM.Provider != "claude" {
		t.Errorf("LLM.Provider = %q", decoded.LLM.Provider)
	}
	if decoded.LLM.EstimatedInputTokens != 50000 {
		t.Errorf("LLM.EstimatedInputTokens = %d", decoded.LLM.EstimatedInputTokens)
	}
	if decoded.Warehouse.Provider != "bigquery" {
		t.Errorf("Warehouse.Provider = %q", decoded.Warehouse.Provider)
	}
	if decoded.Warehouse.EstimatedBytesScanned != 1073741824 {
		t.Errorf("Warehouse.EstimatedBytesScanned = %d", decoded.Warehouse.EstimatedBytesScanned)
	}
	if decoded.TotalUSD != 0.456 {
		t.Errorf("TotalUSD = %f", decoded.TotalUSD)
	}
	if decoded.Breakdown.Exploration != 0.15 {
		t.Errorf("Breakdown.Exploration = %f", decoded.Breakdown.Exploration)
	}
}

func TestCostEstimate_FieldTags(t *testing.T) {
	estimate := CostEstimate{
		LLM:       LLMCostEstimate{Provider: "claude"},
		Warehouse: WarehouseCostEstimate{Provider: "bigquery"},
	}
	data, _ := json.Marshal(estimate)
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)

	expectedFields := []string{"llm", "warehouse", "total_cost_usd", "breakdown"}
	for _, f := range expectedFields {
		if _, ok := raw[f]; !ok {
			t.Errorf("missing JSON field %q in CostEstimate", f)
		}
	}
}

// --- WarehouseConfig ---

func TestWarehouseConfig_JSONRoundTrip(t *testing.T) {
	wh := WarehouseConfig{
		Provider:    "redshift",
		Datasets:    []string{"analytics"},
		FilterField: "app_id",
		FilterValue: "com.example.app",
		Config: map[string]string{
			"workgroup": "default",
			"database":  "analytics",
			"region":    "us-east-1",
		},
	}

	data, _ := json.Marshal(wh)
	var decoded WarehouseConfig
	json.Unmarshal(data, &decoded)

	if decoded.Provider != "redshift" {
		t.Errorf("Provider = %q", decoded.Provider)
	}
	if decoded.FilterField != "app_id" {
		t.Errorf("FilterField = %q", decoded.FilterField)
	}
	if decoded.Config["workgroup"] != "default" {
		t.Errorf("Config[workgroup] = %q", decoded.Config["workgroup"])
	}
}

// --- LLMConfig ---

func TestLLMConfig_JSONRoundTrip(t *testing.T) {
	llm := LLMConfig{
		Provider: "openai",
		Model:    "gpt-4o",
		Config: map[string]string{
			"api_key": "sk-test",
		},
	}

	data, _ := json.Marshal(llm)
	var decoded LLMConfig
	json.Unmarshal(data, &decoded)

	if decoded.Provider != "openai" {
		t.Errorf("Provider = %q", decoded.Provider)
	}
	if decoded.Model != "gpt-4o" {
		t.Errorf("Model = %q", decoded.Model)
	}
	if decoded.Config["api_key"] != "sk-test" {
		t.Errorf("Config[api_key] = %q", decoded.Config["api_key"])
	}
}
