package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
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

		var req chatRequest
		json.NewDecoder(r.Body).Decode(&req)

		resp := chatResponse{
			ID:    "chatcmpl-test",
			Model: req.Model,
			Choices: []struct {
				Message      chatMessage `json:"message"`
				FinishReason string      `json:"finish_reason"`
			}{
				{
					Message:      chatMessage{Role: "assistant", Content: "Hello from mock OpenAI"},
					FinishReason: "stop",
				},
			},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
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
	var receivedMessages []chatMessage

	server := mockOpenAIServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		json.NewDecoder(r.Body).Decode(&req)
		receivedMessages = req.Messages

		resp := chatResponse{
			Model: req.Model,
			Choices: []struct {
				Message      chatMessage `json:"message"`
				FinishReason string      `json:"finish_reason"`
			}{{Message: chatMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
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
		var req chatRequest
		json.NewDecoder(r.Body).Decode(&req)
		receivedModel = req.Model

		resp := chatResponse{
			Model: req.Model,
			Choices: []struct {
				Message      chatMessage `json:"message"`
				FinishReason string      `json:"finish_reason"`
			}{{Message: chatMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
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

func TestChat_APIError(t *testing.T) {
	server := mockOpenAIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
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
		t.Error("should return error for 401")
	}
}

func TestChat_EmptyChoices(t *testing.T) {
	server := mockOpenAIServer(t, func(w http.ResponseWriter, r *http.Request) {
		resp := chatResponse{Model: "gpt-4o"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
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

func TestChat_MaxTokensAndTemperature(t *testing.T) {
	var receivedReq chatRequest

	server := mockOpenAIServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedReq)

		resp := chatResponse{
			Model: receivedReq.Model,
			Choices: []struct {
				Message      chatMessage `json:"message"`
				FinishReason string      `json:"finish_reason"`
			}{{Message: chatMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
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
	provider := NewOpenAIProvider("test-key", "gpt-4o", "http://localhost:1")

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
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{}})
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
		w.Write([]byte(`{"error": {"message": "Invalid API key"}}`))
	})
	defer server.Close()

	provider := NewOpenAIProvider("bad-key", "gpt-4o", server.URL)
	if err := provider.Validate(context.Background()); err == nil {
		t.Error("Validate should fail with bad key")
	}
}

func TestValidate_ServerDown(t *testing.T) {
	provider := NewOpenAIProvider("test-key", "gpt-4o", "http://localhost:1")
	if err := provider.Validate(context.Background()); err == nil {
		t.Error("Validate should fail when server is unreachable")
	}
}

func TestProviderFactory_DefaultModel(t *testing.T) {
	// Can't fully test without actual API, but verify factory doesn't error
	// We use a bad base_url to avoid real API calls
	p, err := gollm.NewProvider("openai", gollm.ProviderConfig{
		"api_key":  "test",
		"base_url": "http://localhost:1",
	})
	if err != nil {
		t.Fatalf("factory error: %v", err)
	}
	if p == nil {
		t.Error("provider should not be nil")
	}
}
