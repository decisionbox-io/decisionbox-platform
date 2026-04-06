//go:build integration

package vertexai

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
)

func TestIntegration_EmbedSingleText(t *testing.T) {
	projectID := os.Getenv("INTEGRATION_TEST_VERTEX_PROJECT_ID")
	if projectID == "" {
		t.Skip("INTEGRATION_TEST_VERTEX_PROJECT_ID not set")
	}

	ctx := context.Background()
	auth, err := newGCPAuth(ctx)
	if err != nil {
		t.Fatalf("Failed to create GCP auth: %v", err)
	}

	location := "us-central1"
	model := "text-embedding-005"
	endpoint := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:predict",
		location, projectID, location, model)

	p := newProvider(model, 768, endpoint, auth)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	result, err := p.Embed(ctx, []string{"The quick brown fox jumps over the lazy dog"})
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if len(result[0]) != 768 {
		t.Fatalf("expected 768 dims, got %d", len(result[0]))
	}
	t.Logf("Vertex AI embed: %d dims, first value: %f", len(result[0]), result[0][0])
}

func TestIntegration_EmbedBatch(t *testing.T) {
	projectID := os.Getenv("INTEGRATION_TEST_VERTEX_PROJECT_ID")
	if projectID == "" {
		t.Skip("INTEGRATION_TEST_VERTEX_PROJECT_ID not set")
	}

	ctx := context.Background()
	auth, err := newGCPAuth(ctx)
	if err != nil {
		t.Fatalf("Failed to create GCP auth: %v", err)
	}

	location := "us-central1"
	model := "text-embedding-005"
	endpoint := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:predict",
		location, projectID, location, model)

	p := newProvider(model, 768, endpoint, auth)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	texts := []string{
		"Players are churning after the tutorial",
		"Revenue increased by 15% last month",
		"User retention is dropping in week 2",
	}
	result, err := p.Embed(ctx, texts)
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result))
	}
	for i, vec := range result {
		if len(vec) != 768 {
			t.Errorf("result[%d]: expected 768 dims, got %d", i, len(vec))
		}
	}
	t.Logf("Vertex AI batch embed: %d texts, %d dims each", len(result), len(result[0]))
}

func TestIntegration_Validate(t *testing.T) {
	projectID := os.Getenv("INTEGRATION_TEST_VERTEX_PROJECT_ID")
	if projectID == "" {
		t.Skip("INTEGRATION_TEST_VERTEX_PROJECT_ID not set")
	}

	ctx := context.Background()
	auth, err := newGCPAuth(ctx)
	if err != nil {
		t.Fatalf("Failed to create GCP auth: %v", err)
	}

	location := "us-central1"
	model := "text-embedding-005"
	endpoint := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:predict",
		location, projectID, location, model)

	p := newProvider(model, 768, endpoint, auth)

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := p.Validate(ctx); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	t.Log("Vertex AI embedding Validate succeeded")
}

func TestIntegration_ViaFactory(t *testing.T) {
	projectID := os.Getenv("INTEGRATION_TEST_VERTEX_PROJECT_ID")
	if projectID == "" {
		t.Skip("INTEGRATION_TEST_VERTEX_PROJECT_ID not set")
	}

	p, err := goembedding.NewProvider("vertex-ai", goembedding.ProviderConfig{
		"project_id": projectID,
		"location":   "us-central1",
		"model":      "text-embedding-005",
	})
	if err != nil {
		t.Fatalf("Factory error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := p.Embed(ctx, []string{"test via factory"})
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if len(result) != 1 || len(result[0]) != 768 {
		t.Fatalf("unexpected result shape: %d results", len(result))
	}
	t.Logf("Vertex AI factory embed succeeded: %d dims", len(result[0]))
}
