// Package database — run_step_repo.go (API side, read-only).
//
// Read-only counterpart of the agent's RunStepRepository. The dashboard
// polls GET /api/v1/runs/{id}/steps with a `since` cursor to stream live
// run progress without re-reading the whole tail every time. The agent
// owns the writers; this repository only reads.
package database

import (
	"context"
	"fmt"
	"time"

	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// RunStepRepository reads per-step rows for a discovery run.
type RunStepRepository struct {
	col *mongo.Collection
}

// NewRunStepRepository wraps the discovery_run_steps collection. The
// collection name lives in libs/go-common/mongodb so agent (writer) and
// api (reader) stay in lockstep on rename.
func NewRunStepRepository(db *DB) *RunStepRepository {
	return &RunStepRepository{col: db.Collection(gomongo.CollectionDiscoveryRunSteps)}
}

// ListByRun returns the steps for a run, ordered by timestamp ascending.
// since (zero allowed) returns rows strictly after that timestamp; limit
// <= 0 means "all".
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

	out := make([]models.RunStep, 0)
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("decode run steps: %w", err)
	}
	return out, nil
}
