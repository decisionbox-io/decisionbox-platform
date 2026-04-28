//go:build integration

package azureopenai

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestIntegration_ListModels exercises the live Azure OpenAI
// /openai/deployments endpoint. Skipped unless
// INTEGRATION_TEST_AZURE_OPENAI_ENDPOINT and
// INTEGRATION_TEST_AZURE_OPENAI_API_KEY are set.
func TestIntegration_ListModels(t *testing.T) {
	endpoint := os.Getenv("INTEGRATION_TEST_AZURE_OPENAI_ENDPOINT")
	apiKey := os.Getenv("INTEGRATION_TEST_AZURE_OPENAI_API_KEY")
	if endpoint == "" || apiKey == "" {
		t.Skip("INTEGRATION_TEST_AZURE_OPENAI_{ENDPOINT,API_KEY} not set")
	}
	deployment := os.Getenv("INTEGRATION_TEST_AZURE_OPENAI_DEPLOYMENT")
	if deployment == "" {
		deployment = "ignored-list-only"
	}

	p := newProvider(endpoint, apiKey, deployment, "text-embedding-3-small", defaultAPIVersion, 1536)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	models, err := p.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels error: %v", err)
	}

	// We don't require >0 — the Azure subscription may have only chat
	// foundation models in a given region. We DO require that any
	// returned row's id contains "embedding" (Azure's foundation-model
	// id convention).
	for _, m := range models {
		if m.ID == "" {
			t.Errorf("got model with empty ID: %+v", m)
		}
		if !strings.Contains(strings.ToLower(m.ID), "embedding") {
			t.Errorf("expected an embedding foundation model id, got %q", m.ID)
		}
	}

	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	t.Logf("Azure OpenAI live-list returned %d embedding foundation models: %v", len(models), ids)
}
