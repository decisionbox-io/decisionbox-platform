//go:build integration

package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// The schema editor's single-row cache operations (ListEntries, GetEntry,
// UpdateColumns, DeleteTable) against a real Mongo. The agent normally writes
// these rows; here we seed them directly to exercise the API-side reads/edits.
func seedCacheEntry(t *testing.T, ctx context.Context, proj, wh, key string, cols []models.ColumnInfo) {
	t.Helper()
	_, err := testDB.Collection("project_schema_cache").InsertOne(ctx, SchemaCacheEntry{
		ProjectID:     proj,
		WarehouseID:   wh,
		WarehouseHash: "h1",
		SchemaKey:     key,
		Schema: models.TableSchema{
			TableName:  key,
			RowCount:   42,
			Columns:    cols,
			KeyColumns: []string{"id"},
			Metrics:    []string{"amount"},
			Dimensions: []string{"status"},
		},
		CachedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("seed cache entry: %v", err)
	}
}

func TestInteg_SchemaCache_EditorRowOps(t *testing.T) {
	ctx := context.Background()
	r := NewSchemaCacheRepository(testDB)
	proj := "proj-cache-edit-integ-1"
	t.Cleanup(func() {
		_, _ = testDB.Collection("project_schema_cache").DeleteMany(ctx, bson.M{"project_id": proj})
	})

	// Two tables on the default warehouse (warehouse_id="default").
	seedCacheEntry(t, ctx, proj, "default", "dbo.orders", []models.ColumnInfo{
		{Name: "id", Type: "int"}, {Name: "amount", Type: "numeric"}, {Name: "status", Type: "text"},
	})
	seedCacheEntry(t, ctx, proj, "default", "dbo.customers", []models.ColumnInfo{
		{Name: "id", Type: "int"},
	})

	t.Run("ListEntries sorted, default matches empty datasource id", func(t *testing.T) {
		entries, err := r.ListEntries(ctx, proj, "") // "" resolves to default rows
		if err != nil {
			t.Fatalf("ListEntries: %v", err)
		}
		if len(entries) != 2 {
			t.Fatalf("got %d entries, want 2", len(entries))
		}
		if entries[0].SchemaKey != "dbo.customers" || entries[1].SchemaKey != "dbo.orders" {
			t.Errorf("sort wrong: %s, %s", entries[0].SchemaKey, entries[1].SchemaKey)
		}
		if len(entries[1].Schema.Columns) != 3 {
			t.Errorf("orders columns = %d, want 3", len(entries[1].Schema.Columns))
		}
	})

	t.Run("GetEntry hit + miss", func(t *testing.T) {
		e, err := r.GetEntry(ctx, proj, "default", "dbo.orders")
		if err != nil {
			t.Fatalf("GetEntry: %v", err)
		}
		if e == nil || e.Schema.RowCount != 42 {
			t.Fatalf("GetEntry hit wrong: %+v", e)
		}
		miss, err := r.GetEntry(ctx, proj, "default", "dbo.nope")
		if err != nil {
			t.Fatalf("GetEntry miss: %v", err)
		}
		if miss != nil {
			t.Fatalf("expected nil for missing table, got %+v", miss)
		}
	})

	t.Run("UpdateColumns removes a column + derived lists + sample values", func(t *testing.T) {
		// Keep id + status (drop "amount", which is the metric). The filtered
		// sample rows must no longer carry "amount".
		kept := []models.ColumnInfo{{Name: "id", Type: "int"}, {Name: "status", Type: "text"}}
		samples := []map[string]interface{}{{"id": int64(1), "status": "ok"}}
		if err := r.UpdateColumns(ctx, proj, "default", "dbo.orders", kept, []string{"id"}, []string{}, []string{"status"}, samples); err != nil {
			t.Fatalf("UpdateColumns: %v", err)
		}
		e, _ := r.GetEntry(ctx, proj, "default", "dbo.orders")
		if len(e.Schema.Columns) != 2 {
			t.Fatalf("columns after update = %d, want 2", len(e.Schema.Columns))
		}
		if len(e.Schema.Metrics) != 0 {
			t.Errorf("metrics should be emptied, got %v", e.Schema.Metrics)
		}
		if len(e.Schema.Dimensions) != 1 || e.Schema.Dimensions[0] != "status" {
			t.Errorf("dimensions wrong: %v", e.Schema.Dimensions)
		}
		if len(e.Schema.SampleData) != 1 {
			t.Fatalf("sample data not persisted: %v", e.Schema.SampleData)
		}
		if _, leaked := e.Schema.SampleData[0]["amount"]; leaked {
			t.Errorf("removed column 'amount' still present in sample row: %v", e.Schema.SampleData[0])
		}
	})

	t.Run("UpdateColumns missing table → ErrNoDocuments", func(t *testing.T) {
		err := r.UpdateColumns(ctx, proj, "default", "dbo.nope", nil, nil, nil, nil, nil)
		if !errors.Is(err, mongo.ErrNoDocuments) {
			t.Fatalf("err = %v, want ErrNoDocuments", err)
		}
	})

	t.Run("DeleteTable removes the row", func(t *testing.T) {
		if err := r.DeleteTable(ctx, proj, "default", "dbo.customers"); err != nil {
			t.Fatalf("DeleteTable: %v", err)
		}
		e, _ := r.GetEntry(ctx, proj, "default", "dbo.customers")
		if e != nil {
			t.Fatalf("expected table gone, got %+v", e)
		}
		if err := r.DeleteTable(ctx, proj, "default", "dbo.customers"); !errors.Is(err, mongo.ErrNoDocuments) {
			t.Fatalf("second delete err = %v, want ErrNoDocuments", err)
		}
	})
}
