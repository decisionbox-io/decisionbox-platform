package ai

import (
	"context"
	"fmt"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

func TestExtractFixedSQL_CodeBlock(t *testing.T) {
	resp := &gollm.ChatResponse{
		Content: "Here's the fix:\n```sql\nSELECT * FROM `dataset.table` WHERE app_id = 'test'\n```\nThis should work.",
	}

	sql, err := extractFixedSQL(resp)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if sql != "SELECT * FROM `dataset.table` WHERE app_id = 'test'" {
		t.Errorf("sql = %q", sql)
	}
}

func TestExtractFixedSQL_GenericBlock(t *testing.T) {
	resp := &gollm.ChatResponse{
		Content: "```\nSELECT count(*) FROM `ds.t`\n```",
	}

	sql, err := extractFixedSQL(resp)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if sql != "SELECT count(*) FROM `ds.t`" {
		t.Errorf("sql = %q", sql)
	}
}

func TestExtractFixedSQL_RawSQL(t *testing.T) {
	resp := &gollm.ChatResponse{
		Content: "SELECT user_id FROM `ds.sessions` WHERE app_id = 'test'",
	}

	sql, err := extractFixedSQL(resp)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if sql == "" {
		t.Error("should extract raw SQL")
	}
}

func TestExtractFixedSQL_NotSQL(t *testing.T) {
	resp := &gollm.ChatResponse{
		Content: "I cannot fix this query because the table doesn't exist.",
	}

	_, err := extractFixedSQL(resp)
	if err == nil {
		t.Error("should return error for non-SQL response")
	}
}

func TestExtractFixedSQL_EmptyResponse(t *testing.T) {
	resp := &gollm.ChatResponse{Content: ""}
	_, err := extractFixedSQL(resp)
	if err == nil {
		t.Error("should return error for empty response")
	}

	_, err = extractFixedSQL(nil)
	if err == nil {
		t.Error("should return error for nil response")
	}
}

func TestExtractCodeBlock(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		language string
		want     string
	}{
		{
			name:     "sql block",
			text:     "```sql\nSELECT 1\n```",
			language: "sql",
			want:     "SELECT 1\n",
		},
		{
			name:     "generic block",
			text:     "```\nSELECT 1\n```",
			language: "",
			want:     "SELECT 1\n",
		},
		{
			name:     "no block",
			text:     "just text",
			language: "sql",
			want:     "",
		},
		{
			name:     "unclosed block",
			text:     "```sql\nSELECT 1",
			language: "sql",
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCodeBlock(tt.text, tt.language)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewSQLFixer(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	client, _ := New(provider, "test-model")

	fixer := NewSQLFixer(SQLFixerOptions{
		Client:       client,
		SQLFixPrompt: "Fix this {{ORIGINAL_SQL}} query. Error: {{ERROR_MESSAGE}}",
		Dataset:      "test_dataset",
		Filter:       "WHERE app_id = 'test'",
	})

	if fixer == nil {
		t.Fatal("fixer should not be nil")
	}
	if fixer.dataset != "test_dataset" {
		t.Errorf("dataset = %q", fixer.dataset)
	}
	if fixer.filter != "WHERE app_id = 'test'" {
		t.Errorf("filter = %q", fixer.filter)
	}
}

func TestSQLFixer_FixSQL_Success(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	provider.DefaultResponse = &gollm.ChatResponse{
		Content:    "```sql\nSELECT COUNT(*) FROM `test_dataset.sessions` WHERE app_id = 'test'\n```",
		Model:      "mock-model",
		StopReason: "end_turn",
		Usage:      gollm.Usage{InputTokens: 100, OutputTokens: 50},
	}

	client, _ := New(provider, "mock-model")

	fixer := NewSQLFixer(SQLFixerOptions{
		Client:       client,
		SQLFixPrompt: "Fix this {{ORIGINAL_SQL}} query. Error: {{ERROR_MESSAGE}}",
		Dataset:      "test_dataset",
		Filter:       "",
	})

	fixed, err := fixer.FixSQL(context.Background(), "SELECT BAD FROM sessions", "column BAD not found", 0)
	if err != nil {
		t.Fatalf("FixSQL error: %v", err)
	}
	if fixed == "" {
		t.Error("fixed query should not be empty")
	}
	if fixed != "SELECT COUNT(*) FROM `test_dataset.sessions` WHERE app_id = 'test'" {
		t.Errorf("fixed = %q", fixed)
	}
}

func TestSQLFixer_FixSQL_LLMError(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	provider.Error = fmt.Errorf("LLM unavailable")

	client, _ := New(provider, "mock-model")

	fixer := NewSQLFixer(SQLFixerOptions{
		Client:       client,
		SQLFixPrompt: "Fix query",
		Dataset:      "ds",
	})

	_, err := fixer.FixSQL(context.Background(), "SELECT 1", "error", 0)
	if err == nil {
		t.Error("should return error when LLM fails")
	}
}

func TestSQLFixer_FixSQL_NotSQLResponse(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	provider.DefaultResponse = &gollm.ChatResponse{
		Content:    "I cannot fix this query because the table doesn't exist.",
		Model:      "mock-model",
		StopReason: "end_turn",
		Usage:      gollm.Usage{InputTokens: 100, OutputTokens: 50},
	}

	client, _ := New(provider, "mock-model")

	fixer := NewSQLFixer(SQLFixerOptions{
		Client:       client,
		SQLFixPrompt: "Fix query",
		Dataset:      "ds",
	})

	_, err := fixer.FixSQL(context.Background(), "SELECT 1", "error", 0)
	if err == nil {
		t.Error("should return error when response is not SQL")
	}
}

func TestSQLFixer_SetSchemaContext(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	client, _ := New(provider, "mock-model")

	fixer := NewSQLFixer(SQLFixerOptions{
		Client:       client,
		SQLFixPrompt: "Fix {{SCHEMA_INFO}}",
	})

	fixer.SetSchemaContext(`{"sessions": {"columns": ["user_id"]}}`)

	if fixer.schemaCtx != `{"sessions": {"columns": ["user_id"]}}` {
		t.Errorf("schemaCtx = %q", fixer.schemaCtx)
	}
}

func TestSQLFixer_FixSQL_TemplateSubstitution(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	provider.DefaultResponse = &gollm.ChatResponse{
		Content:    "SELECT 1 FROM `ds.table`",
		Model:      "mock-model",
		StopReason: "end_turn",
		Usage:      gollm.Usage{InputTokens: 100, OutputTokens: 50},
	}

	client, _ := New(provider, "mock-model")

	fixer := NewSQLFixer(SQLFixerOptions{
		Client:       client,
		SQLFixPrompt: "Fix {{ORIGINAL_SQL}} error {{ERROR_MESSAGE}} dataset {{DATASET}} filter {{FILTER}} schema {{SCHEMA_INFO}}",
		Dataset:      "my_dataset",
		Filter:       "WHERE app_id = 'x'",
	})
	fixer.SetSchemaContext("schema_info_here")

	fixed, err := fixer.FixSQL(context.Background(), "BAD SQL", "syntax error", 0)
	if err != nil {
		t.Fatalf("FixSQL error: %v", err)
	}
	if fixed == "" {
		t.Error("should return fixed SQL")
	}

	// Verify the system prompt was properly substituted by checking the call was made
	if len(provider.Calls) != 1 {
		t.Fatalf("provider should be called once, got %d", len(provider.Calls))
	}
}
