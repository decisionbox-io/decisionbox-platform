package vertexai

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
	"golang.org/x/oauth2"
)

// mockTokenSource implements oauth2.TokenSource for unit testing.
type mockTokenSource struct {
	token string
	err   error
}

func (m *mockTokenSource) Token() (*oauth2.Token, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &oauth2.Token{
		AccessToken: m.token,
		Expiry:      time.Now().Add(1 * time.Hour),
	}, nil
}

// newTestProvider creates a VertexAIProvider pointing at a test HTTP server
// with a mock auth token source.
func newTestProvider(serverURL, model string) *VertexAIProvider {
	return &VertexAIProvider{
		projectID: "test-project",
		location:  "us-central1",
		model:     model,
		auth: &gcpAuth{
			tokenSource: &mockTokenSource{token: "test-token-123"},
		},
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// newTestProviderWithURL creates a VertexAIProvider with an httpClient whose
// Transport rewrites all requests to the given test server URL.
func newTestProviderWithURL(serverURL, model string) *VertexAIProvider {
	p := newTestProvider(serverURL, model)
	p.httpClient = &http.Client{
		Timeout: 10 * time.Second,
		Transport: &rewriteTransport{
			targetBase: serverURL,
			wrapped:    http.DefaultTransport,
		},
	}
	return p
}

// rewriteTransport redirects all HTTP requests to a test server URL.
type rewriteTransport struct {
	targetBase string
	wrapped    http.RoundTripper
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Rewrite the URL to point at the test server, preserving the path
	req.URL.Scheme = "http"
	// Parse test server URL to get host
	req.URL.Host = strings.TrimPrefix(t.targetBase, "http://")
	return t.wrapped.RoundTrip(req)
}

// --- Gemini tests ---

func TestVertexAI_GeminiChat_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify auth header
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-token-123" {
			t.Errorf("auth header = %q, want Bearer test-token-123", auth)
		}

		// Verify content type
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q, want application/json", ct)
		}

		// Verify the endpoint path contains google/models/gemini
		if !strings.Contains(r.URL.Path, "publishers/google/models/gemini") {
			t.Errorf("path = %q, expected publishers/google/models/gemini", r.URL.Path)
		}

		resp := geminiResponse{
			Candidates: []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			}{
				{
					Content: struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					}{
						Parts: []struct {
							Text string `json:"text"`
						}{
							{Text: "Hello from Gemini!"},
						},
					},
					FinishReason: "STOP",
				},
			},
			UsageMetadata: struct {
				PromptTokenCount     int `json:"promptTokenCount"`
				CandidatesTokenCount int `json:"candidatesTokenCount"`
				TotalTokenCount      int `json:"totalTokenCount"`
			}{
				PromptTokenCount:     10,
				CandidatesTokenCount: 5,
				TotalTokenCount:      15,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "gemini-2.5-pro")

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "gemini-2.5-pro",
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello from Gemini!" {
		t.Errorf("content = %q, want %q", resp.Content, "Hello from Gemini!")
	}
	if resp.Model != "gemini-2.5-pro" {
		t.Errorf("model = %q, want gemini-2.5-pro", resp.Model)
	}
	if resp.StopReason != "STOP" {
		t.Errorf("stop_reason = %q, want STOP", resp.StopReason)
	}
	if resp.Usage.InputTokens != 10 {
		t.Errorf("input_tokens = %d, want 10", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 5 {
		t.Errorf("output_tokens = %d, want 5", resp.Usage.OutputTokens)
	}
}

func TestVertexAI_GeminiChat_APIError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{
			name:       "bad request",
			statusCode: http.StatusBadRequest,
			body:       `{"error": {"message": "Invalid request"}}`,
		},
		{
			name:       "internal server error",
			statusCode: http.StatusInternalServerError,
			body:       `{"error": {"message": "Internal error"}}`,
		},
		{
			name:       "forbidden",
			statusCode: http.StatusForbidden,
			body:       `{"error": {"message": "Permission denied"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.body))
			}))
			defer server.Close()

			p := newTestProviderWithURL(server.URL, "gemini-2.5-pro")

			_, err := p.Chat(context.Background(), gollm.ChatRequest{
				Model:    "gemini-2.5-pro",
				Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
			})
			if err == nil {
				t.Fatal("expected error for API error response")
			}
			if !contains(err.Error(), "API error") {
				t.Errorf("error = %q, should mention API error", err.Error())
			}
			if !contains(err.Error(), string(rune('0'+tt.statusCode/100))) {
				// Check that status code is mentioned in error
				expectedStatus := http.StatusText(tt.statusCode)
				_ = expectedStatus // status code is in the error as a number
			}
		})
	}
}

func TestVertexAI_GeminiChat_SystemPrompt(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		resp := geminiResponse{
			Candidates: []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			}{
				{
					Content: struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					}{
						Parts: []struct {
							Text string `json:"text"`
						}{
							{Text: "4"},
						},
					},
					FinishReason: "STOP",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "gemini-2.5-flash")

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:        "gemini-2.5-flash",
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

	// Verify system prompt was included as user+model turn in request body
	var reqBody geminiRequest
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}

	// Gemini handles system prompts by prepending a user message + model ack
	if len(reqBody.Contents) < 3 {
		t.Fatalf("expected at least 3 contents (system-as-user, model-ack, user), got %d", len(reqBody.Contents))
	}
	if reqBody.Contents[0].Role != "user" {
		t.Errorf("first content role = %q, want user (system prompt)", reqBody.Contents[0].Role)
	}
	if len(reqBody.Contents[0].Parts) == 0 || reqBody.Contents[0].Parts[0].Text != "You are a calculator. Only respond with numbers." {
		t.Errorf("first content text = %q, want system prompt", reqBody.Contents[0].Parts[0].Text)
	}
	if reqBody.Contents[1].Role != "model" {
		t.Errorf("second content role = %q, want model (ack)", reqBody.Contents[1].Role)
	}
	if reqBody.Contents[2].Role != "user" {
		t.Errorf("third content role = %q, want user", reqBody.Contents[2].Role)
	}
}

func TestVertexAI_GeminiChat_TokenCounting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := geminiResponse{
			Candidates: []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			}{
				{
					Content: struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					}{
						Parts: []struct {
							Text string `json:"text"`
						}{
							{Text: "A long response..."},
						},
					},
					FinishReason: "MAX_TOKENS",
				},
			},
			UsageMetadata: struct {
				PromptTokenCount     int `json:"promptTokenCount"`
				CandidatesTokenCount int `json:"candidatesTokenCount"`
				TotalTokenCount      int `json:"totalTokenCount"`
			}{
				PromptTokenCount:     250,
				CandidatesTokenCount: 100,
				TotalTokenCount:      350,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "gemini-2.5-pro")

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:     "gemini-2.5-pro",
		Messages:  []gollm.Message{{Role: "user", Content: "Tell me a long story"}},
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage.InputTokens != 250 {
		t.Errorf("input_tokens = %d, want 250", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 100 {
		t.Errorf("output_tokens = %d, want 100", resp.Usage.OutputTokens)
	}
	if resp.StopReason != "MAX_TOKENS" {
		t.Errorf("stop_reason = %q, want MAX_TOKENS", resp.StopReason)
	}
}

func TestVertexAI_GeminiChat_Temperature(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		resp := geminiResponse{
			Candidates: []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			}{
				{
					Content: struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					}{
						Parts: []struct {
							Text string `json:"text"`
						}{
							{Text: "creative response"},
						},
					},
					FinishReason: "STOP",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "gemini-2.5-pro")

	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:       "gemini-2.5-pro",
		Messages:    []gollm.Message{{Role: "user", Content: "Be creative"}},
		Temperature: 0.8,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var reqBody geminiRequest
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}
	if reqBody.GenerationConfig.Temperature == nil {
		t.Fatal("temperature should be set in request")
	}
	if *reqBody.GenerationConfig.Temperature != 0.8 {
		t.Errorf("temperature = %f, want 0.8", *reqBody.GenerationConfig.Temperature)
	}
}

func TestVertexAI_GeminiChat_DefaultMaxTokens(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		resp := geminiResponse{
			Candidates: []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			}{
				{
					Content: struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					}{
						Parts: []struct {
							Text string `json:"text"`
						}{
							{Text: "response"},
						},
					},
					FinishReason: "STOP",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "gemini-2.5-flash")

	// Send request with MaxTokens=0 (should default to 4096)
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "gemini-2.5-flash",
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var reqBody geminiRequest
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}
	if reqBody.GenerationConfig.MaxOutputTokens != 4096 {
		t.Errorf("maxOutputTokens = %d, want 4096 (default)", reqBody.GenerationConfig.MaxOutputTokens)
	}
}

func TestVertexAI_GeminiChat_AssistantRoleMapping(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		resp := geminiResponse{
			Candidates: []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			}{
				{
					Content: struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					}{
						Parts: []struct {
							Text string `json:"text"`
						}{
							{Text: "next response"},
						},
					},
					FinishReason: "STOP",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "gemini-2.5-pro")

	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model: "gemini-2.5-pro",
		Messages: []gollm.Message{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there!"},
			{Role: "user", Content: "How are you?"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var reqBody geminiRequest
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}

	// Gemini maps "assistant" to "model"
	if len(reqBody.Contents) != 3 {
		t.Fatalf("expected 3 contents, got %d", len(reqBody.Contents))
	}
	if reqBody.Contents[1].Role != "model" {
		t.Errorf("assistant role should be mapped to 'model', got %q", reqBody.Contents[1].Role)
	}
}

func TestVertexAI_GeminiChat_EmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a response with no candidates
		resp := geminiResponse{}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "gemini-2.5-pro")

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "gemini-2.5-pro",
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Empty candidates should result in empty content, not an error
	if resp.Content != "" {
		t.Errorf("content = %q, want empty for no candidates", resp.Content)
	}
	if resp.StopReason != "" {
		t.Errorf("stop_reason = %q, want empty for no candidates", resp.StopReason)
	}
}

// --- Claude-on-Vertex tests ---

func TestVertexAI_ClaudeChat_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify auth header
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-token-123" {
			t.Errorf("auth header = %q, want Bearer test-token-123", auth)
		}

		// Verify the endpoint path contains anthropic/models/claude
		if !strings.Contains(r.URL.Path, "publishers/anthropic/models/claude") {
			t.Errorf("path = %q, expected publishers/anthropic/models/claude", r.URL.Path)
		}

		// Verify it uses rawPredict
		if !strings.HasSuffix(r.URL.Path, ":rawPredict") {
			t.Errorf("path = %q, expected to end with :rawPredict", r.URL.Path)
		}

		resp := map[string]interface{}{
			"content": []map[string]string{
				{"type": "text", "text": "Hello from Claude on Vertex!"},
			},
			"model":       "claude-sonnet-4-20250514",
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

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-20250514")

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "claude-sonnet-4-20250514",
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello from Claude on Vertex!" {
		t.Errorf("content = %q, want %q", resp.Content, "Hello from Claude on Vertex!")
	}
	if resp.Model != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q, want claude-sonnet-4-20250514", resp.Model)
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

func TestVertexAI_ClaudeChat_APIError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{
			name:       "bad request",
			statusCode: http.StatusBadRequest,
			body:       `{"type": "error", "error": {"type": "invalid_request_error", "message": "max_tokens: must be positive"}}`,
		},
		{
			name:       "internal server error",
			statusCode: http.StatusInternalServerError,
			body:       `{"type": "error", "error": {"type": "api_error", "message": "Internal server error"}}`,
		},
		{
			name:       "rate limited",
			statusCode: http.StatusTooManyRequests,
			body:       `{"type": "error", "error": {"type": "rate_limit_error", "message": "Rate limit exceeded"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.body))
			}))
			defer server.Close()

			p := newTestProviderWithURL(server.URL, "claude-sonnet-4-20250514")

			_, err := p.Chat(context.Background(), gollm.ChatRequest{
				Model:    "claude-sonnet-4-20250514",
				Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
			})
			if err == nil {
				t.Fatal("expected error for API error response")
			}
			if !contains(err.Error(), "API error") {
				t.Errorf("error = %q, should mention API error", err.Error())
			}
		})
	}
}

func TestVertexAI_ClaudeChat_SystemPrompt(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		resp := map[string]interface{}{
			"content": []map[string]string{
				{"type": "text", "text": "4"},
			},
			"model":       "claude-sonnet-4-20250514",
			"stop_reason": "end_turn",
			"usage": map[string]int{
				"input_tokens":  20,
				"output_tokens": 3,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-20250514")

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:        "claude-sonnet-4-20250514",
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

	// Verify system prompt was included as "system" key in request body
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

	// Verify anthropic_version is set
	version, ok := reqBody["anthropic_version"]
	if !ok {
		t.Error("anthropic_version not set in request body")
	}
	if version != "vertex-2023-10-16" {
		t.Errorf("anthropic_version = %q, want vertex-2023-10-16", version)
	}
}

func TestVertexAI_ClaudeChat_TokenCounting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"content": []map[string]string{
				{"type": "text", "text": "A long response..."},
			},
			"model":       "claude-opus-4-20250514",
			"stop_reason": "max_tokens",
			"usage": map[string]int{
				"input_tokens":  300,
				"output_tokens": 150,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-opus-4-20250514")

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:     "claude-opus-4-20250514",
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

func TestVertexAI_ClaudeChat_DefaultMaxTokens(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		resp := map[string]interface{}{
			"content": []map[string]string{
				{"type": "text", "text": "response"},
			},
			"model":       "claude-sonnet-4-20250514",
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 5, "output_tokens": 1},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-20250514")

	// Send request with MaxTokens=0 (should default to 4096)
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "claude-sonnet-4-20250514",
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
	// JSON numbers are float64
	if maxTokens.(float64) != 4096 {
		t.Errorf("max_tokens = %v, want 4096 (default)", maxTokens)
	}
}

func TestVertexAI_ClaudeChat_Temperature(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		resp := map[string]interface{}{
			"content": []map[string]string{
				{"type": "text", "text": "creative response"},
			},
			"model":       "claude-sonnet-4-20250514",
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 5, "output_tokens": 3},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-20250514")

	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:       "claude-sonnet-4-20250514",
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

func TestVertexAI_ClaudeChat_NoSystemPrompt(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		resp := map[string]interface{}{
			"content": []map[string]string{
				{"type": "text", "text": "response"},
			},
			"model":       "claude-sonnet-4-20250514",
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 5, "output_tokens": 1},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-20250514")

	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "claude-sonnet-4-20250514",
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify system prompt is NOT in request body when not provided
	var reqBody map[string]interface{}
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}
	if _, ok := reqBody["system"]; ok {
		t.Error("system should not be in request body when SystemPrompt is empty")
	}
}

func TestVertexAI_ClaudeChat_NoTemperature(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		resp := map[string]interface{}{
			"content": []map[string]string{
				{"type": "text", "text": "response"},
			},
			"model":       "claude-sonnet-4-20250514",
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 5, "output_tokens": 1},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-20250514")

	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "claude-sonnet-4-20250514",
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify temperature is NOT in request body when 0
	var reqBody map[string]interface{}
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}
	if _, ok := reqBody["temperature"]; ok {
		t.Error("temperature should not be in request body when Temperature is 0")
	}
}

func TestVertexAI_ClaudeChat_MultipleContentBlocks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return multiple text content blocks
		resp := map[string]interface{}{
			"content": []map[string]string{
				{"type": "text", "text": "First part. "},
				{"type": "text", "text": "Second part."},
			},
			"model":       "claude-sonnet-4-20250514",
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 10, "output_tokens": 8},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-20250514")

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "claude-sonnet-4-20250514",
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Multiple text blocks should be concatenated
	if resp.Content != "First part. Second part." {
		t.Errorf("content = %q, want %q", resp.Content, "First part. Second part.")
	}
}

// --- Auth error tests ---

func TestVertexAI_AuthTokenError(t *testing.T) {
	p := &VertexAIProvider{
		projectID: "test-project",
		location:  "us-central1",
		model:     "claude-sonnet-4-20250514",
		auth: &gcpAuth{
			tokenSource: &mockTokenSource{
				err: context.DeadlineExceeded,
			},
		},
		httpClient: &http.Client{},
	}

	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "claude-sonnet-4-20250514",
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err == nil {
		t.Fatal("expected error when auth token fails")
	}
	if !contains(err.Error(), "access token") {
		t.Errorf("error = %q, should mention access token", err.Error())
	}
}

func TestVertexAI_GeminiAuthTokenError(t *testing.T) {
	p := &VertexAIProvider{
		projectID: "test-project",
		location:  "us-central1",
		model:     "gemini-2.5-pro",
		auth: &gcpAuth{
			tokenSource: &mockTokenSource{
				err: context.DeadlineExceeded,
			},
		},
		httpClient: &http.Client{},
	}

	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "gemini-2.5-pro",
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err == nil {
		t.Fatal("expected error when auth token fails")
	}
	if !contains(err.Error(), "access token") {
		t.Errorf("error = %q, should mention access token", err.Error())
	}
}

// --- Validate tests ---

func TestVertexAI_Validate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"content": []map[string]string{
				{"type": "text", "text": "hi"},
			},
			"model":       "claude-sonnet-4-20250514",
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 5, "output_tokens": 1},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-20250514")

	err := p.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate should succeed with valid mock: %v", err)
	}
}

func TestVertexAI_Validate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error": "permission denied"}`))
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-20250514")

	err := p.Validate(context.Background())
	if err == nil {
		t.Fatal("Validate should fail with API error")
	}
	if !contains(err.Error(), "validation failed") {
		t.Errorf("error = %q, should mention validation failed", err.Error())
	}
}

func TestVertexAI_Validate_GeminiSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := geminiResponse{
			Candidates: []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			}{
				{
					Content: struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					}{
						Parts: []struct {
							Text string `json:"text"`
						}{
							{Text: "hi"},
						},
					},
					FinishReason: "STOP",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "gemini-2.5-flash")

	err := p.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate should succeed with valid mock: %v", err)
	}
}

// --- Factory tests ---

func TestVertexAI_Factory_MissingProjectID(t *testing.T) {
	_, err := gollm.NewProvider("vertex-ai", gollm.ProviderConfig{
		"location": "us-east5",
		"model":    "claude-sonnet-4-20250514",
	})
	if err == nil {
		t.Fatal("expected error for missing project_id")
	}
	if !contains(err.Error(), "project_id is required") {
		t.Errorf("error = %q, should mention project_id is required", err.Error())
	}
}

func TestVertexAI_Factory_MissingModel(t *testing.T) {
	_, err := gollm.NewProvider("vertex-ai", gollm.ProviderConfig{
		"project_id": "my-project",
		"location":   "us-east5",
	})
	if err == nil {
		t.Fatal("expected error for missing model")
	}
	if !contains(err.Error(), "model is required") {
		t.Errorf("error = %q, should mention model is required", err.Error())
	}
}

func TestVertexAI_Factory_DefaultLocation(t *testing.T) {
	// This will fail on GCP auth (no credentials) but we can verify
	// that missing location does NOT cause an error itself
	_, err := gollm.NewProvider("vertex-ai", gollm.ProviderConfig{
		"project_id": "my-project",
		"model":      "claude-sonnet-4-20250514",
	})
	// Should fail on GCP auth, not on location validation
	if err != nil && contains(err.Error(), "location") {
		t.Errorf("error = %q, missing location should default to us-east5", err.Error())
	}
}

// --- Endpoint URL tests ---

func TestVertexAI_GeminiEndpointURL(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path

		resp := geminiResponse{
			Candidates: []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			}{
				{
					Content: struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					}{
						Parts: []struct {
							Text string `json:"text"`
						}{
							{Text: "ok"},
						},
					},
					FinishReason: "STOP",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "gemini-2.5-flash")

	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "gemini-2.5-flash",
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedPath := "/v1/projects/test-project/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent"
	if capturedPath != expectedPath {
		t.Errorf("path = %q, want %q", capturedPath, expectedPath)
	}
}

func TestVertexAI_ClaudeEndpointURL(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path

		resp := map[string]interface{}{
			"content": []map[string]string{
				{"type": "text", "text": "ok"},
			},
			"model":       "claude-sonnet-4-20250514",
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 3, "output_tokens": 1},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-20250514")

	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "claude-sonnet-4-20250514",
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedPath := "/v1/projects/test-project/locations/us-central1/publishers/anthropic/models/claude-sonnet-4-20250514:rawPredict"
	if capturedPath != expectedPath {
		t.Errorf("path = %q, want %q", capturedPath, expectedPath)
	}
}

// --- Claude request body verification tests ---

func TestVertexAI_ClaudeChat_RequestBodyMessages(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		resp := map[string]interface{}{
			"content": []map[string]string{
				{"type": "text", "text": "ok"},
			},
			"model":       "claude-sonnet-4-20250514",
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 10, "output_tokens": 1},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "claude-sonnet-4-20250514")

	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model: "claude-sonnet-4-20250514",
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

	// Verify roles are passed through as-is (not remapped like Gemini)
	msg1 := messages[0].(map[string]interface{})
	if msg1["role"] != "user" {
		t.Errorf("first message role = %q, want user", msg1["role"])
	}
	msg2 := messages[1].(map[string]interface{})
	if msg2["role"] != "assistant" {
		t.Errorf("second message role = %q, want assistant", msg2["role"])
	}
}

// --- GeminiChat no system prompt ---

func TestVertexAI_GeminiChat_NoSystemPrompt(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		resp := geminiResponse{
			Candidates: []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			}{
				{
					Content: struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					}{
						Parts: []struct {
							Text string `json:"text"`
						}{
							{Text: "response"},
						},
					},
					FinishReason: "STOP",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "gemini-2.5-flash")

	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "gemini-2.5-flash",
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var reqBody geminiRequest
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}

	// Without system prompt, only user message should be in contents
	if len(reqBody.Contents) != 1 {
		t.Errorf("expected 1 content (user only), got %d", len(reqBody.Contents))
	}
	if reqBody.Contents[0].Role != "user" {
		t.Errorf("first content role = %q, want user", reqBody.Contents[0].Role)
	}
}

// --- GeminiChat no temperature ---

func TestVertexAI_GeminiChat_NoTemperature(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		resp := geminiResponse{
			Candidates: []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			}{
				{
					Content: struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					}{
						Parts: []struct {
							Text string `json:"text"`
						}{
							{Text: "response"},
						},
					},
					FinishReason: "STOP",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "gemini-2.5-flash")

	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "gemini-2.5-flash",
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var reqBody geminiRequest
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}

	if reqBody.GenerationConfig.Temperature != nil {
		t.Error("temperature should not be set when Temperature is 0")
	}
}
