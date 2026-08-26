package llm

import (
	"encoding/json"
	"testing"
)

func sampleSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"slug": map[string]interface{}{"type": "string"},
		},
		// An open-ended (dynamic-key) object — the exact shape strict
		// OpenAI json_schema forbids but Ollama grammar and Anthropic tool
		// input schemas accept. Kept here so the helper is exercised with
		// the shape the feature must preserve.
		"categories": map[string]interface{}{
			"type":                 "object",
			"additionalProperties": map[string]interface{}{"type": "string"},
		},
	}
}

func TestApplyResponseFormatAsTool_NoResponseFormat(t *testing.T) {
	req := ChatRequest{Model: "m"}
	got, injected := ApplyResponseFormatAsTool(req)
	if injected {
		t.Fatal("injected=true with no ResponseFormat")
	}
	if len(got.Tools) != 0 || got.ToolChoice != "" {
		t.Errorf("request mutated: tools=%v toolChoice=%q", got.Tools, got.ToolChoice)
	}
}

func TestApplyResponseFormatAsTool_EmptySchemaNoop(t *testing.T) {
	req := ChatRequest{ResponseFormat: &ResponseFormat{Name: "x"}}
	_, injected := ApplyResponseFormatAsTool(req)
	if injected {
		t.Fatal("injected=true with empty schema")
	}
}

func TestApplyResponseFormatAsTool_InjectsForcedTool(t *testing.T) {
	req := ChatRequest{ResponseFormat: &ResponseFormat{Name: "domain_pack", Schema: sampleSchema()}}
	got, injected := ApplyResponseFormatAsTool(req)
	if !injected {
		t.Fatal("injected=false, want true")
	}
	if len(got.Tools) != 1 {
		t.Fatalf("want 1 tool, got %d", len(got.Tools))
	}
	if got.Tools[0].Name != "domain_pack" {
		t.Errorf("tool name = %q, want domain_pack", got.Tools[0].Name)
	}
	if got.ToolChoice != "domain_pack" {
		t.Errorf("tool_choice = %q, want domain_pack (forced)", got.ToolChoice)
	}
	// The open-ended object must survive into the tool input schema.
	cats, ok := got.Tools[0].InputSchema["categories"].(map[string]interface{})
	if !ok || cats["additionalProperties"] == nil {
		t.Errorf("open-ended 'categories' object dropped from tool schema: %v", got.Tools[0].InputSchema)
	}
}

// A ResponseFormat.Name that collides with a reserved tool-choice token
// must not be used as the forced tool name (it would serialise as a mode
// like {type:"none"} and disable forced tool use).
func TestApplyResponseFormatAsTool_ReservedNameFallsBack(t *testing.T) {
	for _, reserved := range []string{"none", "auto", "any", "required", "NONE"} {
		req := ChatRequest{ResponseFormat: &ResponseFormat{Name: reserved, Schema: sampleSchema()}}
		got, injected := ApplyResponseFormatAsTool(req)
		if !injected {
			t.Fatalf("%s: injected=false", reserved)
		}
		if got.ToolChoice != defaultStructuredToolName || got.Tools[0].Name != defaultStructuredToolName {
			t.Errorf("reserved name %q: tool/tool_choice = %q/%q, want default %q", reserved, got.Tools[0].Name, got.ToolChoice, defaultStructuredToolName)
		}
	}
}

func TestApplyResponseFormatAsTool_DefaultName(t *testing.T) {
	req := ChatRequest{ResponseFormat: &ResponseFormat{Schema: sampleSchema()}}
	got, _ := ApplyResponseFormatAsTool(req)
	if got.Tools[0].Name != defaultStructuredToolName {
		t.Errorf("tool name = %q, want %q", got.Tools[0].Name, defaultStructuredToolName)
	}
}

// The caller's own tools must always win — ResponseFormat can never
// disturb an existing tool-using flow (e.g. /ask function calling).
func TestApplyResponseFormatAsTool_CallerToolsWin(t *testing.T) {
	req := ChatRequest{
		ResponseFormat: &ResponseFormat{Name: "domain_pack", Schema: sampleSchema()},
		Tools:          []ToolDefinition{{Name: "search"}},
		ToolChoice:     "auto",
	}
	got, injected := ApplyResponseFormatAsTool(req)
	if injected {
		t.Fatal("injected=true despite caller-supplied tools")
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "search" {
		t.Errorf("caller tools mutated: %v", got.Tools)
	}
	if got.ToolChoice != "auto" {
		t.Errorf("caller tool_choice mutated: %q", got.ToolChoice)
	}
}

func TestNormalizeStructuredToolResponse_FoldsToolInputToContent(t *testing.T) {
	resp := &ChatResponse{
		StopReason: "tool_use",
		ToolCalls: []ToolCall{{
			ID:    "t1",
			Name:  "domain_pack",
			Input: map[string]interface{}{"slug": "acme"},
		}},
	}
	if err := NormalizeStructuredToolResponse(resp, true); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if resp.Content == "" {
		t.Fatal("content not populated from tool input")
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(resp.Content), &got); err != nil {
		t.Fatalf("content is not valid JSON: %v (%q)", err, resp.Content)
	}
	if got["slug"] != "acme" {
		t.Errorf("content = %q, want slug=acme", resp.Content)
	}
	if resp.ToolCalls != nil {
		t.Error("ToolCalls should be cleared after folding")
	}
	// The tool_use stop reason must be normalised — a hidden tool call must
	// not leave callers thinking there is a tool to execute.
	if resp.StopReason == "tool_use" {
		t.Errorf("StopReason should be normalised away from tool_use, got %q", resp.StopReason)
	}
}

// An empty object {} is a valid structured response (schema with only
// optional fields / empty dynamic maps) and must still be folded into
// Content — not left as an exposed tool call with empty Content.
func TestNormalizeStructuredToolResponse_EmptyObjectFolded(t *testing.T) {
	resp := &ChatResponse{
		StopReason: "tool_use",
		ToolCalls:  []ToolCall{{ID: "t1", Name: "domain_pack", Input: map[string]interface{}{}}},
	}
	if err := NormalizeStructuredToolResponse(resp, true); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if resp.Content != "{}" {
		t.Errorf("Content = %q, want %q", resp.Content, "{}")
	}
	if resp.ToolCalls != nil {
		t.Error("ToolCalls should be cleared")
	}
}

// A nil input map (no arguments captured) normalises to {} rather than
// "null".
func TestNormalizeStructuredToolResponse_NilInputBecomesEmptyObject(t *testing.T) {
	resp := &ChatResponse{ToolCalls: []ToolCall{{ID: "t1", Name: "domain_pack"}}}
	if err := NormalizeStructuredToolResponse(resp, true); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if resp.Content != "{}" {
		t.Errorf("Content = %q, want %q", resp.Content, "{}")
	}
}

func TestNormalizeStructuredToolResponse_NotInjectedIsNoop(t *testing.T) {
	resp := &ChatResponse{Content: "hello", ToolCalls: []ToolCall{{Name: "x", Input: map[string]interface{}{"a": 1}}}}
	if err := NormalizeStructuredToolResponse(resp, false); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if resp.Content != "hello" || resp.ToolCalls == nil {
		t.Error("no-op expected when injected=false")
	}
}

// More than one tool call for the forced schema tool (possible under
// Anthropic parallel tool use) must error rather than silently folding the
// first and dropping the rest.
func TestNormalizeStructuredToolResponse_MultipleToolCallsError(t *testing.T) {
	resp := &ChatResponse{
		StopReason: "tool_use",
		ToolCalls: []ToolCall{
			{ID: "t1", Name: "domain_pack", Input: map[string]interface{}{"a": 1}},
			{ID: "t2", Name: "domain_pack", Input: map[string]interface{}{"b": 2}},
		},
	}
	if err := NormalizeStructuredToolResponse(resp, true); err == nil {
		t.Fatal("want error for multiple tool calls, got nil")
	}
}

// When the model returned plain text (rare under a forced tool_choice)
// the caller's own parser must still see it — Content is left untouched.
func TestNormalizeStructuredToolResponse_TextResponseLeftIntact(t *testing.T) {
	resp := &ChatResponse{Content: `{"slug":"acme"}`}
	if err := NormalizeStructuredToolResponse(resp, true); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if resp.Content != `{"slug":"acme"}` {
		t.Errorf("content mutated: %q", resp.Content)
	}
}
