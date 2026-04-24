package llm

import (
	"context"
	"regexp"
	"strings"
)

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

	// Tools is the optional list of tools the model may call. Providers
	// whose ProviderMeta.SupportsTools is false must return an error when
	// len(Tools) > 0 rather than silently ignoring — callers need to know
	// a capability they relied on isn't available.
	//
	// Wired through Claude (anthropic tool_use blocks), OpenAI / OpenAI-
	// compat (function-calling JSON), and Bedrock (Converse toolConfig).
	// Other providers (Ollama, vertex-ai text models, azure-foundry) reject
	// tools-present requests.
	Tools []ToolDefinition

	// ToolChoice, when non-empty, forces the model's tool-use decision.
	// "auto" (default): model decides.
	// "any" / "required": model must call a tool.
	// "none": model must not call a tool (plain text response).
	// A specific tool name: the model must call that tool.
	ToolChoice string
}

// Message represents a single message in a conversation.
//
// For multi-turn tool-using conversations:
//   - An assistant message that invoked tools has Role="assistant",
//     Content carrying any narrator text, and ToolCalls populated with
//     the tool_use blocks the model emitted. This is necessary so the
//     provider can replay the prior turn back to the server — Anthropic
//     and OpenAI both correlate tool_result to tool_use by ID and reject
//     the next turn if the preceding assistant message lacks them.
//   - The following user message carries ToolResults (one per prior
//     tool_use). Content may be empty.
type Message struct {
	Role    string // "user" or "assistant"
	Content string

	// ToolCalls carries the tool invocations an assistant message
	// previously produced. Only meaningful on Role="assistant".
	// Populate by copying ChatResponse.ToolCalls from the prior turn
	// into the next request's message history.
	ToolCalls []ToolCall

	// ToolResults attaches tool-call outputs from the previous assistant
	// turn. Only meaningful on Role="user". Providers that don't support
	// tools must treat a user message with ToolResults set the same as
	// an error — the caller has mis-wired.
	ToolResults []ToolResult
}

// ChatResponse holds the LLM response.
type ChatResponse struct {
	Content    string // Text content of the response
	Model      string // Model that generated the response
	StopReason string // Why generation stopped ("end_turn", "max_tokens", "tool_use")
	Usage      Usage

	// ToolCalls is populated when StopReason is "tool_use" (or the
	// provider-specific equivalent). Callers should execute each tool
	// and feed the outputs back via Message.ToolResults on the next turn.
	ToolCalls []ToolCall
}

// ToolDefinition describes a tool the model may call. Keep it wire-
// neutral: providers translate this into Anthropic tool_use blocks,
// OpenAI function-calling JSON, or Bedrock Converse toolSpec as needed.
type ToolDefinition struct {
	Name        string
	Description string
	// InputSchema is a JSON Schema object describing the tool's input.
	// The agent owns its shape; providers just pass it through. Keep it
	// small — every token counts against the prompt budget.
	InputSchema map[string]interface{}
}

// ToolCall is one tool invocation the model requested. The caller must
// reply with a ToolResult bearing the same ID on the next turn so the
// provider can correlate.
type ToolCall struct {
	ID    string
	Name  string
	Input map[string]interface{}
}

// ToolResult carries the output of executing a tool. Content is a plain
// string — JSON-encoded blobs are fine; the provider wraps it for the
// specific API's expectations.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// Usage tracks token consumption.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// ErrToolsNotSupported is returned by Provider.Chat when ChatRequest.Tools
// is non-empty on a provider whose ProviderMeta.SupportsTools is false.
// Sentinel so callers can errors.Is against it cleanly.
var ErrToolsNotSupported = sentinelError("tools not supported by this provider")

// sentinelError is a tiny string-backed error used for package-level
// sentinels without adding an "errors" package import here.
type sentinelError string

func (e sentinelError) Error() string { return string(e) }

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

// secretEchoPattern strips strings that look like API keys echoed back
// from an upstream's error response. Some gateways include the request
// headers in 4xx bodies; without this, a 401 "live_error" could leak
// the caller's bearer token into the dashboard.
var secretEchoPattern = regexp.MustCompile(`(?i)(sk-[A-Za-z0-9_\-]+|Bearer\s+[A-Za-z0-9_\-\.]+|x-api-key[:=]\s*[A-Za-z0-9_\-\.]+|api-key[:=]\s*[A-Za-z0-9_\-\.]+)`)

// SanitizeErrorBody trims whitespace, truncates to maxLen runes, and
// masks sequences that look like API keys or Authorization headers so
// the snippet can safely be included in user-facing error messages.
// Intended for provider ListModels and Chat error paths that include
// the raw upstream response body for debuggability.
func SanitizeErrorBody(body []byte, maxLen int) string {
	s := strings.TrimSpace(string(body))
	s = secretEchoPattern.ReplaceAllString(s, "[REDACTED]")
	if maxLen > 0 && len(s) > maxLen {
		s = s[:maxLen] + "..."
	}
	return s
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
