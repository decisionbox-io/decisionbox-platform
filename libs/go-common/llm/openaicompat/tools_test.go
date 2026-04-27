package openaicompat

import (
	"encoding/json"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestBuildRequestBody_NoTools_OmitsToolFields(t *testing.T) {
	body := BuildRequestBody("gpt-4o", gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	raw, _ := json.Marshal(body)
	if contains(raw, "tools") || contains(raw, "tool_choice") {
		t.Errorf("empty Tools should omit fields from wire: %s", raw)
	}
}

func TestBuildRequestBody_Tools_MappedToFunctionArray(t *testing.T) {
	body := BuildRequestBody("gpt-4o", gollm.ChatRequest{
		Tools: []gollm.ToolDefinition{{
			Name:        "inspect_table",
			Description: "fetch DDL",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"tables": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				},
			},
		}},
	})
	if len(body.Tools) != 1 {
		t.Fatalf("tools = %d", len(body.Tools))
	}
	if body.Tools[0].Type != "function" {
		t.Errorf("type = %q", body.Tools[0].Type)
	}
	if body.Tools[0].Function.Name != "inspect_table" {
		t.Errorf("name = %q", body.Tools[0].Function.Name)
	}
	if body.Tools[0].Function.Parameters == nil {
		t.Error("parameters lost")
	}
}

func TestBuildRequestBody_ToolResults_EmitAsToolRole(t *testing.T) {
	body := BuildRequestBody("gpt-4o", gollm.ChatRequest{
		Messages: []gollm.Message{
			{Role: "user", Content: "check"},
			{Role: "user", ToolResults: []gollm.ToolResult{
				{CallID: "call_1", Content: "tables: users, orders"},
				{CallID: "call_2", Content: "err", IsError: true},
			}},
		},
	})
	// Expect 3 messages: the first user + two role=tool (one per result).
	if len(body.Messages) != 3 {
		t.Fatalf("messages = %d", len(body.Messages))
	}
	tool1 := body.Messages[1]
	if tool1.Role != "tool" || tool1.ToolCallID != "call_1" || tool1.Content != "tables: users, orders" {
		t.Errorf("tool1 = %+v", tool1)
	}
	tool2 := body.Messages[2]
	if tool2.Role != "tool" || tool2.ToolCallID != "call_2" {
		t.Errorf("tool2 = %+v", tool2)
	}
}

func TestTranslateToolChoice(t *testing.T) {
	cases := []struct {
		in       string
		expected interface{}
	}{
		{"", nil},
		{"auto", nil},
		{"any", "required"},
		{"required", "required"},
		{"none", "none"},
	}
	for _, c := range cases {
		got := translateToolChoice(c.in)
		if got != c.expected {
			t.Errorf("translateToolChoice(%q) = %v, want %v", c.in, got, c.expected)
		}
	}
	// Named-tool → structured map.
	got := translateToolChoice("my_tool")
	gm, ok := got.(map[string]interface{})
	if !ok {
		t.Fatalf("specific name should produce map, got %T", got)
	}
	if gm["type"] != "function" {
		t.Errorf("type = %v", gm["type"])
	}
}

func TestParseResponseBody_ToolCalls_Decoded(t *testing.T) {
	raw := []byte(`{
		"id": "cmpl-1",
		"model": "gpt-4o",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "",
				"tool_calls": [
					{"id":"call_1","type":"function","function":{"name":"inspect_table","arguments":"{\"tables\":[\"users\"]}"}}
				]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
	}`)
	resp, err := ParseResponseBody(raw)
	if err != nil {
		t.Fatalf("ParseResponseBody: %v", err)
	}
	if resp.StopReason != "tool_calls" {
		t.Errorf("StopReason = %q", resp.StopReason)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "inspect_table" {
		t.Errorf("ToolCall = %+v", tc)
	}
	tables, ok := tc.Input["tables"].([]interface{})
	if !ok || len(tables) != 1 || tables[0] != "users" {
		t.Errorf("Input = %+v", tc.Input)
	}
}

func TestParseResponseBody_UnknownToolTypeSkipped(t *testing.T) {
	raw := []byte(`{
		"model":"x",
		"choices":[{
			"message":{"role":"assistant","content":"","tool_calls":[
				{"id":"c1","type":"code_interpreter","function":{"name":"x","arguments":"{}"}}
			]}
		}],
		"usage":{}
	}`)
	resp, err := ParseResponseBody(raw)
	if err != nil {
		t.Fatalf("ParseResponseBody: %v", err)
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("unknown tool type should be skipped, got %+v", resp.ToolCalls)
	}
}

func TestParseResponseBody_BadArgumentsJSON_EmptyInput(t *testing.T) {
	raw := []byte(`{
		"model":"x",
		"choices":[{
			"message":{"role":"assistant","content":"","tool_calls":[
				{"id":"c1","type":"function","function":{"name":"x","arguments":"not json"}}
			]}
		}],
		"usage":{}
	}`)
	resp, _ := ParseResponseBody(raw)
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Input != nil {
		t.Errorf("bad JSON should parse to nil Input, got %+v", resp.ToolCalls[0].Input)
	}
}

func contains(haystack []byte, needle string) bool {
	// simple substring match — avoid bytes.Contains to skip a dep
	s := string(haystack)
	for i := 0; i+len(needle) <= len(s); i++ {
		if s[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
