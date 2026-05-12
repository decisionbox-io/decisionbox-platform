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

// RunRepository manages DiscoveryRun status documents. Collection name
// lives in mongodb.go (sourced from libs/go-common/mongodb).
type RunRepository struct {
	col *mongo.Collection
}

func NewRunRepository(db *DB) *RunRepository {
	return &RunRepository{col: db.Collection(CollectionDiscoveryRuns)}
}

// Create creates a new discovery run and returns its ID. Per-step rows
// are now persisted in the discovery_run_steps collection via
// RunStepRepository — no embedded `steps` slice initialisation here.
func (r *RunRepository) Create(ctx context.Context, run *models.DiscoveryRun) (string, error) {
	run.StartedAt = time.Now()
	run.UpdatedAt = time.Now()
	run.Status = models.RunStatusPending

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

// IncrementAnalysisCounter atomically bumps one of the analysis-
// phase compaction counters on a run. metric is one of:
//
//   - "step_index_upserts"      → analysis_step_index_upserts
//   - "step_index_search_calls" → analysis_step_index_search_calls
//   - "steps_dropped"           → analysis_steps_dropped
//
// Any other value is a no-op so a future metric name doesn't roll
// into the wrong field.
func (r *RunRepository) IncrementAnalysisCounter(ctx context.Context, runID, metric string, delta int) error {
	if delta <= 0 {
		return nil
	}
	var field string
	switch metric {
	case "step_index_upserts":
		field = "analysis_step_index_upserts"
	case "step_index_search_calls":
		field = "analysis_step_index_search_calls"
	case "steps_dropped":
		field = "analysis_steps_dropped"
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

// AddStep was removed — per-step rows now go to the discovery_run_steps
// collection via RunStepRepository. The previous $push into the embedded
// steps array hit the 16MB BSON limit on long runs.

// Complete marks a run as completed and stamps the discovery_id
// the run produced. The link is critical for run-completion hook
// consumers (plugin-hooks.md Hook 5) — without it they can't query
// insights / recommendations (both keyed on discovery_id), and the
// implicit "run and discovery created around the same time" linkage
// is fragile when concurrent runs land in the same project.
//
// discoveryID is required: a run that completes without producing a
// discovery is a contract violation the caller must surface. An
// empty string returns an error rather than silently writing a
// half-state.
func (r *RunRepository) Complete(ctx context.Context, runID, discoveryID string, insightsFound int) error {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}
	if discoveryID == "" {
		return fmt.Errorf("run %s: complete requires a discovery_id", runID)
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
			"discovery_id":   discoveryID,
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

// ListActiveRecent returns ids of runs that are currently in a non-
// terminal status and were started within the given lookback window.
// The boot-time per-run-collection orphan sweep treats these as
// "live" (don't drop their Qdrant collections).
//
// Returns DiscoveryRun structs (id + status + started_at — the rest
// of the fields are zero-value) so the caller can also log freshness.
func (r *RunRepository) ListActiveRecent(ctx context.Context, lookback time.Duration) ([]models.DiscoveryRun, error) {
	cutoff := time.Now().Add(-lookback)
	filter := bson.M{
		"started_at": bson.M{"$gte": cutoff},
		"status": bson.M{"$in": []string{
			models.RunStatusPending,
			models.RunStatusRunning,
		}},
	}
	cur, err := r.col.Find(ctx, filter, options.Find().SetProjection(bson.M{
		"_id":        1,
		"status":     1,
		"started_at": 1,
	}))
	if err != nil {
		return nil, fmt.Errorf("list active recent runs: %w", err)
	}
	defer cur.Close(ctx)

	out := make([]models.DiscoveryRun, 0)
	for cur.Next(ctx) {
		var doc struct {
			ID        primitive.ObjectID `bson:"_id"`
			Status    string             `bson:"status"`
			StartedAt time.Time          `bson:"started_at"`
		}
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode active run: %w", err)
		}
		out = append(out, models.DiscoveryRun{
			ID:        doc.ID.Hex(),
			Status:    doc.Status,
			StartedAt: doc.StartedAt,
		})
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("cursor active runs: %w", err)
	}
	return out, nil
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
