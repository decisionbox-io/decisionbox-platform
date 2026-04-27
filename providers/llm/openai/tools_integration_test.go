//go:build integration

package openai

import (
	"context"
	"os"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// Integration tests for OpenAI function calling. Require
// INTEGRATION_TEST_OPENAI_API_KEY to be set (falls back to OPENAI_API_KEY
// to match upstream conventions). Use gpt-4.1-nano by default — the
// spike's recommended blurb model — but INTEGRATION_TEST_OPENAI_MODEL
// overrides.

func openAIKey(t *testing.T) string {
	t.Helper()
	key := os.Getenv("INTEGRATION_TEST_OPENAI_API_KEY")
	if key == "" {
		key = os.Getenv("OPENAI_API_KEY")
	}
	if key == "" {
		t.Skip("INTEGRATION_TEST_OPENAI_API_KEY not set")
	}
	return key
}

func openAIModel() string {
	if m := os.Getenv("INTEGRATION_TEST_OPENAI_MODEL"); m != "" {
		return m
	}
	return "gpt-4.1-nano"
}

func TestInteg_OpenAI_FunctionCall_EndToEnd(t *testing.T) {
	key := openAIKey(t)
	p, err := gollm.NewProvider("openai", gollm.ProviderConfig{
		"api_key": key,
		"model":   openAIModel(),
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:     openAIModel(),
		MaxTokens: 256,
		Messages: []gollm.Message{{
			Role:    "user",
			Content: "Call inspect_table for the 'users' table to see its schema.",
		}},
		Tools: []gollm.ToolDefinition{{
			Name:        "inspect_table",
			Description: "Fetch the DDL and a few sample rows for given table names.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"tables": map[string]interface{}{
						"type":  "array",
						"items": map[string]interface{}{"type": "string"},
					},
				},
				"required": []string{"tables"},
			},
		}},
		ToolChoice: "required",
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.ToolCalls) == 0 {
		t.Fatalf("no tool calls returned (StopReason=%q, Content=%q)", resp.StopReason, resp.Content)
	}
	if resp.ToolCalls[0].Name != "inspect_table" {
		t.Errorf("tool name = %q", resp.ToolCalls[0].Name)
	}
	if _, ok := resp.ToolCalls[0].Input["tables"]; !ok {
		t.Errorf("tables arg missing: %+v", resp.ToolCalls[0].Input)
	}
}

func TestInteg_OpenAI_FunctionCall_RoundTrip(t *testing.T) {
	key := openAIKey(t)
	p, err := gollm.NewProvider("openai", gollm.ProviderConfig{
		"api_key": key,
		"model":   openAIModel(),
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	tool := gollm.ToolDefinition{
		Name:        "inspect_table",
		Description: "Fetch DDL and samples.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"tables": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			},
			"required": []string{"tables"},
		},
	}

	turn1, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:     openAIModel(),
		MaxTokens: 128,
		Messages:  []gollm.Message{{Role: "user", Content: "Use inspect_table on 'orders'."}},
		Tools:     []gollm.ToolDefinition{tool},
		ToolChoice: "required",
	})
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if len(turn1.ToolCalls) == 0 {
		t.Skip("model did not call tool on turn 1")
	}
	call := turn1.ToolCalls[0]

	// The round-trip: feed tool_result back. The assistant message must
	// carry ToolCalls so OpenAI correlates role=tool.tool_call_id.
	turn2, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:     openAIModel(),
		MaxTokens: 256,
		Messages: []gollm.Message{
			{Role: "user", Content: "Use inspect_table on 'orders'."},
			{Role: "assistant", Content: turn1.Content, ToolCalls: turn1.ToolCalls},
			{Role: "user", ToolResults: []gollm.ToolResult{{
				CallID:  call.ID,
				Content: "TABLE orders (10M rows) columns: id, user_id, total",
			}}},
		},
		Tools: []gollm.ToolDefinition{tool},
	})
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if turn2.Content == "" && len(turn2.ToolCalls) == 0 {
		t.Error("turn 2 produced no content")
	}
}
