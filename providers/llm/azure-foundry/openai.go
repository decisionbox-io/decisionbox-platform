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

// openaiChatRequest is the OpenAI chat completions request body.
type openaiChatRequest struct {
	Model       string             `json:"model"`
	Messages    []openaiChatMsg    `json:"messages"`
	MaxTokens   int                `json:"max_tokens,omitempty"`
	Temperature float64            `json:"temperature,omitempty"`
}

type openaiChatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// openaiChatResponse is the OpenAI chat completions response body.
type openaiChatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message      openaiChatMsg `json:"message"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

// openaiChat sends a request to an OpenAI-compatible model on Azure AI Foundry.
// Uses the OpenAI Chat Completions API at {endpoint}/openai/v1/chat/completions.
//
// Reference: https://learn.microsoft.com/en-us/azure/foundry/foundry-models/concepts/endpoints
func (p *AzureFoundryProvider) openaiChat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	messages := make([]openaiChatMsg, 0, len(req.Messages)+1)
	if req.SystemPrompt != "" {
		messages = append(messages, openaiChatMsg{Role: "system", Content: req.SystemPrompt})
	}
	for _, msg := range req.Messages {
		messages = append(messages, openaiChatMsg{Role: msg.Role, Content: msg.Content})
	}

	body := openaiChatRequest{
		Model:    req.Model,
		Messages: messages,
	}
	if req.MaxTokens > 0 {
		body.MaxTokens = req.MaxTokens
	}
	if req.Temperature > 0 {
		body.Temperature = req.Temperature
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("azure-foundry/openai: failed to marshal request: %w", err)
	}

	endpoint := p.endpoint + "/openai/v1/chat/completions"

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("azure-foundry/openai: failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api-key", p.apiKey)

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("azure-foundry/openai: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("azure-foundry/openai: failed to read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		var errResp openaiChatResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != nil {
			return nil, fmt.Errorf("azure-foundry/openai: API error (%d): %s - %s", httpResp.StatusCode, errResp.Error.Type, errResp.Error.Message)
		}
		return nil, fmt.Errorf("azure-foundry/openai: API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var resp openaiChatResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("azure-foundry/openai: failed to parse response: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("azure-foundry/openai: no choices in response")
	}

	choice := resp.Choices[0]

	return &gollm.ChatResponse{
		Content:    choice.Message.Content,
		Model:      resp.Model,
		StopReason: choice.FinishReason,
		Usage: gollm.Usage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		},
	}, nil
}
