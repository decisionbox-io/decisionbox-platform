// Package vertexai provides an llm.Provider for Google Vertex AI.
// Vertex hosts three families of models, each speaking a different wire:
//
//   - Gemini via generateContent (WireGoogleNative)
//   - Claude via rawPredict publishers/anthropic/… (WireAnthropic)
//   - Llama / Qwen / DeepSeek / Mistral on MaaS via
//     /v1beta1/.../endpoints/openapi/chat/completions (WireOpenAICompat)
//
// Dispatch is catalog-driven: each model in the registered ProviderMeta
// catalog declares its wire, and the dispatch switch routes per-wire.
// Uncatalogued models can be routed via wire_override; freshly-released
// models in known families fall through to the prefix-based
// FamilyInferrer.
//
// Configuration:
//
//	LLM_PROVIDER=vertex-ai
//	LLM_MODEL=gemini-2.5-pro  (or claude-opus-4-7, or meta/llama-3.3-70b-instruct-maas)
//	VERTEX_PROJECT_ID=my-gcp-project
//	VERTEX_LOCATION=us-east5  (us-east5 for Claude, us-central1 for Gemini)
//	wire_override=google-native|anthropic|openai-compat  (optional)
//
// Authentication:
//
//	Application Default Credentials (ADC). On GKE: Workload Identity.
//	Locally: gcloud auth application-default login
package vertexai

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// providerName is the registry key.
const providerName = "vertex-ai"

func init() {
	gollm.RegisterWithMeta(providerName, factory, gollm.ProviderMeta{
		Name:        "Google Vertex AI",
		Description: "GCP-managed AI platform — Gemini, Claude, Llama, Qwen, DeepSeek, Mistral with GCP auth",
		ConfigFields: []gollm.ConfigField{
			{Key: "project_id", Label: "GCP Project ID", Required: true, Type: "string", Placeholder: "my-gcp-project"},
			{Key: "location", Label: "Region", Type: "string", Default: "us-east5", Description: "GCP region (us-east5 for Claude, us-central1 for Gemini)"},
			{
				Key:         "model",
				Label:       "Model",
				Required:    true,
				Type:        "string",
				FreeText:    true,
				Default:     "gemini-2.5-pro",
				Placeholder: "gemini-2.5-pro or claude-opus-4-7",
				Description: "Pick a catalogued model or type any Vertex model ID.",
			},
			{
				Key:   "wire_override",
				Label: "Wire override",
				Type:  "string",
				Description: "Leave on auto unless your model is not yet in the catalog. " +
					"Vertex AI supports Google-native (Gemini), Anthropic (Claude), and OpenAI Chat Completions (MaaS).",
				Options: []gollm.ConfigOption{
					{Value: "", Label: "Auto — use model catalog"},
					{Value: string(gollm.WireGoogleNative), Label: "Google Gemini (native)"},
					{Value: string(gollm.WireAnthropic), Label: "Anthropic Messages (Claude)"},
					{Value: string(gollm.WireOpenAICompat), Label: "OpenAI Chat Completions"},
				},
			},
		},
		Models:                 buildVertexCatalog(),
		DefaultMaxOutputTokens: 16384,
		FamilyInferrer:         inferVertexWire,
	})
}

func factory(cfg gollm.ProviderConfig) (gollm.Provider, error) {
	projectID := cfg["project_id"]
	if projectID == "" {
		return nil, fmt.Errorf("vertex-ai: project_id is required")
	}
	location := cfg["location"]
	if location == "" {
		location = "us-east5"
	}
	model := cfg["model"]
	if model == "" {
		return nil, fmt.Errorf("vertex-ai: model is required")
	}

	wireOverride := gollm.WireUnknown
	if raw := cfg["wire_override"]; raw != "" {
		parsed := gollm.ParseWire(raw)
		if !parsed.Valid() {
			return nil, fmt.Errorf(
				"vertex-ai: invalid wire_override %q; use one of: %s, %s, %s",
				raw, gollm.WireGoogleNative, gollm.WireAnthropic, gollm.WireOpenAICompat,
			)
		}
		wireOverride = parsed
	}

	timeoutSec, _ := strconv.Atoi(cfg["timeout_seconds"])
	if timeoutSec == 0 {
		timeoutSec = 300 // Opus + large contexts can exceed the 60s default
	}
	ctx := context.Background()

	auth, err := newGCPAuth(ctx)
	if err != nil {
		return nil, err
	}

	return &VertexAIProvider{
		projectID:    projectID,
		location:     location,
		model:        model,
		wireOverride: wireOverride,
		auth:         auth,
		httpClient:   &http.Client{Timeout: time.Duration(timeoutSec) * time.Second},
	}, nil
}

// VertexAIProvider implements llm.Provider for Google Vertex AI.
type VertexAIProvider struct {
	projectID    string
	location     string
	model        string
	wireOverride gollm.Wire
	auth         *gcpAuth
	httpClient   *http.Client
}

// Validate exercises the same dispatch path as a real Chat call with
// max_tokens=1 so credentials and model availability are both checked.
func (p *VertexAIProvider) Validate(ctx context.Context) error {
	_, err := p.Chat(ctx, gollm.ChatRequest{
		Model:     p.model,
		Messages:  []gollm.Message{{Role: "user", Content: "hi"}},
		MaxTokens: 1,
	})
	if err != nil {
		return fmt.Errorf("vertex-ai: validation failed: %w", err)
	}
	return nil
}

// Chat sends a conversation to Vertex AI, dispatching per the wire
// resolved from the catalog (or the configured wire_override).
func (p *VertexAIProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	if req.Model == "" {
		req.Model = p.model
	}
	return p.dispatch(ctx, req)
}
