// Package vertexai provides an llm.Provider for Google Vertex AI.
// Supports Claude (via Anthropic partnership) and Gemini (Google's own models)
// with GCP-native authentication (Application Default Credentials).
//
// Configuration:
//
//	LLM_PROVIDER=vertex-ai
//	LLM_MODEL=claude-sonnet-4-20250514  (or gemini-2.5-pro, gemini-2.5-flash)
//	VERTEX_PROJECT_ID=my-gcp-project
//	VERTEX_LOCATION=us-east5
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
	"strings"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
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

		timeoutSec, _ := strconv.Atoi(cfg["timeout_seconds"])
		if timeoutSec == 0 {
			timeoutSec = 300 // 5 minutes default — Opus needs more time for large prompts
		}
		ctx := context.Background()

		// Initialize GCP auth
		auth, err := newGCPAuth(ctx)
		if err != nil {
			return nil, err
		}

		return &VertexAIProvider{
			projectID:  projectID,
			location:   location,
			model:      model,
			auth:       auth,
			httpClient: &http.Client{Timeout: time.Duration(timeoutSec) * time.Second},
		}, nil
	}, gollm.ProviderMeta{
		Name:        "Google Vertex AI",
		Description: "GCP-managed AI platform — Claude & Gemini models with GCP auth",
		ConfigFields: []gollm.ConfigField{
			{Key: "project_id", Label: "GCP Project ID", Required: true, Type: "string", Placeholder: "my-gcp-project"},
			{Key: "location", Label: "Region", Type: "string", Default: "us-east5", Description: "GCP region (us-east5 for Claude, us-central1 for Gemini)"},
			{Key: "model", Label: "Model", Required: true, Type: "string", Default: "claude-sonnet-4-20250514", Placeholder: "claude-sonnet-4-20250514 or gemini-2.5-pro"},
		},
		DefaultPricing: map[string]gollm.TokenPricing{
			"claude-sonnet-4":   {InputPerMillion: 3.0, OutputPerMillion: 15.0},
			"claude-sonnet-4-5": {InputPerMillion: 3.0, OutputPerMillion: 15.0},
			"claude-opus-4":     {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-opus-4-6":   {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-haiku-4-5":  {InputPerMillion: 0.80, OutputPerMillion: 4.0},
			"gemini-2.5-pro":    {InputPerMillion: 1.25, OutputPerMillion: 10.0},
			"gemini-2.5-flash":  {InputPerMillion: 0.15, OutputPerMillion: 0.60},
		},
	})
}

// VertexAIProvider implements llm.Provider for Google Vertex AI.
// Routes to Claude or Gemini backend based on model name.
type VertexAIProvider struct {
	projectID  string
	location   string
	model      string
	auth       *gcpAuth
	httpClient *http.Client
}

// Validate checks that GCP credentials are valid and the model endpoint is accessible.
// Makes a minimal request (max_tokens=1) to verify auth and model access.
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

// Chat sends a conversation to Vertex AI.
// Routes to Claude or Gemini based on the model name.
func (p *VertexAIProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	if req.Model == "" {
		req.Model = p.model
	}

	if isClaude(req.Model) {
		return p.claudeChat(ctx, req)
	}
	if isGemini(req.Model) {
		return p.geminiChat(ctx, req)
	}

	return nil, fmt.Errorf("vertex-ai: unsupported model %q — use claude-* or gemini-* models", req.Model)
}

// isClaude returns true if the model name is a Claude model.
func isClaude(model string) bool {
	return strings.HasPrefix(model, "claude-") || strings.Contains(model, "claude")
}

// isGemini returns true if the model name is a Gemini model.
func isGemini(model string) bool {
	return strings.HasPrefix(model, "gemini-") || strings.Contains(model, "gemini")
}
