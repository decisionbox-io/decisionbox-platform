package llm

import (
	"encoding/json"
	"strings"
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

// Injecting the forced structured tool must also request single-tool-use
// so a parallel-tool-use provider (Anthropic) returns exactly one object.
func TestApplyResponseFormatAsTool_DisablesParallelToolUse(t *testing.T) {
	req := ChatRequest{ResponseFormat: &ResponseFormat{Name: "domain_pack", Schema: sampleSchema()}}
	got, injected := ApplyResponseFormatAsTool(req)
	if !injected {
		t.Fatal("injected=false")
	}
	if !got.DisableParallelToolUse {
		t.Error("DisableParallelToolUse should be set when forcing the structured tool")
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
	if err := NormalizeStructuredToolResponse(resp, true, nil); err != nil {
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
	if err := NormalizeStructuredToolResponse(resp, true, nil); err != nil {
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
	if err := NormalizeStructuredToolResponse(resp, true, nil); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if resp.Content != "{}" {
		t.Errorf("Content = %q, want %q", resp.Content, "{}")
	}
}

func TestNormalizeStructuredToolResponse_NotInjectedIsNoop(t *testing.T) {
	resp := &ChatResponse{Content: "hello", ToolCalls: []ToolCall{{Name: "x", Input: map[string]interface{}{"a": 1}}}}
	if err := NormalizeStructuredToolResponse(resp, false, nil); err != nil {
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
	if err := NormalizeStructuredToolResponse(resp, true, nil); err == nil {
		t.Fatal("want error for multiple tool calls, got nil")
	}
}

// When the model returned plain text (rare under a forced tool_choice)
// the caller's own parser must still see it — Content is left untouched.
func TestNormalizeStructuredToolResponse_TextResponseLeftIntact(t *testing.T) {
	resp := &ChatResponse{Content: `{"slug":"acme"}`}
	if err := NormalizeStructuredToolResponse(resp, true, nil); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if resp.Content != `{"slug":"acme"}` {
		t.Errorf("content mutated: %q", resp.Content)
	}
}

// Claude 5.x sometimes emits a nested array of objects as ONE JSON-encoded
// string inside the tool input ({"questions": "[{...}]"}). Where the schema
// says array/object and the string is that JSON, the fold decodes it; a
// string-typed field, or a string that is not valid JSON of the expected type,
// is left as the model sent it so the caller's own checks still see it.
func TestNormalizeStructuredToolResponse_DecodesStringifiedNestedValues(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"questions": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"question": map[string]interface{}{"type": "string"},
						"options":  map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "object"}},
					},
				},
			},
			"meta":   map[string]interface{}{"type": "object"},
			"title":  map[string]interface{}{"type": "string"},
			"broken": map[string]interface{}{"type": "array"},
			"wrong":  map[string]interface{}{"type": "array"},
		},
	}
	resp := &ChatResponse{StopReason: "tool_use", ToolCalls: []ToolCall{{ID: "t1", Name: "q", Input: map[string]interface{}{
		"questions": `[{"question":"a?","options":"[{\"id\":\"x\"}]"}]`,
		"meta":      ` {"k":1} `,
		"title":     `[draft] plan`,
		"broken":    `[{"question":"b?"`,
		"wrong":     `{"not":"an array"}`,
	}}}}
	if err := NormalizeStructuredToolResponse(resp, true, &ResponseFormat{Name: "q", Schema: schema}); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(resp.Content), &got); err != nil {
		t.Fatalf("content is not JSON: %v", err)
	}
	qs, ok := got["questions"].([]interface{})
	if !ok || len(qs) != 1 {
		t.Fatalf("questions not decoded to an array: %#v", got["questions"])
	}
	if opts, ok := qs[0].(map[string]interface{})["options"].([]interface{}); !ok || len(opts) != 1 {
		t.Errorf("nested options not decoded: %#v", qs[0])
	}
	if _, ok := got["meta"].(map[string]interface{}); !ok {
		t.Errorf("meta not decoded to an object: %#v", got["meta"])
	}
	for k, want := range map[string]string{"title": `[draft] plan`, "broken": `[{"question":"b?"`, "wrong": `{"not":"an array"}`} {
		if got[k] != want {
			t.Errorf("%s = %#v, want the original string %q", k, got[k], want)
		}
	}
}

// The stringified values are often not valid JSON either, in two recurring
// shapes: one closing brace too many after the array (Haiku 5.5) or after each
// item (Opus 5.5). Dropping brackets that close nothing recovers them; nothing
// is ever added, and a string that still does not parse is left as sent.
func TestNormalizeStructuredToolResponse_DropsUnmatchedClosers(t *testing.T) {
	schema := map[string]interface{}{"type": "object", "properties": map[string]interface{}{
		"questions": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "object"}}}}
	cases := []struct {
		name, in string
		wantLen  int // -1: left as the original string
	}{
		{"extra brace after the array", `[{"question":"a?"},{"question":"b?"}]}`, 2},
		{"extra brace after each item", `[{"question":"a?","options":[{"id":"x","label":"X"}]}},{"question":"b?","options":[]}}]`, 2},
		{"braces inside strings are content", `[{"question":"is } a ] brace?"}]}`, 1},
		{"missing opener stays a string", `[{"options":[{"id":"x"}, "id":"y"}]}]`, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := &ChatResponse{ToolCalls: []ToolCall{{Name: "q", Input: map[string]interface{}{"questions": c.in}}}}
			if err := NormalizeStructuredToolResponse(resp, true, &ResponseFormat{Name: "q", Schema: schema}); err != nil {
				t.Fatal(err)
			}
			var got map[string]interface{}
			if err := json.Unmarshal([]byte(resp.Content), &got); err != nil {
				t.Fatal(err)
			}
			if c.wantLen < 0 {
				if got["questions"] != c.in {
					t.Fatalf("questions = %#v, want the original string", got["questions"])
				}
				return
			}
			qs, ok := got["questions"].([]interface{})
			if !ok || len(qs) != c.wantLen {
				t.Fatalf("questions = %#v, want %d items", got["questions"], c.wantLen)
			}
			if c.name == "braces inside strings are content" && qs[0].(map[string]interface{})["question"] != "is } a ] brace?" {
				t.Errorf("string content changed: %#v", qs[0])
			}
		})
	}
}

// RelaxForcedToolChoice is the documented replacement for a forced tool
// choice on models that reject one: tools stay, the choice becomes "auto",
// and the system prompt names the tool to call.
func TestRelaxForcedToolChoice(t *testing.T) {
	forced := RelaxForcedToolChoice(ChatRequest{SystemPrompt: "base", ToolChoice: "emit", Tools: []ToolDefinition{{Name: "emit"}}})
	if forced.ToolChoice != "auto" || len(forced.Tools) != 1 {
		t.Errorf("forced tool: choice=%q tools=%d", forced.ToolChoice, len(forced.Tools))
	}
	if !strings.HasPrefix(forced.SystemPrompt, "base\n\n") || !strings.Contains(forced.SystemPrompt, `"emit"`) {
		t.Errorf("system prompt should keep the original and name the tool: %q", forced.SystemPrompt)
	}

	anyTool := RelaxForcedToolChoice(ChatRequest{ToolChoice: "any"})
	if anyTool.ToolChoice != "auto" || !strings.Contains(anyTool.SystemPrompt, "one of the provided tools") {
		t.Errorf("any: %+v", anyTool)
	}

	for _, c := range []string{"", "auto", "none"} {
		req := ChatRequest{SystemPrompt: "base", ToolChoice: c}
		if got := RelaxForcedToolChoice(req); got.ToolChoice != c || got.SystemPrompt != "base" {
			t.Errorf("non-forced choice %q must be unchanged, got %+v", c, got)
		}
	}
}
