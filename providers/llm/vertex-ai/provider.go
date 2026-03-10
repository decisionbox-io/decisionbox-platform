// Package vertexai provides an llm.Provider for Google Vertex AI.
// Vertex AI hosts Claude, Gemini, and other models with GCP-native auth.
//
// Status: STUB — registers the provider so it appears in the registry.
// Full implementation coming soon.
//
// Configuration:
//
//	LLM_PROVIDER=vertex-ai
//	LLM_MODEL=claude-sonnet-4-20250514
//	VERTEX_PROJECT_ID=my-gcp-project
//	VERTEX_LOCATION=us-east5
package vertexai

import (
	"context"
	"fmt"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func init() {
	gollm.RegisterWithMeta("vertex-ai", func(cfg gollm.ProviderConfig) (gollm.Provider, error) {
		projectID := cfg["project_id"]
		if projectID == "" {
			return nil, fmt.Errorf("vertex-ai: project_id is required (set VERTEX_PROJECT_ID)")
		}
		location := cfg["location"]
		if location == "" {
			location = "us-central1"
		}
		model := cfg["model"]
		if model == "" {
			return nil, fmt.Errorf("vertex-ai: model is required")
		}

		return &VertexAIProvider{
			projectID: projectID,
			location:  location,
			model:     model,
		}, nil
	}, gollm.ProviderMeta{
		Name:        "Google Vertex AI",
		Description: "GCP-managed AI platform (Claude, Gemini) — coming soon",
		ConfigFields: []gollm.ConfigField{
			{Key: "project_id", Label: "GCP Project ID", Required: true, Type: "string", Placeholder: "my-gcp-project"},
			{Key: "location", Label: "Location", Type: "string", Default: "us-central1"},
			{Key: "model", Label: "Model", Required: true, Type: "string", Default: "claude-sonnet-4-20250514"},
		},
	})
}

// VertexAIProvider implements llm.Provider for Google Vertex AI.
type VertexAIProvider struct {
	projectID string
	location  string
	model     string
}

// Chat sends a conversation to Vertex AI.
// STUB: returns an error with setup instructions.
func (p *VertexAIProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	return nil, fmt.Errorf(
		"vertex-ai provider is not yet implemented. "+
			"Use LLM_PROVIDER=claude or LLM_PROVIDER=openai for now. "+
			"Vertex AI support is coming soon. "+
			"Config: project=%s, location=%s, model=%s",
		p.projectID, p.location, p.model,
	)
}
