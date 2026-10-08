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
func (r *RunRepository) UpdateStatus(ctx context.Context, runID string, status, phase, detail string, progress int, attempt int) error {
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

	_, err = r.col.UpdateOne(ctx, attemptFilter(oid, attempt), update)
	return err
}

// RecordSchemaContextTelemetry stamps the one-shot counters that describe
// the schema context the run used. Called once, immediately after the
// schema renderer builds the catalog. The on-demand action counters
// (lookup_schema, search_tables) are updated separately via
// IncrementSchemaActionCalls as the engine services each action.
func (r *RunRepository) RecordSchemaContextTelemetry(ctx context.Context, runID string, tokens, tableCount int, attempt int) error {
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
	_, err = r.col.UpdateOne(ctx, attemptFilter(oid, attempt), update)
	return err
}

// IncrementSchemaActionCalls atomically bumps the per-action counters
// on a run. action is one of "lookup_schema", "search_tables" or
// "get_correlations"; any other value is a no-op so a future action type
// doesn't accidentally roll into the wrong counter. Safe to call
// concurrently.
func (r *RunRepository) IncrementSchemaActionCalls(ctx context.Context, runID, action string, delta int, attempt int) error {
	if delta <= 0 {
		return nil
	}
	var field string
	switch action {
	case "lookup_schema":
		field = "schema_lookup_calls"
	case "search_tables":
		field = "schema_search_calls"
	case "get_correlations":
		field = "correlation_lookup_calls"
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
	_, err = r.col.UpdateOne(ctx, attemptFilter(oid, attempt), update)
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
func (r *RunRepository) IncrementAnalysisCounter(ctx context.Context, runID, metric string, delta int, attempt int) error {
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
	_, err = r.col.UpdateOne(ctx, attemptFilter(oid, attempt), update)
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
//
// Returns whether the write landed. That is this attempt's CLAIM on the run,
// and the caller needs it: the attempt that did not claim the run must not go
// on to delete the other attempt's results.
func (r *RunRepository) Complete(ctx context.Context, runID, discoveryID string, insightsFound int, attempt int) (bool, error) {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return false, fmt.Errorf("invalid run ID: %w", err)
	}
	if discoveryID == "" {
		return false, fmt.Errorf("run %s: complete requires a discovery_id", runID)
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
			// A completed run has nothing left to resume, so the
			// dashboard's Resume affordance must not survive it. Zeroed
			// here rather than relying on the checkpoint rows being gone:
			// their deletion is best-effort, and an offered-but-impossible
			// Resume is worse than none.
			"last_checkpoint_step": 0,
		},
	}

	// Attempt-fenced, and additionally barred from overriding a
	// cancellation.
	//
	// Cancel is a deliberate hard kill that stays terminal: the API writes
	// `cancelled` and deletes the checkpoints. An agent still finishing its
	// save would otherwise match on (_id, attempt) — the cancel does not
	// change the attempt — and flip the run to `completed`, erasing the
	// cancellation. Worse since this write began reporting whether it
	// claimed the run: a claim licenses retireSupersededAttempts and
	// discardCheckpoints, so a late Complete after a cancel would not just
	// mislabel the run but start deleting on the strength of it.
	//
	// Deliberately NOT the pending/running predicate Fail uses. `failed` has
	// to stay matchable here, because the API's startup sweep marks
	// in-flight runs failed WITHOUT reaping their agents: an agent that then
	// finishes genuinely has a discovery to record, and refusing it would
	// leave a complete result saved but unreachable behind a `failed` run,
	// inviting a resume that re-runs analysis for nothing. A resumed run is
	// already excluded by the attempt fence, not by the status.
	filter := attemptFilter(oid, attempt)
	filter["status"] = bson.M{"$ne": models.RunStatusCancelled}

	res, err := r.col.UpdateOne(ctx, filter, update)
	if err != nil {
		return false, err
	}
	return res.MatchedCount > 0, nil
}

// attemptFilter builds a run filter that matches only while the run is still
// on the given attempt.
//
// It fences a previous attempt's agent that is somehow still alive. That is
// reachable: the API's startup sweep marks in-flight runs `failed` after a
// restart WITHOUT reaping their workloads, so an operator resuming such a run
// can have two agents on one run id — and the terminal status the agent writes
// is the authoritative one. Without this, the orphan's eventual Complete or
// Fail would overwrite the live attempt's outcome.
//
// attempt <= 0 means "unknown" and matches anything, which is what a caller
// that cannot say its attempt gets — the behaviour before attempts existed.
// Attempt 1 also matches a document with NO attempt field, which is how every
// run created before the counter existed reads.
func attemptFilter(oid primitive.ObjectID, attempt int) bson.M {
	filter := bson.M{"_id": oid}
	switch {
	case attempt == 1:
		filter["attempt"] = bson.M{"$in": []any{1, nil}}
	case attempt > 1:
		filter["attempt"] = attempt
	}
	return filter
}

// Fail marks a run as failed. discoveryID is the _id of the partial
// DiscoveryResult that was persisted before the failure, when one
// exists — this is the case for cancellation mid-compute where
// exploration finished, the orchestrator saved a partial result,
// and then the outer cap fired. Empty discoveryID is allowed for
// failures that produced no persisted discovery (factory errors,
// warehouse health-check failure, etc.); in that case the
// discovery_id field is not stamped and the run record stays
// without a back-reference.
//
// Terminal-status invariant: Fail only writes when the run is
// currently in a non-terminal status (`pending` or `running`). A
// run that already reached `completed`, `failed`, or `cancelled`
// is left alone — see e.g. the K8s watcher's exhaustion fallback,
// which fires the OnFailure callback even when the agent has
// already stamped Complete or the user has already cancelled the
// run. Without this guard a successful discovery could be flipped
// to failed hours later by an inconclusive watcher timeout. The
// caller can detect the no-op via NoTerminalStatusChange.
//
// The plugin-hooks Hook 5 and the discovery-log APIs key off
// DiscoveryRun.DiscoveryID; stamping it on the failed run lets
// consumers navigate to the partial result the same way they would
// for a completed run.
func (r *RunRepository) Fail(ctx context.Context, runID, discoveryID, errMsg string, attempt int) (bool, error) {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return false, fmt.Errorf("invalid run ID: %w", err)
	}

	now := time.Now()
	set := bson.M{
		"status":       models.RunStatusFailed,
		"phase_detail": "Discovery failed: " + errMsg,
		"error":        errMsg,
		"completed_at": now,
		"updated_at":   now,
	}
	if discoveryID != "" {
		set["discovery_id"] = discoveryID
	}

	filter := attemptFilter(oid, attempt)
	filter["status"] = bson.M{"$in": []string{
		models.RunStatusPending,
		models.RunStatusRunning,
	}}
	res, err := r.col.UpdateOne(ctx, filter, bson.M{"$set": set})
	if err != nil {
		return false, err
	}
	return res.MatchedCount > 0, nil
}

// MarkExplorationCheckpoint records that a checkpoint now exists for this
// step, which is what the dashboard reads to offer Resume on a failed run.
//
// $max rather than $set: a resumed run re-checkpoints the prefix it replayed,
// so a plain write would walk the value back down to 1 and climb again,
// making the field briefly claim less progress than the run actually has.
// Returns whether the write landed. Because the filter is attempt-fenced,
// that doubles as a free ownership probe: a `false` means this attempt no
// longer owns the run, on a write the agent was making anyway. The checkpoint
// path uses it to stop a superseded agent before it writes anything further.
func (r *RunRepository) MarkExplorationCheckpoint(ctx context.Context, runID string, step int, attempt int) (bool, error) {
	if step <= 0 {
		return true, nil
	}
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return false, fmt.Errorf("invalid run ID: %w", err)
	}
	res, err := r.col.UpdateOne(ctx, attemptFilter(oid, attempt), bson.M{
		"$max": bson.M{"last_checkpoint_step": step},
		"$set": bson.M{"updated_at": time.Now()},
	})
	if err != nil {
		return false, err
	}
	return res.MatchedCount > 0, nil
}

// OwnsRun reports whether the run is still on the given attempt.
//
// A dedicated read, used where there is no write to piggyback the question on
// — the end-of-exploration summary, which is written once per run and whose
// loss to a superseded attempt is the worst case of all: a later resume would
// read it, believe exploration finished, and skip Phase 3 over another
// attempt's work.
//
// attempt <= 0 means "unknown", which owns everything — the behaviour before
// attempts existed.
func (r *RunRepository) OwnsRun(ctx context.Context, runID string, attempt int) (bool, error) {
	if attempt <= 0 {
		return true, nil
	}
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return false, fmt.Errorf("invalid run ID: %w", err)
	}
	n, err := r.col.CountDocuments(ctx, attemptFilter(oid, attempt))
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// AddActiveTime adds this attempt's elapsed compute time to the run's
// cumulative total.
//
// Called once at the terminal write, which is also its limitation: an
// attempt hard-killed before it gets here contributes nothing. See
// DiscoveryRun.ActiveMs.
func (r *RunRepository) AddActiveTime(ctx context.Context, runID string, d time.Duration, attempt int) error {
	if d <= 0 {
		return nil
	}
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}
	_, err = r.col.UpdateOne(ctx, attemptFilter(oid, attempt), bson.M{
		"$inc": bson.M{"active_ms": d.Milliseconds()},
		"$set": bson.M{"updated_at": time.Now()},
	})
	return err
}

// AppendLifecycle pushes one transition onto the run's append-only lifecycle
// log. See models.RunLifecycleEvent for why a single mutable status stops
// being enough once a run can be resumed.
func (r *RunRepository) AppendLifecycle(ctx context.Context, runID string, ev models.RunLifecycleEvent, attempt int) error {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	_, err = r.col.UpdateOne(ctx, attemptFilter(oid, attempt), bson.M{
		"$push": bson.M{"lifecycle": ev},
		"$set":  bson.M{"updated_at": time.Now()},
	})
	return err
}

// IncrementQueryCount increments query counters.
func (r *RunRepository) IncrementQueryCount(ctx context.Context, runID string, success bool, attempt int) error {
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

	_, err = r.col.UpdateOne(ctx, attemptFilter(oid, attempt), update)
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
