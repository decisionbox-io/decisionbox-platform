// Package azurefoundry provides an llm.Provider for Azure AI Foundry
// (Microsoft Foundry). Supports Claude models via the Anthropic Messages API
// and OpenAI models via the OpenAI Chat Completions API, both served from a
// single Azure resource endpoint.
//
// Configuration:
//
//	endpoint=https://my-resource.services.ai.azure.com
//	api_key=your-azure-api-key
//	model=claude-sonnet-4-6  (or gpt-4o, gpt-4o-mini, etc.)
//
// Authentication:
//
//	API key from the Azure AI Foundry portal, passed via the api-key header.
//	Entra ID (Azure AD) is also supported by Azure but not implemented here;
//	use API key auth via the project's llm-api-key secret.
//
// Endpoint routing:
//
//	Claude models  → POST {endpoint}/anthropic/v1/messages
//	OpenAI models  → POST {endpoint}/openai/v1/chat/completions
//
// Reference:
//
//	https://platform.claude.com/docs/en/build-with-claude/claude-in-microsoft-foundry
//	https://learn.microsoft.com/en-us/azure/foundry/foundry-models/concepts/endpoints
package azurefoundry

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func init() {
	gollm.RegisterWithMeta("azure-foundry", func(cfg gollm.ProviderConfig) (gollm.Provider, error) {
		endpoint := cfg["endpoint"]
		if endpoint == "" {
			return nil, fmt.Errorf("azure-foundry: endpoint is required")
		}
		endpoint = strings.TrimRight(endpoint, "/")

		apiKey := cfg["api_key"]
		if apiKey == "" {
			return nil, fmt.Errorf("azure-foundry: api_key is required")
		}

		model := cfg["model"]
		if model == "" {
			return nil, fmt.Errorf("azure-foundry: model is required")
		}

		timeoutSec, _ := strconv.Atoi(cfg["timeout_seconds"])
		if timeoutSec == 0 {
			timeoutSec = 300
		}

		return &AzureFoundryProvider{
			endpoint:   endpoint,
			apiKey:     apiKey,
			model:      model,
			httpClient: &http.Client{Timeout: time.Duration(timeoutSec) * time.Second},
		}, nil
	}, gollm.ProviderMeta{
		Name:        "Azure AI Foundry",
		Description: "Microsoft Azure-managed AI platform — Claude & OpenAI models with API key auth",
		ConfigFields: []gollm.ConfigField{
			{Key: "endpoint", Label: "Endpoint URL", Required: true, Type: "string", Placeholder: "https://my-resource.services.ai.azure.com"},
			{Key: "api_key", Label: "API Key", Required: true, Type: "string", Placeholder: "your-azure-api-key"},
			{Key: "model", Label: "Model", Required: true, Type: "string", Default: "claude-sonnet-4-6", Placeholder: "claude-sonnet-4-6 or gpt-4o"},
		},
		DefaultPricing: map[string]gollm.TokenPricing{
			// Claude models — Anthropic standard pricing via Azure Marketplace
			"claude-opus-4-6":   {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-sonnet-4-6": {InputPerMillion: 3.0, OutputPerMillion: 15.0},
			"claude-sonnet-4-5": {InputPerMillion: 3.0, OutputPerMillion: 15.0},
			"claude-opus-4-5":   {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-opus-4-1":   {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-haiku-4-5":  {InputPerMillion: 0.80, OutputPerMillion: 4.0},
			// OpenAI models
			"gpt-4o":      {InputPerMillion: 2.50, OutputPerMillion: 10.0},
			"gpt-4o-mini": {InputPerMillion: 0.15, OutputPerMillion: 0.60},
		},
	})
}

// AzureFoundryProvider implements llm.Provider for Azure AI Foundry.
// Routes to Claude or OpenAI backend based on model name.
type AzureFoundryProvider struct {
	endpoint   string
	apiKey     string
	model      string
	httpClient *http.Client
}

// Validate checks that credentials are valid and the model endpoint is accessible.
// Makes a minimal request (max_tokens=1) to verify auth and model access.
func (p *AzureFoundryProvider) Validate(ctx context.Context) error {
	_, err := p.Chat(ctx, gollm.ChatRequest{
		Model:     p.model,
		Messages:  []gollm.Message{{Role: "user", Content: "hi"}},
		MaxTokens: 1,
	})
	if err != nil {
		return fmt.Errorf("azure-foundry: validation failed: %w", err)
	}
	return nil
}

// Chat sends a conversation to Azure AI Foundry.
// Routes to Claude or OpenAI based on the model name.
func (p *AzureFoundryProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	if req.Model == "" {
		req.Model = p.model
	}

	if isClaude(req.Model) {
		return p.claudeChat(ctx, req)
	}

	// Default to OpenAI backend for non-Claude models (gpt-4o, o3, DeepSeek, etc.)
	return p.openaiChat(ctx, req)
}

// isClaude returns true if the model name is a Claude model.
func isClaude(model string) bool {
	return strings.HasPrefix(model, "claude-") || strings.Contains(model, "claude")
}
