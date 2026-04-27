package bedrock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// chatAnthropic sends a request to a Claude model on Bedrock using
// InvokeModel. Uses the Anthropic Messages API format with the
// bedrock-specific anthropic_version string baked into the JSON body
// (Bedrock does not accept the anthropic-version HTTP header that
// api.anthropic.com expects).
//
// When ChatRequest.Tools is non-empty the request includes tool_use blocks
// and the response is parsed for tool_use content; ChatResponse.ToolCalls
// carries the parsed calls. Mirrors providers/llm/claude for the on-wire
// format — they talk the same Anthropic Messages API, just at different
// endpoints.
func (p *BedrockProvider) chatAnthropic(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	messages, err := buildAnthropicMessages(req.Messages)
	if err != nil {
		return nil, fmt.Errorf("bedrock/anthropic: %w", err)
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	body := map[string]interface{}{
		"anthropic_version": "bedrock-2023-05-31",
		"messages":          messages,
		"max_tokens":        maxTokens,
	}

	if req.SystemPrompt != "" {
		body["system"] = req.SystemPrompt
	}

	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}

	if len(req.Tools) > 0 {
		body["tools"] = buildAnthropicTools(req.Tools)
	}
	if tc := buildAnthropicToolChoice(req.ToolChoice); tc != nil {
		body["tool_choice"] = tc
	}

	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("bedrock/anthropic: failed to marshal request: %w", err)
	}

	output, err := p.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(req.Model),
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
		Body:        reqBody,
	})
	if err != nil {
		return nil, fmt.Errorf("bedrock/anthropic: InvokeModel failed: %w", err)
	}

	var anthropicResp struct {
		Content []struct {
			Type string `json:"type"`
			// text
			Text string `json:"text,omitempty"`
			// tool_use
			ID    string          `json:"id,omitempty"`
			Name  string          `json:"name,omitempty"`
			Input json.RawMessage `json:"input,omitempty"`
		} `json:"content"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}

	if err := json.NewDecoder(bytes.NewReader(output.Body)).Decode(&anthropicResp); err != nil {
		return nil, fmt.Errorf("bedrock/anthropic: failed to parse response: %w", err)
	}

	content := ""
	var toolCalls []gollm.ToolCall
	for _, block := range anthropicResp.Content {
		switch block.Type {
		case "text":
			content += block.Text
		case "tool_use":
			var input map[string]interface{}
			if len(block.Input) > 0 {
				_ = json.Unmarshal(block.Input, &input)
			}
			toolCalls = append(toolCalls, gollm.ToolCall{
				ID:    block.ID,
				Name:  block.Name,
				Input: input,
			})
		}
	}

	return &gollm.ChatResponse{
		Content:    content,
		Model:      anthropicResp.Model,
		StopReason: anthropicResp.StopReason,
		Usage: gollm.Usage{
			InputTokens:  anthropicResp.Usage.InputTokens,
			OutputTokens: anthropicResp.Usage.OutputTokens,
		},
		ToolCalls: toolCalls,
	}, nil
}

// buildAnthropicMessages translates the neutral Message shape into
// Anthropic's content-block format. Simple text messages stay as a
// {role, content: "text"} pair; tool-result messages expand into a
// content-array with tool_result blocks; and assistant messages with
// ToolCalls populated are echoed back as tool_use content blocks so
// Anthropic can correlate the next turn's tool_result.tool_use_id.
func buildAnthropicMessages(msgs []gollm.Message) ([]map[string]interface{}, error) {
	out := make([]map[string]interface{}, 0, len(msgs))
	for _, m := range msgs {
		if len(m.ToolResults) > 0 {
			if m.Role != "user" {
				return nil, fmt.Errorf("tool_results may only accompany role=user, got %q", m.Role)
			}
			blocks := make([]map[string]interface{}, 0, len(m.ToolResults)+1)
			for _, r := range m.ToolResults {
				b := map[string]interface{}{
					"type":        "tool_result",
					"tool_use_id": r.CallID,
					"content":     r.Content,
				}
				if r.IsError {
					b["is_error"] = true
				}
				blocks = append(blocks, b)
			}
			if m.Content != "" {
				blocks = append(blocks, map[string]interface{}{"type": "text", "text": m.Content})
			}
			out = append(out, map[string]interface{}{"role": m.Role, "content": blocks})
			continue
		}

		if len(m.ToolCalls) > 0 {
			if m.Role != "assistant" {
				return nil, fmt.Errorf("tool_calls may only accompany role=assistant, got %q", m.Role)
			}
			blocks := make([]map[string]interface{}, 0, len(m.ToolCalls)+1)
			if m.Content != "" {
				blocks = append(blocks, map[string]interface{}{"type": "text", "text": m.Content})
			}
			for _, call := range m.ToolCalls {
				input := call.Input
				if input == nil {
					input = map[string]interface{}{}
				}
				blocks = append(blocks, map[string]interface{}{
					"type":  "tool_use",
					"id":    call.ID,
					"name":  call.Name,
					"input": input,
				})
			}
			out = append(out, map[string]interface{}{"role": m.Role, "content": blocks})
			continue
		}

		out = append(out, map[string]interface{}{"role": m.Role, "content": m.Content})
	}
	return out, nil
}

func buildAnthropicTools(tools []gollm.ToolDefinition) []map[string]interface{} {
	out := make([]map[string]interface{}, len(tools))
	for i, t := range tools {
		out[i] = map[string]interface{}{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": t.InputSchema,
		}
	}
	return out
}

// buildAnthropicToolChoice mirrors claude/provider.go's
// convertToolChoiceForClaude. "auto"/"" → nil (Anthropic default).
func buildAnthropicToolChoice(choice string) map[string]interface{} {
	switch choice {
	case "", "auto":
		return nil
	case "any", "required":
		return map[string]interface{}{"type": "any"}
	case "none":
		return map[string]interface{}{"type": "none"}
	default:
		return map[string]interface{}{"type": "tool", "name": choice}
	}
}
