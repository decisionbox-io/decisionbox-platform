//go:build integration

package bedrock

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
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

	p := newProvider(nil, region, "amazon.titan-embed-text-v2:0", 1024)

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
