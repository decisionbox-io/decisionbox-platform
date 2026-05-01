// Package database — run_step_repo.go
//
// Per-step rows for live discovery-run status updates. Replaces the
// embedded `steps []RunStep` array on the discovery_runs document, which
// grew unbounded under StatusReporter's $push streaming and competed for
// the same 16MB BSON budget as the old discoveries log fields.
//
// One doc per RunStep keyed by run_id. The dashboard polls the GET
// /api/v1/runs/{id}/steps endpoint with an opaque cursor (the last row's
// `id`) so it can stream updates without re-reading the whole tail.
//
// Cursor design — why ObjectID, not timestamp:
// MongoDB datetimes are millisecond-precision. Two AddStep calls inside
// the same ms produce two docs with the same `timestamp`. A cursor of
// `timestamp > since` then permanently drops any later row that shares
// the previous poll's last timestamp. ObjectIDs are monotonically
// increasing per writer process (their internal counter increments on
// every ObjectID generation, regardless of clock resolution), so a
// `_id > since_id` cursor is collision-free for our single-agent-per-run
// writer. The dashboard treats `id` as opaque and just echoes the last
// one back on the next poll.
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// RunStepRepository persists and reads the per-step rows that used to
// live as an embedded array on the discovery_runs doc.
type RunStepRepository struct {
	col *mongo.Collection
}

// NewRunStepRepository wraps the discovery_run_steps collection.
func NewRunStepRepository(db *DB) *RunStepRepository {
	return &RunStepRepository{col: db.Collection(CollectionDiscoveryRunSteps)}
}

// RunStepDoc is the wire/storage shape for one row. Embeds RunStep
// inline so the existing field BSON/JSON tags stay stable. The `id`
// field is the doc's ObjectID rendered as hex — the dashboard treats
// it as an opaque cursor (see the package header).
type RunStepDoc struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"-"`
	IDHex     string             `bson:"-" json:"id"`
	RunID     string             `bson:"run_id" json:"run_id"`
	ProjectID string             `bson:"project_id,omitempty" json:"project_id,omitempty"`
	CreatedAt time.Time          `bson:"created_at" json:"created_at"`

	models.RunStep `bson:",inline" json:",inline"`
}

// AddStep inserts one RunStep document. The step's Timestamp is set to
// now if the caller left it zero. projectID is optional — when empty,
// the doc still works (the dashboard queries by run_id) but lean
// per-project filters require it.
func (r *RunStepRepository) AddStep(ctx context.Context, runID, projectID string, step models.RunStep) error {
	if step.Timestamp.IsZero() {
		step.Timestamp = time.Now()
	}
	doc := RunStepDoc{
		RunID:     runID,
		ProjectID: projectID,
		CreatedAt: time.Now(),
		RunStep:   step,
	}
	_, err := r.col.InsertOne(ctx, doc)
	if err != nil {
		return fmt.Errorf("insert run step: %w", err)
	}
	return nil
}

// ListByRun returns the step rows for a run, ordered by `_id` ascending
// (which is monotonic per writer process). sinceID is the last `id` the
// caller has — passing "" returns the head of the stream. limit <= 0
// means "all". Each returned RunStepDoc has IDHex populated for the
// caller to feed back as the next cursor.
func (r *RunStepRepository) ListByRun(ctx context.Context, runID, sinceID string, limit int) ([]RunStepDoc, error) {
	filter := bson.M{"run_id": runID}
	if sinceID != "" {
		oid, err := primitive.ObjectIDFromHex(sinceID)
		if err != nil {
			return nil, fmt.Errorf("invalid since_id %q: %w", sinceID, err)
		}
		filter["_id"] = bson.M{"$gt": oid}
	}
	opts := options.Find().SetSort(bson.D{{Key: "_id", Value: 1}})
	if limit > 0 {
		opts = opts.SetLimit(int64(limit))
	}
	cur, err := r.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list run steps: %w", err)
	}
	defer cur.Close(ctx)

	var docs []RunStepDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode run steps: %w", err)
	}
	for i := range docs {
		docs[i].IDHex = docs[i].ID.Hex()
	}
	return docs, nil
}

// EnsureIndexes creates a (run_id, _id) index for the dashboard polling
// pattern. _id alone is auto-indexed but the compound prefix lets the
// per-run filter use it directly.
func (r *RunStepRepository) EnsureIndexes(ctx context.Context) error {
	idx := mongo.IndexModel{Keys: bson.D{
		{Key: "run_id", Value: 1},
		{Key: "_id", Value: 1},
	}}
	if _, err := r.col.Indexes().CreateOne(ctx, idx); err != nil {
		return fmt.Errorf("ensure index on %s: %w", CollectionDiscoveryRunSteps, err)
	}
	return nil
}
