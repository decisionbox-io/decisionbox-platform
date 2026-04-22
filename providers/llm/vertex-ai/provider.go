// Package vertexai provides an llm.Provider for Google Vertex AI.
// Vertex hosts three families of models, each speaking a different wire:
//
//   - Gemini via generateContent (GoogleNative wire)
//   - Claude via rawPredict publishers/anthropic/… (Anthropic wire)
//   - Llama / Qwen / DeepSeek / Mistral on MaaS and Gemini's OpenAI surface
//     via /v1beta1/.../endpoints/openapi/chat/completions (OpenAICompat)
//
// Dispatch is catalog-driven: each model's wire is looked up in
// libs/go-common/llm/modelcatalog at request time. Uncatalogued models
// can be routed explicitly via the optional wire_override config key.
//
// Configuration:
//
//	LLM_PROVIDER=vertex-ai
//	LLM_MODEL=gemini-2.5-pro  (or claude-opus-4-6@…, or meta/llama-3.3-70b-instruct-maas)
//	VERTEX_PROJECT_ID=my-gcp-project
//	VERTEX_LOCATION=us-east5  (us-east5 for Claude, us-central1 for Gemini)
//	wire_override=google-native|anthropic|openai-compat  (optional)
//
// Authentication:
//
//	Uses Application Default Credentials (ADC). On GKE this works via
//	Workload Identity. Locally, run: gcloud auth application-default login
package vertexai

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/modelcatalog"
)

func init() {
	gollm.RegisterWithMeta("vertex-ai", func(cfg gollm.ProviderConfig) (gollm.Provider, error) {
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

		wireOverride := modelcatalog.Unknown
		if raw := cfg["wire_override"]; raw != "" {
			parsed := modelcatalog.ParseWire(raw)
			if !parsed.Valid() {
				return nil, fmt.Errorf(
					"vertex-ai: invalid wire_override %q; use one of: %s, %s, %s",
					raw, modelcatalog.GoogleNative, modelcatalog.Anthropic, modelcatalog.OpenAICompat,
				)
			}
			wireOverride = parsed
		}

		timeoutSec, _ := strconv.Atoi(cfg["timeout_seconds"])
		if timeoutSec == 0 {
			timeoutSec = 300 // Opus + large contexts can exceed the 60s default
		}
		ctx := context.Background()

		// Initialize GCP auth
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
	}, gollm.ProviderMeta{
		Name:        "Google Vertex AI",
		Description: "GCP-managed AI platform — Gemini, Claude, Llama, Qwen, DeepSeek, Mistral with GCP auth",
		ConfigFields: []gollm.ConfigField{
			{Key: "project_id", Label: "GCP Project ID", Required: true, Type: "string", Placeholder: "my-gcp-project"},
			{Key: "location", Label: "Region", Type: "string", Default: "us-east5", Description: "GCP region (us-east5 for Claude, us-central1 for Gemini)"},
			{Key: "model", Label: "Model", Required: true, Type: "string", Default: "gemini-2.5-pro", Placeholder: "gemini-2.5-pro or claude-opus-4-6@20251101"},
			{Key: "wire_override", Label: "Wire override", Type: "string", Description: "Only for models not in the catalog. One of: google-native, anthropic, openai-compat."},
		},
		DefaultPricing: map[string]gollm.TokenPricing{
			"claude-opus-4-6":   {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-sonnet-4-6": {InputPerMillion: 3.0, OutputPerMillion: 15.0},
			"claude-sonnet-4-5": {InputPerMillion: 3.0, OutputPerMillion: 15.0},
			"claude-opus-4-5":   {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-opus-4-1":   {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-sonnet-4":   {InputPerMillion: 3.0, OutputPerMillion: 15.0},
			"claude-opus-4":     {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-haiku-4-5":  {InputPerMillion: 0.80, OutputPerMillion: 4.0},
			"gemini-2.5-pro":    {InputPerMillion: 1.25, OutputPerMillion: 10.0},
			"gemini-2.5-flash":  {InputPerMillion: 0.15, OutputPerMillion: 0.60},
		},
		MaxOutputTokens: map[string]int{
			"claude-opus-4-6":   128000,
			"claude-sonnet-4-6": 64000,
			"claude-sonnet-4-5": 64000,
			"claude-opus-4-5":   64000,
			"claude-opus-4-1":   32000,
			"claude-sonnet-4":   64000,
			"claude-opus-4":     32000,
			"claude-haiku-4-5":  64000,
			"gemini-2.5-pro":    65536,
			"gemini-2.5-flash":  65536,
			"_default":          16384,
		},
	})
}

// VertexAIProvider implements llm.Provider for Google Vertex AI.
// Routes per wire resolved from the model catalog.
type VertexAIProvider struct {
	projectID    string
	location     string
	model        string
	wireOverride modelcatalog.Wire
	auth         *gcpAuth
	httpClient   *http.Client
}

// Validate checks that GCP credentials are valid and the model endpoint is
// reachable. Makes a minimal request (max_tokens=1) to exercise the same
// dispatch path as a real call.
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

// Chat sends a conversation to Vertex AI, dispatching on the wire format
// resolved from the model catalog (or the configured wire_override).
func (p *VertexAIProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	if req.Model == "" {
		req.Model = p.model
	}
	return p.dispatch(ctx, req)
}
