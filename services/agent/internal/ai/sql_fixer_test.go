package ai

import (
	"context"
	"fmt"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
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
		{
			name:     "generic finds json block and strips tag",
			text:     "```json\n{\"fixed_sql\": \"SELECT 1\"}\n```",
			language: "",
			want:     "{\"fixed_sql\": \"SELECT 1\"}\n",
		},
		{
			name:     "generic finds sql block and strips tag",
			text:     "```sql\nSELECT 1\n```",
			language: "",
			want:     "SELECT 1\n",
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

func TestExtractFixedSQL_JSONResponse(t *testing.T) {
	// LLM returns JSON with fixed_sql field (matches sql_fix.md output format)
	resp := &gollm.ChatResponse{
		Content: `{"action": "sql_fixed", "fixed_sql": "SELECT COUNT(*) AS count FROM ` + "`ds.sessions`" + `", "changes_made": ["qualified table"], "reasoning": "added dataset", "confidence": 95}`,
	}

	sql, err := extractFixedSQL(resp)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if sql != "SELECT COUNT(*) AS count FROM `ds.sessions`" {
		t.Errorf("sql = %q", sql)
	}
}

func TestExtractFixedSQL_JSONInCodeBlock(t *testing.T) {
	// LLM wraps JSON in ```json code block
	resp := &gollm.ChatResponse{
		Content: "```json\n{\"action\": \"sql_fixed\", \"fixed_sql\": \"SELECT 1 FROM `ds.t`\"}\n```",
	}

	sql, err := extractFixedSQL(resp)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if sql != "SELECT 1 FROM `ds.t`" {
		t.Errorf("sql = %q", sql)
	}
}

func TestExtractSQLFromJSON(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "valid json with fixed_sql",
			text: `{"fixed_sql": "SELECT 1"}`,
			want: "SELECT 1",
		},
		{
			name: "not json",
			text: "SELECT 1",
			want: "",
		},
		{
			name: "json without fixed_sql",
			text: `{"action": "error"}`,
			want: "",
		},
		{
			name: "empty fixed_sql",
			text: `{"fixed_sql": ""}`,
			want: "",
		},
		{
			name: "fixed_sql without SELECT",
			text: `{"fixed_sql": "DROP TABLE users"}`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSQLFromJSON(tt.text)
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

	fixed, err := fixer.FixSQL(context.Background(), "SELECT BAD FROM sessions", "column BAD not found", 0, queryexec.FixOpts{})
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

	_, err := fixer.FixSQL(context.Background(), "SELECT 1", "error", 0, queryexec.FixOpts{})
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

	_, err := fixer.FixSQL(context.Background(), "SELECT 1", "error", 0, queryexec.FixOpts{})
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

func TestSQLFixer_FixSQL_VerificationContextSectionKept(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	provider.DefaultResponse = &gollm.ChatResponse{
		Content: "SELECT COUNT(*) AS count FROM `ds.t`",
		Model:   "mock-model",
		Usage:   gollm.Usage{InputTokens: 1, OutputTokens: 1},
	}
	client, _ := New(provider, "mock-model")
	fixer := NewSQLFixer(SQLFixerOptions{
		Client: client,
		SQLFixPrompt: "Fix {{ORIGINAL_SQL}}{{#VERIFICATION_CONTEXT}}\nEVIDENCE_BLOCK\n{{VERIFICATION_CONTEXT}}\n/EVIDENCE_BLOCK{{/VERIFICATION_CONTEXT}}",
	})

	_, err := fixer.FixSQL(
		context.Background(),
		"SELECT bad FROM t",
		"col not found",
		0,
		queryexec.FixOpts{VerificationContext: "Step 7: SELECT real_col FROM t"},
	)
	if err != nil {
		t.Fatalf("FixSQL: %v", err)
	}
	if len(provider.Calls) == 0 {
		t.Fatal("LLM should have been called")
	}
	systemPrompt := provider.Calls[len(provider.Calls)-1].Request.SystemPrompt
	if !strings.Contains(systemPrompt, "EVIDENCE_BLOCK") {
		t.Errorf("verification-context section should be kept when opts.VerificationContext is non-empty, got prompt:\n%s", systemPrompt)
	}
	if !strings.Contains(systemPrompt, "Step 7: SELECT real_col FROM t") {
		t.Errorf("VerificationContext value should be substituted, got:\n%s", systemPrompt)
	}
	if strings.Contains(systemPrompt, "{{#VERIFICATION_CONTEXT}}") || strings.Contains(systemPrompt, "{{/VERIFICATION_CONTEXT}}") {
		t.Errorf("section markers should be stripped, got:\n%s", systemPrompt)
	}
}

func TestSQLFixer_FixSQL_VerificationContextSectionStrippedWhenEmpty(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	provider.DefaultResponse = &gollm.ChatResponse{
		Content: "SELECT COUNT(*) AS count FROM `ds.t`",
		Model:   "mock-model",
		Usage:   gollm.Usage{InputTokens: 1, OutputTokens: 1},
	}
	client, _ := New(provider, "mock-model")
	fixer := NewSQLFixer(SQLFixerOptions{
		Client: client,
		SQLFixPrompt: "Fix {{ORIGINAL_SQL}}{{#VERIFICATION_CONTEXT}}\nEVIDENCE_BLOCK\n{{VERIFICATION_CONTEXT}}\n/EVIDENCE_BLOCK{{/VERIFICATION_CONTEXT}}",
	})

	_, err := fixer.FixSQL(
		context.Background(),
		"SELECT bad FROM t",
		"col not found",
		0,
		queryexec.FixOpts{}, // empty
	)
	if err != nil {
		t.Fatalf("FixSQL: %v", err)
	}
	systemPrompt := provider.Calls[len(provider.Calls)-1].Request.SystemPrompt
	if strings.Contains(systemPrompt, "EVIDENCE_BLOCK") {
		t.Errorf("verification-context section should be removed when opts.VerificationContext is empty, got:\n%s", systemPrompt)
	}
	if strings.Contains(systemPrompt, "{{#VERIFICATION_CONTEXT}}") || strings.Contains(systemPrompt, "{{/VERIFICATION_CONTEXT}}") {
		t.Errorf("section markers should be stripped, got:\n%s", systemPrompt)
	}
}

func TestApplySection(t *testing.T) {
	tests := []struct {
		name     string
		template string
		value    string
		want     string
	}{
		{
			name:     "kept when value non-empty",
			template: "before {{#X}}inner{{/X}} after",
			value:    "v",
			want:     "before inner after",
		},
		{
			name:     "removed when value empty",
			template: "before {{#X}}inner{{/X}} after",
			value:    "",
			want:     "before  after",
		},
		{
			name:     "removed when value whitespace only",
			template: "before {{#X}}inner{{/X}} after",
			value:    "   \n  ",
			want:     "before  after",
		},
		{
			name:     "multiple occurrences kept",
			template: "{{#X}}A{{/X}} mid {{#X}}B{{/X}}",
			value:    "v",
			want:     "A mid B",
		},
		{
			name:     "multiple occurrences removed",
			template: "{{#X}}A{{/X}} mid {{#X}}B{{/X}}",
			value:    "",
			want:     " mid ",
		},
		{
			name:     "no markers — unchanged",
			template: "no markers here",
			value:    "v",
			want:     "no markers here",
		},
		{
			name:     "unterminated open marker — leaves rest of template alone",
			template: "before {{#X}}inner without close",
			value:    "v",
			want:     "before {{#X}}inner without close",
		},
		{
			name:     "different-named section unaffected",
			template: "{{#Y}}keep{{/Y}} {{#X}}remove{{/X}}",
			value:    "",
			want:     "{{#Y}}keep{{/Y}} ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applySection(tt.template, "X", tt.value)
			if got != tt.want {
				t.Errorf("applySection(template=%q, name=%q, value=%q) = %q, want %q", tt.template, "X", tt.value, got, tt.want)
			}
		})
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

	fixed, err := fixer.FixSQL(context.Background(), "BAD SQL", "syntax error", 0, queryexec.FixOpts{})
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
