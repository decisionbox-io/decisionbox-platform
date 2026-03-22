package llm

import "context"

// Provider abstracts LLM chat operations.
// Implement this interface to add support for a new LLM provider
// (e.g., OpenAI, Gemini, Mistral, local models via Ollama).
//
// Selection via LLM_PROVIDER env var (e.g., "claude", "openai").
type Provider interface {
	// Chat sends a conversation to the LLM and returns a response.
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)

	// Validate checks that the provider credentials and configuration are valid.
	// Implementations should use lightweight API calls (e.g., list models)
	// that verify access without consuming tokens.
	Validate(ctx context.Context) error
}

// ChatRequest defines the input for an LLM chat call.
type ChatRequest struct {
	Model        string    // Model ID (e.g., "claude-sonnet-4-20250514", "gpt-4o")
	SystemPrompt string    // System-level instruction (separate from messages for Claude/OpenAI)
	Messages     []Message // Conversation messages
	MaxTokens    int       // Maximum tokens in response
	Temperature  float64   // 0.0 = deterministic, 1.0 = creative
}

// Message represents a single message in a conversation.
type Message struct {
	Role    string // "user" or "assistant"
	Content string
}

// ChatResponse holds the LLM response.
type ChatResponse struct {
	Content    string // Text content of the response
	Model      string // Model that generated the response
	StopReason string // Why generation stopped (e.g., "end_turn", "max_tokens")
	Usage      Usage
}

// Usage tracks token consumption.
type Usage struct {
	InputTokens  int
	OutputTokens int
}
