// Package azurefoundry provides an llm.Provider for Azure AI Foundry
// (Microsoft Foundry). Supports Claude via the Anthropic Messages API
// and OpenAI-family models via the OpenAI Chat Completions API, both
// served from a single Azure resource endpoint.
//
// Dispatch is catalog-driven: each model in the registered ProviderMeta
// catalog declares its wire. Customer-renamed deployments (Foundry
// allows per-tenant aliases) fall through to the FamilyInferrer's
// prefix table; truly custom names need wire_override.
//
// Configuration:
//
//	endpoint=https://my-resource.services.ai.azure.com
//	api_key=your-azure-api-key
//	model=claude-sonnet-4-6  (or gpt-5, gpt-4o, etc.)
//	wire_override=anthropic|openai-compat  (optional)
//
// Endpoint routing:
//
//	WireAnthropic    → POST {endpoint}/anthropic/v1/messages
//	WireOpenAICompat → POST {endpoint}/openai/v1/chat/completions
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
	"strings"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// providerName is the registry key.
const providerName = "azure-foundry"

// azureFoundryDefaultTimeout is the historical default HTTP timeout
// for Azure Foundry calls. Operators raise it via the
// LLM_TIMEOUT env var or per-project timeout_seconds when
// long-form generations run past 5 minutes.
const azureFoundryDefaultTimeout = 300 * time.Second

func init() {
	gollm.RegisterWithMeta(providerName, factory, gollm.ProviderMeta{
		Name:        "Azure AI Foundry",
		Description: "Microsoft Azure-managed AI platform — Claude & OpenAI models with API key auth",
		ConfigFields: []gollm.ConfigField{
			{Key: "endpoint", Label: "Endpoint URL", Required: true, Type: "string", Placeholder: "https://my-resource.services.ai.azure.com"},
			{
				Key:         "model",
				Label:       "Model",
				Required:    true,
				Type:        "string",
				FreeText:    true,
				Default:     "claude-sonnet-4-6",
				Placeholder: "claude-sonnet-4-6 or gpt-4o",
				Description: "Pick a catalogued model or type any Azure deployment name.",
			},
			{
				Key:   "wire_override",
				Label: "Wire override",
				Type:  "string",
				Description: "Leave on auto unless your deployment is not yet in the catalog. " +
					"Azure Foundry supports Anthropic (Claude) and OpenAI Chat Completions.",
				Options: []gollm.ConfigOption{
					{Value: "", Label: "Auto — use model catalog"},
					{Value: string(gollm.WireAnthropic), Label: "Anthropic Messages (Claude)"},
					{Value: string(gollm.WireOpenAICompat), Label: "OpenAI Chat Completions"},
				},
			},
		},
		AuthMethods: []gollm.AuthMethod{
			{
				ID: "api_key", Name: "API Key",
				Description: "Azure AI Foundry API key.",
				Fields: []gollm.ConfigField{
					{Key: "credentials_json", Label: "API Key", Required: true, Type: "credential", Placeholder: "your-azure-api-key"},
				},
			},
		},
		Models:                 buildAzureFoundryCatalog(),
		DefaultMaxOutputTokens: 16384,
		FamilyInferrer:         inferAzureWire,
	})
}

func factory(cfg gollm.ProviderConfig) (gollm.Provider, error) {
	endpoint := cfg["endpoint"]
	if endpoint == "" {
		return nil, fmt.Errorf("azure-foundry: endpoint is required")
	}
	endpoint = strings.TrimRight(endpoint, "/")

	apiKey := cfg["credentials_json"]
	if apiKey == "" {
		return nil, fmt.Errorf("azure-foundry: API key is required")
	}

	model := cfg["model"]
	if model == "" {
		return nil, fmt.Errorf("azure-foundry: model is required")
	}

	wireOverride := gollm.WireUnknown
	if raw := cfg["wire_override"]; raw != "" {
		parsed := gollm.ParseWire(raw)
		// Azure Foundry dispatches on Anthropic and OpenAICompat
		// only — reject other wires at factory time.
		if parsed != gollm.WireAnthropic && parsed != gollm.WireOpenAICompat {
			return nil, fmt.Errorf(
				"azure-foundry: invalid wire_override %q; use one of: %s, %s",
				raw, gollm.WireAnthropic, gollm.WireOpenAICompat,
			)
		}
		wireOverride = parsed
	}

	timeout := gollm.ResolveHTTPTimeout(cfg, azureFoundryDefaultTimeout)

	return &AzureFoundryProvider{
		endpoint:     endpoint,
		apiKey:       apiKey,
		model:        model,
		wireOverride: wireOverride,
		httpClient:   &http.Client{Timeout: timeout},
	}, nil
}

// AzureFoundryProvider implements llm.Provider for Azure AI Foundry.
type AzureFoundryProvider struct {
	endpoint     string
	apiKey       string
	model        string
	wireOverride gollm.Wire
	httpClient   *http.Client
}

// Validate exercises the same dispatch path as a real Chat call with
// max_tokens=1 so credentials and model availability are both checked.
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

// Chat sends a conversation to Azure AI Foundry, dispatching per the
// wire resolved from the catalog (or the configured wire_override).
func (p *AzureFoundryProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	if req.Model == "" {
		req.Model = p.model
	}
	return p.dispatch(ctx, req)
}
