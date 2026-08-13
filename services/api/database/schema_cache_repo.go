package database

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// schemaCacheWarehouseCond scopes a schema-cache query to one warehouse. The
// default/primary warehouse (empty id or "default") also matches rows written
// before warehouse_id existed (missing field / "" / null) so single-warehouse
// caches keep resolving; a named secondary warehouse matches its id exactly.
// Mirrors the agent-side write path's per-warehouse scoping.
func schemaCacheWarehouseCond(warehouseID string) interface{} {
	if warehouseID == "" || warehouseID == models.DefaultWarehouseID {
		return bson.M{"$in": bson.A{models.DefaultWarehouseID, "", nil}}
	}
	return warehouseID
}

// SchemaCacheRepository provides the API-side surface for the
// project_schema_cache collection. The agent owns Find/Save (those run
// inside the index-schema subprocess); the API only needs to drop rows
// when the user clicks "Clear schema cache" in Project Settings →
// Advanced. Keeping the API-side repo to a single Invalidate method
// matches the "least privilege" rule the rest of the API follows for
// agent-owned collections.
type SchemaCacheRepository struct {
	col *mongo.Collection
}

// NewSchemaCacheRepository wires the repo against project_schema_cache.
// Collection name must stay in sync with the agent-side
// CollectionSchemaCache constant and the index definition in init.go.
func NewSchemaCacheRepository(db *DB) *SchemaCacheRepository {
	return &SchemaCacheRepository{col: db.Collection("project_schema_cache")}
}

// Invalidate drops every cached schema row for a project so the next
// indexing run skips the cache and rediscovers from the warehouse.
// Idempotent — a no-op when nothing is cached.
func (r *SchemaCacheRepository) Invalidate(ctx context.Context, projectID string) error {
	if projectID == "" {
		return errors.New("projectID is required")
	}
	if _, err := r.col.DeleteMany(ctx, bson.M{"project_id": projectID}); err != nil {
		return fmt.Errorf("schema cache invalidate: %w", err)
	}
	return nil
}

// ListTables returns the distinct cached schema_key values for one of a
// project's warehouses, sorted ascending. Each schema_key is the qualified table
// name the agent stored — typically "<dataset>.<table>" for BigQuery,
// "<schema>.<table>" for Postgres / Redshift / Snowflake / Databricks,
// or "<schema>.<table>" (e.g. "dbo.orders") for MSSQL — i.e. whatever
// the warehouse provider chose to canonicalise on. Empty slice (not
// nil) when the cache is empty so JSON marshals it as `[]`. Read-only
// — the agent owns writes; this method exists so dashboard pages
// (the discovery scope picker, table-filter UIs, and similar) can
// show what the agent actually sees without reaching into the
// warehouse driver.
//
// warehouseID scopes the listing to one datasource (multi-warehouse): the
// dashboard's discovery-scope picker must show only the tables the discovery run
// can reach (the primary warehouse), not every warehouse indexed into the shared
// cache. The default/primary matches legacy rows written before warehouse_id
// existed; a named secondary matches its id exactly.
func (r *SchemaCacheRepository) ListTables(ctx context.Context, projectID, warehouseID string) ([]string, error) {
	if projectID == "" {
		return nil, errors.New("projectID is required")
	}
	values, err := r.col.Distinct(ctx, "schema_key", bson.M{
		"project_id":   projectID,
		"warehouse_id": schemaCacheWarehouseCond(warehouseID),
	})
	if err != nil {
		return nil, fmt.Errorf("schema cache list tables: %w", err)
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out, nil
}

// LastCachedAt returns the most recent cached_at timestamp across all
// rows for a project, or (zeroTime, nil) when the cache is empty for
// that project. The agent writes every row in a Save() with the same
// `now` value, so any single row's timestamp is the catalog-pass
// completion time — but we MAX over rows in case the cache spans more
// than one Save (concurrent agents would be a bug, but the query is
// cheap and safe).
func (r *SchemaCacheRepository) LastCachedAt(ctx context.Context, projectID string) (time.Time, error) {
	if projectID == "" {
		return time.Time{}, errors.New("projectID is required")
	}
	opts := options.FindOne().
		SetSort(bson.D{{Key: "cached_at", Value: -1}}).
		SetProjection(bson.M{"cached_at": 1, "_id": 0})
	var doc struct {
		CachedAt time.Time `bson:"cached_at"`
	}
	err := r.col.FindOne(ctx, bson.M{"project_id": projectID}, opts).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("schema cache last cached at: %w", err)
	}
	return doc.CachedAt, nil
}
