//go:build integration

package bedrock

import (
	"context"
	"os"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// These tests hit the real Bedrock API to verify that the tool_use wire
// plumbing works end-to-end. They're gated on INTEGRATION_TEST_BEDROCK_REGION
// so CI without AWS credentials skips them gracefully.

func TestInteg_Bedrock_ClaudeToolUse_EndToEnd(t *testing.T) {
	region := skipIfNoRegion(t)

	p, err := buildProvider(region, bedrockModel())
	if err != nil {
		t.Fatalf("build provider: %v", err)
	}

	// A simple "use this tool" prompt with tool_choice=required forces
	// the model down the tool_use branch deterministically.
	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:     bedrockModel(),
		MaxTokens: 256,
		Messages: []gollm.Message{{
			Role:    "user",
			Content: "Please inspect the 'users' table. Use the inspect_table tool.",
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
	maybeSkipOnRateLimit(t, err)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.StopReason != "tool_use" {
		t.Fatalf("StopReason = %q, want tool_use (did the model actually invoke the tool?)", resp.StopReason)
	}
	if len(resp.ToolCalls) == 0 {
		t.Fatal("no tool calls returned from live API")
	}
	call := resp.ToolCalls[0]
	if call.Name != "inspect_table" {
		t.Errorf("tool name = %q", call.Name)
	}
	if _, ok := call.Input["tables"]; !ok {
		t.Errorf("tables arg missing from tool call: %+v", call.Input)
	}
}

func TestInteg_Bedrock_ClaudeToolUse_RoundTrip(t *testing.T) {
	region := skipIfNoRegion(t)
	p, err := buildProvider(region, bedrockModel())
	if err != nil {
		t.Fatalf("build provider: %v", err)
	}

	// Turn 1: ask to use the tool.
	turn1, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:     bedrockModel(),
		MaxTokens: 256,
		Messages: []gollm.Message{{
			Role:    "user",
			Content: "Use the inspect_table tool on the 'orders' table.",
		}},
		Tools: []gollm.ToolDefinition{{
			Name:        "inspect_table",
			Description: "Fetch DDL and samples for given table names.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"tables": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				},
				"required": []string{"tables"},
			},
		}},
		ToolChoice: "required",
	})
	maybeSkipOnRateLimit(t, err)
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if len(turn1.ToolCalls) == 0 {
		t.Skip("model did not issue a tool call on turn 1 — skip the round-trip assertion")
	}
	call := turn1.ToolCalls[0]

	// Turn 2: reply with tool_result. The model should summarise.
	// The assistant message must carry ToolCalls so Anthropic can
	// correlate the tool_result.tool_use_id on the next user turn.
	turn2, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:     bedrockModel(),
		MaxTokens: 256,
		Messages: []gollm.Message{
			{Role: "user", Content: "Use the inspect_table tool on the 'orders' table."},
			{Role: "assistant", Content: turn1.Content, ToolCalls: turn1.ToolCalls},
			{Role: "user", ToolResults: []gollm.ToolResult{{
				CallID:  call.ID,
				Content: `TABLE orders (10M rows)\ncolumns: id INT64, user_id INT64, total FLOAT64`,
			}}},
		},
		Tools: []gollm.ToolDefinition{{
			Name: "inspect_table",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"tables": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				},
			},
		}},
	})
	maybeSkipOnRateLimit(t, err)
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if turn2.StopReason == "tool_use" {
		// The model called another tool — accept that as a valid outcome
		// of a multi-turn tool dance; the key assertion is that the wire
		// round-trip (assistant tool_use → user tool_result → server) did
		// not 400 and produced a sane reply.
		t.Logf("turn 2 issued another tool call — acceptable")
	}
	if turn2.Content == "" && len(turn2.ToolCalls) == 0 {
		t.Error("turn 2 produced neither text nor tool_calls")
	}
}

func buildProvider(region, model string) (gollm.Provider, error) {
	return gollm.NewProvider("bedrock", gollm.ProviderConfig{
		"region": region,
		"model":  model,
	})
}

// Basic env check used by the tests above.
func init() {
	// Suppresses "unused" warnings from imports on non-integration builds.
	_ = os.Getenv
}
