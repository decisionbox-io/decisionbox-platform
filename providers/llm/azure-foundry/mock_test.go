package azurefoundry

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

// newTestProvider creates an AzureFoundryProvider pointing at a test HTTP server.
func newTestProvider(serverURL, model string) *AzureFoundryProvider {
	return &AzureFoundryProvider{
		endpoint:   serverURL,
		apiKey:     "test-api-key-123",
		model:      model,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// newTestProviderWithURL creates an AzureFoundryProvider with a transport
// that rewrites all requests to the given test server URL.
func newTestProviderWithURL(serverURL, model string) *AzureFoundryProvider {
	return &AzureFoundryProvider{
		endpoint: "https://my-resource.services.ai.azure.com",
		apiKey:   "test-api-key-123",
		model:    model,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &rewriteTransport{
				targetBase: serverURL,
				wrapped:    http.DefaultTransport,
			},
		},
	}
}

// rewriteTransport redirects all HTTP requests to a test server URL.
type rewriteTransport struct {
	targetBase string
	wrapped    http.RoundTripper
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(t.targetBase, "http://")
	return t.wrapped.RoundTrip(req)
}

// --- Claude backend tests ---

func TestAzureFoundry_ClaudeChat_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify endpoint path
		if !strings.HasSuffix(r.URL.Path, "/anthropic/v1/messages") {
			t.Errorf("path = %q, want /anthropic/v1/messages suffix", r.URL.Path)
		}

		// Verify Azure auth header
		if apiKey := r.Header.Get("api-key"); apiKey != "test-api-key-123" {
			t.Errorf("api-key header = %q, want test-api-key-123", apiKey)
		}

		// Verify anthropic-version header
		if v := r.Header.Get("anthropic-version"); v != "2023-06-01" {
			t.Errorf("anthropic-version = %q, want 2023-06-01", v)
		}

		resp := map[string]interface{}{
			"content": []map[string]string{
				{"type": "text", "text": "Hello from Claude on Azure!"},
			},
			"model":       "claude-sonnet-4-6",
			"stop_reason": "end_turn",
			"usage": map[string]int{
				"input_tokens":  15,
				"output_tokens": 8,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-6")

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello from Claude on Azure!" {
		t.Errorf("content = %q, want %q", resp.Content, "Hello from Claude on Azure!")
	}
	if resp.Model != "claude-sonnet-4-6" {
		t.Errorf("model = %q, want claude-sonnet-4-6", resp.Model)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q, want end_turn", resp.StopReason)
	}
	if resp.Usage.InputTokens != 15 {
		t.Errorf("input_tokens = %d, want 15", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 8 {
		t.Errorf("output_tokens = %d, want 8", resp.Usage.OutputTokens)
	}
}

func TestAzureFoundry_ClaudeChat_APIError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{"bad request", http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: must be positive"}}`},
		{"internal error", http.StatusInternalServerError, `{"type":"error","error":{"type":"api_error","message":"Internal server error"}}`},
		{"rate limited", http.StatusTooManyRequests, `{"type":"error","error":{"type":"rate_limit_error","message":"Rate limit exceeded"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.body))
			}))
			defer server.Close()

			p := newTestProviderWithURL(server.URL, "claude-sonnet-4-6")
			_, err := p.Chat(context.Background(), gollm.ChatRequest{
				Model:    "claude-sonnet-4-6",
				Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
			})
			if err == nil {
				t.Fatal("expected error for API error response")
			}
			if !strings.Contains(err.Error(), "API error") {
				t.Errorf("error = %q, should mention API error", err.Error())
			}
		})
	}
}

func TestAzureFoundry_ClaudeChat_SystemPrompt(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		resp := map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": "4"}},
			"model":       "claude-sonnet-4-6",
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 20, "output_tokens": 3},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-6")

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:        "claude-sonnet-4-6",
		SystemPrompt: "You are a calculator. Only respond with numbers.",
		Messages:     []gollm.Message{{Role: "user", Content: "What is 2+2?"}},
		MaxTokens:    10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "4" {
		t.Errorf("content = %q, want %q", resp.Content, "4")
	}

	var reqBody map[string]interface{}
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}
	system, ok := reqBody["system"]
	if !ok {
		t.Error("system prompt not included in request body")
	}
	if system != "You are a calculator. Only respond with numbers." {
		t.Errorf("system = %q", system)
	}
}

func TestAzureFoundry_ClaudeChat_NoSystemPrompt(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		resp := map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": "response"}},
			"model":       "claude-sonnet-4-6",
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 5, "output_tokens": 1},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-6")
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var reqBody map[string]interface{}
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}
	if _, ok := reqBody["system"]; ok {
		t.Error("system should not be in request body when SystemPrompt is empty")
	}
}

func TestAzureFoundry_ClaudeChat_DefaultMaxTokens(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		resp := map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": "response"}},
			"model":       "claude-sonnet-4-6",
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 5, "output_tokens": 1},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-6")
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var reqBody map[string]interface{}
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}
	maxTokens, ok := reqBody["max_tokens"]
	if !ok {
		t.Fatal("max_tokens not set in request body")
	}
	if maxTokens.(float64) != 4096 {
		t.Errorf("max_tokens = %v, want 4096 (default)", maxTokens)
	}
}

func TestAzureFoundry_ClaudeChat_Temperature(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		resp := map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": "creative"}},
			"model":       "claude-sonnet-4-6",
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 5, "output_tokens": 1},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-6")
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:       "claude-sonnet-4-6",
		Messages:    []gollm.Message{{Role: "user", Content: "Be creative"}},
		Temperature: 0.9,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var reqBody map[string]interface{}
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}
	temp, ok := reqBody["temperature"]
	if !ok {
		t.Fatal("temperature not set in request body")
	}
	if temp.(float64) != 0.9 {
		t.Errorf("temperature = %v, want 0.9", temp)
	}
}

func TestAzureFoundry_ClaudeChat_NoTemperature(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		resp := map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": "response"}},
			"model":       "claude-sonnet-4-6",
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 5, "output_tokens": 1},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-6")
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var reqBody map[string]interface{}
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}
	if _, ok := reqBody["temperature"]; ok {
		t.Error("temperature should not be in request body when Temperature is 0")
	}
}

func TestAzureFoundry_ClaudeChat_MultipleContentBlocks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"content": []map[string]string{
				{"type": "text", "text": "First part. "},
				{"type": "text", "text": "Second part."},
			},
			"model":       "claude-sonnet-4-6",
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 10, "output_tokens": 8},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-6")
	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "First part. Second part." {
		t.Errorf("content = %q, want %q", resp.Content, "First part. Second part.")
	}
}

func TestAzureFoundry_ClaudeChat_TokenCounting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": "A long response..."}},
			"model":       "claude-opus-4-6",
			"stop_reason": "max_tokens",
			"usage":       map[string]int{"input_tokens": 300, "output_tokens": 150},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-opus-4-6")
	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:     "claude-opus-4-6",
		Messages:  []gollm.Message{{Role: "user", Content: "Tell me a long story"}},
		MaxTokens: 150,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage.InputTokens != 300 {
		t.Errorf("input_tokens = %d, want 300", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 150 {
		t.Errorf("output_tokens = %d, want 150", resp.Usage.OutputTokens)
	}
	if resp.StopReason != "max_tokens" {
		t.Errorf("stop_reason = %q, want max_tokens", resp.StopReason)
	}
}

// --- OpenAI backend tests ---

func TestAzureFoundry_OpenAIChat_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify endpoint path
		if !strings.HasSuffix(r.URL.Path, "/openai/v1/chat/completions") {
			t.Errorf("path = %q, want /openai/v1/chat/completions suffix", r.URL.Path)
		}

		// Verify Azure auth header
		if apiKey := r.Header.Get("api-key"); apiKey != "test-api-key-123" {
			t.Errorf("api-key header = %q, want test-api-key-123", apiKey)
		}

		// Should NOT have anthropic-version header
		if v := r.Header.Get("anthropic-version"); v != "" {
			t.Errorf("anthropic-version = %q, should not be set for OpenAI", v)
		}

		resp := openaiChatResponse{
			ID:    "chatcmpl-123",
			Model: "gpt-4o",
			Choices: []struct {
				Message      openaiChatMsg `json:"message"`
				FinishReason string        `json:"finish_reason"`
			}{
				{
					Message:      openaiChatMsg{Role: "assistant", Content: "Hello from GPT on Azure!"},
					FinishReason: "stop",
				},
			},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			}{
				PromptTokens:     10,
				CompletionTokens: 6,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "gpt-4o")

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "gpt-4o",
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello from GPT on Azure!" {
		t.Errorf("content = %q, want %q", resp.Content, "Hello from GPT on Azure!")
	}
	if resp.Model != "gpt-4o" {
		t.Errorf("model = %q, want gpt-4o", resp.Model)
	}
	if resp.StopReason != "stop" {
		t.Errorf("stop_reason = %q, want stop", resp.StopReason)
	}
	if resp.Usage.InputTokens != 10 {
		t.Errorf("input_tokens = %d, want 10", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 6 {
		t.Errorf("output_tokens = %d, want 6", resp.Usage.OutputTokens)
	}
}

func TestAzureFoundry_OpenAIChat_APIError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{"unauthorized", http.StatusUnauthorized, `{"error":{"message":"Invalid API key","type":"invalid_request_error","code":"invalid_api_key"}}`},
		{"internal error", http.StatusInternalServerError, `{"error":{"message":"Internal error","type":"server_error"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.body))
			}))
			defer server.Close()

			p := newTestProviderWithURL(server.URL, "gpt-4o")
			_, err := p.Chat(context.Background(), gollm.ChatRequest{
				Model:    "gpt-4o",
				Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
			})
			if err == nil {
				t.Fatal("expected error for API error response")
			}
			if !strings.Contains(err.Error(), "API error") {
				t.Errorf("error = %q, should mention API error", err.Error())
			}
		})
	}
}

func TestAzureFoundry_OpenAIChat_SystemPrompt(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		resp := openaiChatResponse{
			Model: "gpt-4o",
			Choices: []struct {
				Message      openaiChatMsg `json:"message"`
				FinishReason string        `json:"finish_reason"`
			}{
				{Message: openaiChatMsg{Role: "assistant", Content: "4"}, FinishReason: "stop"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "gpt-4o")
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:        "gpt-4o",
		SystemPrompt: "You are a calculator.",
		Messages:     []gollm.Message{{Role: "user", Content: "2+2?"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var reqBody openaiChatRequest
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}

	// First message should be the system prompt
	if len(reqBody.Messages) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(reqBody.Messages))
	}
	if reqBody.Messages[0].Role != "system" {
		t.Errorf("first message role = %q, want system", reqBody.Messages[0].Role)
	}
	if reqBody.Messages[0].Content != "You are a calculator." {
		t.Errorf("system content = %q", reqBody.Messages[0].Content)
	}
}

func TestAzureFoundry_OpenAIChat_EmptyChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openaiChatResponse{Model: "gpt-4o"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "gpt-4o")
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "gpt-4o",
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
	if !strings.Contains(err.Error(), "no choices") {
		t.Errorf("error = %q, should mention no choices", err.Error())
	}
}

func TestAzureFoundry_OpenAIChat_NonClaudeModel(t *testing.T) {
	// Non-Claude models (DeepSeek, o3, etc.) should route to OpenAI backend
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/openai/v1/chat/completions") {
			t.Errorf("path = %q, expected OpenAI endpoint", r.URL.Path)
		}

		resp := openaiChatResponse{
			Model: "DeepSeek-V3",
			Choices: []struct {
				Message      openaiChatMsg `json:"message"`
				FinishReason string        `json:"finish_reason"`
			}{
				{Message: openaiChatMsg{Role: "assistant", Content: "Hello from DeepSeek!"}, FinishReason: "stop"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "DeepSeek-V3")
	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "DeepSeek-V3",
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello from DeepSeek!" {
		t.Errorf("content = %q", resp.Content)
	}
}

// --- Endpoint URL tests ---

func TestAzureFoundry_ClaudeEndpointURL(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path

		resp := map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": "ok"}},
			"model":       "claude-sonnet-4-6",
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 3, "output_tokens": 1},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProvider(server.URL, "claude-sonnet-4-6")
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedPath != "/anthropic/v1/messages" {
		t.Errorf("path = %q, want /anthropic/v1/messages", capturedPath)
	}
}

func TestAzureFoundry_OpenAIEndpointURL(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path

		resp := openaiChatResponse{
			Model: "gpt-4o",
			Choices: []struct {
				Message      openaiChatMsg `json:"message"`
				FinishReason string        `json:"finish_reason"`
			}{
				{Message: openaiChatMsg{Role: "assistant", Content: "ok"}, FinishReason: "stop"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProvider(server.URL, "gpt-4o")
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "gpt-4o",
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedPath != "/openai/v1/chat/completions" {
		t.Errorf("path = %q, want /openai/v1/chat/completions", capturedPath)
	}
}

// --- Validate tests ---

func TestAzureFoundry_Validate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": "hi"}},
			"model":       "claude-sonnet-4-6",
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 5, "output_tokens": 1},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-6")
	err := p.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate should succeed with valid mock: %v", err)
	}
}

func TestAzureFoundry_Validate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error": "permission denied"}`))
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-6")
	err := p.Validate(context.Background())
	if err == nil {
		t.Fatal("Validate should fail with API error")
	}
	if !strings.Contains(err.Error(), "validation failed") {
		t.Errorf("error = %q, should mention validation failed", err.Error())
	}
}

// --- Claude request body verification ---

func TestAzureFoundry_ClaudeChat_RequestBodyMessages(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		resp := map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": "ok"}},
			"model":       "claude-sonnet-4-6",
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 10, "output_tokens": 1},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-6")
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model: "claude-sonnet-4-6",
		Messages: []gollm.Message{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there!"},
			{Role: "user", Content: "How are you?"},
		},
		MaxTokens: 50,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var reqBody map[string]interface{}
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}

	messages, ok := reqBody["messages"].([]interface{})
	if !ok {
		t.Fatal("messages not found in request body")
	}
	if len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(messages))
	}

	msg1 := messages[0].(map[string]interface{})
	if msg1["role"] != "user" {
		t.Errorf("first message role = %q, want user", msg1["role"])
	}
	msg2 := messages[1].(map[string]interface{})
	if msg2["role"] != "assistant" {
		t.Errorf("second message role = %q, want assistant", msg2["role"])
	}

	// Verify model is included in request body
	if reqBody["model"] != "claude-sonnet-4-6" {
		t.Errorf("model = %q, want claude-sonnet-4-6", reqBody["model"])
	}
}

func TestAzureFoundry_ClaudeChat_EmptyContentArray(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"content":     []map[string]string{},
			"model":       "claude-sonnet-4-6",
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 5, "output_tokens": 0},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-6")
	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "" {
		t.Errorf("content = %q, want empty for no content blocks", resp.Content)
	}
}

func TestAzureFoundry_OpenAIChat_MaxTokensAndTemperature(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		resp := openaiChatResponse{
			Model: "gpt-4o",
			Choices: []struct {
				Message      openaiChatMsg `json:"message"`
				FinishReason string        `json:"finish_reason"`
			}{
				{Message: openaiChatMsg{Role: "assistant", Content: "ok"}, FinishReason: "stop"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "gpt-4o")
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:       "gpt-4o",
		Messages:    []gollm.Message{{Role: "user", Content: "Hello"}},
		MaxTokens:   200,
		Temperature: 0.7,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var reqBody openaiChatRequest
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}
	if reqBody.MaxTokens != 200 {
		t.Errorf("max_tokens = %d, want 200", reqBody.MaxTokens)
	}
	if reqBody.Temperature != 0.7 {
		t.Errorf("temperature = %f, want 0.7", reqBody.Temperature)
	}
}
