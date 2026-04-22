package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/openaicompat"
)

func mockOpenAIServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(handler)
}

func defaultHandler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s, want /chat/completions", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "" {
			t.Error("missing Authorization header")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("missing Content-Type header")
		}

		var req openaicompat.RequestBody
		_ = json.NewDecoder(r.Body).Decode(&req)

		resp := openaicompat.ResponseBody{
			ID:    "chatcmpl-test",
			Model: req.Model,
			Choices: []openaicompat.Choice{
				{
					Message:      openaicompat.Message{Role: "assistant", Content: "Hello from mock OpenAI"},
					FinishReason: "stop",
				},
			},
			Usage: openaicompat.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func TestChat_Success(t *testing.T) {
	server := mockOpenAIServer(t, defaultHandler(t))
	defer server.Close()

	provider := NewOpenAIProvider("test-key", "gpt-4o", server.URL)

	resp, err := provider.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	if resp.Content != "Hello from mock OpenAI" {
		t.Errorf("content = %q", resp.Content)
	}
	if resp.Model != "gpt-4o" {
		t.Errorf("model = %q", resp.Model)
	}
	if resp.StopReason != "stop" {
		t.Errorf("stop_reason = %q", resp.StopReason)
	}
	if resp.Usage.InputTokens != 10 {
		t.Errorf("input_tokens = %d", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 5 {
		t.Errorf("output_tokens = %d", resp.Usage.OutputTokens)
	}
}

func TestChat_SystemPrompt(t *testing.T) {
	var receivedMessages []openaicompat.Message

	server := mockOpenAIServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req openaicompat.RequestBody
		_ = json.NewDecoder(r.Body).Decode(&req)
		receivedMessages = req.Messages

		resp := openaicompat.ResponseBody{
			Model: req.Model,
			Choices: []openaicompat.Choice{
				{Message: openaicompat.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	defer server.Close()

	provider := NewOpenAIProvider("test-key", "gpt-4o", server.URL)

	_, err := provider.Chat(context.Background(), gollm.ChatRequest{
		SystemPrompt: "You are a test assistant",
		Messages:     []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	if len(receivedMessages) != 2 {
		t.Fatalf("messages = %d, want 2 (system + user)", len(receivedMessages))
	}
	if receivedMessages[0].Role != "system" {
		t.Errorf("first message role = %q, want system", receivedMessages[0].Role)
	}
	if receivedMessages[1].Role != "user" {
		t.Errorf("second message role = %q, want user", receivedMessages[1].Role)
	}
}

func TestChat_ModelOverride(t *testing.T) {
	var receivedModel string

	server := mockOpenAIServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req openaicompat.RequestBody
		_ = json.NewDecoder(r.Body).Decode(&req)
		receivedModel = req.Model

		resp := openaicompat.ResponseBody{
			Model: req.Model,
			Choices: []openaicompat.Choice{
				{Message: openaicompat.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	defer server.Close()

	provider := NewOpenAIProvider("test-key", "gpt-4o", server.URL)

	_, err := provider.Chat(context.Background(), gollm.ChatRequest{
		Model:    "gpt-4o-mini",
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if receivedModel != "gpt-4o-mini" {
		t.Errorf("model = %q, want gpt-4o-mini", receivedModel)
	}
}

func TestChat_APIError_Typed(t *testing.T) {
	server := mockOpenAIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{
				"message": "Invalid API key",
				"type":    "invalid_api_key",
			},
		})
	})
	defer server.Close()

	provider := NewOpenAIProvider("bad-key", "gpt-4o", server.URL)

	_, err := provider.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("should return error for 401")
	}
	if !strings.Contains(err.Error(), "invalid_api_key") {
		t.Errorf("error %q should carry typed APIError fields", err.Error())
	}
	if !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("error %q should include server message", err.Error())
	}
}

func TestChat_APIError_RawBodyFallback(t *testing.T) {
	server := mockOpenAIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>502 Bad Gateway</html>"))
	})
	defer server.Close()

	provider := NewOpenAIProvider("test-key", "gpt-4o", server.URL)
	_, err := provider.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("should return error for 502")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("error %q should mention status 502", err.Error())
	}
	if !strings.Contains(err.Error(), "Bad Gateway") {
		t.Errorf("error %q should include raw body when no JSON error envelope", err.Error())
	}
}

func TestChat_EmptyChoices(t *testing.T) {
	server := mockOpenAIServer(t, func(w http.ResponseWriter, r *http.Request) {
		resp := openaicompat.ResponseBody{Model: "gpt-4o"}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	defer server.Close()

	provider := NewOpenAIProvider("test-key", "gpt-4o", server.URL)

	_, err := provider.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Error("should error on empty choices")
	}
}

func TestChat_MalformedJSON(t *testing.T) {
	server := mockOpenAIServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "definitely not JSON")
	})
	defer server.Close()

	provider := NewOpenAIProvider("test-key", "gpt-4o", server.URL)
	_, err := provider.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestChat_MaxTokensAndTemperature(t *testing.T) {
	var receivedReq openaicompat.RequestBody

	server := mockOpenAIServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedReq)

		resp := openaicompat.ResponseBody{
			Model: receivedReq.Model,
			Choices: []openaicompat.Choice{
				{Message: openaicompat.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	defer server.Close()

	provider := NewOpenAIProvider("test-key", "gpt-4o", server.URL)

	_, _ = provider.Chat(context.Background(), gollm.ChatRequest{
		Messages:    []gollm.Message{{Role: "user", Content: "hi"}},
		MaxTokens:   2000,
		Temperature: 0.7,
	})

	if receivedReq.MaxTokens != 2000 {
		t.Errorf("max_tokens = %d, want 2000", receivedReq.MaxTokens)
	}
	if receivedReq.Temperature != 0.7 {
		t.Errorf("temperature = %f, want 0.7", receivedReq.Temperature)
	}
}

func TestChat_ServerDown(t *testing.T) {
	provider := NewOpenAIProvider("test-key", "gpt-4o", "http://127.0.0.1:1")

	_, err := provider.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Error("should error when server is unreachable")
	}
}

func TestNewOpenAIProvider_Defaults(t *testing.T) {
	p := NewOpenAIProvider("key", "model", "")
	if p.baseURL != defaultBaseURL {
		t.Errorf("baseURL = %q, want %q", p.baseURL, defaultBaseURL)
	}
}

func TestProviderRegistered(t *testing.T) {
	meta, ok := gollm.GetProviderMeta("openai")
	if !ok {
		t.Fatal("openai not registered")
	}
	if meta.Name == "" {
		t.Error("missing provider name")
	}

	// MaxOutputTokens
	if meta.MaxOutputTokens == nil {
		t.Fatal("MaxOutputTokens should not be nil")
	}
	if len(meta.MaxOutputTokens) != 7 {
		t.Errorf("MaxOutputTokens has %d entries, want 7", len(meta.MaxOutputTokens))
	}
	if meta.MaxOutputTokens["gpt-4o"] != 16384 {
		t.Errorf("MaxOutputTokens[gpt-4o] = %d, want 16384", meta.MaxOutputTokens["gpt-4o"])
	}
	if meta.MaxOutputTokens["o3"] != 100000 {
		t.Errorf("MaxOutputTokens[o3] = %d, want 100000", meta.MaxOutputTokens["o3"])
	}

	// Verify GetMaxOutputTokens helper
	if got := gollm.GetMaxOutputTokens("openai", "gpt-4.1"); got != 32768 {
		t.Errorf("GetMaxOutputTokens(openai, gpt-4.1) = %d, want 32768", got)
	}
	// Verify _default fallback
	if got := gollm.GetMaxOutputTokens("openai", "gpt-future"); got != 16384 {
		t.Errorf("GetMaxOutputTokens(openai, gpt-future) = %d, want 16384 (_default)", got)
	}
}

func TestProviderFactory_MissingKey(t *testing.T) {
	_, err := gollm.NewProvider("openai", gollm.ProviderConfig{
		"model": "gpt-4o",
	})
	if err == nil {
		t.Error("should error without api_key")
	}
}

func TestValidate_Success(t *testing.T) {
	server := mockOpenAIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/models" {
			t.Errorf("path = %s, want /models", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{}})
	})
	defer server.Close()

	provider := NewOpenAIProvider("test-key", "gpt-4o", server.URL)
	if err := provider.Validate(context.Background()); err != nil {
		t.Fatalf("Validate should succeed: %v", err)
	}
}

func TestValidate_Unauthorized(t *testing.T) {
	server := mockOpenAIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": {"message": "Invalid API key"}}`))
	})
	defer server.Close()

	provider := NewOpenAIProvider("bad-key", "gpt-4o", server.URL)
	if err := provider.Validate(context.Background()); err == nil {
		t.Error("Validate should fail with bad key")
	}
}

func TestValidate_ServerDown(t *testing.T) {
	provider := NewOpenAIProvider("test-key", "gpt-4o", "http://127.0.0.1:1")
	if err := provider.Validate(context.Background()); err == nil {
		t.Error("Validate should fail when server is unreachable")
	}
}

func TestProviderFactory_DefaultModel(t *testing.T) {
	// Can't fully test without actual API, but verify factory doesn't error.
	// Use a bad base_url to avoid real API calls.
	p, err := gollm.NewProvider("openai", gollm.ProviderConfig{
		"api_key":  "test",
		"base_url": "http://127.0.0.1:1",
	})
	if err != nil {
		t.Fatalf("factory error: %v", err)
	}
	if p == nil {
		t.Error("provider should not be nil")
	}
}
