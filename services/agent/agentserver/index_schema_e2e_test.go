//go:build e2e

package agentserver

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/config"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TestE2E_IndexSchema_OlistThroughGateway runs the real schema-index pipeline
// end-to-end against a BigQuery warehouse (the olist demo dataset by default),
// routing blurb generation through the DecisionBox AI gateway alias and
// embedding through Bedrock Titan, into a real Qdrant. It exercises the path
// that surfaced issue #11's "dimensions must be positive, got 0" bug:
// discover_schemas → ensure_collection (dimension-resolved) → blurbs → embed →
// qdrant_upsert. A nil error means every leg, including correct collection
// sizing, succeeded.
//
// Gated behind the e2e tag and DBX_E2E_INDEX=1 plus the usual agent env
// (MONGODB_URI, QDRANT_URL — gRPC host:port — and ambient GCP ADC + AWS creds).
// Point blurbs at the gateway with DBX_GATEWAY_URL / DBX_GATEWAY_KEY; without
// them the blurb LLM falls back to Bedrock Haiku.
//
//	DBX_E2E_INDEX=1 MONGODB_URI=... QDRANT_URL=127.0.0.1:6334 \
//	DBX_GATEWAY_URL=http://127.0.0.1:18080/v1 DBX_GATEWAY_KEY=dbx-... \
//	go test -tags e2e -run TestE2E_IndexSchema_Olist ./services/agent/agentserver/ -v
func TestE2E_IndexSchema_OlistThroughGateway(t *testing.T) {
	if os.Getenv("DBX_E2E_INDEX") != "1" {
		t.Skip("set DBX_E2E_INDEX=1 (and the warehouse/gateway env) to run the olist index e2e")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.Qdrant.URL == "" {
		t.Skip("QDRANT_URL is required for the index e2e")
	}

	whProject := envOr("DBX_E2E_WAREHOUSE_PROJECT", "decisionbox")
	whDataset := envOr("DBX_E2E_WAREHOUSE_DATASET", "demo_olist_ecommerce")
	region := envOr("AWS_REGION", "us-east-1")

	// Blurb LLM: the gateway alias when the gateway env is set, else Bedrock.
	blurb := bson.M{"provider": "bedrock", "model": "us.anthropic.claude-haiku-4-5-20251001-v1:0",
		"config": bson.M{"region": region, "auth_method": "iam_role"}}
	if url := os.Getenv("DBX_GATEWAY_URL"); url != "" && os.Getenv("DBX_GATEWAY_KEY") != "" {
		blurb = bson.M{"provider": "openai", "model": "decisionbox-blurb-model",
			"config": bson.M{"base_url": url, "auth_method": "api_key"}}
		t.Setenv("BLURB_LLM_API_KEY", os.Getenv("DBX_GATEWAY_KEY"))
	}

	ctx := context.Background()
	mongoClient, err := initMongoDB(ctx, cfg)
	if err != nil {
		t.Fatalf("initMongoDB: %v", err)
	}
	defer func() { _ = mongoClient.Disconnect(ctx) }()

	oid := primitive.NewObjectID()
	projects := database.New(mongoClient).Collection(database.CollectionProjects)
	doc := bson.M{
		"_id": oid, "name": "Olist E2E (index)", "domain": "ecommerce", "category": "ecommerce",
		"description": "Brazilian Olist e-commerce marketplace: orders, items, payments, reviews, customers, sellers, products.",
		"warehouse": bson.M{"provider": "bigquery", "project_id": whProject, "location": "US",
			"datasets": []string{whDataset}, "config": bson.M{"auth_method": "adc"}},
		"llm":       bson.M{"provider": "bedrock", "model": "us.anthropic.claude-haiku-4-5-20251001-v1:0", "config": bson.M{"region": region, "auth_method": "iam_role"}},
		"blurb_llm": blurb,
		"embedding": bson.M{"provider": "bedrock", "model": "amazon.titan-embed-text-v2:0", "config": bson.M{"region": region, "auth_method": "iam_role"}},
		"status":    "ready", "created_at": time.Now(), "updated_at": time.Now(),
	}
	if _, err := projects.InsertOne(ctx, doc); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	t.Cleanup(func() {
		_, _ = projects.DeleteOne(context.Background(), bson.M{"_id": oid})
	})

	// The actual production index path. A nil return means discover_schemas →
	// ensure_collection → blurbs → embed → qdrant_upsert all succeeded — and
	// because Qdrant rejects vectors whose length doesn't match the collection,
	// a successful upsert is itself proof the collection was sized correctly
	// (the bug this fixes created a 0-dim collection and failed here).
	if err := runIndexSchema(cfg, oid.Hex(), "e2e-index"); err != nil {
		t.Fatalf("runIndexSchema against %s.%s: %v", whProject, whDataset, err)
	}
	t.Logf("indexed %s.%s end-to-end through the gateway blurb alias", whProject, whDataset)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
