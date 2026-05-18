//go:build integration

package bedrock

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

// TestIntegration_ListModels exercises the live Bedrock control-plane
// ListFoundationModels call filtered to EMBEDDING modality. Skipped
// unless INTEGRATION_TEST_BEDROCK_REGION is set; uses the default AWS
// credential chain (so a valid IAM identity must be configured).
func TestIntegration_ListModels(t *testing.T) {
	region := os.Getenv("INTEGRATION_TEST_BEDROCK_REGION")
	if region == "" {
		t.Skip("INTEGRATION_TEST_BEDROCK_REGION not set")
	}

	// Resolve an aws.Config the way the factory would for iam_role auth
	// — falls through to the SDK's ambient chain. This exercises the
	// same code path as a real ListModels call now that the provider
	// reuses the factory-built awsCfg instead of re-deriving it.
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(region))
	if err != nil {
		t.Fatalf("load default aws config: %v", err)
	}
	p := newProvider(nil, awsCfg, region, "amazon.titan-embed-text-v2:0", 1024)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	models, err := p.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels error: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("expected at least one embedding model from Bedrock; got 0")
	}

	// Every row must carry a non-empty ID. The Titan family is what
	// every commercial Bedrock region serves today, so assert at least
	// one ID starts with "amazon.titan-embed-".
	sawTitan := false
	for _, m := range models {
		if m.ID == "" {
			t.Errorf("got an embedding model with empty ID: %+v", m)
		}
		if strings.HasPrefix(m.ID, "amazon.titan-embed-") {
			sawTitan = true
		}
	}
	if !sawTitan {
		t.Errorf("expected at least one amazon.titan-embed-* model in region %q, got: %+v", region, models)
	}
}
