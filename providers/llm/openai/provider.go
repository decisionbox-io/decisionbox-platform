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

// openaiDefaultTimeout is the historical default HTTP timeout for the
// OpenAI chat-completions call. Operators raise it via the
// LLM_TIMEOUT env var or per-project timeout_seconds when
// long-form generations run past 5 minutes.
const openaiDefaultTimeout = 5 * time.Minute

func init() {
	gollm.RegisterWithMeta("openai", func(cfg gollm.ProviderConfig) (gollm.Provider, error) {
		apiKey := cfg["credentials_json"]
		if apiKey == "" {
			return nil, fmt.Errorf("openai: API key is required")
		}

		model := cfg["model"]
		if model == "" {
			model = "gpt-4o"
		}

		baseURL := cfg["base_url"]
		if baseURL == "" {
			baseURL = defaultBaseURL
		}

		timeout := gollm.ResolveHTTPTimeout(cfg, openaiDefaultTimeout)
		return NewOpenAIProvider(apiKey, model, baseURL, timeout), nil
	}, gollm.ProviderMeta{
		Name:        "OpenAI",
		Description: "OpenAI API - GPT-4o, GPT-4o-mini, and compatible APIs",
		ConfigFields: []gollm.ConfigField{
			{
				Key:         "model",
				Label:       "Model",
				Required:    true,
				Type:        "string",
				FreeText:    true,
				Default:     "gpt-4o",
				Description: "Pick a catalogued model or type any OpenAI model ID.",
			},
			{Key: "base_url", Label: "Base URL", Type: "string", Default: "https://api.openai.com/v1", Description: "For OpenAI-compatible APIs"},
		},
		AuthMethods: []gollm.AuthMethod{
			{
				ID: "api_key", Name: "API Key",
				Description: "OpenAI API key (or compatible API key for self-hosted gateways).",
				Fields: []gollm.ConfigField{
					{Key: "credentials_json", Label: "API Key", Required: true, Type: "credential", Placeholder: "sk-..."},
				},
			},
		},
		Models:                 buildOpenAICatalog(),
		FamilyInferrer:         inferOpenAIWire,
		DefaultMaxOutputTokens: 16384,
		// OpenAI's chat-completions endpoint supports function calling on
		// gpt-4o, gpt-4o-mini, gpt-4.1, gpt-4.1-mini. Reasoning models
		// (o3, o4-mini) do not expose tool_use through Converse-style
		// function calling today — tool-dependent callers must pick a
		// non-reasoning model or accept a no-tool fallback.
		SupportsTools: true,
	})
}

// OpenAIProvider implements llm.Provider using the OpenAI chat completions API.
type OpenAIProvider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

// NewOpenAIProvider creates a new OpenAI LLM provider. A zero or
// negative timeout falls back to openaiDefaultTimeout so callers that
// don't care (mainly tests) don't have to think about it.
func NewOpenAIProvider(apiKey, model, baseURL string, timeout time.Duration) *OpenAIProvider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if timeout <= 0 {
		timeout = openaiDefaultTimeout
	}
	return &OpenAIProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL,
		client:  &http.Client{Timeout: timeout},
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
