package ollama

import (
	"context"
	"fmt"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	ollamaapi "github.com/ollama/ollama/api"
)

// mockOllamaClient implements ollamaClient for unit testing.
type mockOllamaClient struct {
	chatResp    ollamaapi.ChatResponse
	chatErr     error
	listResp    *ollamaapi.ListResponse
	listErr     error
	lastChatReq *ollamaapi.ChatRequest
}

func (m *mockOllamaClient) Chat(_ context.Context, req *ollamaapi.ChatRequest, fn ollamaapi.ChatResponseFunc) error {
	m.lastChatReq = req
	if m.chatErr != nil {
		return m.chatErr
	}
	return fn(m.chatResp)
}

func (m *mockOllamaClient) List(_ context.Context) (*ollamaapi.ListResponse, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResp, nil
}

// newMockOllamaProvider creates an OllamaProvider backed by a mock client.
func newMockOllamaProvider(mock *mockOllamaClient, model string) *OllamaProvider {
	return &OllamaProvider{
		client: mock,
		model:  model,
	}
}

func TestOllamaProvider_Chat_Success(t *testing.T) {
	mock := &mockOllamaClient{
		chatResp: ollamaapi.ChatResponse{
			Model: "qwen2.5:7b",
			Message: ollamaapi.Message{
				Role:    "assistant",
				Content: "Hello from Ollama!",
			},
			Done:       true,
			DoneReason: "stop",
			Metrics: ollamaapi.Metrics{
				PromptEvalCount: 12,
				EvalCount:       6,
			},
		},
	}
	p := newMockOllamaProvider(mock, "qwen2.5:7b")

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello from Ollama!" {
		t.Errorf("content = %q, want %q", resp.Content, "Hello from Ollama!")
	}
	if resp.Model != "qwen2.5:7b" {
		t.Errorf("model = %q, want qwen2.5:7b", resp.Model)
	}
	if resp.StopReason != "stop" {
		t.Errorf("stop_reason = %q, want stop", resp.StopReason)
	}
	if resp.Usage.InputTokens != 12 {
		t.Errorf("input_tokens = %d, want 12", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 6 {
		t.Errorf("output_tokens = %d, want 6", resp.Usage.OutputTokens)
	}
}

func TestOllamaProvider_Chat_Error(t *testing.T) {
	mock := &mockOllamaClient{
		chatErr: fmt.Errorf("connection refused"),
	}
	p := newMockOllamaProvider(mock, "qwen2.5:7b")

	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err == nil {
		t.Fatal("expected error for chat failure")
	}
	if !containsStr(err.Error(), "chat failed") {
		t.Errorf("error = %q, should mention chat failed", err.Error())
	}
}

func TestOllamaProvider_Chat_SystemPrompt(t *testing.T) {
	mock := &mockOllamaClient{
		chatResp: ollamaapi.ChatResponse{
			Model: "qwen2.5:7b",
			Message: ollamaapi.Message{
				Role:    "assistant",
				Content: "4",
			},
			Done:       true,
			DoneReason: "stop",
		},
	}
	p := newMockOllamaProvider(mock, "qwen2.5:7b")

	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		SystemPrompt: "You are a calculator. Only respond with numbers.",
		Messages:     []gollm.Message{{Role: "user", Content: "What is 2+2?"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify system prompt was added as the first message
	if len(mock.lastChatReq.Messages) < 2 {
		t.Fatalf("expected at least 2 messages (system + user), got %d", len(mock.lastChatReq.Messages))
	}
	if mock.lastChatReq.Messages[0].Role != "system" {
		t.Errorf("first message role = %q, want system", mock.lastChatReq.Messages[0].Role)
	}
	if mock.lastChatReq.Messages[0].Content != "You are a calculator. Only respond with numbers." {
		t.Errorf("system content = %q", mock.lastChatReq.Messages[0].Content)
	}
	if mock.lastChatReq.Messages[1].Role != "user" {
		t.Errorf("second message role = %q, want user", mock.lastChatReq.Messages[1].Role)
	}
}

func TestOllamaProvider_Chat_TokenCounting(t *testing.T) {
	mock := &mockOllamaClient{
		chatResp: ollamaapi.ChatResponse{
			Model: "qwen2.5:7b",
			Message: ollamaapi.Message{
				Role:    "assistant",
				Content: "A long response text...",
			},
			Done:       true,
			DoneReason: "length",
			Metrics: ollamaapi.Metrics{
				PromptEvalCount: 200,
				EvalCount:       150,
			},
		},
	}
	p := newMockOllamaProvider(mock, "qwen2.5:7b")

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages:  []gollm.Message{{Role: "user", Content: "Tell me a story"}},
		MaxTokens: 150,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage.InputTokens != 200 {
		t.Errorf("input_tokens = %d, want 200", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 150 {
		t.Errorf("output_tokens = %d, want 150", resp.Usage.OutputTokens)
	}
}

func TestOllamaProvider_Validate_ModelFound(t *testing.T) {
	mock := &mockOllamaClient{
		listResp: &ollamaapi.ListResponse{
			Models: []ollamaapi.ListModelResponse{
				{Name: "llama3:8b"},
				{Name: "qwen2.5:7b"},
				{Name: "mistral:latest"},
			},
		},
	}
	p := newMockOllamaProvider(mock, "qwen2.5:7b")

	err := p.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate should succeed when model is found: %v", err)
	}
}

func TestOllamaProvider_Validate_ModelNotFound(t *testing.T) {
	mock := &mockOllamaClient{
		listResp: &ollamaapi.ListResponse{
			Models: []ollamaapi.ListModelResponse{
				{Name: "llama3:8b"},
				{Name: "mistral:latest"},
			},
		},
	}
	p := newMockOllamaProvider(mock, "qwen2.5:7b")

	err := p.Validate(context.Background())
	if err == nil {
		t.Fatal("Validate should fail when model is not found")
	}
	if !containsStr(err.Error(), "not found") {
		t.Errorf("error = %q, should mention not found", err.Error())
	}
	if !containsStr(err.Error(), "qwen2.5:7b") {
		t.Errorf("error = %q, should mention the missing model name", err.Error())
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
