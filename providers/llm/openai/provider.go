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
	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/openaicompat"
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
		MaxOutputTokens: map[string]int{
			"gpt-4o":       16384,
			"gpt-4o-mini":  16384,
			"gpt-4.1":      32768,
			"gpt-4.1-mini": 32768,
			"o3":           100000,
			"o4-mini":      100000,
			"_default":     16384,
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

// Chat sends a conversation to OpenAI and returns the response.
func (p *OpenAIProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	body := openaicompat.BuildRequestBody(p.model, req)

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
		if apiErr := openaicompat.ExtractAPIError(respBody); apiErr != nil {
			return nil, fmt.Errorf("openai: API error (%d): %s - %s", httpResp.StatusCode, apiErr.Type, apiErr.Message)
		}
		return nil, fmt.Errorf("openai: API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	resp, err := openaicompat.ParseResponseBody(respBody)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	return resp, nil
}
