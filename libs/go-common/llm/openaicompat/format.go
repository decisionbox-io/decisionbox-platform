// Package openaicompat contains a shared helper for providers that speak the
// OpenAI /chat/completions schema. Multiple clouds expose this same wire
// format (OpenAI direct, Azure AI Foundry's /openai path, Bedrock for
// Qwen/DeepSeek/Mistral/Llama, Vertex AI Model Garden MaaS endpoints), so
// keeping the request/response shapes and error extraction in one place
// means a new cloud that speaks this wire needs no schema code of its own.
//
// The helper is intentionally minimal: it handles the fields the DecisionBox
// agent uses today (messages, system prompt, max_tokens, temperature, token
// usage). Streaming, tool calls, logprobs and multi-modal content are out of
// scope — they have no consumer yet and adding them here without a consumer
// would be speculative.
package openaicompat

import (
	"encoding/json"
	"fmt"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// RequestBody is the OpenAI /chat/completions request body.
type RequestBody struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
}

// Message is one turn in the chat history.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ResponseBody is the OpenAI /chat/completions response body.
type ResponseBody struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
	Error   *APIError `json:"error,omitempty"`
}

// Choice is one response candidate. OpenAI-compat APIs always return the
// assistant response in choices[0]; choices[>0] is never populated in
// non-streaming, non-n>1 calls, which is the only mode the agent uses.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage holds token counts reported by the server.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// APIError is the error envelope returned by OpenAI-compatible servers when
// the HTTP status is non-2xx. Different backends populate different fields;
// callers should not rely on any one being non-empty.
type APIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
	Param   string `json:"param"`
}

// Error implements the error interface.
func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if e.Type != "" {
		return e.Type + ": " + e.Message
	}
	return e.Message
}

// BuildRequestBody converts a neutral gollm.ChatRequest to the OpenAI
// wire format. The system prompt is placed as a leading {role:"system"}
// message — this is how every OpenAI-compat server the agent talks to
// expects it (there is no top-level "system" field on this wire).
//
// Callers must supply the concrete model ID; the request's Model field is
// used only when non-empty (providers substitute their default otherwise).
func BuildRequestBody(model string, req gollm.ChatRequest) RequestBody {
	effectiveModel := req.Model
	if effectiveModel == "" {
		effectiveModel = model
	}

	messages := make([]Message, 0, len(req.Messages)+1)
	if req.SystemPrompt != "" {
		messages = append(messages, Message{Role: "system", Content: req.SystemPrompt})
	}
	for _, m := range req.Messages {
		messages = append(messages, Message{Role: m.Role, Content: m.Content})
	}

	body := RequestBody{
		Model:    effectiveModel,
		Messages: messages,
	}
	if req.MaxTokens > 0 {
		body.MaxTokens = req.MaxTokens
	}
	if req.Temperature > 0 {
		body.Temperature = req.Temperature
	}
	return body
}

// ParseResponseBody parses the response body bytes into a neutral
// gollm.ChatResponse. Returns an error if:
//   - the bytes are not valid JSON
//   - the decoded body has zero choices (implies a malformed server response)
//
// Missing usage is tolerated (returns zeros) because some proxies strip it.
func ParseResponseBody(raw []byte) (*gollm.ChatResponse, error) {
	var body ResponseBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if len(body.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	choice := body.Choices[0]
	return &gollm.ChatResponse{
		Content:    choice.Message.Content,
		Model:      body.Model,
		StopReason: choice.FinishReason,
		Usage: gollm.Usage{
			InputTokens:  body.Usage.PromptTokens,
			OutputTokens: body.Usage.CompletionTokens,
		},
	}, nil
}

// ExtractAPIError tries to parse an OpenAI-style error envelope from a
// non-2xx response body. Returns nil if the body is not JSON, does not
// contain an "error" object, or the error has no message. Callers should
// fall back to a raw-body error when this returns nil.
func ExtractAPIError(raw []byte) *APIError {
	var body ResponseBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil
	}
	if body.Error == nil {
		return nil
	}
	if body.Error.Message == "" && body.Error.Type == "" {
		return nil
	}
	return body.Error
}
