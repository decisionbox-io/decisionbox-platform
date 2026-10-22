package azurefoundry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestAzureFoundry_ClaudePromptCacheAndUsage(t *testing.T) {
	var raw []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",
			"usage":{"input_tokens":5,"output_tokens":2,"cache_creation_input_tokens":45,"cache_read_input_tokens":950}}`))
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-opus-4-8")
	p.promptCache = true
	resp, err := p.Chat(context.Background(), gollm.ChatRequest{Model: "claude-opus-4-8", SystemPrompt: "sys",
		Messages: []gollm.Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "c"}}})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]interface{}
	_ = json.Unmarshal(raw, &body)
	if _, ok := body["system"].([]interface{}); !ok {
		t.Errorf("system should carry a cache breakpoint: %#v", body["system"])
	}
	msgs := body["messages"].([]interface{})
	if _, ok := msgs[2].(map[string]interface{})["content"].([]interface{}); !ok {
		t.Errorf("last message should carry a cache breakpoint: %#v", msgs[2])
	}
	if want := (gollm.Usage{InputTokens: 1000, OutputTokens: 2, CacheReadTokens: 950, CacheWriteTokens: 45}); resp.Usage != want {
		t.Errorf("usage = %+v, want %+v", resp.Usage, want)
	}

	p.promptCache = false
	if _, err := p.Chat(context.Background(), gollm.ChatRequest{Model: "claude-opus-4-8", SystemPrompt: "sys",
		Messages: []gollm.Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "c"}}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "cache_control") {
		t.Errorf("caching off must send no breakpoints: %s", raw)
	}
}
