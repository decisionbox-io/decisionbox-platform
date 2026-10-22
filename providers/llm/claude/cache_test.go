package claude

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// captureServer records the raw request body and replies with resp.
func captureServer(t *testing.T, resp string, body *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(resp))
	}))
}

func providerWithCache(t *testing.T, server *httptest.Server, cache bool) *ClaudeProvider {
	t.Helper()
	p, err := NewClaudeProvider(ClaudeConfig{APIKey: "k", Model: "claude-sonnet-4-6", MaxRetries: 1, Timeout: 5 * time.Second, PromptCache: cache})
	if err != nil {
		t.Fatal(err)
	}
	p.httpClient.Transport = &rewriteTransport{target: strings.TrimPrefix(server.URL, "http://")}
	return p
}

const okBody = `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`

func TestChat_PromptCacheBreakpoints(t *testing.T) {
	var raw []byte
	srv := captureServer(t, okBody, &raw)
	defer srv.Close()
	p := providerWithCache(t, srv, true)
	_, err := p.Chat(context.Background(), gollm.ChatRequest{SystemPrompt: "sys", Messages: []gollm.Message{
		{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "c"}}})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]interface{}
	_ = json.Unmarshal(raw, &body)
	if sys, ok := body["system"].([]interface{}); !ok || sys[0].(map[string]interface{})["cache_control"] == nil {
		t.Errorf("system should carry a cache breakpoint: %#v", body["system"])
	}
	msgs := body["messages"].([]interface{})
	last := msgs[2].(map[string]interface{})["content"].([]interface{})
	if last[0].(map[string]interface{})["cache_control"] == nil {
		t.Errorf("last message should carry a cache breakpoint: %#v", msgs[2])
	}
	if msgs[0].(map[string]interface{})["content"] != "a" {
		t.Errorf("earlier messages must be unchanged: %#v", msgs[0])
	}
}

func TestChat_PromptCacheOff(t *testing.T) {
	var raw []byte
	srv := captureServer(t, okBody, &raw)
	defer srv.Close()
	p := providerWithCache(t, srv, false)
	if _, err := p.Chat(context.Background(), gollm.ChatRequest{SystemPrompt: "sys", Messages: []gollm.Message{
		{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "c"}}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "cache_control") || !strings.Contains(string(raw), `"system":"sys"`) {
		t.Errorf("caching off must keep the historical shape: %s", raw)
	}
}

func TestChat_ForcedToolChoiceRelaxedForRejectingModel(t *testing.T) {
	var raw []byte
	srv := captureServer(t, `{"content":[{"type":"tool_use","id":"t1","name":"verdict","input":{"ok":true}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`, &raw)
	defer srv.Close()
	p := providerWithCache(t, srv, false)
	resp, err := p.Chat(context.Background(), gollm.ChatRequest{Model: "claude-opus-5-5", SystemPrompt: "sys",
		Messages:       []gollm.Message{{Role: "user", Content: "x"}},
		ResponseFormat: &gollm.ResponseFormat{Name: "verdict", Schema: map[string]interface{}{"type": "object"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"tool_choice":{"disable_parallel_tool_use":true,"type":"auto"}`) {
		t.Errorf("Opus 5.5 must get an auto tool choice: %s", raw)
	}
	if resp.Content != `{"ok":true}` {
		t.Errorf("structured reply should still be folded into Content, got %q", resp.Content)
	}
}

func TestChat_UsageReportsTotalPromptAndCacheSplit(t *testing.T) {
	var raw []byte
	srv := captureServer(t, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":3,"cache_creation_input_tokens":90,"cache_read_input_tokens":900}}`, &raw)
	defer srv.Close()
	p := providerWithCache(t, srv, true)
	resp, err := p.Chat(context.Background(), gollm.ChatRequest{Messages: []gollm.Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := (gollm.Usage{InputTokens: 1000, OutputTokens: 3, CacheReadTokens: 900, CacheWriteTokens: 90}); resp.Usage != want {
		t.Errorf("usage = %+v, want %+v", resp.Usage, want)
	}
}

func TestCatalog_ClaudeOpus48AndClaude5Line(t *testing.T) {
	for id, rejects := range map[string]bool{"claude-opus-4-8": false, "claude-opus-5-5": true, "claude-sonnet-5-5": true, "claude-haiku-5-5": false} {
		if got := gollm.GetMaxInputTokens("claude", id); got != 1000000 {
			t.Errorf("%s: window = %d, want 1000000", id, got)
		}
		if got := gollm.RejectsForcedToolChoice("claude", id); got != rejects {
			t.Errorf("%s: rejects forced tool choice = %v, want %v", id, got, rejects)
		}
	}
	// Older models keep their 200K window.
	if got := gollm.GetMaxInputTokens("claude", "claude-sonnet-4-6"); got != 200000 {
		t.Errorf("claude-sonnet-4-6 window = %d, want 200000", got)
	}
}
