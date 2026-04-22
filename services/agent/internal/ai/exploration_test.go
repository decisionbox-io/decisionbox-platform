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
		name       string
		input      string
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

// TestParseAction_NoSilentCompleteFromProse is the regression test for the
// bug that terminated Qwen3 / DeepSeek-R1 exploration after 2-18 steps.
// The old inferActionFromText substring-matched "done" / "complete" /
// "finished" anywhere in the response and treated that as a completion
// signal — any reasoning-model prose containing those words ended the run
// silently. The new parser requires an explicit JSON object with a known
// action key and rejects everything else.
func TestParseAction_NoSilentCompleteFromProse(t *testing.T) {
	engine := &ExplorationEngine{}

	tests := []string{
		"I have completed the analysis of all the data.",
		"I'm done exploring retention.",
		"Finished analyzing session patterns — let me try another angle next.",
		"The previous query completed successfully, so now I will...",
		"Let me think about this more carefully",
		"", // empty response
	}

	for _, tt := range tests {
		t.Run(tt, func(t *testing.T) {
			action, err := engine.parseAction(tt)
			if err == nil {
				t.Fatalf("parseAction(%q) should return error (no JSON action), got action=%+v", tt, action)
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
	cb := func(stepNum int, action, thinking, query string, rowCount int, queryTimeMs int64, queryFixed bool, errMsg string) {
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
	engine.onStep(1, "query_data", "thinking", "SELECT 1", 5, 100, false, "")
	if !called {
		t.Error("onStep callback was not invoked")
	}
}

func TestOnStepCallback_Parameters(t *testing.T) {
	var gotStep int
	var gotAction, gotThinking, gotQuery, gotErr string
	var gotRows int
	var gotTimeMs int64
	var gotFixed bool

	cb := func(stepNum int, action, thinking, query string, rowCount int, queryTimeMs int64, queryFixed bool, errMsg string) {
		gotStep = stepNum
		gotAction = action
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

	engine.onStep(3, "query_data", "checking retention", "SELECT COUNT(*) FROM sessions", 42, 250, true, "some error")

	if gotStep != 3 {
		t.Errorf("stepNum = %d, want 3", gotStep)
	}
	if gotAction != "query_data" {
		t.Errorf("action = %q, want query_data", gotAction)
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
		OnStep: func(stepNum int, action, thinking, query string, rowCount int, queryTimeMs int64, queryFixed bool, errMsg string) {
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

	// Text with completion-like prose but no JSON must NOT silently complete.
	_, err := engine.parseAction("I have completed the analysis of all the data.")
	if err == nil {
		t.Fatal("parseAction should return error when response has no action JSON")
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

// -----------------------------------------------------------------------------
// Comprehensive coverage for the exploration parser and control flow.
//
// These tests exist because a regression in any of: extractJSON,
// parseAction, runStepWithRetry, or the min-step floor in Explore can silently
// terminate exploration on reasoning models (Qwen3, DeepSeek-R1, GPT-OSS,
// ...). The original bug manifested as runs that completed in 2-18 steps
// instead of 100.
// -----------------------------------------------------------------------------

func TestExtractJSON_Comprehensive(t *testing.T) {
	engine := &ExplorationEngine{}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "fenced json block with language tag",
			input: "Here is the action:\n```json\n{\"query\": \"SELECT 1\"}\n```\n",
			want:  `{"query": "SELECT 1"}`,
		},
		{
			name:  "fenced block without language tag",
			input: "```\n{\"query\": \"SELECT 1\"}\n```",
			want:  `{"query": "SELECT 1"}`,
		},
		{
			name:  "uppercase JSON language tag",
			input: "```JSON\n{\"query\": \"SELECT 2\"}\n```",
			want:  `{"query": "SELECT 2"}`,
		},
		{
			name:  "reasoning preamble before action",
			input: `{"plan": "first explore users", "step": 1} then {"query": "SELECT id FROM users"}`,
			want:  `{"query": "SELECT id FROM users"}`,
		},
		{
			name:  "thinking block before query",
			input: `{"thinking": "I should check retention"}{"query": "SELECT * FROM retention"}`,
			want:  `{"query": "SELECT * FROM retention"}`,
		},
		{
			name:  "multiple preambles, only last has done",
			input: `{"summary_so_far": "explored"} {"observation": "complete"} {"done": true, "summary": "finished"}`,
			want:  `{"done": true, "summary": "finished"}`,
		},
		{
			name:  "brace inside SQL string literal does not break parse",
			input: `{"query": "SELECT JSON_EXTRACT(col, '$.foo') FROM t WHERE x = '}'"}`,
			want:  `{"query": "SELECT JSON_EXTRACT(col, '$.foo') FROM t WHERE x = '}'"}`,
		},
		{
			name:  "escaped quote in string literal",
			input: `{"query": "SELECT \"my col\" FROM t"}`,
			want:  `{"query": "SELECT \"my col\" FROM t"}`,
		},
		{
			name:  "nested json",
			input: `{"query": "SELECT 1", "meta": {"purpose": "test"}}`,
			want:  `{"query": "SELECT 1", "meta": {"purpose": "test"}}`,
		},
		{
			name:  "prose only, no json",
			input: "I have done a complete analysis and finished everything.",
			want:  "",
		},
		{
			name:  "object with no action key falls back to last balanced object",
			input: `{"thinking": "still exploring"}`,
			want:  `{"thinking": "still exploring"}`,
		},
		{
			name:  "fenced block preferred over raw json when both present",
			input: "Some raw: {\"thinking\": \"plan\"} then\n```json\n{\"query\": \"SELECT 1\"}\n```",
			want:  `{"query": "SELECT 1"}`,
		},
		{
			name:  "unbalanced braces returns empty",
			input: `{"query": "SELECT 1"`,
			want:  "",
		},
		{
			// Regression (Copilot PR #176 review): a fenced preamble that has
			// no action key must NOT hijack the parse when the real action
			// JSON lives outside the fences.
			name: "fenced preamble without action key does not shadow raw action JSON",
			input: "Thinking out loud:\n```json\n{\"plan\": \"step 1 then 2\"}\n```\n" +
				"Now running the query:\n" +
				`{"thinking": "execute", "query": "SELECT 1"}`,
			want: `{"thinking": "execute", "query": "SELECT 1"}`,
		},
		{
			// Dual regression: fenced action JSON still wins when it is the
			// ONLY block carrying an action key.
			name:  "fenced action JSON beats raw preamble",
			input: `{"plan": "x"} then ` + "```json\n" + `{"query": "SELECT 2"}` + "\n```",
			want:  `{"query": "SELECT 2"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := engine.extractJSON(tt.input)
			if got != tt.want {
				t.Errorf("extractJSON()\n  got:  %q\n  want: %q", got, tt.want)
			}
		})
	}
}

func TestJSONHasActionKey(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{`{"query": "SELECT 1"}`, true},
		{`{"done": true}`, true},
		{`{"action": "complete"}`, true},
		{`{"thinking": "nothing"}`, false},
		{`{"summary": "stuff"}`, false},
		{`not json`, false},
		{`{"plan": "x", "step": 1}`, false},
		{`{"query": ""}`, true}, // key presence alone counts
	}
	for _, tc := range cases {
		if got := jsonHasActionKey(tc.in); got != tc.want {
			t.Errorf("jsonHasActionKey(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestFindBalancedJSONObjects(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "single object",
			in:   `prefix {"a": 1} suffix`,
			want: []string{`{"a": 1}`},
		},
		{
			name: "two sibling objects",
			in:   `{"a": 1} some text {"b": 2}`,
			want: []string{`{"a": 1}`, `{"b": 2}`},
		},
		{
			name: "nested object counts as one",
			in:   `{"a": {"b": 1}}`,
			want: []string{`{"a": {"b": 1}}`},
		},
		{
			name: "brace inside string does not count",
			in:   `{"q": "x } y"} {"b": 2}`,
			want: []string{`{"q": "x } y"}`, `{"b": 2}`},
		},
		{
			name: "escaped quote inside string",
			in:   `{"q": "a \" } b"}`,
			want: []string{`{"q": "a \" } b"}`},
		},
		{
			name: "unbalanced aborts scan",
			in:   `{"a": 1`,
			want: nil,
		},
		{
			name: "no objects at all",
			in:   "just prose",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := findBalancedJSONObjects(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d objects, want %d — got=%v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("object[%d]\n  got:  %q\n  want: %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestParseAction_StrictModernContract(t *testing.T) {
	engine := &ExplorationEngine{}

	type want struct {
		action  string
		query   string
		done    bool
		summary string
		errish  bool // true if we expect an error
	}
	tests := []struct {
		name  string
		input string
		want  want
	}{
		{
			name:  "query shape",
			input: `{"thinking": "check count", "query": "SELECT COUNT(*) FROM t"}`,
			want:  want{action: "query_data", query: "SELECT COUNT(*) FROM t"},
		},
		{
			name:  "done shape with summary",
			input: `{"done": true, "summary": "all areas covered"}`,
			want:  want{action: "complete", done: true, summary: "all areas covered"},
		},
		{
			name:  "legacy action=query_data with query",
			input: `{"action": "query_data", "query": "SELECT 1"}`,
			want:  want{action: "query_data", query: "SELECT 1"},
		},
		{
			name:  "legacy action=complete",
			input: `{"action": "complete", "reason": "done"}`,
			want:  want{action: "complete"},
		},
		{
			name:  "reasoning preamble then query — last wins",
			input: `{"plan": "step 1: count users"} {"thinking": "execute", "query": "SELECT 1"}`,
			want:  want{action: "query_data", query: "SELECT 1"},
		},
		{
			name:  "thinking-only json is rejected (NOT silent complete)",
			input: `{"thinking": "pondering next move"}`,
			want:  want{errish: true},
		},
		{
			name:  "empty json object is rejected",
			input: `{}`,
			want:  want{errish: true},
		},
		{
			name:  "malformed json is rejected",
			input: `{"query": "SELECT 1"`,
			want:  want{errish: true},
		},
		{
			name:  "plain prose with 'done' in it is rejected",
			input: "Great, I'm done with area 1, moving on to area 2.",
			want:  want{errish: true},
		},
		{
			name:  "plain prose with 'finished' is rejected",
			input: "The query finished successfully so I can continue.",
			want:  want{errish: true},
		},
		{
			name:  "action: 'done' with query key still queries (query takes precedence)",
			input: `{"query": "SELECT 1", "action": "something-weird"}`,
			want:  want{action: "query_data", query: "SELECT 1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, err := engine.parseAction(tt.input)
			if tt.want.errish {
				if err == nil {
					t.Fatalf("expected error, got action=%+v", action)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseAction error: %v", err)
			}
			if action.Action != tt.want.action {
				t.Errorf("action = %q, want %q", action.Action, tt.want.action)
			}
			if tt.want.query != "" && action.Query != tt.want.query {
				t.Errorf("query = %q, want %q", action.Query, tt.want.query)
			}
			if tt.want.done && !action.Done {
				t.Errorf("done = false, want true")
			}
			if tt.want.summary != "" && action.Summary != tt.want.summary {
				t.Errorf("summary = %q, want %q", action.Summary, tt.want.summary)
			}
		})
	}
}

// buildTestEngine constructs a minimal ExplorationEngine wired to a
// scripted mock LLM provider. Used by the retry and min-step tests.
func buildTestEngine(t *testing.T, opts ExplorationEngineOptions, queue []string) (*ExplorationEngine, *testutil.MockLLMProvider) {
	t.Helper()

	provider := testutil.NewMockLLMProvider()
	for _, content := range queue {
		provider.ResponseQueue = append(provider.ResponseQueue, &gollm.ChatResponse{
			Content:    content,
			Model:      "mock-model",
			StopReason: "end_turn",
			Usage:      gollm.Usage{InputTokens: 10, OutputTokens: 10},
		})
	}
	// Drain queue; when empty the provider falls back to this default.
	provider.DefaultResponse = &gollm.ChatResponse{
		Content:    `{"done": true, "summary": "fallback default"}`,
		Model:      "mock-model",
		StopReason: "end_turn",
		Usage:      gollm.Usage{InputTokens: 10, OutputTokens: 10},
	}

	client, err := New(provider, "mock-model")
	if err != nil {
		t.Fatalf("New(provider): %v", err)
	}
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	executor := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 1,
	})

	opts.Client = client
	opts.Executor = executor
	if opts.Dataset == "" {
		opts.Dataset = "test_dataset"
	}

	return NewExplorationEngine(opts), provider
}

func TestRunStepWithRetry_NudgesAndRecovers(t *testing.T) {
	// First response: unparseable prose (old bug would silently complete).
	// Second response: malformed JSON (also unparseable).
	// Third response: a valid query. Run should recover with 3 LLM calls.
	engine, provider := buildTestEngine(t, ExplorationEngineOptions{
		MaxSteps: 5,
	}, []string{
		"I think I'm done here", // prose, no JSON — would have triggered false complete
		`{"thinking": "almost"`, // malformed JSON
		`{"thinking": "run it", "query": "SELECT 1 FROM test_dataset.users"}`,
	})

	result, err := engine.Explore(context.Background(), ExplorationContext{
		ProjectID:     "proj-1",
		Dataset:       "test_dataset",
		InitialPrompt: "Explore the data.",
	})
	if err != nil {
		t.Fatalf("Explore error: %v", err)
	}

	// The third response runs a query at step 1. Then we hit the mock's
	// DefaultResponse (done:true) on the next LLM call and complete at step 2.
	if !result.Completed {
		t.Error("run should complete once queue is exhausted and default returns done")
	}
	if len(provider.Calls) < 3 {
		t.Errorf("provider got %d calls, expected at least 3 (one per retry + one to complete)", len(provider.Calls))
	}
	// Verify a nudge message actually went into the conversation.
	var sawNudge bool
	for _, call := range provider.Calls {
		for _, msg := range call.Request.Messages {
			if msg.Role == "user" && containsStr(msg.Content, "could not be parsed as an exploration action") {
				sawNudge = true
				break
			}
		}
	}
	if !sawNudge {
		t.Error("expected at least one reformat-nudge user message in the conversation")
	}
}

func TestRunStepWithRetry_AllAttemptsFail(t *testing.T) {
	// Queue enough unparseable responses to exhaust the retry budget for
	// step 1 (1 initial + maxParseRetries retries).
	queue := make([]string, 0, maxParseRetries+1)
	for i := 0; i <= maxParseRetries; i++ {
		queue = append(queue, "Just prose, no JSON here.")
	}

	engine, provider := buildTestEngine(t, ExplorationEngineOptions{MaxSteps: 5}, queue)

	result, err := engine.Explore(context.Background(), ExplorationContext{
		ProjectID:     "proj-1",
		Dataset:       "test_dataset",
		InitialPrompt: "Explore the data.",
	})
	if err == nil {
		t.Fatal("expected hard error when all retries fail to parse")
	}
	if result == nil {
		t.Fatal("result should be non-nil even on error")
	}
	if result.Completed {
		t.Error("result should NOT be marked completed on hard parse failure")
	}
	if len(provider.Calls) != maxParseRetries+1 {
		t.Errorf("expected exactly %d LLM calls (1 + %d retries), got %d", maxParseRetries+1, maxParseRetries, len(provider.Calls))
	}
}

func TestRunStepWithRetry_NoRetryOnFirstGoodResponse(t *testing.T) {
	engine, provider := buildTestEngine(t, ExplorationEngineOptions{MaxSteps: 1}, []string{
		`{"done": true, "summary": "one-shot"}`,
	})

	result, err := engine.Explore(context.Background(), ExplorationContext{
		ProjectID:     "proj-1",
		Dataset:       "test_dataset",
		InitialPrompt: "Explore the data.",
	})
	if err != nil {
		t.Fatalf("Explore error: %v", err)
	}
	if !result.Completed {
		t.Error("should be marked completed")
	}
	if len(provider.Calls) != 1 {
		t.Errorf("expected exactly 1 LLM call with no retries, got %d", len(provider.Calls))
	}
}

func TestMinSteps_RejectsPrematureCompletion(t *testing.T) {
	// Mock returns done on every call. With MinSteps=5 the first 4 "done"
	// signals must be rejected; completion accepted on step 5.
	engine, provider := buildTestEngine(t, ExplorationEngineOptions{
		MaxSteps: 20,
		MinSteps: 5,
	}, nil) // no queue — falls through to DefaultResponse (done:true)

	result, err := engine.Explore(context.Background(), ExplorationContext{
		ProjectID:     "proj-1",
		Dataset:       "test_dataset",
		InitialPrompt: "Explore the data.",
	})
	if err != nil {
		t.Fatalf("Explore error: %v", err)
	}
	if !result.Completed {
		t.Error("exploration should eventually complete at step >= MinSteps")
	}
	if result.TotalSteps != 5 {
		t.Errorf("TotalSteps = %d, want 5 (MinSteps boundary)", result.TotalSteps)
	}
	// Rejected-completion steps should have been recorded.
	var rejected int
	for _, s := range result.Steps {
		if s.Action == "complete_rejected" {
			rejected++
		}
	}
	if rejected != 4 {
		t.Errorf("expected 4 rejected completions (steps 1..4), got %d", rejected)
	}

	// Each rejection should have added a nudge message.
	var nudges int
	for _, call := range provider.Calls {
		for _, msg := range call.Request.Messages {
			if msg.Role == "user" && containsStr(msg.Content, "minimum") {
				nudges++
				break
			}
		}
	}
	if nudges == 0 {
		t.Error("expected at least one min-steps nudge in the conversation history")
	}
}

// TestMinSteps_CallbackCarriesRejectedAction is the regression for the
// Copilot PR #176 review: the StepCallback must receive action="complete_rejected"
// (not "" or "query_data") when the engine rejects an early done signal,
// so downstream StatusReporter can record the step with the right Type
// and skip the query counter.
func TestMinSteps_CallbackCarriesRejectedAction(t *testing.T) {
	type capturedStep struct {
		step   int
		action string
		query  string
		errMsg string
	}
	var captured []capturedStep

	opts := ExplorationEngineOptions{
		MaxSteps: 10,
		MinSteps: 3,
		OnStep: func(stepNum int, action, thinking, query string, rowCount int, queryTimeMs int64, queryFixed bool, errMsg string) {
			captured = append(captured, capturedStep{
				step:   stepNum,
				action: action,
				query:  query,
				errMsg: errMsg,
			})
		},
	}
	engine, _ := buildTestEngine(t, opts, nil) // DefaultResponse is done:true every call

	result, err := engine.Explore(context.Background(), ExplorationContext{
		ProjectID:     "proj-1",
		Dataset:       "test_dataset",
		InitialPrompt: "Explore the data.",
	})
	if err != nil {
		t.Fatalf("Explore error: %v", err)
	}
	if !result.Completed {
		t.Fatal("run should complete at step 3 (MinSteps boundary)")
	}

	// Expect two complete_rejected callbacks (steps 1 and 2); nothing for
	// step 3 (accepted completion breaks out before the query-step branch
	// emits a callback, which is current behaviour — asserted here so any
	// future change is deliberate).
	var rejectedCallbacks int
	for _, c := range captured {
		if c.action == "complete_rejected" {
			rejectedCallbacks++
			if c.query != "" {
				t.Errorf("rejected-completion callback for step %d carried a non-empty query %q — should be empty", c.step, c.query)
			}
			if !containsStr(c.errMsg, "rejected premature completion") {
				t.Errorf("rejected-completion callback errMsg = %q, want containing 'rejected premature completion'", c.errMsg)
			}
		}
		if c.action == "" {
			t.Errorf("callback for step %d got empty action — would hide intent from StatusReporter", c.step)
		}
	}
	if rejectedCallbacks != 2 {
		t.Errorf("expected exactly 2 complete_rejected callbacks (steps 1 and 2), got %d", rejectedCallbacks)
	}
}

// TestStatusReporter_CompleteRejected_SkipsQueryCounter is colocated here
// with the engine tests to document the StepCallback → StatusReporter
// contract (see services/agent/internal/discovery/status.go). A rejected
// completion step must not inflate the query counter. The StatusReporter
// itself has more direct tests; this one asserts the contract from the
// engine side: when the engine emits action="complete_rejected" the
// integration with StatusReporter is type-compatible.
func TestStatusReporter_CallbackShapeCompiles(t *testing.T) {
	// Compile-time assertion that a function matching the new callback
	// signature can be passed through OnStep. If the signature drifts this
	// file stops compiling.
	_ = ExplorationEngineOptions{
		OnStep: func(stepNum int, action, thinking, query string, rowCount int, queryTimeMs int64, queryFixed bool, errMsg string) {
			_ = stepNum
			_ = action
			_ = thinking
			_ = query
			_ = rowCount
			_ = queryTimeMs
			_ = queryFixed
			_ = errMsg
		},
	}
}

func TestMinSteps_ZeroDisablesFloor(t *testing.T) {
	engine, _ := buildTestEngine(t, ExplorationEngineOptions{
		MaxSteps: 20,
		MinSteps: 0,
	}, nil)

	result, err := engine.Explore(context.Background(), ExplorationContext{
		ProjectID:     "proj-1",
		Dataset:       "test_dataset",
		InitialPrompt: "Explore the data.",
	})
	if err != nil {
		t.Fatalf("Explore error: %v", err)
	}
	if !result.Completed {
		t.Error("should complete")
	}
	if result.TotalSteps != 1 {
		t.Errorf("TotalSteps = %d, want 1 (done accepted immediately when MinSteps=0)", result.TotalSteps)
	}
}

func TestMinSteps_Boundary_EqualToStep(t *testing.T) {
	// Exactly at MinSteps, completion must be ACCEPTED (>=, not strict <).
	// Three queries then done on the 4th turn, with MinSteps=4.
	engine, _ := buildTestEngine(t, ExplorationEngineOptions{
		MaxSteps: 10,
		MinSteps: 4,
	}, []string{
		`{"query": "SELECT 1 FROM test_dataset.users"}`,
		`{"query": "SELECT 2 FROM test_dataset.users"}`,
		`{"query": "SELECT 3 FROM test_dataset.users"}`,
		`{"done": true, "summary": "covered"}`, // at step 4, >= MinSteps, accepted
	})

	result, err := engine.Explore(context.Background(), ExplorationContext{
		ProjectID:     "proj-1",
		Dataset:       "test_dataset",
		InitialPrompt: "Explore the data.",
	})
	if err != nil {
		t.Fatalf("Explore error: %v", err)
	}
	if !result.Completed {
		t.Error("should complete on boundary")
	}
	if result.TotalSteps != 4 {
		t.Errorf("TotalSteps = %d, want 4 (done accepted at step == MinSteps)", result.TotalSteps)
	}
	for _, s := range result.Steps {
		if s.Action == "complete_rejected" {
			t.Errorf("should not have rejected any step at boundary, saw rejected at step %d", s.Step)
		}
	}
}

func TestMinSteps_CappedToMaxSteps(t *testing.T) {
	engine := NewExplorationEngine(ExplorationEngineOptions{
		MaxSteps: 5,
		MinSteps: 100, // asked for more than MaxSteps
	})
	if engine.minSteps != 5 {
		t.Errorf("minSteps = %d, want 5 (capped to MaxSteps)", engine.minSteps)
	}
	// MaxSteps must not be bumped up to match MinSteps.
	if engine.maxSteps != 5 {
		t.Errorf("maxSteps = %d, want 5 (unchanged)", engine.maxSteps)
	}
}

func TestMinSteps_NegativeClampedToZero(t *testing.T) {
	engine := NewExplorationEngine(ExplorationEngineOptions{
		MaxSteps: 10,
		MinSteps: -5,
	})
	if engine.minSteps != 0 {
		t.Errorf("minSteps = %d, want 0 (negative clamped)", engine.minSteps)
	}
}

// TestQwen3StyleResponse_DoesNotTerminateEarly simulates the actual
// failure mode from the bug report: a reasoning-style response mixing a
// plan/thinking JSON preamble with the real action. The old parser picked
// the preamble (no action key) and, finding no query/done, silently
// completed. With the new parser the action JSON wins and exploration
// continues.
func TestQwen3StyleResponse_DoesNotTerminateEarly(t *testing.T) {
	qwenStyleResponse := `Let me think about this.
{"plan": "first count users, then look at retention", "notes": "the previous query succeeded"}
Now I'll run the query:
` + "```json\n" + `{"thinking": "count active users", "query": "SELECT COUNT(*) FROM test_dataset.users"}` + "\n```"

	engine, provider := buildTestEngine(t, ExplorationEngineOptions{
		MaxSteps: 3,
	}, []string{
		qwenStyleResponse,
		qwenStyleResponse,
		`{"done": true, "summary": "got enough"}`,
	})

	result, err := engine.Explore(context.Background(), ExplorationContext{
		ProjectID:     "proj-qwen",
		Dataset:       "test_dataset",
		InitialPrompt: "Explore the data.",
	})
	if err != nil {
		t.Fatalf("Explore error: %v", err)
	}
	if result.TotalSteps < 3 {
		t.Errorf("TotalSteps = %d, want 3 — reasoning-style responses should not short-circuit", result.TotalSteps)
	}
	// Confirm the first two steps actually ran queries (not silent completes).
	for i := 0; i < 2 && i < len(result.Steps); i++ {
		if result.Steps[i].Action != "query_data" {
			t.Errorf("step %d action = %q, want query_data", i+1, result.Steps[i].Action)
		}
		if result.Steps[i].Query == "" {
			t.Errorf("step %d query should be non-empty", i+1)
		}
	}
	if len(provider.Calls) != 3 {
		t.Errorf("provider calls = %d, want 3", len(provider.Calls))
	}
}
