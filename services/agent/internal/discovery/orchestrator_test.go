package discovery

import (
	"context"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/domainpack"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

func TestBuildFilterClause(t *testing.T) {
	tests := []struct {
		name        string
		filterField string
		filterValue string
		want        string
	}{
		{"with filter", "app_id", "test-123", "WHERE app_id = 'test-123'"},
		{"empty field", "", "test-123", ""},
		{"empty value", "app_id", "", ""},
		{"no filter", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &Orchestrator{filterField: tt.filterField, filterValue: tt.filterValue}
			got := o.buildFilterClause()
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildFilterContext(t *testing.T) {
	o := &Orchestrator{filterField: "app_id", filterValue: "abc"}
	ctx := o.buildFilterContext()
	if ctx == "" {
		t.Error("should return context when filter is set")
	}

	o2 := &Orchestrator{}
	if o2.buildFilterContext() != "" {
		t.Error("should return empty when no filter")
	}
}

func TestBuildFilterRule(t *testing.T) {
	o := &Orchestrator{filterField: "app_id", filterValue: "abc"}
	rule := o.buildFilterRule()
	if rule == "" {
		t.Error("should return rule when filter is set")
	}

	o2 := &Orchestrator{}
	rule2 := o2.buildFilterRule()
	if rule2 == "" {
		t.Error("should return no-filter-required message")
	}
}

func TestBuildAnalysisAreasDescription(t *testing.T) {
	o := &Orchestrator{}
	areas := []domainpack.AnalysisArea{
		{ID: "churn", Name: "Churn Risks", Description: "Players leaving"},
		{ID: "levels", Name: "Level Difficulty", Description: "Hard levels"},
	}

	desc := o.buildAnalysisAreasDescription(areas)
	if desc == "" {
		t.Error("should produce description")
	}
	if !contains(desc, "Churn Risks") || !contains(desc, "Level Difficulty") {
		t.Error("should contain area names")
	}
}

func TestBuildPreviousContext(t *testing.T) {
	o := &Orchestrator{}

	// No context
	if o.buildPreviousContext(nil, nil, nil, nil) != "" {
		t.Error("nil context should return empty")
	}

	// Empty context
	ctx := models.NewProjectContext("test")
	if o.buildPreviousContext(ctx, nil, nil, nil) != "" {
		t.Error("empty context should return empty")
	}

	// Context with discoveries
	ctx.TotalDiscoveries = 5
	ctx.AddNote("schema", "sessions table has user_id", 0.9)
	result := o.buildPreviousContext(ctx, nil, nil, nil)
	if result == "" {
		t.Error("should return context when discoveries exist")
	}

	// Context with previous insights
	prevInsights := []models.InsightSummary{
		{Name: "High Churn at Level 45", AnalysisArea: "churn", Severity: "critical", AffectedCount: 500, Date: "2026-03-10"},
	}
	result = o.buildPreviousContext(ctx, prevInsights, nil, nil)
	if !contains(result, "High Churn at Level 45") {
		t.Error("should include previous insights")
	}
	if !contains(result, "Do NOT repeat") {
		t.Error("should include dedup instruction")
	}

	// Context with feedback
	feedback := []models.FeedbackSummary{
		{InsightName: "Bad Insight", Rating: "dislike", Comment: "not actionable"},
		{InsightName: "Good Insight", Rating: "like"},
	}
	result = o.buildPreviousContext(ctx, nil, nil, feedback)
	if !contains(result, "Bad Insight") || !contains(result, "not actionable") {
		t.Error("should include disliked feedback with comment")
	}
	if !contains(result, "Good Insight") {
		t.Error("should include liked feedback")
	}

	// Context with previous recommendations
	prevRecs := []models.RecommendationSummary{
		{Title: "Send Extra Lives", Category: "churn", Priority: 1},
	}
	result = o.buildPreviousContext(ctx, nil, prevRecs, nil)
	if !contains(result, "Send Extra Lives") {
		t.Error("should include previous recommendations")
	}
}

func TestSimplifySchemas(t *testing.T) {
	o := &Orchestrator{}
	schemas := map[string]models.TableSchema{
		"sessions": {
			TableName: "sessions",
			RowCount:  1000,
			Columns: []models.ColumnInfo{
				{Name: "user_id", Type: "STRING", Category: "primary_key"},
				{Name: "duration", Type: "INT64", Category: "metric"},
			},
			Metrics:    []string{"duration"},
			Dimensions: []string{},
		},
	}

	simplified := o.simplifySchemas(schemas)

	if _, ok := simplified["sessions"]; !ok {
		t.Fatal("should contain sessions table")
	}
	table := simplified["sessions"].(map[string]interface{})
	if table["row_count"].(int64) != 1000 {
		t.Error("row_count should be 1000")
	}
	cols := table["columns"].([]map[string]string)
	if len(cols) != 2 {
		t.Errorf("columns = %d, want 2", len(cols))
	}
}

func TestFilterQueriesByKeywords(t *testing.T) {
	o := &Orchestrator{}
	steps := []models.ExplorationStep{
		{Query: "SELECT * FROM sessions WHERE retention > 0", Thinking: "check retention", QueryPurpose: "retention analysis"},
		{Query: "SELECT * FROM levels WHERE quit_rate > 0.5", Thinking: "check level difficulty"},
		{Query: "", Thinking: "no query here"}, // should be filtered out
		{Query: "SELECT revenue FROM purchases", Thinking: "revenue check"},
	}

	// Filter for churn/retention keywords
	filtered := o.filterQueriesByKeywords(steps, []string{"retention", "churn", "cohort"})
	if len(filtered) != 1 {
		t.Errorf("filtered = %d, want 1", len(filtered))
	}

	// Filter for level keywords
	filtered = o.filterQueriesByKeywords(steps, []string{"level", "quit", "difficulty"})
	if len(filtered) != 1 {
		t.Errorf("filtered = %d, want 1", len(filtered))
	}

	// Filter for revenue keywords
	filtered = o.filterQueriesByKeywords(steps, []string{"revenue", "purchase"})
	if len(filtered) != 1 {
		t.Errorf("filtered = %d, want 1", len(filtered))
	}
}

func TestCleanJSONResponse(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "json code block",
			input: "Here:\n```json\n{\"key\": \"value\"}\n```",
			want:  `{"key": "value"}`,
		},
		{
			name:  "generic code block",
			input: "```\n{\"key\": \"value\"}\n```",
			want:  `{"key": "value"}`,
		},
		{
			name:  "raw json with prefix",
			input: "Result: {\"key\": \"value\"}",
			want:  `{"key": "value"}`,
		},
		{
			name:  "already clean json",
			input: `{"key": "value"}`,
			want:  `{"key": "value"}`,
		},
		{
			name:  "array json",
			input: "Result: [{\"a\":1}]",
			want:  `[{"a":1}]`,
		},
		{
			name:  "no json",
			input: "just plain text",
			want:  "just plain text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanJSONResponse(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- parseInsights ---

func TestParseInsights_ValidJSON(t *testing.T) {
	o := &Orchestrator{}

	response := `{
		"insights": [
			{
				"name": "High Churn at Level 45",
				"description": "Players dropping off at level 45",
				"severity": "critical",
				"affected_count": 2847,
				"risk_score": 0.85,
				"confidence": 0.92,
				"source_steps": [1, 3, 5]
			},
			{
				"id": "custom-id",
				"name": "Revenue Drop in Week 3",
				"description": "Revenue declining for week-3 cohort",
				"severity": "high",
				"affected_count": 1200,
				"risk_score": 0.7,
				"confidence": 0.88
			}
		]
	}`

	insights, err := o.parseInsights(response, "churn")
	if err != nil {
		t.Fatalf("parseInsights error: %v", err)
	}

	if len(insights) != 2 {
		t.Fatalf("insights = %d, want 2", len(insights))
	}

	// First insight: auto-generated ID
	if insights[0].ID != "churn-1" {
		t.Errorf("insights[0].ID = %q, want churn-1", insights[0].ID)
	}
	if insights[0].AnalysisArea != "churn" {
		t.Errorf("insights[0].AnalysisArea = %q, want churn", insights[0].AnalysisArea)
	}
	if insights[0].Name != "High Churn at Level 45" {
		t.Errorf("insights[0].Name = %q", insights[0].Name)
	}
	if insights[0].AffectedCount != 2847 {
		t.Errorf("insights[0].AffectedCount = %d, want 2847", insights[0].AffectedCount)
	}
	if insights[0].DiscoveredAt.IsZero() {
		t.Error("insights[0].DiscoveredAt should be set")
	}

	// Second insight: custom ID preserved
	if insights[1].ID != "custom-id" {
		t.Errorf("insights[1].ID = %q, want custom-id", insights[1].ID)
	}
	if insights[1].AnalysisArea != "churn" {
		t.Errorf("insights[1].AnalysisArea = %q, want churn", insights[1].AnalysisArea)
	}
}

func TestParseInsights_JSONInCodeBlock(t *testing.T) {
	o := &Orchestrator{}

	response := "Here are the insights:\n```json\n{\"insights\": [{\"name\": \"Test\", \"severity\": \"low\"}]}\n```\nDone."

	insights, err := o.parseInsights(response, "engagement")
	if err != nil {
		t.Fatalf("parseInsights error: %v", err)
	}
	if len(insights) != 1 {
		t.Fatalf("insights = %d, want 1", len(insights))
	}
	if insights[0].Name != "Test" {
		t.Errorf("Name = %q, want Test", insights[0].Name)
	}
	if insights[0].AnalysisArea != "engagement" {
		t.Errorf("AnalysisArea = %q, want engagement", insights[0].AnalysisArea)
	}
}

func TestParseInsights_MalformedJSON(t *testing.T) {
	o := &Orchestrator{}

	response := "This is not valid JSON at all, just some text about churn patterns."

	_, err := o.parseInsights(response, "churn")
	if err == nil {
		t.Error("parseInsights should return error for malformed JSON")
	}
}

func TestParseInsights_EmptyArray(t *testing.T) {
	o := &Orchestrator{}

	response := `{"insights": []}`

	insights, err := o.parseInsights(response, "churn")
	if err != nil {
		t.Fatalf("parseInsights error: %v", err)
	}
	if len(insights) != 0 {
		t.Errorf("insights = %d, want 0", len(insights))
	}
}

func TestParseInsights_WithSourceSteps(t *testing.T) {
	o := &Orchestrator{}

	response := `{
		"insights": [{
			"name": "Retention Drop",
			"severity": "high",
			"affected_count": 500,
			"source_steps": [1, 4, 7],
			"indicators": ["Session drop", "Revenue decline"]
		}]
	}`

	insights, err := o.parseInsights(response, "retention")
	if err != nil {
		t.Fatalf("parseInsights error: %v", err)
	}
	if len(insights[0].SourceSteps) != 3 {
		t.Errorf("SourceSteps = %d, want 3", len(insights[0].SourceSteps))
	}
	if len(insights[0].Indicators) != 2 {
		t.Errorf("Indicators = %d, want 2", len(insights[0].Indicators))
	}
}

// --- generateRecommendations parse ---

func TestGenerateRecommendations_NoInsights(t *testing.T) {
	o := &Orchestrator{}

	// With no insights, should return empty recommendations without calling LLM
	recs, step := o.generateRecommendations(context.Background(), "template", nil, "base", "dataset")

	if len(recs) != 0 {
		t.Errorf("recs = %d, want 0 for nil insights", len(recs))
	}
	if step == nil {
		t.Fatal("step should not be nil")
	}
	if step.InsightCount != 0 {
		t.Errorf("InsightCount = %d, want 0", step.InsightCount)
	}
}

func TestGenerateRecommendations_EmptyInsights(t *testing.T) {
	o := &Orchestrator{}

	recs, step := o.generateRecommendations(context.Background(), "template", []models.Insight{}, "base", "dataset")

	if len(recs) != 0 {
		t.Errorf("recs = %d, want 0 for empty insights", len(recs))
	}
	if step == nil {
		t.Fatal("step should not be nil")
	}
}

// --- cleanJSONResponse additional cases ---

func TestCleanJSONResponse_WhitespacePrefix(t *testing.T) {
	got := cleanJSONResponse("   \n\n  {\"key\": \"value\"}")
	if got != "{\"key\": \"value\"}" {
		t.Errorf("got %q, want '{\"key\": \"value\"}'", got)
	}
}

func TestCleanJSONResponse_JSONArray(t *testing.T) {
	got := cleanJSONResponse("[{\"a\":1},{\"b\":2}]")
	if got != "[{\"a\":1},{\"b\":2}]" {
		t.Errorf("got %q", got)
	}
}

// --- resolvePrompts: base context override ---

func TestResolvePrompts_ProjectOverridesBaseContext(t *testing.T) {
	o := &Orchestrator{
		projectPrompts: &models.ProjectPrompts{
			BaseContext:   "custom base context",
			AnalysisAreas: map[string]models.AnalysisAreaConfig{},
		},
	}

	dpPrompts := domainpack.PromptTemplates{
		BaseContext: "default base context",
	}

	prompts, _ := o.resolvePrompts(dpPrompts, nil)

	if prompts.BaseContext != "custom base context" {
		t.Errorf("BaseContext = %q, want custom", prompts.BaseContext)
	}
}

// --- buildPreviousContext: disliked feedback without comment ---

func TestBuildPreviousContext_DislikedNoComment(t *testing.T) {
	o := &Orchestrator{}
	ctx := models.NewProjectContext("test")
	ctx.TotalDiscoveries = 1

	feedback := []models.FeedbackSummary{
		{InsightName: "Irrelevant Insight", Rating: "dislike"},
	}

	result := o.buildPreviousContext(ctx, nil, nil, feedback)
	if !contains(result, "Irrelevant Insight") {
		t.Error("should include disliked insight name")
	}
	if !contains(result, "marked not useful") {
		t.Error("should show 'marked not useful' when no comment")
	}
}

// --- buildPreviousContext: notes with varying relevance ---

func TestBuildPreviousContext_NotesRelevanceFilter(t *testing.T) {
	o := &Orchestrator{}
	ctx := models.NewProjectContext("test")
	ctx.TotalDiscoveries = 1

	// Add notes with various relevance levels
	ctx.AddNote("schema", "low relevance note", 0.2)
	ctx.AddNote("schema", "medium relevance note", 0.5)
	ctx.AddNote("schema", "high relevance note", 0.9)

	result := o.buildPreviousContext(ctx, nil, nil, nil)

	// Only notes with relevance >= 0.5 should appear in Key Learnings
	if !contains(result, "high relevance note") {
		t.Error("should include high relevance note")
	}
	if !contains(result, "medium relevance note") {
		t.Error("should include medium relevance note (>= 0.5)")
	}
	if contains(result, "low relevance note") {
		t.Error("should NOT include low relevance note (< 0.5)")
	}
}

// --- filterQueriesByKeywords: case insensitive ---

func TestFilterQueriesByKeywords_CaseInsensitive(t *testing.T) {
	o := &Orchestrator{}
	steps := []models.ExplorationStep{
		{Query: "SELECT * FROM SESSIONS WHERE RETENTION > 0", Thinking: "Check RETENTION"},
	}

	filtered := o.filterQueriesByKeywords(steps, []string{"retention"})
	if len(filtered) != 1 {
		t.Errorf("filtered = %d, want 1 (should be case insensitive)", len(filtered))
	}
}

func TestFilterQueriesByKeywords_NoMatch(t *testing.T) {
	o := &Orchestrator{}
	steps := []models.ExplorationStep{
		{Query: "SELECT * FROM sessions", Thinking: "general query"},
	}

	filtered := o.filterQueriesByKeywords(steps, []string{"churn", "retention"})
	if len(filtered) != 0 {
		t.Errorf("filtered = %d, want 0", len(filtered))
	}
}

func TestFilterQueriesByKeywords_EmptySteps(t *testing.T) {
	o := &Orchestrator{}
	filtered := o.filterQueriesByKeywords(nil, []string{"churn"})
	if len(filtered) != 0 {
		t.Errorf("filtered = %d, want 0", len(filtered))
	}
}

func TestFilterQueriesByKeywords_EmptyKeywords(t *testing.T) {
	o := &Orchestrator{}
	steps := []models.ExplorationStep{
		{Query: "SELECT * FROM sessions"},
	}
	filtered := o.filterQueriesByKeywords(steps, nil)
	if len(filtered) != 0 {
		t.Errorf("filtered = %d, want 0 (no keywords match)", len(filtered))
	}
}

// --- inferColumnCategory ---

func TestInferColumnCategory(t *testing.T) {
	tests := []struct {
		name     string
		colName  string
		colType  string
		wantCat  string
	}{
		{"user_id is primary_key", "user_id", "STRING", "primary_key"},
		{"player_id is primary_key", "player_id", "STRING", "primary_key"},
		{"session_id is primary_key", "session_id", "STRING", "primary_key"},
		{"event_id is primary_key", "event_id", "STRING", "primary_key"},
		{"id is primary_key", "id", "STRING", "primary_key"},
		{"created_at is time", "created_at", "STRING", "time"},
		{"updated_at is time", "updated_at", "STRING", "time"},
		{"timestamp is time", "timestamp", "STRING", "time"},
		{"start_time is time", "start_time", "STRING", "time"},
		{"end_time is time", "end_time", "STRING", "time"},
		{"date is time", "date", "STRING", "time"},
		{"TIMESTAMP type is time", "event_time", "TIMESTAMP", "time"},
		{"DATE type is time", "event_date", "DATE", "time"},
		{"DATETIME type is time", "logged_at", "DATETIME", "time"},
		{"INT64 is metric", "duration", "INT64", "metric"},
		{"FLOAT64 is metric", "score", "FLOAT64", "metric"},
		{"NUMERIC is metric", "amount", "NUMERIC", "metric"},
		{"BIGNUMERIC is metric", "total", "BIGNUMERIC", "metric"},
		{"INTEGER is metric", "count", "INTEGER", "metric"},
		{"FLOAT is metric", "rate", "FLOAT", "metric"},
		{"STRING is dimension", "country", "STRING", "dimension"},
		{"BOOL is dimension", "is_premium", "BOOL", "dimension"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferColumnCategory(tt.colName, tt.colType)
			if got != tt.wantCat {
				t.Errorf("inferColumnCategory(%q, %q) = %q, want %q", tt.colName, tt.colType, got, tt.wantCat)
			}
		})
	}
}

func TestCategorizeColumn(t *testing.T) {
	tests := []struct {
		name     string
		col      models.ColumnInfo
		wantKey  bool
		wantMet  bool
		wantDim  bool
	}{
		{
			name:    "primary key",
			col:     models.ColumnInfo{Name: "user_id", Category: "primary_key"},
			wantKey: true,
		},
		{
			name:    "metric",
			col:     models.ColumnInfo{Name: "duration", Category: "metric"},
			wantMet: true,
		},
		{
			name:    "dimension",
			col:     models.ColumnInfo{Name: "country", Category: "dimension"},
			wantDim: true,
		},
		{
			name:    "time",
			col:     models.ColumnInfo{Name: "created_at", Category: "time"},
			wantDim: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema := &models.TableSchema{
				KeyColumns: make([]string, 0),
				Metrics:    make([]string, 0),
				Dimensions: make([]string, 0),
			}
			categorizeColumn(&tt.col, schema)

			if tt.wantKey && len(schema.KeyColumns) != 1 {
				t.Errorf("KeyColumns = %d, want 1", len(schema.KeyColumns))
			}
			if tt.wantMet && len(schema.Metrics) != 1 {
				t.Errorf("Metrics = %d, want 1", len(schema.Metrics))
			}
			if tt.wantDim && len(schema.Dimensions) != 1 {
				t.Errorf("Dimensions = %d, want 1", len(schema.Dimensions))
			}
		})
	}
}

func TestNewSchemaDiscovery(t *testing.T) {
	sd := NewSchemaDiscovery(SchemaDiscoveryOptions{
		ProjectID: "proj-123",
		Datasets:  []string{"events", "analytics"},
		Filter:    "WHERE app_id = 'test'",
	})

	if sd.projectID != "proj-123" {
		t.Errorf("projectID = %q, want proj-123", sd.projectID)
	}
	if len(sd.datasets) != 2 {
		t.Errorf("datasets = %d, want 2", len(sd.datasets))
	}
	if sd.filter != "WHERE app_id = 'test'" {
		t.Errorf("filter = %q", sd.filter)
	}
}

// --- simplifySchemas: additional cases ---

func TestSimplifySchemas_Empty(t *testing.T) {
	o := &Orchestrator{}
	simplified := o.simplifySchemas(map[string]models.TableSchema{})

	if len(simplified) != 0 {
		t.Errorf("simplified = %d, want 0", len(simplified))
	}
}

func TestSimplifySchemas_MultipleTablesWithAllFields(t *testing.T) {
	o := &Orchestrator{}
	schemas := map[string]models.TableSchema{
		"events.sessions": {
			TableName: "events.sessions",
			RowCount:  10000,
			Columns: []models.ColumnInfo{
				{Name: "user_id", Type: "STRING", Category: "primary_key"},
			},
			Metrics:    []string{"duration"},
			Dimensions: []string{"country"},
		},
		"events.users": {
			TableName: "events.users",
			RowCount:  5000,
			Columns:   []models.ColumnInfo{},
		},
	}

	simplified := o.simplifySchemas(schemas)
	if len(simplified) != 2 {
		t.Errorf("simplified = %d, want 2", len(simplified))
	}

	sessions := simplified["events.sessions"].(map[string]interface{})
	if sessions["row_count"].(int64) != 10000 {
		t.Error("row_count should be 10000")
	}
}

// --- NewOrchestrator helper (no DB) ---

func TestGenerateRecommendations_ParseResponse(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	provider.DefaultResponse.Content = `{
		"recommendations": [
			{
				"id": "r-1",
				"title": "Send Extra Lives",
				"category": "churn",
				"description": "Send lives to players stuck at level 45",
				"priority": 1,
				"target_segment": "stuck_players",
				"segment_size": 2847,
				"expected_impact": {
					"metric": "retention",
					"estimated_improvement": "15-20%",
					"reasoning": "Based on similar interventions"
				},
				"actions": ["Configure push notification", "Set up reward trigger"],
				"related_insight_ids": ["churn-1"],
				"confidence": 0.85
			}
		]
	}`

	client, _ := ai.New(provider, "mock-model")
	o := &Orchestrator{aiClient: client}

	insights := []models.Insight{
		{ID: "churn-1", Name: "High Churn", AnalysisArea: "churn", AffectedCount: 2847},
	}

	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}} {{INSIGHTS_SUMMARY}} {{DISCOVERY_DATE}}", insights, "", "dataset")

	if len(recs) != 1 {
		t.Fatalf("recs = %d, want 1", len(recs))
	}
	if recs[0].Title != "Send Extra Lives" {
		t.Errorf("Title = %q", recs[0].Title)
	}
	if recs[0].Priority != 1 {
		t.Errorf("Priority = %d, want 1", recs[0].Priority)
	}
	if len(recs[0].RelatedInsightIDs) != 1 || recs[0].RelatedInsightIDs[0] != "churn-1" {
		t.Errorf("RelatedInsightIDs = %v", recs[0].RelatedInsightIDs)
	}
	if step == nil {
		t.Fatal("step should not be nil")
	}
	if step.InsightCount != 1 {
		t.Errorf("InsightCount = %d, want 1", step.InsightCount)
	}
	if step.Response == "" {
		t.Error("step.Response should be captured")
	}
	if step.Error != "" {
		t.Errorf("step.Error = %q, should be empty", step.Error)
	}
}

func TestGenerateRecommendations_LLMError(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	provider.Error = context.DeadlineExceeded

	client, _ := ai.New(provider, "mock-model")
	o := &Orchestrator{aiClient: client}

	insights := []models.Insight{
		{ID: "i-1", Name: "Test"},
	}

	recs, step := o.generateRecommendations(context.Background(), "template", insights, "", "dataset")

	if len(recs) != 0 {
		t.Errorf("recs = %d, want 0 on error", len(recs))
	}
	if step.Error == "" {
		t.Error("step.Error should be set on LLM error")
	}
}

func TestGenerateRecommendations_ParseError(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	provider.DefaultResponse.Content = "This is not valid JSON at all"

	client, _ := ai.New(provider, "mock-model")
	o := &Orchestrator{aiClient: client}

	insights := []models.Insight{
		{ID: "i-1", Name: "Test"},
	}

	recs, step := o.generateRecommendations(context.Background(), "template", insights, "", "dataset")

	if len(recs) != 0 {
		t.Errorf("recs = %d, want 0 on parse error", len(recs))
	}
	if step.Error == "" {
		t.Error("step.Error should be set on parse error")
	}
}

func TestExecutorAdapter(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	executor := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 1,
	})

	adapter := &executorAdapter{executor: executor}

	data, err := adapter.Execute(context.Background(), "SELECT 1", "test")
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if data == nil {
		t.Fatal("data should not be nil")
	}
	if len(data) == 0 {
		t.Error("data should have rows")
	}
}

func TestExecutorAdapter_Error(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	wh.QueryError = context.DeadlineExceeded

	executor := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 0,
	})

	adapter := &executorAdapter{executor: executor}

	_, err := adapter.Execute(context.Background(), "SELECT 1", "test")
	if err == nil {
		t.Error("should return error when executor fails")
	}
}

func TestBuildFilterClause_AllCombinations(t *testing.T) {
	tests := []struct {
		field string
		value string
		want  string
	}{
		{"app_id", "game-123", "WHERE app_id = 'game-123'"},
		{"tenant_id", "t-abc", "WHERE tenant_id = 't-abc'"},
		{"", "test", ""},
		{"app_id", "", ""},
		{"", "", ""},
	}

	for _, tt := range tests {
		o := &Orchestrator{filterField: tt.field, filterValue: tt.value}
		got := o.buildFilterClause()
		if got != tt.want {
			t.Errorf("buildFilterClause(%q, %q) = %q, want %q", tt.field, tt.value, got, tt.want)
		}
	}
}
