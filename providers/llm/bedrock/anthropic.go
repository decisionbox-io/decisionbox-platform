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
func (p *BedrockProvider) chatAnthropic(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	messages := make([]map[string]string, 0, len(req.Messages))
	for _, msg := range req.Messages {
		messages = append(messages, map[string]string{
			"role":    msg.Role,
			"content": msg.Content,
		})
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
			Text string `json:"text"`
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
	for _, block := range anthropicResp.Content {
		if block.Type == "text" {
			content += block.Text
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
	}, nil
}
