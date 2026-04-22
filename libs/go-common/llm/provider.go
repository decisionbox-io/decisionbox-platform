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

// ModelLister is an optional capability interface: providers that can list
// the models available under the caller's credentials implement it.
// Providers without an upstream list endpoint (e.g. Ollama's /api/tags
// is implemented; a custom self-hosted OpenAI-compat gateway might not
// be) should either not implement this interface or return an empty
// slice with a nil error.
//
// ListModels is read-only. Implementations must not consume tokens; they
// should use the provider's list endpoint (GET /v1/models,
// ListFoundationModels, etc.). A failure here must never block project
// creation — the handler returns the catalog as a fallback.
type ModelLister interface {
	ListModels(ctx context.Context) ([]RemoteModel, error)
}

// RemoteModel is one row returned by an upstream list endpoint.
// DisplayName is optional — not every upstream exposes one, so the
// dashboard falls back to the ID when rendering.
type RemoteModel struct {
	ID          string
	DisplayName string
	// Lifecycle is a free-form status string from the upstream. Known
	// values include "ACTIVE", "LEGACY", "INTERNAL_TESTING". Empty when
	// the upstream does not expose lifecycle. The dashboard can use this
	// to hide deprecated models.
	Lifecycle string
}
