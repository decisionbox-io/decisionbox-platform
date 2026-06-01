//go:build integration

// Integration tests for the sql_validation_jobs collection — exercises
// the enqueue/get surface against a real Mongo (testcontainer) so the
// stamped lifecycle fields and the indexes declared in init.go behave as
// designed.
package database

import (
	"context"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"github.com/google/uuid"
)

func newSQLJobRepo(t *testing.T) *SQLValidationJobRepository {
	t.Helper()
	repo := NewSQLValidationJobRepository(testDB)
	if err := repo.col.Drop(context.Background()); err != nil {
		t.Fatalf("drop collection: %v", err)
	}
	if err := InitDatabase(context.Background(), testDB); err != nil {
		t.Fatalf("re-init after drop: %v", err)
	}
	return repo
}

func TestInteg_SQLValidationJobs_EnqueueAndGet(t *testing.T) {
	ctx := context.Background()
	repo := newSQLJobRepo(t)

	job := &models.SQLValidationJob{
		ID:         uuid.New().String(),
		ProjectID:  "507f1f77bcf86cd799439011",
		Statements: []string{"SELECT 1", "SELECT 2"},
	}
	if err := repo.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Enqueue must stamp pending + a fresh enqueued_at and reset the
	// lifecycle fields regardless of what the caller passed in.
	if job.Status != models.ValidationJobStatusPending {
		t.Errorf("status = %q, want pending", job.Status)
	}
	if job.EnqueuedAt.IsZero() {
		t.Errorf("enqueued_at not stamped")
	}
	if job.Attempt != 0 {
		t.Errorf("attempt = %d, want 0 (bumped to 1 on first claim)", job.Attempt)
	}

	got, err := repo.GetByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatalf("get returned nil for an enqueued job")
	}
	if got.ProjectID != job.ProjectID {
		t.Errorf("project_id = %q, want %q", got.ProjectID, job.ProjectID)
	}
	if len(got.Statements) != 2 || got.Statements[0] != "SELECT 1" {
		t.Errorf("statements round-trip = %v", got.Statements)
	}
	if got.IsTerminal() {
		t.Errorf("a pending job must not be terminal")
	}
}

// A reused struct carrying terminal state must not smuggle that state
// into a fresh row — Enqueue resets results / error / timestamps.
func TestInteg_SQLValidationJobs_EnqueueResetsStaleState(t *testing.T) {
	ctx := context.Background()
	repo := newSQLJobRepo(t)

	job := &models.SQLValidationJob{
		ID:         uuid.New().String(),
		ProjectID:  "507f1f77bcf86cd799439011",
		Statements: []string{"SELECT 1"},
		Status:     models.ValidationJobStatusCompleted,
		Error:      "stale error",
		Attempt:    5,
		Results:    []models.SQLValidationResult{{SQL: "SELECT 1", OK: true}},
	}
	if err := repo.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	got, err := repo.GetByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != models.ValidationJobStatusPending {
		t.Errorf("status = %q, want pending (reset)", got.Status)
	}
	if got.Error != "" {
		t.Errorf("error = %q, want empty (reset)", got.Error)
	}
	if len(got.Results) != 0 {
		t.Errorf("results = %v, want empty (reset)", got.Results)
	}
	if got.Attempt != 0 {
		t.Errorf("attempt = %d, want 0 (reset)", got.Attempt)
	}
}

// A nil statement batch must round-trip as a non-nil empty slice so the
// JSON/bson wire shape is a stable `[]`, never `null`.
func TestInteg_SQLValidationJobs_NilStatementsNormalizedToEmpty(t *testing.T) {
	ctx := context.Background()
	repo := newSQLJobRepo(t)

	job := &models.SQLValidationJob{
		ID:        uuid.New().String(),
		ProjectID: "507f1f77bcf86cd799439011",
		// Statements left nil.
	}
	if err := repo.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if job.Statements == nil {
		t.Errorf("Enqueue should normalize nil statements to an empty slice")
	}

	got, err := repo.GetByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Statements == nil {
		t.Errorf("GetByID returned nil statements, want empty slice")
	}
	if len(got.Statements) != 0 {
		t.Errorf("statements = %v, want empty", got.Statements)
	}
}

func TestInteg_SQLValidationJobs_GetMissingReturnsNilNil(t *testing.T) {
	ctx := context.Background()
	repo := newSQLJobRepo(t)

	got, err := repo.GetByID(ctx, uuid.New().String())
	if err != nil {
		t.Fatalf("get missing: unexpected error %v", err)
	}
	if got != nil {
		t.Errorf("get missing = %+v, want nil", got)
	}
}

func TestInteg_SQLValidationJobs_EnqueueValidation(t *testing.T) {
	ctx := context.Background()
	repo := newSQLJobRepo(t)

	if err := repo.Enqueue(ctx, nil); err == nil {
		t.Errorf("enqueue nil job should error")
	}
	if err := repo.Enqueue(ctx, &models.SQLValidationJob{ProjectID: "p"}); err == nil {
		t.Errorf("enqueue without ID should error")
	}
	if err := repo.Enqueue(ctx, &models.SQLValidationJob{ID: uuid.New().String()}); err == nil {
		t.Errorf("enqueue without project_id should error")
	}
}
