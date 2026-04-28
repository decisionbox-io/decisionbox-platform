//go:build integration

package vertexai

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestIntegration_ListModels exercises the live Vertex AI publishers-
// google-models endpoint filtered to embedding models. Skipped unless
// INTEGRATION_TEST_VERTEX_PROJECT_ID is set; uses ADC for auth.
func TestIntegration_ListModels(t *testing.T) {
	projectID := os.Getenv("INTEGRATION_TEST_VERTEX_PROJECT_ID")
	if projectID == "" {
		t.Skip("INTEGRATION_TEST_VERTEX_PROJECT_ID not set")
	}
	location := os.Getenv("INTEGRATION_TEST_VERTEX_LOCATION")
	if location == "" {
		location = "us-central1"
	}

	auth, err := newGCPAuth(context.Background())
	if err != nil {
		t.Fatalf("newGCPAuth: %v", err)
	}
	p := newProvider("text-embedding-005", 768, "http://unused", auth, location, projectID)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	models, err := p.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels error: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("expected at least one embedding publisher model from Vertex AI; got 0")
	}

	sawEmbedding := false
	for _, m := range models {
		if m.ID == "" {
			t.Errorf("got embedding model with empty ID: %+v", m)
		}
		if strings.Contains(m.ID, "embedding") {
			sawEmbedding = true
		}
	}
	if !sawEmbedding {
		t.Errorf("expected at least one *embedding* model in region %q project %q, got: %+v", location, projectID, models)
	}

	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	t.Logf("Vertex AI live-list returned %d embedding models: %v", len(models), ids)
}
