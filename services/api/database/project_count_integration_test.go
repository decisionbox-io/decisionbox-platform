//go:build integration

package database

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

// CountWithWarehouse is the ground-truth data-source count reconciled against
// data_sources_per_deployment. Each warehouse is one data source: a
// multi-warehouse project contributes len(warehouses), a legacy single-warehouse
// project contributes 1, and an unconfigured project contributes 0 — so the
// total must sum warehouses across projects, not count projects.
func TestInteg_CountWithWarehouse_SumsWarehousesAcrossProjects(t *testing.T) {
	ctx := context.Background()
	col := testDB.Collection("projects")
	// Isolate: the count is deployment-wide, so start from a clean slate.
	if _, err := col.DeleteMany(ctx, bson.M{}); err != nil {
		t.Fatalf("clear projects: %v", err)
	}
	t.Cleanup(func() { _, _ = col.DeleteMany(ctx, bson.M{}) })

	repo := NewProjectRepository(testDB)

	// Empty deployment → 0.
	if n, err := repo.CountWithWarehouse(ctx); err != nil || n != 0 {
		t.Fatalf("empty deployment count = %d (err %v), want 0", n, err)
	}

	docs := []interface{}{
		// Legacy single warehouse → 1.
		bson.M{"name": "legacy", "warehouse": bson.M{"provider": "postgres", "datasets": bson.A{"public"}}},
		// Multi-warehouse → 3.
		bson.M{"name": "multi", "warehouses": bson.A{
			bson.M{"id": "wh_a", "provider": "postgres"},
			bson.M{"id": "wh_b", "provider": "redshift"},
			bson.M{"id": "wh_c", "provider": "bigquery"},
		}},
		// Unconfigured (no warehouse) → 0.
		bson.M{"name": "blank"},
		// Empty legacy provider → 0.
		bson.M{"name": "empty-provider", "warehouse": bson.M{"provider": ""}},
	}
	if _, err := col.InsertMany(ctx, docs); err != nil {
		t.Fatalf("seed projects: %v", err)
	}

	got, err := repo.CountWithWarehouse(ctx)
	if err != nil {
		t.Fatalf("CountWithWarehouse: %v", err)
	}
	if want := 4; got != want { // 1 (legacy) + 3 (multi) + 0 + 0
		t.Fatalf("data-source count = %d, want %d (sum of warehouses, not project count)", got, want)
	}
}
