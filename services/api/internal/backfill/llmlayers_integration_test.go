//go:build integration

package backfill

import (
	"context"
	"testing"

	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// The per-layer backfill, against a real MongoDB.
//
// What is worth proving here is not that it can write three fields — it is
// what it leaves alone. The reason this backfill is a targeted $set rather
// than a project save is that a whole-document write would take the embedding
// block with it, and on a live project that block may carry a probed
// `dimensions` matching an existing vector collection. A test that only
// checked the three new fields would pass just as happily on the dangerous
// implementation.

func TestInteg_BackfillLLMLayers_StampsAndLeavesEverythingElseAlone(t *testing.T) {
	client, cleanup := backfillMongo(t)
	defer cleanup()
	ctx := context.Background()
	col := client.Collection(projectsCollection)

	t.Setenv("AI_GATEWAY_URL", "https://ai.example.test/v1")
	t.Setenv("AI_GATEWAY_ANALYSIS_MODEL", "decisionbox-analysis-model")
	t.Setenv("AI_GATEWAY_BLURB_MODEL", "decisionbox-blurb-model")
	t.Setenv("AI_GATEWAY_EMBED_MODEL", "decisionbox-embed-model")
	t.Setenv("LLM_API_KEY", "k")
	t.Setenv("EMBEDDING_API_KEY", "k")

	// A project exactly as a managed deployment left it before per-layer
	// models: AI config stamped by the old managedai, no per-layer fields,
	// and — the part that matters — a probed embedding dimension.
	legacy := primitive.NewObjectID()
	if _, err := col.InsertOne(ctx, bson.M{
		"_id":  legacy,
		"name": "legacy managed project",
		"llm": bson.M{"provider": "openai", "model": "decisionbox-analysis-model",
			"config": bson.M{"base_url": "https://ai.example.test/v1"}},
		"embedding": bson.M{"provider": "openai", "model": "decisionbox-embed-model",
			"config": bson.M{"base_url": "https://ai.example.test/v1", "dimensions": "1536"}},
	}); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}

	// A project already stamped — re-running must not touch it.
	done := primitive.NewObjectID()
	if _, err := col.InsertOne(ctx, bson.M{
		"_id":                done,
		"name":               "already stamped",
		"llm_config_version": llmlayer.ConfigVersion,
		"layer_models": bson.M{llmlayer.DefaultLayer: bson.M{
			"provider_id": llmlayer.DefaultProviderID, "model": "decisionbox-analysis-model"}},
	}); err != nil {
		t.Fatalf("seed done: %v", err)
	}

	// Dry run first: it must report the work and write nothing.
	counts, err := backfillLLMLayers(ctx, col, "", true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if counts.stamped != 1 || counts.skipped != 1 || counts.scanned != 2 {
		t.Fatalf("dry run counts = %+v, want scanned 2 / stamped 1 / skipped 1", counts)
	}
	var afterDry bson.M
	if err := col.FindOne(ctx, bson.M{"_id": legacy}).Decode(&afterDry); err != nil {
		t.Fatalf("read after dry run: %v", err)
	}
	if _, present := afterDry["layer_models"]; present {
		t.Fatal("dry run wrote layer_models — a dry run that writes is worse than no dry run")
	}

	// The real thing.
	counts, err = backfillLLMLayers(ctx, col, "", false)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if counts.stamped != 1 || counts.skipped != 1 {
		t.Fatalf("counts = %+v, want stamped 1 / skipped 1", counts)
	}

	var got bson.M
	if err := col.FindOne(ctx, bson.M{"_id": legacy}).Decode(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}

	// Stamped: every layer plus the reserved default row.
	layers, ok := got["layer_models"].(bson.M)
	if !ok {
		t.Fatalf("layer_models missing or wrong type: %T", got["layer_models"])
	}
	if want := len(llmlayer.Keys()) + 1; len(layers) != want {
		t.Errorf("layer_models has %d rows, want %d (every layer plus the default row)", len(layers), want)
	}
	if v, _ := got["llm_config_version"].(int32); int(v) != llmlayer.ConfigVersion {
		t.Errorf("llm_config_version = %v, want %d", got["llm_config_version"], llmlayer.ConfigVersion)
	}
	if providers, ok := got["llm_providers"].(bson.A); !ok || len(providers) != 1 {
		t.Errorf("llm_providers = %v, want exactly one managed entry", got["llm_providers"])
	}

	// The point of the targeted $set: the probed embedding dimension survives.
	emb, ok := got["embedding"].(bson.M)
	if !ok {
		t.Fatalf("embedding block missing: %T", got["embedding"])
	}
	embCfg, ok := emb["config"].(bson.M)
	if !ok {
		t.Fatalf("embedding.config missing: %T", emb["config"])
	}
	if embCfg["dimensions"] != "1536" {
		t.Errorf("embedding dimensions = %v, want \"1536\" preserved — a whole-document write would have dropped it, "+
			"forcing a re-probe or mismatching the existing vector collection", embCfg["dimensions"])
	}
	if got["name"] != "legacy managed project" {
		t.Errorf("name = %v, want untouched", got["name"])
	}

	// Idempotent: a second run finds nothing to do.
	counts, err = backfillLLMLayers(ctx, col, "", false)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if counts.stamped != 0 || counts.skipped != 2 {
		t.Errorf("second run counts = %+v, want stamped 0 / skipped 2 — re-running must be safe", counts)
	}
}

func TestInteg_BackfillLLMLayers_ProjectIDScopesTheWrite(t *testing.T) {
	client, cleanup := backfillMongo(t)
	defer cleanup()
	ctx := context.Background()
	col := client.Collection(projectsCollection)

	t.Setenv("AI_GATEWAY_URL", "https://ai.example.test/v1")
	t.Setenv("AI_GATEWAY_ANALYSIS_MODEL", "decisionbox-analysis-model")
	t.Setenv("AI_GATEWAY_BLURB_MODEL", "decisionbox-blurb-model")
	t.Setenv("AI_GATEWAY_EMBED_MODEL", "decisionbox-embed-model")
	t.Setenv("LLM_API_KEY", "k")
	t.Setenv("EMBEDDING_API_KEY", "k")

	target, other := primitive.NewObjectID(), primitive.NewObjectID()
	for _, id := range []primitive.ObjectID{target, other} {
		if _, err := col.InsertOne(ctx, bson.M{"_id": id, "name": "unstamped"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// This is how the first production run is done: one tenant, one project.
	counts, err := backfillLLMLayers(ctx, col, target.Hex(), false)
	if err != nil {
		t.Fatalf("scoped backfill: %v", err)
	}
	if counts.scanned != 1 || counts.stamped != 1 {
		t.Fatalf("counts = %+v, want it to have looked at exactly one project", counts)
	}

	var untouched bson.M
	if err := col.FindOne(ctx, bson.M{"_id": other}).Decode(&untouched); err != nil {
		t.Fatalf("read other: %v", err)
	}
	if _, present := untouched["layer_models"]; present {
		t.Error("-project-id stamped a project it was not pointed at")
	}

	if _, err := backfillLLMLayers(ctx, col, "not-a-hex-id", false); err == nil {
		t.Error("a malformed -project-id must be refused, not silently treated as 'all projects'")
	}
}
