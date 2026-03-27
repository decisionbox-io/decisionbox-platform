package azurefoundry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// claudeChat sends a request to Claude on Azure AI Foundry.
// Uses the Anthropic Messages API format at {endpoint}/anthropic/v1/messages.
//
// Reference: https://platform.claude.com/docs/en/build-with-claude/claude-in-microsoft-foundry
func (p *AzureFoundryProvider) claudeChat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
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
		"model":      req.Model,
		"messages":   messages,
		"max_tokens": maxTokens,
	}

	if req.SystemPrompt != "" {
		body["system"] = req.SystemPrompt
	}

	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}

	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("azure-foundry/claude: failed to marshal request: %w", err)
	}

	endpoint := p.endpoint + "/anthropic/v1/messages"

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("azure-foundry/claude: failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("azure-foundry/claude: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("azure-foundry/claude: failed to read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("azure-foundry/claude: API error (status %d): %s", httpResp.StatusCode, string(respBody))
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

	if err := json.Unmarshal(respBody, &anthropicResp); err != nil {
		return nil, fmt.Errorf("azure-foundry/claude: failed to parse response: %w", err)
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
