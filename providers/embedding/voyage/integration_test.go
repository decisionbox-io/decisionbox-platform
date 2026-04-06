//go:build integration

package voyage

import (
	"context"
	"os"
	"testing"
	"time"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
)

func TestIntegration_EmbedSingleText(t *testing.T) {
	apiKey := os.Getenv("INTEGRATION_TEST_VOYAGE_API_KEY")
	if apiKey == "" {
		t.Skip("INTEGRATION_TEST_VOYAGE_API_KEY not set")
	}

	p := newProvider(apiKey, "voyage-3-lite", "", defaultBaseURL, 512)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := p.Embed(ctx, []string{"The quick brown fox jumps over the lazy dog"})
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if len(result[0]) != 512 {
		t.Fatalf("expected 512 dims, got %d", len(result[0]))
	}
	t.Logf("Voyage embed: %d dims, first value: %f", len(result[0]), result[0][0])
}

func TestIntegration_EmbedBatch(t *testing.T) {
	apiKey := os.Getenv("INTEGRATION_TEST_VOYAGE_API_KEY")
	if apiKey == "" {
		t.Skip("INTEGRATION_TEST_VOYAGE_API_KEY not set")
	}

	p := newProvider(apiKey, "voyage-3-lite", "", defaultBaseURL, 512)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
		if len(vec) != 512 {
			t.Errorf("result[%d]: expected 512 dims, got %d", i, len(vec))
		}
	}
	t.Logf("Voyage batch embed: %d texts, %d dims each", len(result), len(result[0]))
}

func TestIntegration_EmbedWithInputType(t *testing.T) {
	apiKey := os.Getenv("INTEGRATION_TEST_VOYAGE_API_KEY")
	if apiKey == "" {
		t.Skip("INTEGRATION_TEST_VOYAGE_API_KEY not set")
	}

	p := newProvider(apiKey, "voyage-3-lite", "document", defaultBaseURL, 512)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := p.Embed(ctx, []string{"This is a document about user retention metrics."})
	if err != nil {
		t.Fatalf("Embed with input_type error: %v", err)
	}
	if len(result) != 1 || len(result[0]) != 512 {
		t.Fatalf("unexpected result shape")
	}
	t.Logf("Voyage embed with input_type=document: %d dims", len(result[0]))
}

func TestIntegration_Validate(t *testing.T) {
	apiKey := os.Getenv("INTEGRATION_TEST_VOYAGE_API_KEY")
	if apiKey == "" {
		t.Skip("INTEGRATION_TEST_VOYAGE_API_KEY not set")
	}

	p := newProvider(apiKey, "voyage-3-lite", "", defaultBaseURL, 512)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := p.Validate(ctx); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	t.Log("Voyage embedding Validate succeeded")
}

func TestIntegration_ViaFactory(t *testing.T) {
	apiKey := os.Getenv("INTEGRATION_TEST_VOYAGE_API_KEY")
	if apiKey == "" {
		t.Skip("INTEGRATION_TEST_VOYAGE_API_KEY not set")
	}

	p, err := goembedding.NewProvider("voyage", goembedding.ProviderConfig{
		"api_key": apiKey,
		"model":   "voyage-3-lite",
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
	if len(result) != 1 || len(result[0]) != 512 {
		t.Fatalf("unexpected result shape: %d results", len(result))
	}
	t.Logf("Voyage factory embed succeeded: %d dims", len(result[0]))
}

func TestIntegration_InvalidAPIKey(t *testing.T) {
	p := newProvider("pa-invalid-key-12345", "voyage-3-lite", "", defaultBaseURL, 512)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := p.Embed(ctx, []string{"test"})
	if err == nil {
		t.Fatal("should return error for invalid API key")
	}
	t.Logf("Invalid key error: %v", err)
}
