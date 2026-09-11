package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Run-history query bounds. DefaultSchemaIndexRunLimit applies when the caller
// doesn't specify one; MaxSchemaIndexRunLimit caps it so a crafted ?limit=
// can't ask for an unbounded scan.
const (
	DefaultSchemaIndexRunLimit = 50
	MaxSchemaIndexRunLimit     = 200
)

// SchemaIndexRunRepository reads the durable per-(datasource × run) result
// records the agent stamps on completion. One document per (project_id,
// datasource_id, run_id) in project_schema_index_runs. The agent writes; the
// API only reads (for the dashboard history + project roll-up).
type SchemaIndexRunRepository struct {
	col *mongo.Collection
}

// NewSchemaIndexRunRepository wires the repo against project_schema_index_runs.
func NewSchemaIndexRunRepository(db *DB) *SchemaIndexRunRepository {
	return &SchemaIndexRunRepository{col: db.Collection("project_schema_index_runs")}
}

// List returns a project's schema-index run records, newest finished_at first.
// datasourceID == "" lists every datasource; a non-empty value filters to one.
// limit <= 0 uses DefaultSchemaIndexRunLimit; anything above
// MaxSchemaIndexRunLimit is clamped. Returns an empty (non-nil) slice when no
// records match — the dashboard renders that as the empty-history state.
func (r *SchemaIndexRunRepository) List(ctx context.Context, projectID, datasourceID string, limit int) ([]models.SchemaIndexRun, error) {
	if projectID == "" {
		return nil, errors.New("projectID is required")
	}
	if limit <= 0 {
		limit = DefaultSchemaIndexRunLimit
	}
	if limit > MaxSchemaIndexRunLimit {
		limit = MaxSchemaIndexRunLimit
	}
	filter := bson.M{"project_id": projectID}
	if datasourceID != "" {
		filter["datasource_id"] = datasourceID
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "finished_at", Value: -1}}).
		SetLimit(int64(limit))
	cur, err := r.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list schema-index runs: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()
	runs := make([]models.SchemaIndexRun, 0)
	if err := cur.All(ctx, &runs); err != nil {
		return nil, fmt.Errorf("decode schema-index runs: %w", err)
	}
	return runs, nil
}

// LatestByDatasource returns the single most recent run per datasource for a
// project, newest finished_at first. Backs the project-page roll-up, which
// needs exactly one line per datasource — a single aggregation instead of
// client-side dedup of a bounded history page, so a datasource is never
// silently omitted just because its latest run fell outside the page. The
// (project_id, datasource_id, finished_at) index supports the match+sort.
func (r *SchemaIndexRunRepository) LatestByDatasource(ctx context.Context, projectID string) ([]models.SchemaIndexRun, error) {
	if projectID == "" {
		return nil, errors.New("projectID is required")
	}
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.D{{Key: "project_id", Value: projectID}}}},
		// Newest first within each datasource so $first is the latest run.
		{{Key: "$sort", Value: bson.D{{Key: "datasource_id", Value: 1}, {Key: "finished_at", Value: -1}}}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$datasource_id"},
			{Key: "doc", Value: bson.D{{Key: "$first", Value: "$$ROOT"}}},
		}}},
		{{Key: "$replaceRoot", Value: bson.D{{Key: "newRoot", Value: "$doc"}}}},
		// Stable display order across datasources: newest run first.
		{{Key: "$sort", Value: bson.D{{Key: "finished_at", Value: -1}}}},
	}
	cur, err := r.col.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate latest schema-index runs: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()
	runs := make([]models.SchemaIndexRun, 0)
	if err := cur.All(ctx, &runs); err != nil {
		return nil, fmt.Errorf("decode latest schema-index runs: %w", err)
	}
	return runs, nil
}
