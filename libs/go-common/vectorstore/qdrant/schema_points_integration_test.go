//go:build integration_qdrant

package qdrant

import (
	"context"
	"testing"

	pb "github.com/qdrant/go-client/qdrant"
)

// Exercises the schema-editor point operations (Get/Upsert/SetPayload/Delete)
// against a real Qdrant. The per-project schema collection is created directly
// here (the agent's indexer owns that in production).
func TestIntegrationSchemaPointCRUD(t *testing.T) {
	ctx := context.Background()
	const proj = "schemaedit-integ-1"
	const wh = "default"
	const table = "dbo.orders"
	dims := 8
	name := schemaCollectionName(proj)

	// Create the per-project schema collection (indexer's job in prod).
	if err := testProvider.client.CreateCollection(ctx, &pb.CreateCollection{
		CollectionName: name,
		VectorsConfig: pb.NewVectorsConfig(&pb.VectorParams{
			Size:     uint64(dims),
			Distance: pb.Distance_Cosine,
		}),
	}); err != nil {
		t.Fatalf("create schema collection: %v", err)
	}

	vec := make([]float64, dims)
	for i := range vec {
		vec[i] = 0.1 * float64(i+1)
	}
	payload := map[string]interface{}{
		"project_id":      proj,
		"warehouse_id":    wh,
		"table":           "orders",
		"dataset":         "dbo",
		"blurb":           "original blurb",
		"keywords":        []interface{}{"orders"},
		"row_count":       int64(100),
		"column_count":    int64(3),
		"blurb_model":     "prior/model",
		"embedding_model": "test/embed",
	}

	t.Run("upsert then get", func(t *testing.T) {
		if err := testProvider.UpsertSchemaPoint(ctx, proj, wh, table, vec, payload); err != nil {
			t.Fatalf("UpsertSchemaPoint: %v", err)
		}
		got, err := testProvider.GetSchemaPoints(ctx, proj, wh, []string{table})
		if err != nil {
			t.Fatalf("GetSchemaPoints: %v", err)
		}
		p, ok := got[table]
		if !ok {
			t.Fatalf("point for %q not returned", table)
		}
		if p.Payload["blurb"] != "original blurb" {
			t.Errorf("blurb = %v", p.Payload["blurb"])
		}
	})

	t.Run("set payload patches without a vector", func(t *testing.T) {
		if err := testProvider.SetSchemaPayload(ctx, proj, wh, table, map[string]interface{}{
			"keywords":     []interface{}{"orders", "revenue"},
			"column_count": int64(2),
		}); err != nil {
			t.Fatalf("SetSchemaPayload: %v", err)
		}
		got, _ := testProvider.GetSchemaPoints(ctx, proj, wh, []string{table})
		kws, _ := got[table].Payload["keywords"].([]interface{})
		if len(kws) != 2 {
			t.Errorf("keywords after patch = %v", got[table].Payload["keywords"])
		}
		// The blurb (not in the patch) must be preserved by the merge.
		if got[table].Payload["blurb"] != "original blurb" {
			t.Errorf("blurb clobbered by payload patch: %v", got[table].Payload["blurb"])
		}
	})

	t.Run("delete removes the point", func(t *testing.T) {
		if err := testProvider.DeleteSchemaPoint(ctx, proj, wh, table); err != nil {
			t.Fatalf("DeleteSchemaPoint: %v", err)
		}
		got, _ := testProvider.GetSchemaPoints(ctx, proj, wh, []string{table})
		if _, ok := got[table]; ok {
			t.Errorf("point still present after delete")
		}
	})

	t.Run("missing collection is a no-op / empty", func(t *testing.T) {
		const gone = "no-such-project"
		got, err := testProvider.GetSchemaPoints(ctx, gone, wh, []string{table})
		if err != nil || len(got) != 0 {
			t.Fatalf("GetSchemaPoints on missing collection: err=%v len=%d", err, len(got))
		}
		if err := testProvider.SetSchemaPayload(ctx, gone, wh, table, map[string]interface{}{"keywords": []interface{}{"x"}}); err != nil {
			t.Errorf("SetSchemaPayload on missing collection should be a no-op, got %v", err)
		}
		if err := testProvider.DeleteSchemaPoint(ctx, gone, wh, table); err != nil {
			t.Errorf("DeleteSchemaPoint on missing collection should be a no-op, got %v", err)
		}
	})
}
