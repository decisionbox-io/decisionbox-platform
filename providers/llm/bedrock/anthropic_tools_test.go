package bedrock

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// capturingToolMock wraps mockBedrockClient with capturingMockBedrockClient
// so tests can both stub a response body AND inspect the outgoing JSON.
func capturingClient(responseBody []byte) (*capturingMockBedrockClient, *BedrockProvider) {
	if responseBody == nil {
		responseBody = []byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}
	inner := &mockBedrockClient{responseBody: responseBody}
	cap := &capturingMockBedrockClient{delegate: inner}
	p := newMockBedrockProvider(inner)
	p.client = cap
	p.model = "anthropic.claude-sonnet-4-20250514-v1:0"
	return cap, p
}

func TestAnthropic_ToolsOmittedOnNonToolRequests(t *testing.T) {
	mock, p := capturingClient(nil)

	_, err := p.chatAnthropic(context.Background(), gollm.ChatRequest{
		Model:    p.model,
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("chatAnthropic: %v", err)
	}
	if strings.Contains(string(mock.lastBody), `"tools"`) {
		t.Errorf("tools should be omitted when not set: %s", mock.lastBody)
	}
}

func TestAnthropic_ToolsIncludedWhenSet(t *testing.T) {
	mock, p := capturingClient(nil)

	_, err := p.chatAnthropic(context.Background(), gollm.ChatRequest{
		Model:    p.model,
		Messages: []gollm.Message{{Role: "user", Content: "inspect users"}},
		Tools: []gollm.ToolDefinition{{
			Name:        "inspect_table",
			Description: "fetch DDL",
			InputSchema: map[string]interface{}{"type": "object"},
		}},
		ToolChoice: "required",
	})
	if err != nil {
		t.Fatalf("chatAnthropic: %v", err)
	}
	var body map[string]interface{}
	_ = json.Unmarshal(mock.lastBody, &body)
	tools, ok := body["tools"].([]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("tools in body: %+v", body["tools"])
	}
	tc, ok := body["tool_choice"].(map[string]interface{})
	if !ok || tc["type"] != "any" {
		t.Errorf("tool_choice = %+v", body["tool_choice"])
	}
}

func TestAnthropic_ParsesToolUseResponse(t *testing.T) {
	_, p := capturingClient([]byte(`{
		"content":[
			{"type":"text","text":"Let me inspect"},
			{"type":"tool_use","id":"toolu_x","name":"inspect_table","input":{"tables":["users","orders"]}}
		],
		"stop_reason":"tool_use",
		"usage":{"input_tokens":100,"output_tokens":25}
	}`))

	resp, err := p.chatAnthropic(context.Background(), gollm.ChatRequest{
		Model:    p.model,
		Messages: []gollm.Message{{Role: "user", Content: "do it"}},
		Tools: []gollm.ToolDefinition{{
			Name:        "inspect_table",
			InputSchema: map[string]interface{}{"type": "object"},
		}},
	})
	if err != nil {
		t.Fatalf("chatAnthropic: %v", err)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("StopReason = %q", resp.StopReason)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "inspect_table" {
		t.Errorf("ToolCall.Name = %q", resp.ToolCalls[0].Name)
	}
	tables, ok := resp.ToolCalls[0].Input["tables"].([]interface{})
	if !ok || len(tables) != 2 {
		t.Errorf("ToolCall.Input = %+v", resp.ToolCalls[0].Input)
	}
}

func TestAnthropic_ToolResultsEmittedAsContentBlocks(t *testing.T) {
	mock, p := capturingClient(nil)

	_, err := p.chatAnthropic(context.Background(), gollm.ChatRequest{
		Model: p.model,
		Messages: []gollm.Message{
			{Role: "user", ToolResults: []gollm.ToolResult{
				{CallID: "toolu_x", Content: "users: 3 cols"},
				{CallID: "toolu_y", Content: "orders: timeout", IsError: true},
			}},
		},
	})
	if err != nil {
		t.Fatalf("chatAnthropic: %v", err)
	}
	var body map[string]interface{}
	_ = json.Unmarshal(mock.lastBody, &body)
	msgs, _ := body["messages"].([]interface{})
	if len(msgs) != 1 {
		t.Fatalf("messages = %d", len(msgs))
	}
	first := msgs[0].(map[string]interface{})
	content, ok := first["content"].([]interface{})
	if !ok {
		t.Fatalf("content should be []interface{}, got %T", first["content"])
	}
	if len(content) != 2 {
		t.Fatalf("tool_result blocks = %d", len(content))
	}
	first0 := content[0].(map[string]interface{})
	if first0["type"] != "tool_result" || first0["tool_use_id"] != "toolu_x" {
		t.Errorf("first block = %+v", first0)
	}
	first1 := content[1].(map[string]interface{})
	if first1["is_error"] != true {
		t.Errorf("error marker missing: %+v", first1)
	}
}

func TestAnthropic_ToolResultsOnAssistantMessageErrors(t *testing.T) {
	_, p := capturingClient(nil)

	_, err := p.chatAnthropic(context.Background(), gollm.ChatRequest{
		Model: p.model,
		Messages: []gollm.Message{
			{Role: "assistant", ToolResults: []gollm.ToolResult{{CallID: "x", Content: "y"}}},
		},
	})
	if err == nil {
		t.Error("tool_results on role=assistant should error")
	}
}

func TestAnthropic_BuildAnthropicToolChoice(t *testing.T) {
	cases := []struct {
		in string
		want map[string]interface{}
	}{
		{"", nil},
		{"auto", nil},
		{"any", map[string]interface{}{"type": "any"}},
		{"required", map[string]interface{}{"type": "any"}},
		{"none", map[string]interface{}{"type": "none"}},
		{"inspect_table", map[string]interface{}{"type": "tool", "name": "inspect_table"}},
	}
	for _, c := range cases {
		got := buildAnthropicToolChoice(c.in)
		if (got == nil) != (c.want == nil) {
			t.Errorf("buildAnthropicToolChoice(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		if got == nil {
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("buildAnthropicToolChoice(%q)[%q] = %v, want %v", c.in, k, got[k], v)
			}
		}
	}
}
