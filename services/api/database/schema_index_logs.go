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

// SchemaIndexLog is a single line of agent-subprocess output captured
// during a schema-indexing run. One row per raw stderr line emitted by
// the agent binary. Stored in project_schema_index_logs with a 7-day
// TTL — the dashboard polls the recent tail while an indexing run is
// live, and ops can query the whole collection when debugging a
// post-mortem.
//
// The collection sits on the API side rather than in the agent's own
// debug-logs pipeline so we don't need the agent to care about the
// "user enabled a debug UI" flag — the API already tees stderr for the
// live-tail feature in /tmp/dbx-schema-retrieval/api.log; we fan out
// another write to Mongo at the same point.
type SchemaIndexLog struct {
	ID        string    `bson:"_id,omitempty"        json:"id"`
	ProjectID string    `bson:"project_id"           json:"project_id"`
	RunID     string    `bson:"run_id"               json:"run_id"`
	Line      string    `bson:"line"                 json:"line"`
	CreatedAt time.Time `bson:"created_at"           json:"created_at"`
}

// SchemaIndexLogRepository wraps the project_schema_index_logs
// collection. The repo is write-heavy during an indexing run (one
// insert per agent stderr line) and read-rarely (only when a user
// opens the debug panel on a running or recently-finished project).
type SchemaIndexLogRepository struct {
	col *mongo.Collection
}

// NewSchemaIndexLogRepository wires the repo.
func NewSchemaIndexLogRepository(db *DB) *SchemaIndexLogRepository {
	return &SchemaIndexLogRepository{col: db.Collection("project_schema_index_logs")}
}

// Append adds one log line. No-op when projectID is empty — we never
// want to drop the whole runner over a missing id.
func (r *SchemaIndexLogRepository) Append(ctx context.Context, projectID, runID, line string) error {
	if projectID == "" || line == "" {
		return nil
	}
	doc := SchemaIndexLog{
		ProjectID: projectID,
		RunID:     runID,
		Line:      line,
		CreatedAt: time.Now().UTC(),
	}
	if _, err := r.col.InsertOne(ctx, doc); err != nil {
		return fmt.Errorf("schema_index_log: append: %w", err)
	}
	return nil
}

// List returns log lines for a project, newer than `since`, sorted
// ascending by created_at so the dashboard can render the live tail in
// chronological order. When since is zero, returns the most recent
// `limit` entries (still sorted ascending).
func (r *SchemaIndexLogRepository) List(ctx context.Context, projectID string, since time.Time, limit int) ([]SchemaIndexLog, error) {
	if projectID == "" {
		return nil, errors.New("schema_index_log: projectID is required")
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 2000 {
		limit = 2000
	}

	filter := bson.M{"project_id": projectID}
	if !since.IsZero() {
		filter["created_at"] = bson.M{"$gt": since}
	}

	// Without `since` we want the latest N in chronological order, so
	// sort desc + limit, then reverse. With `since` filter we can sort
	// asc directly (we already cut everything older at the filter).
	var opts *options.FindOptions
	if since.IsZero() {
		opts = options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(int64(limit))
	} else {
		opts = options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}).SetLimit(int64(limit))
	}

	cursor, err := r.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("schema_index_log: list: %w", err)
	}
	defer cursor.Close(ctx)

	var out []SchemaIndexLog
	if err := cursor.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("schema_index_log: decode: %w", err)
	}

	// Reverse when we sorted desc so the UI always sees ascending order.
	if since.IsZero() {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, nil
}

// DeleteByProject removes all logs for a project. Called when a project
// is deleted — the TTL would eventually expire them, but we'd rather
// not leak logs across project-id reuse (unlikely but possible in
// single-node dev).
func (r *SchemaIndexLogRepository) DeleteByProject(ctx context.Context, projectID string) error {
	if projectID == "" {
		return errors.New("schema_index_log: projectID is required")
	}
	if _, err := r.col.DeleteMany(ctx, bson.M{"project_id": projectID}); err != nil {
		return fmt.Errorf("schema_index_log: delete by project: %w", err)
	}
	return nil
}
