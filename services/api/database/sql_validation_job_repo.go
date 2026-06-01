package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

const (
	// sqlValidationJobsCollection is the on-disk Mongo collection name.
	// The matching index definitions live in init.go alongside every
	// other collection's schema. The agent duplicates this constant
	// inline (services/agent/agentserver/validate_sql.go) because it
	// cannot import the api package — keep the two in sync.
	sqlValidationJobsCollection = "sql_validation_jobs"
)

// SQLValidationJobRepo is the persistence contract for batch SQL
// compile-check jobs. Callers enqueue a batch and poll for the verdict;
// the agent (running as a short-lived Job) claims the row, validates
// each statement against the project's warehouse, and writes the results
// back. The interface is intentionally small — Enqueue + GetByID are all
// a caller needs to dispatch a `validate-sql` agent Job and poll Mongo
// for the outcome (there is no HTTP surface and no in-API worker for SQL
// validation; the agent claims the job itself).
type SQLValidationJobRepo interface {
	// Enqueue inserts a fresh pending job. The caller MUST set ID (a
	// UUID) and ProjectID. Statements may be empty.
	Enqueue(ctx context.Context, job *models.SQLValidationJob) error
	// GetByID returns the job by its UUID, or (nil, nil) when not found.
	GetByID(ctx context.Context, id string) (*models.SQLValidationJob, error)
}

// SQLValidationJobRepository persists SQLValidationJob documents in the
// sql_validation_jobs collection. It mirrors the enqueue/get surface of
// ValidationJobRepository; the agent owns the claim → run → complete
// transitions inline (it cannot import this package), so they are not
// exposed here.
type SQLValidationJobRepository struct {
	col *mongo.Collection
}

// Compile-time assertion that the repository satisfies the interface.
var _ SQLValidationJobRepo = (*SQLValidationJobRepository)(nil)

// NewSQLValidationJobRepository wires the repo against the
// sql_validation_jobs collection.
func NewSQLValidationJobRepository(db *DB) *SQLValidationJobRepository {
	return &SQLValidationJobRepository{col: db.Collection(sqlValidationJobsCollection)}
}

// Enqueue inserts a fresh pending job. The caller MUST set ID (a UUID —
// generate one so the response can name a stable id immediately) and
// ProjectID. EnqueuedAt, Status, Attempt and the lifecycle timestamps
// are stamped / reset here so a reused struct cannot smuggle terminal
// state into a new row. Statements may be empty (an empty batch is a
// valid no-op that completes with no results).
func (r *SQLValidationJobRepository) Enqueue(ctx context.Context, job *models.SQLValidationJob) error {
	if job == nil {
		return errors.New("job is required")
	}
	if job.ID == "" {
		return errors.New("job ID is required")
	}
	if job.ProjectID == "" {
		return errors.New("project_id is required")
	}
	// Normalize a nil batch to an empty slice so the stored (and later
	// returned) shape is a stable `[]`, never `null` — callers polling the
	// result rely on `statements` being an array.
	if job.Statements == nil {
		job.Statements = []string{}
	}
	job.Status = models.ValidationJobStatusPending
	job.EnqueuedAt = time.Now().UTC()
	job.Attempt = 0 // bumped to 1 on first claim
	job.Error = ""
	job.Results = nil
	job.WorkerID = ""
	job.ClaimedAt = nil
	job.StartedAt = nil
	job.HeartbeatAt = nil
	job.CompletedAt = nil
	job.CancelledAt = nil

	if _, err := r.col.InsertOne(ctx, job); err != nil {
		return fmt.Errorf("insert sql_validation_job: %w", err)
	}
	return nil
}

// GetByID returns the job by its UUID, or (nil, nil) when not found.
// Distinguishing "not found" from "transport error" with nil-nil keeps
// callers concise: `if job == nil { 404 }`.
func (r *SQLValidationJobRepository) GetByID(ctx context.Context, id string) (*models.SQLValidationJob, error) {
	if id == "" {
		return nil, errors.New("id is required")
	}
	var job models.SQLValidationJob
	err := r.col.FindOne(ctx, bson.M{"_id": id}).Decode(&job)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find sql_validation_job: %w", err)
	}
	// Keep the wire shape stable: a row stored with a null statements
	// field (e.g. inserted out-of-band) reads back as `[]`, not `null`.
	if job.Statements == nil {
		job.Statements = []string{}
	}
	return &job, nil
}
