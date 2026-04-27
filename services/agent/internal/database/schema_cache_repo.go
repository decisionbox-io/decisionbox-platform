package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// SchemaCacheRepository persists discovered warehouse table schemas so a
// subsequent BuildIndex call can skip the expensive catalog pass when the
// warehouse config (datasets, filters, connection shape) hasn't changed.
//
// Keyed by (project_id, warehouse_hash). The hash covers every input that
// could change what discover_schemas returns — a mismatch invalidates the
// cache implicitly via query miss, so we never need an explicit
// invalidation path for config edits. A 7-day TTL caps staleness for
// warehouses whose physical schema drifts without the project config
// changing.
//
// One doc per (project, schema_key). We intentionally don't bundle into a
// single per-project doc: Mongo's 16 MB BSON cap would be at risk on
// ERP-scale warehouses (FINPORT-class, 1400+ tables), and a partial write
// on a crashed run still leaves the previous cache intact.
type SchemaCacheRepository struct {
	db *DB
}

func NewSchemaCacheRepository(db *DB) *SchemaCacheRepository {
	return &SchemaCacheRepository{db: db}
}

func (r *SchemaCacheRepository) col() *mongo.Collection {
	return r.db.Collection(CollectionSchemaCache)
}

// SchemaCacheEntry is the on-disk shape. SchemaKey matches the key shape
// that DiscoverSchemas returns (e.g. "dbo.orders").
type SchemaCacheEntry struct {
	ProjectID     string             `bson:"project_id"`
	WarehouseHash string             `bson:"warehouse_hash"`
	SchemaKey     string             `bson:"schema_key"`
	Schema        models.TableSchema `bson:"schema"`
	CachedAt      time.Time          `bson:"cached_at"`
}

// Find returns the cached schema map for (projectID, warehouseHash) or
// (nil, nil) if the cache is cold or was invalidated by a hash change.
// An empty result is indistinguishable from "no cache" — callers treat
// both the same and fall through to fresh discovery.
func (r *SchemaCacheRepository) Find(ctx context.Context, projectID, warehouseHash string) (map[string]models.TableSchema, error) {
	if projectID == "" {
		return nil, errors.New("projectID is required")
	}
	if warehouseHash == "" {
		return nil, errors.New("warehouseHash is required")
	}
	cur, err := r.col().Find(ctx, bson.M{
		"project_id":     projectID,
		"warehouse_hash": warehouseHash,
	})
	if err != nil {
		return nil, fmt.Errorf("schema cache find: %w", err)
	}
	defer cur.Close(ctx)

	out := make(map[string]models.TableSchema)
	for cur.Next(ctx) {
		var e SchemaCacheEntry
		if err := cur.Decode(&e); err != nil {
			return nil, fmt.Errorf("schema cache decode: %w", err)
		}
		out[e.SchemaKey] = e.Schema
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("schema cache cursor: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// Save replaces the cached schemas for (projectID, warehouseHash). Every
// prior row for this project — including those tagged with a different
// hash — is dropped so stale warehouses don't accumulate. TTL handles
// absolute-age cleanup if the project is abandoned.
func (r *SchemaCacheRepository) Save(ctx context.Context, projectID, warehouseHash string, schemas map[string]models.TableSchema) error {
	if projectID == "" {
		return errors.New("projectID is required")
	}
	if warehouseHash == "" {
		return errors.New("warehouseHash is required")
	}
	if len(schemas) == 0 {
		return nil
	}

	if _, err := r.col().DeleteMany(ctx, bson.M{"project_id": projectID}); err != nil {
		return fmt.Errorf("schema cache clear prior: %w", err)
	}

	now := time.Now().UTC()
	docs := make([]interface{}, 0, len(schemas))
	for key, sch := range schemas {
		docs = append(docs, SchemaCacheEntry{
			ProjectID:     projectID,
			WarehouseHash: warehouseHash,
			SchemaKey:     key,
			Schema:        sch,
			CachedAt:      now,
		})
	}
	// Ordered=false so a single bad row doesn't abort the rest — the
	// cache is best-effort; a partial write just means a partial cache
	// hit next run.
	if _, err := r.col().InsertMany(ctx, docs, options.InsertMany().SetOrdered(false)); err != nil {
		return fmt.Errorf("schema cache save: %w", err)
	}
	return nil
}

// Invalidate drops every cache row for a project. Exposed for the API's
// "reindex from scratch" button and for tests.
func (r *SchemaCacheRepository) Invalidate(ctx context.Context, projectID string) error {
	if projectID == "" {
		return errors.New("projectID is required")
	}
	if _, err := r.col().DeleteMany(ctx, bson.M{"project_id": projectID}); err != nil {
		return fmt.Errorf("schema cache invalidate: %w", err)
	}
	return nil
}
