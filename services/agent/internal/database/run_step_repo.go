// Package database — run_step_repo.go
//
// Per-step rows for live discovery-run status updates. Replaces the
// embedded `steps []RunStep` array on the discovery_runs document, which
// grew unbounded under StatusReporter's $push streaming and competed for
// the same 16MB BSON budget as the old discoveries log fields.
//
// One doc per RunStep keyed by run_id. The dashboard polls the GET
// /api/v1/runs/{id}/steps endpoint with a `since` cursor so it can stream
// updates without re-reading the whole tail every time.
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
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
// inline so the existing field BSON tags stay stable.
type RunStepDoc struct {
	RunID     string    `bson:"run_id" json:"run_id"`
	ProjectID string    `bson:"project_id,omitempty" json:"project_id,omitempty"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`

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

// ListByRun returns the step rows for a run, ordered by timestamp
// ascending. since (zero allowed) returns all rows after that
// timestamp; limit <= 0 means "all".
func (r *RunStepRepository) ListByRun(ctx context.Context, runID string, since time.Time, limit int) ([]models.RunStep, error) {
	filter := bson.M{"run_id": runID}
	if !since.IsZero() {
		filter["timestamp"] = bson.M{"$gt": since}
	}
	opts := options.Find().SetSort(bson.D{{Key: "timestamp", Value: 1}})
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
	out := make([]models.RunStep, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.RunStep)
	}
	return out, nil
}

// EnsureIndexes creates a (run_id, timestamp) index for paginated reads.
func (r *RunStepRepository) EnsureIndexes(ctx context.Context) error {
	idx := mongo.IndexModel{Keys: bson.D{
		{Key: "run_id", Value: 1},
		{Key: "timestamp", Value: 1},
	}}
	if _, err := r.col.Indexes().CreateOne(ctx, idx); err != nil {
		return fmt.Errorf("ensure index on %s: %w", CollectionDiscoveryRunSteps, err)
	}
	return nil
}
