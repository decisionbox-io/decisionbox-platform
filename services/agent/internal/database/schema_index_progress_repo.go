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

// SchemaIndexProgressRepository writes live progress during an indexing
// run. The agent is the primary writer (during BuildIndex); the API is the
// primary reader (for the /schema-index/status endpoint). Both services
// target the same MongoDB collection.
type SchemaIndexProgressRepository struct {
	db *DB
}

// NewSchemaIndexProgressRepository wires the agent-side repo.
func NewSchemaIndexProgressRepository(db *DB) *SchemaIndexProgressRepository {
	return &SchemaIndexProgressRepository{db: db}
}

func (r *SchemaIndexProgressRepository) col() *mongo.Collection {
	return r.db.Collection(CollectionSchemaIndexProgress)
}

// Reset upserts a fresh progress doc at the start of a new indexing run.
// Clears prior tables_total / tables_done / error_message.
func (r *SchemaIndexProgressRepository) Reset(ctx context.Context, projectID, runID string) error {
	if projectID == "" {
		return errors.New("projectID is required")
	}
	now := time.Now().UTC()
	_, err := r.col().UpdateOne(ctx,
		bson.M{"project_id": projectID},
		bson.M{"$set": bson.M{
			"project_id":    projectID,
			"run_id":        runID,
			"phase":         models.SchemaIndexPhaseListingTables,
			"tables_total":  0,
			"tables_done":   0,
			"started_at":    now,
			"updated_at":    now,
			"error_message": "",
		}},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		return fmt.Errorf("reset schema-index progress: %w", err)
	}
	return nil
}

// SetPhase advances the phase marker.
func (r *SchemaIndexProgressRepository) SetPhase(ctx context.Context, projectID, phase string) error {
	if projectID == "" {
		return errors.New("projectID is required")
	}
	if !isValidAgentSchemaIndexPhase(phase) {
		return fmt.Errorf("invalid schema-index phase: %q", phase)
	}
	res, err := r.col().UpdateOne(ctx,
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

// SetTotals stamps tables_total (and optionally overrides tables_done to
// clamp at total). Use when the worker learns the total table count after
// the listing phase.
func (r *SchemaIndexProgressRepository) SetTotals(ctx context.Context, projectID string, total int) error {
	if projectID == "" {
		return errors.New("projectID is required")
	}
	if total < 0 {
		return fmt.Errorf("tables_total=%d: must be non-negative", total)
	}
	res, err := r.col().UpdateOne(ctx,
		bson.M{"project_id": projectID},
		bson.M{"$set": bson.M{"tables_total": total, "updated_at": time.Now().UTC()}},
	)
	if err != nil {
		return fmt.Errorf("set schema-index totals: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("schema-index progress not found for project %q", projectID)
	}
	return nil
}

// SetCounters stamps both tables_total and tables_done in one
// update — used when the indexer moves between phases that each have
// their own 0→N progression (e.g. schema_discovery wraps up with
// done=total, then describing_tables resets to done=0 with the same
// total).
func (r *SchemaIndexProgressRepository) SetCounters(ctx context.Context, projectID string, total, done int) error {
	if projectID == "" {
		return errors.New("projectID is required")
	}
	if total < 0 || done < 0 {
		return fmt.Errorf("counters must be non-negative: total=%d done=%d", total, done)
	}
	res, err := r.col().UpdateOne(ctx,
		bson.M{"project_id": projectID},
		bson.M{"$set": bson.M{
			"tables_total": total,
			"tables_done":  done,
			"updated_at":   time.Now().UTC(),
		}},
	)
	if err != nil {
		return fmt.Errorf("set schema-index counters: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("schema-index progress not found for project %q", projectID)
	}
	return nil
}

// IncrementDone atomically advances tables_done by delta. Safe under
// concurrent worker goroutines.
func (r *SchemaIndexProgressRepository) IncrementDone(ctx context.Context, projectID string, delta int) error {
	if projectID == "" {
		return errors.New("projectID is required")
	}
	if delta <= 0 {
		return nil
	}
	res, err := r.col().UpdateOne(ctx,
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
		return fmt.Errorf("schema-index progress not found for project %q", projectID)
	}
	return nil
}

// RecordError stamps the error_message; the API worker flips the project
// status to failed separately.
func (r *SchemaIndexProgressRepository) RecordError(ctx context.Context, projectID, msg string) error {
	if projectID == "" {
		return errors.New("projectID is required")
	}
	res, err := r.col().UpdateOne(ctx,
		bson.M{"project_id": projectID},
		bson.M{"$set": bson.M{"error_message": msg, "updated_at": time.Now().UTC()}},
	)
	if err != nil {
		return fmt.Errorf("record schema-index error: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("schema-index progress not found for project %q", projectID)
	}
	return nil
}

func isValidAgentSchemaIndexPhase(phase string) bool {
	switch phase {
	case models.SchemaIndexPhaseListingTables,
		models.SchemaIndexPhaseSchemaDiscovery,
		models.SchemaIndexPhaseDescribingTables,
		models.SchemaIndexPhaseEmbedding:
		return true
	}
	return false
}
