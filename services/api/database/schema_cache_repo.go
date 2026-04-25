package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

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
