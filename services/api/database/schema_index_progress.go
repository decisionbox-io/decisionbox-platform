package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// SchemaIndexProgressRepository tracks live indexing progress, one document
// per project. Upserted by (project_id) at phase boundaries and after every
// N tables so the dashboard can poll at 2s intervals. Reset to zero at the
// start of each indexing run by Reset().
type SchemaIndexProgressRepository struct {
	col *mongo.Collection
}

// NewSchemaIndexProgressRepository wires the repo against the
// project_schema_index_progress collection.
func NewSchemaIndexProgressRepository(db *DB) *SchemaIndexProgressRepository {
	return &SchemaIndexProgressRepository{col: db.Collection("project_schema_index_progress")}
}

// Reset clears the progress doc at the start of a new indexing run. Upserts
// a fresh document with started_at = now() and zero counters. Any prior
// error_message is cleared.
func (r *SchemaIndexProgressRepository) Reset(ctx context.Context, projectID, runID string) error {
	if projectID == "" {
		return errors.New("projectID is required")
	}
	now := time.Now().UTC()
	filter := bson.M{"project_id": projectID}
	update := bson.M{
		"$set": bson.M{
			"project_id":    projectID,
			"run_id":        runID,
			"phase":         models.SchemaIndexPhaseListingTables,
			"tables_total":  0,
			"tables_done":   0,
			"started_at":    now,
			"updated_at":    now,
			"error_message": "",
		},
	}
	opts := options.Update().SetUpsert(true)
	if _, err := r.col.UpdateOne(ctx, filter, update, opts); err != nil {
		return fmt.Errorf("reset schema-index progress: %w", err)
	}
	return nil
}

// SetPhase advances the indexing phase and refreshes updated_at.
// Does not touch tables_total / tables_done — use UpdateTables for those.
func (r *SchemaIndexProgressRepository) SetPhase(ctx context.Context, projectID, phase string) error {
	if projectID == "" {
		return errors.New("projectID is required")
	}
	if !isValidSchemaIndexPhase(phase) {
		return fmt.Errorf("invalid schema-index phase: %q", phase)
	}
	res, err := r.col.UpdateOne(ctx,
		bson.M{"project_id": projectID},
		bson.M{"$set": bson.M{"phase": phase, "updated_at": time.Now().UTC()}},
	)
	if err != nil {
		return fmt.Errorf("set schema-index phase: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("schema-index progress not found for project %q; call Reset first", projectID)
	}
	return nil
}

// UpdateTables sets tables_total + tables_done for progress-bar rendering.
// Monotonically clamps: never moves tables_done backwards within the same
// run, and never exceeds tables_total once set.
func (r *SchemaIndexProgressRepository) UpdateTables(ctx context.Context, projectID string, total, done int) error {
	if projectID == "" {
		return errors.New("projectID is required")
	}
	if total < 0 || done < 0 {
		return fmt.Errorf("tables_total=%d tables_done=%d: must be non-negative", total, done)
	}
	if total > 0 && done > total {
		done = total
	}
	res, err := r.col.UpdateOne(ctx,
		bson.M{"project_id": projectID},
		bson.M{"$set": bson.M{
			"tables_total": total,
			"tables_done":  done,
			"updated_at":   time.Now().UTC(),
		}},
	)
	if err != nil {
		return fmt.Errorf("update schema-index tables: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("schema-index progress not found for project %q; call Reset first", projectID)
	}
	return nil
}

// IncrementDone advances tables_done by delta (atomic $inc). Safe to call
// from multiple goroutines concurrently — used by the parallel blurb worker
// pool to report per-table completion.
func (r *SchemaIndexProgressRepository) IncrementDone(ctx context.Context, projectID string, delta int) error {
	if projectID == "" {
		return errors.New("projectID is required")
	}
	if delta <= 0 {
		return nil
	}
	res, err := r.col.UpdateOne(ctx,
		bson.M{"project_id": projectID},
		bson.M{
			"$inc": bson.M{"tables_done": delta},
			"$set": bson.M{"updated_at": time.Now().UTC()},
		},
	)
	if err != nil {
		return fmt.Errorf("increment schema-index tables_done: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("schema-index progress not found for project %q; call Reset first", projectID)
	}
	return nil
}

// RecordError stamps the error_message field without moving the phase.
// Used by the worker to surface why the run failed.
func (r *SchemaIndexProgressRepository) RecordError(ctx context.Context, projectID, msg string) error {
	if projectID == "" {
		return errors.New("projectID is required")
	}
	res, err := r.col.UpdateOne(ctx,
		bson.M{"project_id": projectID},
		bson.M{"$set": bson.M{"error_message": msg, "updated_at": time.Now().UTC()}},
	)
	if err != nil {
		return fmt.Errorf("record schema-index error: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("schema-index progress not found for project %q; call Reset first", projectID)
	}
	return nil
}

// Get fetches the progress doc. Returns nil (not an error) when no document
// exists yet — callers treat that as "never indexed."
func (r *SchemaIndexProgressRepository) Get(ctx context.Context, projectID string) (*models.SchemaIndexProgress, error) {
	if projectID == "" {
		return nil, errors.New("projectID is required")
	}
	var p models.SchemaIndexProgress
	if err := r.col.FindOne(ctx, bson.M{"project_id": projectID}).Decode(&p); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, fmt.Errorf("get schema-index progress: %w", err)
	}
	return &p, nil
}

// Delete removes the progress doc, typically when a project is deleted or
// when the user explicitly resets the schema index.
func (r *SchemaIndexProgressRepository) Delete(ctx context.Context, projectID string) error {
	if projectID == "" {
		return errors.New("projectID is required")
	}
	if _, err := r.col.DeleteOne(ctx, bson.M{"project_id": projectID}); err != nil {
		return fmt.Errorf("delete schema-index progress: %w", err)
	}
	return nil
}

func isValidSchemaIndexPhase(phase string) bool {
	switch phase {
	case models.SchemaIndexPhaseListingTables,
		models.SchemaIndexPhaseDescribingTables,
		models.SchemaIndexPhaseEmbedding:
		return true
	}
	return false
}
