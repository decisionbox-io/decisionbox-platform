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

const (
	// validationJobsCollection is the on-disk Mongo collection name. The
	// matching index definitions live in init.go alongside every other
	// collection's schema.
	validationJobsCollection = "validation_jobs"
)

// ValidationJobRepository persists ValidationJob documents and exposes
// the state-machine transitions the manual-validation pipeline relies
// on (enqueue, claim, heartbeat, mark, cancel, requeue-stale,
// list-by-doc).
//
// State diagram:
//
//	pending  ─> running ─┬─> completed
//	                     ├─> failed
//	                     └─> cancelled
//
// Workers atomically move pending → running via FindOneAndUpdate so two
// API instances on the same Mongo can never claim the same job. The
// partial-unique-on-active index registered in init.go makes Mongo
// itself reject a duplicate Enqueue under concurrent clicks; the
// handler converts E11000 to HTTP 409.
type ValidationJobRepository struct {
	col *mongo.Collection
}

// NewValidationJobRepository wires the repo against the validation_jobs
// collection.
func NewValidationJobRepository(db *DB) *ValidationJobRepository {
	return &ValidationJobRepository{col: db.Collection(validationJobsCollection)}
}

// ErrDuplicateActiveJob is returned by Enqueue when the
// partial-unique-on-active index rejects the insert because another
// non-terminal job already exists for the same (discovery, doc).
// Handlers translate this to HTTP 409.
var ErrDuplicateActiveJob = errors.New("a validation job is already active for this document")

// Enqueue inserts a fresh pending job. The caller MUST set ID (a UUID
// — the handler generates one so the response can name a stable id
// immediately), ProjectID, DiscoveryID, DocKind, DocID. EnqueuedAt and
// Status are stamped here. Returns ErrDuplicateActiveJob when the
// active-job uniqueness index fires.
func (r *ValidationJobRepository) Enqueue(ctx context.Context, job *models.ValidationJob) error {
	if job == nil {
		return errors.New("job is required")
	}
	if job.ID == "" {
		return errors.New("job ID is required")
	}
	if job.ProjectID == "" || job.DiscoveryID == "" || job.DocID == "" || job.DocKind == "" {
		return errors.New("project_id, discovery_id, doc_kind, doc_id are required")
	}
	job.Status = models.ValidationJobStatusPending
	job.EnqueuedAt = time.Now().UTC()
	job.Attempt = 0 // bumped to 1 on first claim
	job.Step = ""
	job.Error = ""
	job.ClaimedAt = nil
	job.StartedAt = nil
	job.HeartbeatAt = nil
	job.CompletedAt = nil
	job.CancelledAt = nil

	if _, err := r.col.InsertOne(ctx, job); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return ErrDuplicateActiveJob
		}
		return fmt.Errorf("insert validation_job: %w", err)
	}
	return nil
}

// GetByID returns the job by its UUID, or (nil, nil) when not found.
// Distinguishing "not found" from "transport error" with nil-nil keeps
// handlers concise: `if job == nil { 404 }`.
func (r *ValidationJobRepository) GetByID(ctx context.Context, id string) (*models.ValidationJob, error) {
	if id == "" {
		return nil, errors.New("id is required")
	}
	var job models.ValidationJob
	err := r.col.FindOne(ctx, bson.M{"_id": id}).Decode(&job)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find validation_job: %w", err)
	}
	return &job, nil
}

// ListByDoc returns all jobs (most-recent first by EnqueuedAt) for a
// (discovery, doc) pair. Used by the dashboard's "is there an active
// job?" probe and the history view.
func (r *ValidationJobRepository) ListByDoc(ctx context.Context, discoveryID, docKind, docID string, limit int) ([]models.ValidationJob, error) {
	if discoveryID == "" || docKind == "" || docID == "" {
		return nil, errors.New("discovery_id, doc_kind, doc_id are required")
	}
	if limit <= 0 {
		limit = 20
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "enqueued_at", Value: -1}}).
		SetLimit(int64(limit))
	cur, err := r.col.Find(ctx, bson.M{
		"discovery_id": discoveryID,
		"doc_kind":     docKind,
		"doc_id":       docID,
	}, opts)
	if err != nil {
		return nil, fmt.Errorf("find validation_jobs by doc: %w", err)
	}
	defer cur.Close(ctx)

	out := make([]models.ValidationJob, 0)
	for cur.Next(ctx) {
		var j models.ValidationJob
		if err := cur.Decode(&j); err != nil {
			return nil, fmt.Errorf("decode validation_job: %w", err)
		}
		out = append(out, j)
	}
	return out, nil
}

// ClaimNextPending atomically claims the oldest pending job for the
// given project. Returns (nil, nil) when there is nothing to claim.
//
// The atomic FindOneAndUpdate(filter: status=pending, set: status=running)
// + sort by enqueued_at gives FIFO ordering and is safe across multiple
// API replicas pointing at the same Mongo. Attempt is incremented in
// the same write so retries from stale recovery converge on a bounded
// upper limit.
func (r *ValidationJobRepository) ClaimNextPending(ctx context.Context, projectID, workerID string) (*models.ValidationJob, error) {
	if projectID == "" {
		return nil, errors.New("projectID is required")
	}
	now := time.Now().UTC()
	filter := bson.M{
		"project_id": projectID,
		"status":     models.ValidationJobStatusPending,
	}
	update := bson.M{
		"$set": bson.M{
			"status":       models.ValidationJobStatusRunning,
			"claimed_at":   now,
			"heartbeat_at": now,
			"worker_id":    workerID,
		},
		"$inc": bson.M{"attempt": 1},
	}
	opts := options.FindOneAndUpdate().
		SetSort(bson.D{{Key: "enqueued_at", Value: 1}}).
		SetReturnDocument(options.After)

	var job models.ValidationJob
	err := r.col.FindOneAndUpdate(ctx, filter, update, opts).Decode(&job)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim next pending: %w", err)
	}
	return &job, nil
}

// Heartbeat updates the liveness timestamp on a running job so stale
// recovery doesn't requeue it. Filters on attempt to silently no-op
// when stale recovery has already requeued the job under a higher
// attempt (the agent process that wrote this heartbeat is now an
// orphan — see the stale-recovery double-write risk in the plan).
func (r *ValidationJobRepository) Heartbeat(ctx context.Context, id string, attempt int) error {
	if id == "" {
		return errors.New("id is required")
	}
	_, err := r.col.UpdateOne(ctx,
		bson.M{
			"_id":     id,
			"attempt": attempt,
			"status":  models.ValidationJobStatusRunning,
		},
		bson.M{"$set": bson.M{"heartbeat_at": time.Now().UTC()}},
	)
	if err != nil {
		return fmt.Errorf("heartbeat: %w", err)
	}
	return nil
}

// MarkStep transitions the job's current execution step ("verifier" /
// "refuter" / "combining"). Bumps heartbeat_at in the same write. Like
// Heartbeat, scoped to the matching attempt so a stale-claim race
// can't smear progress writes across requeued jobs.
func (r *ValidationJobRepository) MarkStep(ctx context.Context, id string, attempt int, step string) error {
	if id == "" {
		return errors.New("id is required")
	}
	if step == "" {
		return errors.New("step is required")
	}
	now := time.Now().UTC()
	_, err := r.col.UpdateOne(ctx,
		bson.M{
			"_id":     id,
			"attempt": attempt,
			"status":  models.ValidationJobStatusRunning,
		},
		bson.M{
			"$set": bson.M{
				"step":         step,
				"heartbeat_at": now,
				// First step transition also stamps started_at — the
				// "running but no progress" gap between claim and first
				// step is otherwise invisible. Doing $setOnInsert
				// semantics with $set is tricky here; instead the agent
				// writes started_at explicitly on first MarkStep via
				// the SetStartedAt helper.
			},
		},
	)
	if err != nil {
		return fmt.Errorf("mark step: %w", err)
	}
	return nil
}

// SetStartedAt is called by the agent once, when it begins actual work
// (after Mongo + warehouse + LLM client construction). Lets the
// dashboard distinguish "queued but agent hasn't started yet" from
// "actively running".
func (r *ValidationJobRepository) SetStartedAt(ctx context.Context, id string, attempt int) error {
	if id == "" {
		return errors.New("id is required")
	}
	now := time.Now().UTC()
	_, err := r.col.UpdateOne(ctx,
		bson.M{
			"_id":     id,
			"attempt": attempt,
			"status":  models.ValidationJobStatusRunning,
		},
		bson.M{"$set": bson.M{"started_at": now, "heartbeat_at": now}},
	)
	if err != nil {
		return fmt.Errorf("set started_at: %w", err)
	}
	return nil
}

// MarkCompleted transitions a running job to completed. Stamps
// completed_at + clears any prior error. Idempotent on retry.
func (r *ValidationJobRepository) MarkCompleted(ctx context.Context, id string, attempt int) error {
	return r.markTerminal(ctx, id, attempt, models.ValidationJobStatusCompleted, "")
}

// MarkFailed transitions a running job to failed. Stamps completed_at
// + the error string. The terminal TTL partial index catches it.
func (r *ValidationJobRepository) MarkFailed(ctx context.Context, id string, attempt int, errMsg string) error {
	return r.markTerminal(ctx, id, attempt, models.ValidationJobStatusFailed, errMsg)
}

func (r *ValidationJobRepository) markTerminal(ctx context.Context, id string, attempt int, status, errMsg string) error {
	if id == "" {
		return errors.New("id is required")
	}
	now := time.Now().UTC()
	set := bson.M{
		"status":       status,
		"completed_at": now,
		"heartbeat_at": now,
	}
	if errMsg != "" {
		set["error"] = errMsg
	}
	_, err := r.col.UpdateOne(ctx,
		bson.M{
			"_id":     id,
			"attempt": attempt,
			"status":  models.ValidationJobStatusRunning,
		},
		bson.M{"$set": set},
	)
	if err != nil {
		return fmt.Errorf("mark %s: %w", status, err)
	}
	return nil
}

// Cancel transitions any non-terminal job to cancelled. Idempotent —
// cancelling an already-terminal job returns ErrAlreadyTerminal so the
// handler can return 409 cleanly. Unlike the terminal markers above,
// Cancel does NOT filter on attempt: a user-initiated cancel must take
// effect even if stale recovery just requeued the job.
var ErrAlreadyTerminal = errors.New("validation job is already in a terminal state")
var ErrNotFound = errors.New("validation job not found")

func (r *ValidationJobRepository) Cancel(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("id is required")
	}
	now := time.Now().UTC()
	res, err := r.col.UpdateOne(ctx,
		bson.M{
			"_id": id,
			"status": bson.M{"$in": []string{
				models.ValidationJobStatusPending,
				models.ValidationJobStatusRunning,
			}},
		},
		bson.M{"$set": bson.M{
			"status":       models.ValidationJobStatusCancelled,
			"completed_at": now,
			"cancelled_at": now,
		}},
	)
	if err != nil {
		return fmt.Errorf("cancel: %w", err)
	}
	if res.MatchedCount == 0 {
		// Either the job doesn't exist or it's already terminal.
		// Distinguish for the handler.
		existing, gErr := r.GetByID(ctx, id)
		if gErr != nil {
			return fmt.Errorf("post-cancel lookup: %w", gErr)
		}
		if existing == nil {
			return ErrNotFound
		}
		return ErrAlreadyTerminal
	}
	return nil
}

// RequeueStale flips running jobs whose heartbeat is older than
// staleAfter back to pending so a fresh worker can re-claim them. When
// attempt has reached maxAttempts the job is failed instead (the
// retry budget is exhausted). Returns the count of jobs requeued and
// failed.
func (r *ValidationJobRepository) RequeueStale(ctx context.Context, staleAfter time.Duration, maxAttempts int) (requeued int, failed int, err error) {
	now := time.Now().UTC()
	cutoff := now.Add(-staleAfter)
	filter := bson.M{
		"status": models.ValidationJobStatusRunning,
		"$or": []bson.M{
			{"heartbeat_at": bson.M{"$lt": cutoff}},
			{"heartbeat_at": nil, "claimed_at": bson.M{"$lt": cutoff}},
		},
	}
	cur, fErr := r.col.Find(ctx, filter)
	if fErr != nil {
		return 0, 0, fmt.Errorf("find stale: %w", fErr)
	}
	defer cur.Close(ctx)

	for cur.Next(ctx) {
		var j models.ValidationJob
		if dErr := cur.Decode(&j); dErr != nil {
			return requeued, failed, fmt.Errorf("decode stale: %w", dErr)
		}
		if j.Attempt >= maxAttempts {
			// Retry budget exhausted — terminal-fail it. Skip the
			// attempt filter so we don't race a hypothetical sibling
			// writer.
			_, uErr := r.col.UpdateOne(ctx,
				bson.M{"_id": j.ID, "status": models.ValidationJobStatusRunning},
				bson.M{"$set": bson.M{
					"status":       models.ValidationJobStatusFailed,
					"completed_at": now,
					"error":        fmt.Sprintf("stale recovery: exceeded retry budget (%d attempts)", maxAttempts),
				}},
			)
			if uErr != nil {
				return requeued, failed, fmt.Errorf("fail stale: %w", uErr)
			}
			failed++
			continue
		}
		// Requeue — flip back to pending so a fresh worker can claim.
		// Clear worker_id + heartbeat_at + step so the new claim
		// starts from a known state.
		_, uErr := r.col.UpdateOne(ctx,
			bson.M{"_id": j.ID, "status": models.ValidationJobStatusRunning, "attempt": j.Attempt},
			bson.M{
				"$set": bson.M{
					"status":       models.ValidationJobStatusPending,
					"worker_id":    "",
					"step":         "",
					"heartbeat_at": nil,
				},
			},
		)
		if uErr != nil {
			return requeued, failed, fmt.Errorf("requeue stale: %w", uErr)
		}
		requeued++
	}
	return requeued, failed, nil
}

// CountActiveByDoc returns the count of non-terminal jobs for a
// (discovery, doc) pair. Used by handlers for the pre-Enqueue 409
// check — fast path that avoids hitting the unique-index error for
// the common "user double-clicked" case.
func (r *ValidationJobRepository) CountActiveByDoc(ctx context.Context, discoveryID, docKind, docID string) (int64, error) {
	if discoveryID == "" || docKind == "" || docID == "" {
		return 0, errors.New("discovery_id, doc_kind, doc_id are required")
	}
	return r.col.CountDocuments(ctx, bson.M{
		"discovery_id": discoveryID,
		"doc_kind":     docKind,
		"doc_id":       docID,
		"status": bson.M{"$in": []string{
			models.ValidationJobStatusPending,
			models.ValidationJobStatusRunning,
		}},
	})
}
