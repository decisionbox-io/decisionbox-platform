package database

import (
	"context"
	"fmt"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// RunRepository manages DiscoveryRun documents.
type RunRepository struct {
	col *mongo.Collection
}

func NewRunRepository(db *DB) *RunRepository {
	return &RunRepository{col: db.Collection("discovery_runs")}
}

// Create creates a new discovery run record.
func (r *RunRepository) Create(ctx context.Context, projectID string) (string, error) {
	run := models.DiscoveryRun{
		ProjectID: projectID,
		Status:    "pending",
		Phase:     "init",
		Progress:  0,
		StartedAt: time.Now(),
		UpdatedAt: time.Now(),
		Steps:     make([]models.RunStep, 0),
	}

	result, err := r.col.InsertOne(ctx, run)
	if err != nil {
		return "", fmt.Errorf("create run: %w", err)
	}

	if oid, ok := result.InsertedID.(primitive.ObjectID); ok {
		return oid.Hex(), nil
	}
	return "", nil
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

// ListTerminalWithReservation returns runs that have ended (succeeded,
// failed, or cancelled) AND still carry a non-empty policy reservation.
// Used by the post-completion confirmer goroutine so discovery runs
// that the agent wrote as "completed" directly to Mongo still trigger
// a Confirm on the policy Checker. The caller typically limits the
// scan to a handful at a time.
func (r *RunRepository) ListTerminalWithReservation(ctx context.Context, limit int) ([]*models.DiscoveryRun, error) {
	if limit <= 0 {
		limit = 50
	}
	filter := bson.M{
		"status":                bson.M{"$in": []string{"completed", "failed", "cancelled"}},
		"policy_reservation_id": bson.M{"$nin": []any{"", nil}},
	}
	opts := options.Find().SetLimit(int64(limit))
	cursor, err := r.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list terminal runs with reservation: %w", err)
	}
	defer cursor.Close(ctx) //nolint:errcheck
	var runs []*models.DiscoveryRun
	if err := cursor.All(ctx, &runs); err != nil {
		return nil, fmt.Errorf("decode terminal runs: %w", err)
	}
	return runs, nil
}

// ClearPolicyReservationID unsets the reservation handle on a run so
// the post-completion confirmer does not re-process it on the next
// scan. Called after a successful Confirm.
func (r *RunRepository) ClearPolicyReservationID(ctx context.Context, runID string) error {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}
	_, err = r.col.UpdateByID(ctx, oid, bson.M{
		"$unset": bson.M{"policy_reservation_id": ""},
		"$set":   bson.M{"updated_at": time.Now()},
	})
	return err
}

// SetPolicyReservationID stores the opaque reservation handle returned
// by the policy Checker when the run was triggered. Persisted so exit
// paths outside the trigger request (cancel, crash sweeper, agent
// completion callback) can resolve the reservation back to the control
// plane without keeping request-scoped state.
func (r *RunRepository) SetPolicyReservationID(ctx context.Context, runID, reservationID string) error {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}
	_, err = r.col.UpdateByID(ctx, oid, bson.M{
		"$set": bson.M{"policy_reservation_id": reservationID, "updated_at": time.Now()},
	})
	return err
}

// Fail marks a run as failed.
func (r *RunRepository) Fail(ctx context.Context, runID string, errMsg string) error {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return err
	}

	now := time.Now()
	update := bson.M{
		"$set": bson.M{
			"status":       "failed",
			"error":        errMsg,
			"phase_detail": "Failed: " + errMsg,
			"completed_at": now,
			"updated_at":   now,
		},
	}

	_, err = r.col.UpdateByID(ctx, oid, update)
	return err
}

// Cancel marks a run as cancelled.
func (r *RunRepository) Cancel(ctx context.Context, runID string) error {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return err
	}

	now := time.Now()
	update := bson.M{
		"$set": bson.M{
			"status":       "cancelled",
			"phase_detail": "Cancelled by user",
			"completed_at": now,
			"updated_at":   now,
		},
	}

	_, err = r.col.UpdateByID(ctx, oid, update)
	return err
}

// CleanupStaleRuns marks any pending/running runs as failed.
// Called on API startup to clean up runs from previous container lifecycle.
func (r *RunRepository) CleanupStaleRuns(ctx context.Context) (int, error) {
	filter := bson.M{
		"status": bson.M{"$in": []string{"pending", "running"}},
	}

	now := time.Now()
	update := bson.M{
		"$set": bson.M{
			"status":       "failed",
			"error":        "stale: API restarted while run was in progress",
			"phase_detail": "Failed: API restarted during discovery",
			"completed_at": now,
			"updated_at":   now,
		},
	}

	result, err := r.col.UpdateMany(ctx, filter, update)
	if err != nil {
		return 0, err
	}
	return int(result.ModifiedCount), nil
}

// GetRunningByProject checks if there's an active run for a project.
func (r *RunRepository) GetRunningByProject(ctx context.Context, projectID string) (*models.DiscoveryRun, error) {
	filter := bson.M{
		"project_id": projectID,
		"status":     bson.M{"$in": []string{"pending", "running"}},
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
