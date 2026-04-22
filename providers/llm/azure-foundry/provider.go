// Package azurefoundry provides an llm.Provider for Azure AI Foundry
// (Microsoft Foundry). Supports Claude models via the Anthropic Messages API
// and OpenAI-family models via the OpenAI Chat Completions API, both
// served from a single Azure resource endpoint.
//
// Dispatch is catalog-driven: each model's wire is looked up in
// libs/go-common/llm/modelcatalog. Uncatalogued models can be routed
// explicitly via the optional wire_override config key
// (project.llm.wire_override).
//
// Configuration:
//
//	endpoint=https://my-resource.services.ai.azure.com
//	api_key=your-azure-api-key
//	model=claude-sonnet-4-6  (or gpt-5, gpt-4o, etc.)
//	wire_override=anthropic|openai-compat  (optional)
//
// Authentication:
//
//	API key from the Azure AI Foundry portal, passed via the api-key header.
//	Entra ID (Azure AD) is also supported by Azure but not implemented here;
//	use API key auth via the project's llm-api-key secret.
//
// Endpoint routing:
//
//	Claude models (Anthropic wire) → POST {endpoint}/anthropic/v1/messages
//	OpenAI-compat models           → POST {endpoint}/openai/v1/chat/completions
//
// References:
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
	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/modelcatalog"
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

		wireOverride := modelcatalog.Unknown
		if raw := cfg["wire_override"]; raw != "" {
			parsed := modelcatalog.ParseWire(raw)
			if !parsed.Valid() {
				return nil, fmt.Errorf(
					"azure-foundry: invalid wire_override %q; use one of: %s, %s",
					raw, modelcatalog.Anthropic, modelcatalog.OpenAICompat,
				)
			}
			wireOverride = parsed
		}

		timeoutSec, _ := strconv.Atoi(cfg["timeout_seconds"])
		if timeoutSec == 0 {
			timeoutSec = 300
		}

		return &AzureFoundryProvider{
			endpoint:     endpoint,
			apiKey:       apiKey,
			model:        model,
			wireOverride: wireOverride,
			httpClient:   &http.Client{Timeout: time.Duration(timeoutSec) * time.Second},
		}, nil
	}, gollm.ProviderMeta{
		Name:        "Azure AI Foundry",
		Description: "Microsoft Azure-managed AI platform — Claude & OpenAI models with API key auth",
		ConfigFields: []gollm.ConfigField{
			{Key: "endpoint", Label: "Endpoint URL", Required: true, Type: "string", Placeholder: "https://my-resource.services.ai.azure.com"},
			{Key: "api_key", Label: "API Key", Required: true, Type: "string", Placeholder: "your-azure-api-key"},
			{Key: "model", Label: "Model", Required: true, Type: "string", Default: "claude-sonnet-4-6", Placeholder: "claude-sonnet-4-6 or gpt-4o"},
			{Key: "wire_override", Label: "Wire override", Type: "string", Description: "Only for models not in the catalog. One of: anthropic, openai-compat."},
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
		MaxOutputTokens: map[string]int{
			"claude-opus-4-6":   128000,
			"claude-sonnet-4-6": 64000,
			"claude-sonnet-4-5": 64000,
			"claude-opus-4-5":   64000,
			"claude-opus-4-1":   32000,
			"claude-haiku-4-5":  64000,
			"gpt-4o":            16384,
			"gpt-4o-mini":       16384,
			"_default":          16384,
		},
	})
}

// AzureFoundryProvider implements llm.Provider for Azure AI Foundry.
// Routes per wire resolved from the model catalog.
type AzureFoundryProvider struct {
	endpoint     string
	apiKey       string
	model        string
	wireOverride modelcatalog.Wire
	httpClient   *http.Client
}

// Validate checks that credentials are valid and the model endpoint is
// reachable. Makes a minimal request (max_tokens=1) so it exercises the
// same dispatch path as a real call.
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

// Chat sends a conversation to Azure AI Foundry, dispatching on the wire
// format resolved from the model catalog (or the configured wire_override).
func (p *AzureFoundryProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	if req.Model == "" {
		req.Model = p.model
	}
	return p.dispatch(ctx, req)
}
