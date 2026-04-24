package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// These tests cover the new tool-use path added in Phase B1. They stub
// the Anthropic API so they run fast and deterministically without
// touching the network. An end-to-end test hitting the real API lives in
// the agent's integration tests.

func newProviderAgainst(t *testing.T, server *httptest.Server) *ClaudeProvider {
	t.Helper()
	p, err := NewClaudeProvider(ClaudeConfig{
		APIKey:     "test-key",
		Model:      "claude-sonnet-4-6",
		MaxRetries: 1,
		Timeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Reuse list_models_test.go's rewriteTransport — same package.
	target := strings.TrimPrefix(server.URL, "http://")
	p.httpClient.Transport = &rewriteTransport{target: target}
	return p
}

func TestChat_WithTools_RequestShape(t *testing.T) {
	var captured claudeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		// Stub returns a tool_use response so the parser path runs.
		resp := claudeResponse{
			Model: "claude-sonnet-4-6",
			Content: []claudeResponseContent{
				{Type: "text", Text: "Let me check."},
				{Type: "tool_use", ID: "toolu_1", Name: "inspect_table", Input: json.RawMessage(`{"tables":["users"]}`)},
			},
			StopReason: "tool_use",
		}
		resp.Usage.InputTokens = 100
		resp.Usage.OutputTokens = 50
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newProviderAgainst(t, server)
	out, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:        "claude-sonnet-4-6",
		SystemPrompt: "you are helpful",
		Messages:     []gollm.Message{{Role: "user", Content: "inspect users table"}},
		Tools: []gollm.ToolDefinition{{
			Name:        "inspect_table",
			Description: "fetch DDL + samples",
			InputSchema: map[string]interface{}{"type": "object"},
		}},
		ToolChoice: "auto",
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// Request shape assertions.
	if len(captured.Tools) != 1 {
		t.Fatalf("tools sent = %d", len(captured.Tools))
	}
	if captured.Tools[0].Name != "inspect_table" {
		t.Errorf("tool name = %q", captured.Tools[0].Name)
	}
	if captured.ToolChoice != nil { // "auto" → omitted
		t.Errorf("tool_choice should be omitted on 'auto', got %+v", captured.ToolChoice)
	}

	// Response shape assertions.
	if out.StopReason != "tool_use" {
		t.Errorf("StopReason = %q", out.StopReason)
	}
	if len(out.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d", len(out.ToolCalls))
	}
	call := out.ToolCalls[0]
	if call.ID != "toolu_1" || call.Name != "inspect_table" {
		t.Errorf("ToolCall = %+v", call)
	}
	if tables, ok := call.Input["tables"].([]interface{}); !ok || len(tables) != 1 || tables[0] != "users" {
		t.Errorf("ToolCall.Input = %+v", call.Input)
	}
	// Text content next to tool_use is preserved for audit.
	if !strings.Contains(out.Content, "Let me check") {
		t.Errorf("text content lost: %q", out.Content)
	}
}

func TestChat_ToolChoice_Translations(t *testing.T) {
	cases := []struct {
		input    string
		expected map[string]interface{}
	}{
		{"", nil},
		{"auto", nil},
		{"any", map[string]interface{}{"type": "any"}},
		{"required", map[string]interface{}{"type": "any"}},
		{"none", map[string]interface{}{"type": "none"}},
		{"inspect_table", map[string]interface{}{"type": "tool", "name": "inspect_table"}},
	}
	for _, c := range cases {
		got := convertToolChoiceForClaude(c.input)
		if (got == nil) != (c.expected == nil) {
			t.Errorf("input %q: got=%v want=%v", c.input, got, c.expected)
			continue
		}
		if got == nil {
			continue
		}
		for k, v := range c.expected {
			if got[k] != v {
				t.Errorf("input %q: got[%q]=%v, want %v", c.input, k, got[k], v)
			}
		}
	}
}

func TestChat_ToolResultsOnUserMessage_Roundtrip(t *testing.T) {
	var captured claudeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		resp := claudeResponse{
			Content:    []claudeResponseContent{{Type: "text", Text: "thanks"}},
			StopReason: "end_turn",
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newProviderAgainst(t, server)
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{
			{Role: "user", Content: "ran the tool"},
			{Role: "assistant", Content: ""}, // a tool_use turn reduced to empty
			{Role: "user", ToolResults: []gollm.ToolResult{
				{CallID: "toolu_1", Content: "tables: a,b,c"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// Last message rendered as content blocks array.
	last := captured.Messages[len(captured.Messages)-1]
	blocks, ok := last.Content.([]interface{})
	if !ok {
		// When marshaled back via JSON round-trip the []claudeContentBlock
		// arrives as []interface{}; pluck one and assert it carries
		// tool_result shape.
		// Decode via raw JSON as a belt-and-braces check.
		raw, _ := json.Marshal(last.Content)
		if !strings.Contains(string(raw), "tool_result") {
			t.Errorf("expected tool_result block in last message, got %s", raw)
		}
		return
	}
	if len(blocks) == 0 {
		t.Errorf("tool_result blocks missing")
	}
}

func TestChat_ToolResults_OnAssistantMessage_Errors(t *testing.T) {
	// ToolResults on a non-user message is a caller mis-wire.
	p := &ClaudeProvider{
		apiKey:     "k",
		model:      "claude-sonnet-4-6",
		maxRetries: 1,
		httpClient: http.DefaultClient,
	}
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{
			{Role: "assistant", ToolResults: []gollm.ToolResult{{CallID: "x", Content: "y"}}},
		},
	})
	if err == nil {
		t.Error("tool_results on assistant should error")
	}
}

func TestChat_ToolResult_IsErrorMarker(t *testing.T) {
	var captured claudeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		resp := claudeResponse{Content: []claudeResponseContent{{Type: "text", Text: "ok"}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newProviderAgainst(t, server)
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{
			Role: "user",
			ToolResults: []gollm.ToolResult{
				{CallID: "c1", Content: "timed out", IsError: true},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(captured.Messages[0].Content)
	if !strings.Contains(string(raw), `"is_error":true`) {
		t.Errorf("is_error marker missing: %s", raw)
	}
}

func TestChat_WithoutTools_UnchangedBehavior(t *testing.T) {
	// Sanity: pre-existing callers that don't set Tools keep working.
	var captured claudeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		resp := claudeResponse{
			Content:    []claudeResponseContent{{Type: "text", Text: "hi"}},
			StopReason: "end_turn",
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newProviderAgainst(t, server)
	out, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.ToolCalls) != 0 {
		t.Errorf("no tools in: should return no tool_calls, got %d", len(out.ToolCalls))
	}
	if out.Content != "hi" {
		t.Errorf("content = %q", out.Content)
	}
	if captured.Tools != nil {
		t.Error("tools should be omitted when empty")
	}
}
