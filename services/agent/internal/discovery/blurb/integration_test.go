//go:build integration

package blurb

import (
	"context"
	"os"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"

	// Register the providers we exercise. Blank imports wire the factories
	// into gollm.NewProvider.
	_ "github.com/decisionbox-io/decisionbox/providers/llm/bedrock"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/openai"
)

// Real blurb generation tests hit the winning spike combos:
//   * Bedrock Qwen3-32B  (best quality-per-dollar per FINDINGS.md §Cost×Quality)
//   * OpenAI gpt-4.1-nano (default when the project already uses OpenAI)
//
// Env-gated:
//   INTEGRATION_TEST_BEDROCK_REGION=us-east-1 (AWS creds required)
//   INTEGRATION_TEST_OPENAI_API_KEY=sk-... (or fall back to OPENAI_API_KEY)

func ordersSchema() Input {
	return Input{
		Dataset: "sales",
		Schema: models.TableSchema{
			TableName: "orders",
			RowCount:  1_234_567,
			Columns: []models.ColumnInfo{
				{Name: "order_id", Type: "INT64", Category: "primary_key"},
				{Name: "customer_id", Type: "INT64"},
				{Name: "order_date", Type: "DATE", Category: "time"},
				{Name: "status", Type: "STRING", Nullable: false, Category: "dimension"},
				{Name: "total_usd", Type: "FLOAT64", Nullable: true, Category: "metric"},
			},
			KeyColumns: []string{"order_id", "customer_id"},
			SampleData: []map[string]interface{}{
				{"order_id": 1, "customer_id": 42, "status": "shipped", "total_usd": 129.99},
				{"order_id": 2, "customer_id": 43, "status": "pending", "total_usd": 45.50},
			},
		},
		DomainPackBlurb: "E-commerce warehouse for a mid-size online retailer.",
	}
}

func usersSchema() Input {
	return Input{
		Dataset: "sales",
		Schema: models.TableSchema{
			TableName: "users",
			RowCount:  250_000,
			Columns: []models.ColumnInfo{
				{Name: "user_id", Type: "INT64", Category: "primary_key"},
				{Name: "email", Type: "STRING", Nullable: true, Category: "dimension"},
				{Name: "signup_ts", Type: "TIMESTAMP", Category: "time"},
				{Name: "plan", Type: "STRING", Category: "dimension"},
			},
			KeyColumns: []string{"user_id"},
		},
		DomainPackBlurb: "E-commerce warehouse for a mid-size online retailer.",
	}
}

func TestInteg_Blurb_BedrockQwen(t *testing.T) {
	region := os.Getenv("INTEGRATION_TEST_BEDROCK_REGION")
	if region == "" {
		t.Skip("INTEGRATION_TEST_BEDROCK_REGION not set")
	}

	model := os.Getenv("INTEGRATION_TEST_BEDROCK_BLURB_MODEL")
	if model == "" {
		// Default to the spike-winning Qwen3-32B on Bedrock.
		model = "qwen.qwen3-32b-v1:0"
	}

	llm, err := gollm.NewProvider("bedrock", gollm.ProviderConfig{
		"region": region,
		"model":  model,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	g, err := New(Config{LLM: llm, Model: model, ProviderName: "bedrock", Workers: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	outs, err := g.Generate(context.Background(), []Input{ordersSchema(), usersSchema()}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, o := range outs {
		if o.Err != nil {
			t.Errorf("blurb failed for %s: %v", o.Table, o.Err)
			continue
		}
		if len(o.Blurb) < 40 {
			t.Errorf("blurb for %s too short: %q", o.Table, o.Blurb)
		}
		// Grounding smoke test: real table name should appear in the
		// blurb (the spike showed this consistently across all
		// non-reasoning models).
		if !strings.Contains(strings.ToLower(o.Blurb), strings.ToLower(o.Table)) {
			t.Logf("note: table name %q not literally in blurb %q (acceptable but unusual)", o.Table, o.Blurb)
		}
		if o.OutputTokens == 0 {
			t.Errorf("usage missing for %s", o.Table)
		}
	}
}

func TestInteg_Blurb_OpenAIGPT41Nano(t *testing.T) {
	key := os.Getenv("INTEGRATION_TEST_OPENAI_API_KEY")
	if key == "" {
		key = os.Getenv("OPENAI_API_KEY")
	}
	if key == "" {
		t.Skip("INTEGRATION_TEST_OPENAI_API_KEY not set")
	}

	model := "gpt-4.1-nano"
	if m := os.Getenv("INTEGRATION_TEST_OPENAI_BLURB_MODEL"); m != "" {
		model = m
	}

	llm, err := gollm.NewProvider("openai", gollm.ProviderConfig{
		"api_key": key,
		"model":   model,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	g, err := New(Config{LLM: llm, Model: model, ProviderName: "openai", Workers: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	outs, err := g.Generate(context.Background(), []Input{ordersSchema(), usersSchema()}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, o := range outs {
		if o.Err != nil {
			t.Errorf("blurb failed for %s: %v", o.Table, o.Err)
			continue
		}
		if o.Blurb == "" {
			t.Errorf("empty blurb for %s", o.Table)
		}
	}
}
