//go:build integration

package voyage

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestIntegration_ListModels exercises the live Voyage AI list-models
// endpoint with the user-supplied API key. Skipped unless
// INTEGRATION_TEST_VOYAGE_API_KEY is set.
func TestIntegration_ListModels(t *testing.T) {
	apiKey := os.Getenv("INTEGRATION_TEST_VOYAGE_API_KEY")
	if apiKey == "" {
		t.Skip("INTEGRATION_TEST_VOYAGE_API_KEY not set")
	}

	p := newProvider(apiKey, "voyage-3", "", defaultBaseURL, 1024)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	models, err := p.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels error: %v", err)
	}

	// Voyage's /v1/models endpoint isn't documented and returns 404
	// for valid keys today — ListModels treats that as "no live-list
	// capability" and returns (nil, nil), surfaced to the dashboard
	// as an empty dropdown (user types a model id manually). If
	// Voyage ever ships the endpoint we'll start seeing rows here.
	if len(models) == 0 {
		t.Logf("Voyage live-list returned 0 models — expected today; the user types an id manually")
		return
	}

	// We don't assert on count — Voyage's list endpoint is undocumented
	// and may return zero rows. We DO assert that any returned row is a
	// voyage-* embedding id (not a reranker).
	for _, m := range models {
		if m.ID == "" {
			t.Errorf("got an embedding model with empty ID: %+v", m)
		}
		if !strings.HasPrefix(m.ID, "voyage-") {
			t.Errorf("expected voyage-* id, got %q", m.ID)
		}
		if strings.HasPrefix(m.ID, "voyage-rerank") {
			t.Errorf("rerank model leaked into embedding list: %q", m.ID)
		}
	}
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	t.Logf("Voyage live-list returned %d embedding models: %v", len(models), ids)
}
