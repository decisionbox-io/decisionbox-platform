// Package openaicompat contains a shared helper for providers that speak the
// OpenAI /chat/completions schema. Multiple clouds expose this same wire
// format (OpenAI direct, Azure AI Foundry's /openai path, Bedrock for
// Qwen/DeepSeek/Mistral/Llama, Vertex AI Model Garden MaaS endpoints), so
// keeping the request/response shapes and error extraction in one place
// means a new cloud that speaks this wire needs no schema code of its own.
//
// The helper is intentionally minimal: it handles the fields the DecisionBox
// agent uses today (messages, system prompt, max_tokens, temperature, token
// usage, and tool/function calling). Streaming, logprobs, and multi-modal
// content are out of scope — they have no consumer yet and adding them here
// without a consumer would be speculative.
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

	// Tools and ToolChoice are populated only when the caller supplies
	// ChatRequest.Tools. Empty slices/strings are omitted so providers
	// that don't support tool use see exactly the request shape they
	// always did.
	Tools      []Tool      `json:"tools,omitempty"`
	ToolChoice interface{} `json:"tool_choice,omitempty"`
}

// Tool is one entry in the `tools` array. OpenAI distinguishes tool type
// (currently only "function"); keep Type pluggable for future variants.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction describes the callable the model may invoke.
type ToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// Message is one turn in the chat history. For tool-using conversations:
//   - Assistant turns that called a tool have role=assistant and ToolCalls
//     populated; Content may be an empty string.
//   - Tool-result turns have role=tool, ToolCallID bound to the call,
//     and Content carrying the tool output.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Name       string     `json:"name,omitempty"`        // legacy function-call naming
	ToolCallID string     `json:"tool_call_id,omitempty"` // only on role=tool
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall is one tool invocation from the assistant turn. OpenAI returns
// Function.Arguments as a JSON string; callers parse it into a map.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction pairs the function name with a JSON-string argument blob.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
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
//
// When req.Tools is non-empty, tool definitions are attached and each
// gollm.Message.ToolResults entry is emitted as a role=tool turn.
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
		// A user message carrying ToolResults expands into one role=tool
		// message per result. OpenAI rejects role=tool without a preceding
		// assistant turn carrying tool_calls — the caller is responsible
		// for getting the history right; we only translate.
		if len(m.ToolResults) > 0 {
			for _, r := range m.ToolResults {
				messages = append(messages, Message{
					Role:       "tool",
					Content:    r.Content,
					ToolCallID: r.CallID,
				})
			}
			continue
		}

		// Assistant message that invoked tools — echo the tool_calls back
		// so OpenAI can correlate the next role=tool message by ID.
		if len(m.ToolCalls) > 0 {
			tcs := make([]ToolCall, len(m.ToolCalls))
			for i, call := range m.ToolCalls {
				args := "{}"
				if call.Input != nil {
					// Serialize to the JSON-string shape OpenAI expects
					// on the wire. We ignore the error because Input is
					// a plain map; failure modes are impossible here.
					b, _ := json.Marshal(call.Input)
					args = string(b)
				}
				tcs[i] = ToolCall{
					ID:   call.ID,
					Type: "function",
					Function: ToolCallFunction{
						Name:      call.Name,
						Arguments: args,
					},
				}
			}
			messages = append(messages, Message{
				Role:      m.Role,
				Content:   m.Content,
				ToolCalls: tcs,
			})
			continue
		}

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
	if len(req.Tools) > 0 {
		body.Tools = make([]Tool, len(req.Tools))
		for i, t := range req.Tools {
			body.Tools[i] = Tool{
				Type: "function",
				Function: ToolFunction{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.InputSchema,
				},
			}
		}
	}
	if tc := translateToolChoice(req.ToolChoice); tc != nil {
		body.ToolChoice = tc
	}
	return body
}

// translateToolChoice maps the wire-neutral string to OpenAI's
// tool_choice field. "" and "auto" omit the field (OpenAI's default).
// "any"/"required" → "required". "none" → "none". Any other value is
// treated as a specific function name: {type: "function", function: {name: X}}.
func translateToolChoice(choice string) interface{} {
	switch choice {
	case "", "auto":
		return nil
	case "any", "required":
		return "required"
	case "none":
		return "none"
	default:
		return map[string]interface{}{
			"type":     "function",
			"function": map[string]interface{}{"name": choice},
		}
	}
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
	resp := &gollm.ChatResponse{
		Content:    choice.Message.Content,
		Model:      body.Model,
		StopReason: choice.FinishReason,
		Usage: gollm.Usage{
			InputTokens:  body.Usage.PromptTokens,
			OutputTokens: body.Usage.CompletionTokens,
		},
	}
	if len(choice.Message.ToolCalls) > 0 {
		resp.ToolCalls = make([]gollm.ToolCall, 0, len(choice.Message.ToolCalls))
		for _, tc := range choice.Message.ToolCalls {
			if tc.Type != "" && tc.Type != "function" {
				// Unknown tool type — skip rather than misrepresent it.
				continue
			}
			var input map[string]interface{}
			if tc.Function.Arguments != "" {
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &input)
			}
			resp.ToolCalls = append(resp.ToolCalls, gollm.ToolCall{
				ID:    tc.ID,
				Name:  tc.Function.Name,
				Input: input,
			})
		}
	}
	return resp, nil
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
