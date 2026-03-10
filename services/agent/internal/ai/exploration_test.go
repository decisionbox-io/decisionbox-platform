package ai

import (
	"testing"
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
