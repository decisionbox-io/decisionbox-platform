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

// SchemaCacheEntry is the on-disk shape of a project_schema_cache row —
// mirror of the agent-side database.SchemaCacheEntry (the agent writes these
// during indexing). SchemaKey is the qualified table name (e.g. "dbo.orders");
// Schema carries the columns + sample metadata. Kept in lockstep with the
// agent definition; a drift silently drops fields on decode.
type SchemaCacheEntry struct {
	ProjectID     string             `bson:"project_id"`
	WarehouseID   string             `bson:"warehouse_id"`
	WarehouseHash string             `bson:"warehouse_hash"`
	SchemaKey     string             `bson:"schema_key"`
	Schema        models.TableSchema `bson:"schema"`
	CachedAt      time.Time          `bson:"cached_at"`
}

// SchemaCacheRepository provides the API-side surface for the
// project_schema_cache collection. The agent owns bulk Find/Save (those run
// inside the index-schema subprocess); the API needs to drop rows when the
// user clicks "Clear schema cache", list what's indexed, and — for the schema
// editor — read one table's full schema and apply single-row column removals /
// table deletions.
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

// ListEntries returns the full cached schema rows (columns + sample metadata,
// not just the table name) for one of a project's warehouses, sorted by
// schema_key ascending. Backs the schema editor, which shows each table's
// columns + blurb. Empty (non-nil) slice when nothing is cached.
func (r *SchemaCacheRepository) ListEntries(ctx context.Context, projectID, warehouseID string) ([]SchemaCacheEntry, error) {
	if projectID == "" {
		return nil, errors.New("projectID is required")
	}
	opts := options.Find().SetSort(bson.D{{Key: "schema_key", Value: 1}})
	cur, err := r.col.Find(ctx, bson.M{
		"project_id":   projectID,
		"warehouse_id": schemaCacheWarehouseCond(warehouseID),
	}, opts)
	if err != nil {
		return nil, fmt.Errorf("schema cache list entries: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()
	entries := make([]SchemaCacheEntry, 0)
	if err := cur.All(ctx, &entries); err != nil {
		return nil, fmt.Errorf("decode schema cache entries: %w", err)
	}
	return entries, nil
}

// GetEntry returns a single cached table (by qualified schema_key) for one
// warehouse, or (nil, nil) when it isn't cached. schemaKey is the qualified
// name the agent stored (e.g. "dbo.orders").
func (r *SchemaCacheRepository) GetEntry(ctx context.Context, projectID, warehouseID, schemaKey string) (*SchemaCacheEntry, error) {
	if projectID == "" || schemaKey == "" {
		return nil, errors.New("projectID and schemaKey are required")
	}
	var e SchemaCacheEntry
	err := r.col.FindOne(ctx, bson.M{
		"project_id":   projectID,
		"warehouse_id": schemaCacheWarehouseCond(warehouseID),
		"schema_key":   schemaKey,
	}).Decode(&e)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, fmt.Errorf("schema cache get entry: %w", err)
	}
	return &e, nil
}

// UpdateColumns replaces a cached table's column set (and the derived
// key_columns / metrics / dimensions the caller narrowed to the surviving
// columns) so a manual column removal is reflected in what the discovery agent
// reads next run. sampleData is the cached sample rows already filtered to the
// kept columns — the removed column's *values* must be stripped here too, or
// the discovery / Ask schema provider (which surfaces SampleData as sample
// rows) would keep leaking the removed column's data until a cache rebuild.
// Returns mongo.ErrNoDocuments when the table isn't cached.
func (r *SchemaCacheRepository) UpdateColumns(ctx context.Context, projectID, warehouseID, schemaKey string, columns []models.ColumnInfo, keyColumns, metrics, dimensions []string, sampleData []map[string]interface{}) error {
	if projectID == "" || schemaKey == "" {
		return errors.New("projectID and schemaKey are required")
	}
	res, err := r.col.UpdateOne(ctx, bson.M{
		"project_id":   projectID,
		"warehouse_id": schemaCacheWarehouseCond(warehouseID),
		"schema_key":   schemaKey,
	}, bson.M{"$set": bson.M{
		"schema.columns":     columns,
		"schema.key_columns": keyColumns,
		"schema.metrics":     metrics,
		"schema.dimensions":  dimensions,
		"schema.sample_data": sampleData,
	}})
	if err != nil {
		return fmt.Errorf("schema cache update columns: %w", err)
	}
	if res.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return nil
}

// DeleteTable removes one cached table (by qualified schema_key) for a
// warehouse so it no longer appears in the discovery agent's schema view.
// Returns mongo.ErrNoDocuments when the table isn't cached.
func (r *SchemaCacheRepository) DeleteTable(ctx context.Context, projectID, warehouseID, schemaKey string) error {
	if projectID == "" || schemaKey == "" {
		return errors.New("projectID and schemaKey are required")
	}
	res, err := r.col.DeleteOne(ctx, bson.M{
		"project_id":   projectID,
		"warehouse_id": schemaCacheWarehouseCond(warehouseID),
		"schema_key":   schemaKey,
	})
	if err != nil {
		return fmt.Errorf("schema cache delete table: %w", err)
	}
	if res.DeletedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return nil
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
