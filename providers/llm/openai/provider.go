// Package openai provides an llm.Provider backed by the OpenAI API.
// Uses net/http directly (no SDK dependency) for minimal footprint.
//
// Register via init():
//
//	import _ "github.com/decisionbox-io/decisionbox/providers/llm/openai"
//
// Configuration:
//
//	LLM_PROVIDER=openai
//	LLM_API_KEY=sk-...
//	LLM_MODEL=gpt-4o
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

const defaultBaseURL = "https://api.openai.com/v1"

func init() {
	gollm.RegisterWithMeta("openai", func(cfg gollm.ProviderConfig) (gollm.Provider, error) {
		apiKey := cfg["api_key"]
		if apiKey == "" {
			return nil, fmt.Errorf("openai: api_key is required")
		}

		model := cfg["model"]
		if model == "" {
			model = "gpt-4o"
		}

		baseURL := cfg["base_url"]
		if baseURL == "" {
			baseURL = defaultBaseURL
		}

		return NewOpenAIProvider(apiKey, model, baseURL), nil
	}, gollm.ProviderMeta{
		Name:        "OpenAI",
		Description: "OpenAI API - GPT-4o, GPT-4o-mini, and compatible APIs",
		ConfigFields: []gollm.ConfigField{
			{Key: "api_key", Label: "API Key", Required: true, Type: "string", Placeholder: "sk-..."},
			{Key: "model", Label: "Model", Required: true, Type: "string", Default: "gpt-4o"},
			{Key: "base_url", Label: "Base URL", Type: "string", Default: "https://api.openai.com/v1", Description: "For OpenAI-compatible APIs"},
		},
		DefaultPricing: map[string]gollm.TokenPricing{
			"gpt-4o":      {InputPerMillion: 2.50, OutputPerMillion: 10.0},
			"gpt-4o-mini": {InputPerMillion: 0.15, OutputPerMillion: 0.60},
			"gpt-4.1":     {InputPerMillion: 2.0, OutputPerMillion: 8.0},
			"gpt-4.1-mini": {InputPerMillion: 0.40, OutputPerMillion: 1.60},
			"o3":          {InputPerMillion: 2.0, OutputPerMillion: 8.0},
			"o4-mini":     {InputPerMillion: 1.10, OutputPerMillion: 4.40},
		},
	})
}

// OpenAIProvider implements llm.Provider using the OpenAI chat completions API.
type OpenAIProvider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

// NewOpenAIProvider creates a new OpenAI LLM provider.
func NewOpenAIProvider(apiKey, model, baseURL string) *OpenAIProvider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &OpenAIProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL,
		client:  &http.Client{Timeout: 5 * time.Minute},
	}
}

// Validate checks that the API key is valid by listing models.
// GET /v1/models — no token cost.
func (p *OpenAIProvider) Validate(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/models", nil)
	if err != nil {
		return fmt.Errorf("openai: failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("openai: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("openai: validation failed (status %d): %s", resp.StatusCode, string(body))
	}
	return nil
}

// chatRequest is the OpenAI chat completions request body.
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatResponse is the OpenAI chat completions response body.
type chatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// Chat sends a conversation to OpenAI and returns the response.
func (p *OpenAIProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}

	// Build messages
	messages := make([]chatMessage, 0, len(req.Messages)+1)
	if req.SystemPrompt != "" {
		messages = append(messages, chatMessage{Role: "system", Content: req.SystemPrompt})
	}
	for _, msg := range req.Messages {
		messages = append(messages, chatMessage{Role: msg.Role, Content: msg.Content})
	}

	body := chatRequest{
		Model:    model,
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
		return nil, fmt.Errorf("openai: failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("openai: failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	httpResp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai: failed to read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		var errResp chatResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != nil {
			return nil, fmt.Errorf("openai: API error (%d): %s - %s", httpResp.StatusCode, errResp.Error.Type, errResp.Error.Message)
		}
		return nil, fmt.Errorf("openai: API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var resp chatResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("openai: failed to parse response: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("openai: no choices in response")
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
