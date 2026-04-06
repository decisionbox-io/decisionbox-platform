//go:build integration

package bedrock

import (
	"context"
	"os"
	"testing"
	"time"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

func TestIntegration_EmbedSingleText(t *testing.T) {
	region := os.Getenv("INTEGRATION_TEST_BEDROCK_REGION")
	if region == "" {
		t.Skip("INTEGRATION_TEST_BEDROCK_REGION not set")
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(region))
	if err != nil {
		t.Fatalf("Failed to load AWS config: %v", err)
	}
	client := bedrockruntime.NewFromConfig(awsCfg)

	p := newProvider(client, region, "amazon.titan-embed-text-v2:0", 1024)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := p.Embed(ctx, []string{"The quick brown fox jumps over the lazy dog"})
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if len(result[0]) != 1024 {
		t.Fatalf("expected 1024 dims, got %d", len(result[0]))
	}
	t.Logf("Bedrock embed: %d dims, first value: %f", len(result[0]), result[0][0])
}

func TestIntegration_EmbedBatch(t *testing.T) {
	region := os.Getenv("INTEGRATION_TEST_BEDROCK_REGION")
	if region == "" {
		t.Skip("INTEGRATION_TEST_BEDROCK_REGION not set")
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(region))
	if err != nil {
		t.Fatalf("Failed to load AWS config: %v", err)
	}
	client := bedrockruntime.NewFromConfig(awsCfg)

	p := newProvider(client, region, "amazon.titan-embed-text-v2:0", 1024)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
		if len(vec) != 1024 {
			t.Errorf("result[%d]: expected 1024 dims, got %d", i, len(vec))
		}
	}
	t.Logf("Bedrock batch embed: %d texts, %d dims each", len(result), len(result[0]))
}

func TestIntegration_Validate(t *testing.T) {
	region := os.Getenv("INTEGRATION_TEST_BEDROCK_REGION")
	if region == "" {
		t.Skip("INTEGRATION_TEST_BEDROCK_REGION not set")
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(region))
	if err != nil {
		t.Fatalf("Failed to load AWS config: %v", err)
	}
	client := bedrockruntime.NewFromConfig(awsCfg)

	p := newProvider(client, region, "amazon.titan-embed-text-v2:0", 1024)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := p.Validate(ctx); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	t.Log("Bedrock embedding Validate succeeded")
}

func TestIntegration_ViaFactory(t *testing.T) {
	region := os.Getenv("INTEGRATION_TEST_BEDROCK_REGION")
	if region == "" {
		t.Skip("INTEGRATION_TEST_BEDROCK_REGION not set")
	}

	p, err := goembedding.NewProvider("bedrock", goembedding.ProviderConfig{
		"region": region,
		"model":  "amazon.titan-embed-text-v2:0",
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
	if len(result) != 1 || len(result[0]) != 1024 {
		t.Fatalf("unexpected result shape: %d results", len(result))
	}
	t.Logf("Bedrock factory embed succeeded: %d dims", len(result[0]))
}
