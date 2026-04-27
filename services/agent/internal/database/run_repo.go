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

const CollectionDiscoveryRuns = "discovery_runs"

// RunRepository manages DiscoveryRun status documents.
type RunRepository struct {
	col *mongo.Collection
}

func NewRunRepository(db *DB) *RunRepository {
	return &RunRepository{col: db.Collection(CollectionDiscoveryRuns)}
}

// Create creates a new discovery run and returns its ID.
func (r *RunRepository) Create(ctx context.Context, run *models.DiscoveryRun) (string, error) {
	run.StartedAt = time.Now()
	run.UpdatedAt = time.Now()
	run.Status = models.RunStatusPending
	run.Steps = make([]models.RunStep, 0)

	result, err := r.col.InsertOne(ctx, run)
	if err != nil {
		return "", fmt.Errorf("create run: %w", err)
	}

	if oid, ok := result.InsertedID.(primitive.ObjectID); ok {
		return oid.Hex(), nil
	}
	return "", nil
}

// UpdateStatus updates the run's status, phase, and detail.
func (r *RunRepository) UpdateStatus(ctx context.Context, runID string, status, phase, detail string, progress int) error {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}

	update := bson.M{
		"$set": bson.M{
			"status":       status,
			"phase":        phase,
			"phase_detail": detail,
			"progress":     progress,
			"updated_at":   time.Now(),
		},
	}

	_, err = r.col.UpdateByID(ctx, oid, update)
	return err
}

// RecordSchemaContextTelemetry stamps the one-shot counters that describe
// the schema context the run used. Called once, immediately after the
// schema renderer builds the catalog. The on-demand action counters
// (lookup_schema, search_tables) are updated separately via
// IncrementSchemaActionCalls as the engine services each action.
func (r *RunRepository) RecordSchemaContextTelemetry(ctx context.Context, runID string, tokens, tableCount int) error {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}
	update := bson.M{
		"$set": bson.M{
			"schema_tokens":      tokens,
			"schema_table_count": tableCount,
			"updated_at":         time.Now(),
		},
	}
	_, err = r.col.UpdateByID(ctx, oid, update)
	return err
}

// IncrementSchemaActionCalls atomically bumps the per-action counters
// on a run. action is one of "lookup_schema" or "search_tables"; any
// other value is a no-op so a future action type doesn't accidentally
// roll into the wrong counter. Safe to call concurrently.
func (r *RunRepository) IncrementSchemaActionCalls(ctx context.Context, runID, action string, delta int) error {
	if delta <= 0 {
		return nil
	}
	var field string
	switch action {
	case "lookup_schema":
		field = "schema_lookup_calls"
	case "search_tables":
		field = "schema_search_calls"
	default:
		return nil
	}
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}
	update := bson.M{
		"$inc": bson.M{field: delta},
		"$set": bson.M{"updated_at": time.Now()},
	}
	_, err = r.col.UpdateByID(ctx, oid, update)
	return err
}

// AddStep appends a step to the run's step log.
func (r *RunRepository) AddStep(ctx context.Context, runID string, step models.RunStep) error {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}

	step.Timestamp = time.Now()

	update := bson.M{
		"$push": bson.M{"steps": step},
		"$set":  bson.M{"updated_at": time.Now()},
	}

	_, err = r.col.UpdateByID(ctx, oid, update)
	return err
}

// Complete marks a run as completed with stats.
func (r *RunRepository) Complete(ctx context.Context, runID string, insightsFound int) error {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}

	now := time.Now()
	update := bson.M{
		"$set": bson.M{
			"status":         models.RunStatusCompleted,
			"phase":          models.PhaseComplete,
			"phase_detail":   "Discovery completed successfully",
			"progress":       100,
			"completed_at":   now,
			"updated_at":     now,
			"insights_found": insightsFound,
		},
	}

	_, err = r.col.UpdateByID(ctx, oid, update)
	return err
}

// Fail marks a run as failed.
func (r *RunRepository) Fail(ctx context.Context, runID string, errMsg string) error {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}

	now := time.Now()
	update := bson.M{
		"$set": bson.M{
			"status":       models.RunStatusFailed,
			"phase_detail": "Discovery failed: " + errMsg,
			"error":        errMsg,
			"completed_at": now,
			"updated_at":   now,
		},
	}

	_, err = r.col.UpdateByID(ctx, oid, update)
	return err
}

// IncrementQueryCount increments query counters.
func (r *RunRepository) IncrementQueryCount(ctx context.Context, runID string, success bool) error {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return err
	}

	inc := bson.M{"total_queries": 1}
	if success {
		inc["successful_queries"] = 1
	} else {
		inc["failed_queries"] = 1
	}

	update := bson.M{
		"$inc": inc,
		"$set": bson.M{"updated_at": time.Now()},
	}

	_, err = r.col.UpdateByID(ctx, oid, update)
	return err
}

// GetByID returns a discovery run by ID.
func (r *RunRepository) GetByID(ctx context.Context, runID string) (*models.DiscoveryRun, error) {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return nil, fmt.Errorf("invalid run ID: %w", err)
	}

	var run models.DiscoveryRun
	if err := r.col.FindOne(ctx, bson.M{"_id": oid}).Decode(&run); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &run, nil
}

// GetLatestByProject returns the most recent run for a project.
func (r *RunRepository) GetLatestByProject(ctx context.Context, projectID string) (*models.DiscoveryRun, error) {
	opts := options.FindOne().SetSort(bson.D{{Key: "started_at", Value: -1}})

	var run models.DiscoveryRun
	err := r.col.FindOne(ctx, bson.M{"project_id": projectID}, opts).Decode(&run)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &run, nil
}

// GetRunningByProject returns any currently running run for a project.
func (r *RunRepository) GetRunningByProject(ctx context.Context, projectID string) (*models.DiscoveryRun, error) {
	filter := bson.M{
		"project_id": projectID,
		"status":     bson.M{"$in": []string{models.RunStatusPending, models.RunStatusRunning}},
	}

	var run models.DiscoveryRun
	err := r.col.FindOne(ctx, filter).Decode(&run)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &run, nil
}
