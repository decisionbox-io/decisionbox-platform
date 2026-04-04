package vertexai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// claudeChat sends a request to Claude on Vertex AI.
// Uses the Anthropic Messages API format via Vertex AI's rawPredict endpoint.
func (p *VertexAIProvider) claudeChat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	// Build Anthropic Messages API request body
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
		"anthropic_version": "vertex-2023-10-16",
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
		return nil, fmt.Errorf("vertex-ai/claude: failed to marshal request: %w", err)
	}

	// Vertex AI endpoint for Claude
	// Global endpoint has no region prefix: https://aiplatform.googleapis.com/...
	// Regional endpoints use: https://{location}-aiplatform.googleapis.com/...
	var host string
	if p.location == "global" {
		host = "aiplatform.googleapis.com"
	} else {
		host = fmt.Sprintf("%s-aiplatform.googleapis.com", p.location)
	}
	endpoint := fmt.Sprintf(
		"https://%s/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:rawPredict",
		host, p.projectID, p.location, req.Model,
	)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("vertex-ai/claude: failed to create request: %w", err)
	}

	// GCP auth token
	token, err := p.auth.token(ctx)
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai/claude: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai/claude: failed to read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vertex-ai/claude: API error (status %d): %s", httpResp.StatusCode, string(respBody))
	}

	// Parse Anthropic Messages API response (same format as direct Claude)
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
		return nil, fmt.Errorf("vertex-ai/claude: failed to parse response: %w", err)
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
