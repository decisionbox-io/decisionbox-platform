package ai

import (
	"context"
	"fmt"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

func TestParseActionQueryFormat(t *testing.T) {
	engine := &ExplorationEngine{}

	tests := []struct {
		name     string
		input    string
		wantAction string
		wantQuery  bool
	}{
		{
			name:       "simple query",
			input:      `{"thinking": "check retention", "query": "SELECT * FROM test"}`,
			wantAction: "query_data",
			wantQuery:  true,
		},
		{
			name:       "done format",
			input:      `{"done": true, "summary": "exploration complete"}`,
			wantAction: "complete",
			wantQuery:  false,
		},
		{
			name:       "legacy action format",
			input:      `{"action": "query_data", "thinking": "test", "query": "SELECT 1", "query_purpose": "test"}`,
			wantAction: "query_data",
			wantQuery:  true,
		},
		{
			name:       "json in code block",
			input:      "Some text\n```json\n{\"thinking\": \"test\", \"query\": \"SELECT 1\"}\n```\nMore text",
			wantAction: "query_data",
			wantQuery:  true,
		},
		{
			name:       "empty action defaults to complete",
			input:      `{"thinking": "nothing more to explore"}`,
			wantAction: "complete",
			wantQuery:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, err := engine.parseAction(tt.input)
			if err != nil {
				t.Fatalf("parseAction error: %v", err)
			}
			if action.Action != tt.wantAction {
				t.Errorf("action = %q, want %q", action.Action, tt.wantAction)
			}
			if tt.wantQuery && action.Query == "" {
				t.Error("expected query to be present")
			}
		})
	}
}

func TestExtractJSON(t *testing.T) {
	engine := &ExplorationEngine{}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "json code block",
			input: "Here is the result:\n```json\n{\"key\": \"value\"}\n```\nDone.",
			want:  `{"key": "value"}`,
		},
		{
			name:  "generic code block",
			input: "```\n{\"key\": \"value\"}\n```",
			want:  `{"key": "value"}`,
		},
		{
			name:  "raw json",
			input: `Some text {"key": "value"} more text`,
			want:  `{"key": "value"}`,
		},
		{
			name:  "nested braces",
			input: `{"outer": {"inner": "value"}}`,
			want:  `{"outer": {"inner": "value"}}`,
		},
		{
			name:  "no json",
			input: "Just plain text with no json",
			want:  "",
		},
		{
			name:  "non-json code block",
			input: "```\nSELECT * FROM test\n```",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := engine.extractJSON(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInferActionFromText(t *testing.T) {
	engine := &ExplorationEngine{}

	tests := []struct {
		name       string
		input      string
		wantAction string
	}{
		{"completion signal", "I have completed the analysis", "complete"},
		{"done signal", "I'm done exploring", "complete"},
		{"finished signal", "Finished with exploration", "complete"},
		{"sql query", "SELECT user_id FROM sessions WHERE app_id = 'test'", "query_data"},
		{"unknown text", "Let me think about this more carefully", "complete"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, err := engine.inferActionFromText(tt.input)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if action.Action != tt.wantAction {
				t.Errorf("action = %q, want %q", action.Action, tt.wantAction)
			}
		})
	}
}

func TestExplorationResultDefaults(t *testing.T) {
	result := &ExplorationResult{
		Completed: false,
	}

	if result.TotalSteps != 0 {
		t.Error("TotalSteps should default to 0")
	}
	if result.Completed {
		t.Error("Completed should default to false")
	}
}

func TestNewExplorationEngine_Defaults(t *testing.T) {
	engine := NewExplorationEngine(ExplorationEngineOptions{})
	if engine.maxSteps != 100 {
		t.Errorf("maxSteps = %d, want 100 (default)", engine.maxSteps)
	}
	if engine.onStep != nil {
		t.Error("onStep should be nil by default")
	}
}

func TestNewExplorationEngine_WithOnStep(t *testing.T) {
	called := false
	cb := func(stepNum int, thinking, query string, rowCount int, queryTimeMs int64, queryFixed bool, errMsg string) {
		called = true
	}

	engine := NewExplorationEngine(ExplorationEngineOptions{
		MaxSteps: 10,
		OnStep:   cb,
	})

	if engine.maxSteps != 10 {
		t.Errorf("maxSteps = %d, want 10", engine.maxSteps)
	}
	if engine.onStep == nil {
		t.Fatal("onStep should be set")
	}

	// Invoke the callback
	engine.onStep(1, "thinking", "SELECT 1", 5, 100, false, "")
	if !called {
		t.Error("onStep callback was not invoked")
	}
}

func TestOnStepCallback_Parameters(t *testing.T) {
	var gotStep int
	var gotThinking, gotQuery, gotErr string
	var gotRows int
	var gotTimeMs int64
	var gotFixed bool

	cb := func(stepNum int, thinking, query string, rowCount int, queryTimeMs int64, queryFixed bool, errMsg string) {
		gotStep = stepNum
		gotThinking = thinking
		gotQuery = query
		gotRows = rowCount
		gotTimeMs = queryTimeMs
		gotFixed = queryFixed
		gotErr = errMsg
	}

	engine := NewExplorationEngine(ExplorationEngineOptions{
		MaxSteps: 5,
		OnStep:   cb,
	})

	engine.onStep(3, "checking retention", "SELECT COUNT(*) FROM sessions", 42, 250, true, "some error")

	if gotStep != 3 {
		t.Errorf("stepNum = %d, want 3", gotStep)
	}
	if gotThinking != "checking retention" {
		t.Errorf("thinking = %q", gotThinking)
	}
	if gotQuery != "SELECT COUNT(*) FROM sessions" {
		t.Errorf("query = %q", gotQuery)
	}
	if gotRows != 42 {
		t.Errorf("rowCount = %d, want 42", gotRows)
	}
	if gotTimeMs != 250 {
		t.Errorf("queryTimeMs = %d, want 250", gotTimeMs)
	}
	if !gotFixed {
		t.Error("queryFixed should be true")
	}
	if gotErr != "some error" {
		t.Errorf("errMsg = %q", gotErr)
	}
}

func TestExplorationContextFields(t *testing.T) {
	ctx := ExplorationContext{
		ProjectID:     "proj-123",
		Dataset:       "my_dataset",
		InitialPrompt: "Explore the data...",
	}

	if ctx.ProjectID != "proj-123" {
		t.Error("ProjectID not set")
	}
	if ctx.InitialPrompt == "" {
		t.Error("InitialPrompt should be set")
	}
}

func TestExploration_Explore_Completion(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	// LLM returns "done" immediately
	provider.DefaultResponse = &gollm.ChatResponse{
		Content:    `{"done": true, "summary": "exploration complete, found retention patterns"}`,
		Model:      "mock-model",
		StopReason: "end_turn",
		Usage:      gollm.Usage{InputTokens: 100, OutputTokens: 50},
	}

	client, _ := New(provider, "mock-model")
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	executor := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 1,
	})

	engine := NewExplorationEngine(ExplorationEngineOptions{
		Client:   client,
		Executor: executor,
		MaxSteps: 10,
		Dataset:  "test_dataset",
	})

	result, err := engine.Explore(context.Background(), ExplorationContext{
		ProjectID:      "proj-123",
		Dataset:        "test_dataset",
		InitialPrompt:  "Explore the data",
	})

	if err != nil {
		t.Fatalf("Explore error: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	if !result.Completed {
		t.Error("exploration should be completed")
	}
	if result.TotalSteps != 1 {
		t.Errorf("TotalSteps = %d, want 1", result.TotalSteps)
	}
	if result.CompletionMsg == "" {
		t.Error("CompletionMsg should be set")
	}
	if result.Duration == 0 {
		t.Error("Duration should be set")
	}
}

func TestExploration_Explore_MaxSteps(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	// LLM always returns a query, never completes
	provider.DefaultResponse = &gollm.ChatResponse{
		Content:    `{"thinking": "need more data", "query": "SELECT COUNT(*) FROM sessions"}`,
		Model:      "mock-model",
		StopReason: "end_turn",
		Usage:      gollm.Usage{InputTokens: 100, OutputTokens: 50},
	}

	client, _ := New(provider, "mock-model")
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	executor := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 1,
	})

	maxSteps := 3
	engine := NewExplorationEngine(ExplorationEngineOptions{
		Client:   client,
		Executor: executor,
		MaxSteps: maxSteps,
		Dataset:  "test_dataset",
	})

	result, err := engine.Explore(context.Background(), ExplorationContext{
		ProjectID:      "proj-123",
		Dataset:        "test_dataset",
		InitialPrompt:  "Explore the data",
	})

	if err != nil {
		t.Fatalf("Explore error: %v", err)
	}
	if result.Completed {
		t.Error("exploration should NOT be completed when max steps reached")
	}
	if result.TotalSteps != maxSteps {
		t.Errorf("TotalSteps = %d, want %d", result.TotalSteps, maxSteps)
	}
	if result.CompletionMsg == "" {
		t.Error("CompletionMsg should indicate max steps reached")
	}
}

func TestExploration_Explore_LLMError(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	provider.Error = fmt.Errorf("LLM service unavailable")

	client, _ := New(provider, "mock-model")
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	executor := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 1,
	})

	engine := NewExplorationEngine(ExplorationEngineOptions{
		Client:   client,
		Executor: executor,
		MaxSteps: 5,
		Dataset:  "test_dataset",
	})

	result, err := engine.Explore(context.Background(), ExplorationContext{
		ProjectID:      "proj-123",
		Dataset:        "test_dataset",
		InitialPrompt:  "Explore the data",
	})

	if err == nil {
		t.Fatal("Explore should return error when LLM fails")
	}
	if result == nil {
		t.Fatal("result should not be nil even on error")
	}
	if result.Completed {
		t.Error("exploration should NOT be completed on error")
	}
	if result.Error == nil {
		t.Error("result.Error should be set")
	}
}

func TestExploration_ExecuteAction_QueryData(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	executor := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 1,
	})

	engine := &ExplorationEngine{
		executor: executor,
	}

	action := &ExplorationAction{
		Action:       "query_data",
		Thinking:     "checking user count",
		QueryPurpose: "count users",
		Query:        "SELECT COUNT(*) FROM users",
	}

	step := &models.ExplorationStep{Step: 1}
	resultMsg := engine.executeAction(context.Background(), action, step)

	if resultMsg == "" {
		t.Error("result message should not be empty")
	}
	if step.Query != "SELECT COUNT(*) FROM users" {
		t.Errorf("step.Query = %q", step.Query)
	}
	if step.RowCount == 0 {
		t.Error("step.RowCount should be set from query result")
	}
}

func TestExploration_ExecuteAction_Complete(t *testing.T) {
	engine := &ExplorationEngine{}

	action := &ExplorationAction{
		Action: "complete",
		Reason: "All data explored",
	}

	step := &models.ExplorationStep{Step: 5}
	resultMsg := engine.executeAction(context.Background(), action, step)

	if resultMsg == "" {
		t.Error("result message should not be empty")
	}
	if resultMsg != "Exploration complete: All data explored" {
		t.Errorf("resultMsg = %q", resultMsg)
	}
}

func TestExploration_ExecuteAction_ExploreSchema(t *testing.T) {
	engine := &ExplorationEngine{}

	action := &ExplorationAction{
		Action: "explore_schema",
	}

	step := &models.ExplorationStep{Step: 1}
	resultMsg := engine.executeAction(context.Background(), action, step)

	if resultMsg == "" {
		t.Error("result message should not be empty for explore_schema")
	}
}

func TestExploration_ExecuteAction_AnalyzePattern(t *testing.T) {
	engine := &ExplorationEngine{}

	action := &ExplorationAction{
		Action:       "analyze_pattern",
		AnalysisType: "retention_drop",
	}

	step := &models.ExplorationStep{Step: 2}
	resultMsg := engine.executeAction(context.Background(), action, step)

	if resultMsg == "" {
		t.Error("result message should not be empty for analyze_pattern")
	}
	if !step.IsInsight {
		t.Error("step.IsInsight should be true for analyze_pattern")
	}
}

func TestExploration_ExecuteAction_Unknown(t *testing.T) {
	engine := &ExplorationEngine{}

	action := &ExplorationAction{
		Action: "unknown_action",
	}

	step := &models.ExplorationStep{Step: 1}
	resultMsg := engine.executeAction(context.Background(), action, step)

	if resultMsg != "Unknown action: unknown_action" {
		t.Errorf("resultMsg = %q, want 'Unknown action: unknown_action'", resultMsg)
	}
}

func TestExploration_FormatResults(t *testing.T) {
	engine := &ExplorationEngine{}

	data := []map[string]interface{}{
		{"user_id": "u1", "count": 10},
		{"user_id": "u2", "count": 20},
	}

	formatted := engine.formatResults(data)

	if formatted == "" {
		t.Error("formatted results should not be empty")
	}
	// Should be valid JSON
	if formatted[0] != '[' {
		t.Errorf("formatted results should start with '[', got %q", string(formatted[0]))
	}
}

func TestExploration_FormatResults_Empty(t *testing.T) {
	engine := &ExplorationEngine{}

	data := []map[string]interface{}{}
	formatted := engine.formatResults(data)

	if formatted == "" {
		t.Error("formatted results should not be empty even for empty data")
	}
	if formatted != "[]" {
		t.Errorf("formatted empty data = %q, want '[]'", formatted)
	}
}

func TestExploration_BuildInitialMessage(t *testing.T) {
	engine := &ExplorationEngine{maxSteps: 50}

	msg := engine.buildInitialMessage(ExplorationContext{
		ProjectID: "proj-123",
		Dataset:   "my_dataset",
	})

	if msg == "" {
		t.Error("initial message should not be empty")
	}
	if !containsStr(msg, "50") {
		t.Error("initial message should mention max steps")
	}
}

func TestExploration_Explore_WithOnStepCallback(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	provider.DefaultResponse = &gollm.ChatResponse{
		Content:    `{"done": true, "summary": "done"}`,
		Model:      "mock-model",
		StopReason: "end_turn",
		Usage:      gollm.Usage{InputTokens: 50, OutputTokens: 25},
	}

	client, _ := New(provider, "mock-model")
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	executor := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 1,
	})

	callbackCalled := false
	engine := NewExplorationEngine(ExplorationEngineOptions{
		Client:   client,
		Executor: executor,
		MaxSteps: 5,
		Dataset:  "test_dataset",
		OnStep: func(stepNum int, thinking, query string, rowCount int, queryTimeMs int64, queryFixed bool, errMsg string) {
			callbackCalled = true
		},
	})

	result, err := engine.Explore(context.Background(), ExplorationContext{
		ProjectID:      "proj-123",
		Dataset:        "test_dataset",
		InitialPrompt:  "Explore",
	})

	if err != nil {
		t.Fatalf("Explore error: %v", err)
	}
	if !result.Completed {
		t.Error("should be completed")
	}
	if !callbackCalled {
		t.Error("onStep callback should have been called")
	}
}

func TestExploration_Explore_QueryThenComplete(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	callCount := 0
	// First call returns a query, second call returns done
	origChat := provider.Chat
	_ = origChat
	provider.DefaultResponse = nil

	mockProvider := &sequentialMockProvider{
		responses: []*gollm.ChatResponse{
			{
				Content:    `{"thinking": "check user count", "query": "SELECT COUNT(*) FROM users"}`,
				Model:      "mock-model",
				StopReason: "end_turn",
				Usage:      gollm.Usage{InputTokens: 100, OutputTokens: 50},
			},
			{
				Content:    `{"done": true, "summary": "found 100 users"}`,
				Model:      "mock-model",
				StopReason: "end_turn",
				Usage:      gollm.Usage{InputTokens: 150, OutputTokens: 60},
			},
		},
		callCount: &callCount,
	}

	client, _ := New(mockProvider, "mock-model")
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	executor := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 1,
	})

	engine := NewExplorationEngine(ExplorationEngineOptions{
		Client:   client,
		Executor: executor,
		MaxSteps: 10,
		Dataset:  "test_dataset",
	})

	result, err := engine.Explore(context.Background(), ExplorationContext{
		ProjectID:      "proj-123",
		Dataset:        "test_dataset",
		InitialPrompt:  "Explore",
	})

	if err != nil {
		t.Fatalf("Explore error: %v", err)
	}
	if !result.Completed {
		t.Error("should be completed after query + done")
	}
	if result.TotalSteps != 2 {
		t.Errorf("TotalSteps = %d, want 2", result.TotalSteps)
	}
	if len(result.Steps) != 2 {
		t.Errorf("Steps = %d, want 2", len(result.Steps))
	}
	// First step should be a query action
	if result.Steps[0].Action != "query_data" {
		t.Errorf("Steps[0].Action = %q, want query_data", result.Steps[0].Action)
	}
	// Second step should be complete
	if result.Steps[1].Action != "complete" {
		t.Errorf("Steps[1].Action = %q, want complete", result.Steps[1].Action)
	}
}

// sequentialMockProvider returns responses in order.
type sequentialMockProvider struct {
	responses []*gollm.ChatResponse
	callCount *int
}

func (m *sequentialMockProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	idx := *m.callCount
	*m.callCount++
	if idx < len(m.responses) {
		return m.responses[idx], nil
	}
	// Default: return done
	return &gollm.ChatResponse{
		Content:    `{"done": true, "summary": "fallback done"}`,
		Model:      "mock-model",
		StopReason: "end_turn",
		Usage:      gollm.Usage{InputTokens: 10, OutputTokens: 5},
	}, nil
}

func (m *sequentialMockProvider) Validate(ctx context.Context) error {
	return nil
}

func TestExploration_ExecuteAction_QueryData_Error(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	wh.QueryError = fmt.Errorf("table not found")

	executor := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 0, // No retries
	})

	engine := &ExplorationEngine{
		executor: executor,
	}

	action := &ExplorationAction{
		Action: "query_data",
		Query:  "SELECT * FROM nonexistent",
	}

	step := &models.ExplorationStep{Step: 1}
	resultMsg := engine.executeAction(context.Background(), action, step)

	if resultMsg == "" {
		t.Error("result message should not be empty on error")
	}
	if step.Error == "" {
		t.Error("step.Error should be set on query failure")
	}
	if step.Fixed {
		t.Error("step.Fixed should be false when query failed")
	}
}

func TestExploration_ExecuteQuery_Success_WithMoreThan10Rows(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	// Create result with 15 rows
	rows := make([]map[string]interface{}, 15)
	for i := 0; i < 15; i++ {
		rows[i] = map[string]interface{}{"id": i}
	}
	wh.DefaultResult.Rows = rows

	executor := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 1,
	})

	engine := &ExplorationEngine{executor: executor}

	action := &ExplorationAction{
		Action:       "query_data",
		Query:        "SELECT id FROM users",
		QueryPurpose: "list users",
	}

	step := &models.ExplorationStep{Step: 1}
	resultMsg := engine.executeAction(context.Background(), action, step)

	if step.RowCount != 15 {
		t.Errorf("RowCount = %d, want 15", step.RowCount)
	}
	// The result message should indicate showing 10 of 15 rows
	if !containsStr(resultMsg, "Showing 10 of 15") {
		t.Errorf("result should show truncation message, got: %s", resultMsg[:200])
	}
}

func TestExploration_ParseAction_NoJSON(t *testing.T) {
	engine := &ExplorationEngine{}

	// Text with completion signal but no JSON
	action, err := engine.parseAction("I have completed the analysis of all the data.")
	if err != nil {
		t.Fatalf("parseAction error: %v", err)
	}
	if action.Action != "complete" {
		t.Errorf("action = %q, want complete", action.Action)
	}
}

// containsStr is a helper for string containment checks.
func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
