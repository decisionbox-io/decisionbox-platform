package ai

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
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
