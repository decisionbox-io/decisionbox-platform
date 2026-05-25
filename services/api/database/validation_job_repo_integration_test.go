//go:build integration

// Integration tests for the validation-jobs collection — exercises
// every state-machine transition against a real Mongo (testcontainer)
// so the indexes we declared in init.go behave as designed under load.
//
// The race test in particular pins the partial-unique-on-active
// invariant: two concurrent Enqueue calls for the same (discovery,
// doc) MUST resolve to exactly one inserted row and one
// ErrDuplicateActiveJob. Without the partial-unique index, both
// inserts would succeed and the worker would later run validation
// twice.
package database

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"github.com/google/uuid"
)

func newJobRepo(t *testing.T) *ValidationJobRepository {
	t.Helper()
	repo := NewValidationJobRepository(testDB)
	if err := repo.col.Drop(context.Background()); err != nil {
		t.Fatalf("drop collection: %v", err)
	}
	// Re-create indexes after the drop so the partial-unique-on-active
	// invariant is enforced for the test.
	if err := InitDatabase(context.Background(), testDB); err != nil {
		t.Fatalf("re-init after drop: %v", err)
	}
	return repo
}

func newJob(t *testing.T) *models.ValidationJob {
	t.Helper()
	return &models.ValidationJob{
		ID:          uuid.New().String(),
		ProjectID:   "proj-" + uuid.New().String()[:8],
		DiscoveryID: "disc-" + uuid.New().String()[:8],
		DocKind:     models.ValidationJobDocKindInsight,
		DocID:       "doc-" + uuid.New().String()[:8],
	}
}

func TestInteg_ValidationJobs_EnqueueClaimComplete_HappyPath(t *testing.T) {
	ctx := context.Background()
	repo := newJobRepo(t)
	j := newJob(t)

	if err := repo.Enqueue(ctx, j); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	claimed, err := repo.ClaimNextPending(ctx, j.ProjectID, "test-worker-1")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed == nil || claimed.ID != j.ID {
		t.Fatalf("claim returned wrong job: %+v", claimed)
	}
	if claimed.Status != models.ValidationJobStatusRunning {
		t.Errorf("claim should flip status to running, got %q", claimed.Status)
	}
	if claimed.Attempt != 1 {
		t.Errorf("claim should set attempt to 1, got %d", claimed.Attempt)
	}
	if claimed.WorkerID != "test-worker-1" {
		t.Errorf("worker_id = %q, want test-worker-1", claimed.WorkerID)
	}
	if claimed.ClaimedAt == nil {
		t.Errorf("claimed_at should be set after claim")
	}
	if claimed.HeartbeatAt == nil {
		t.Errorf("heartbeat_at should be set after claim")
	}

	if err := repo.SetStartedAt(ctx, j.ID, claimed.Attempt); err != nil {
		t.Fatalf("set started_at: %v", err)
	}
	if err := repo.MarkStep(ctx, j.ID, claimed.Attempt, models.ValidationJobStepVerifier); err != nil {
		t.Fatalf("mark step verifier: %v", err)
	}
	if err := repo.MarkStep(ctx, j.ID, claimed.Attempt, models.ValidationJobStepRefuter); err != nil {
		t.Fatalf("mark step refuter: %v", err)
	}
	if err := repo.MarkCompleted(ctx, j.ID, claimed.Attempt); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	final, err := repo.GetByID(ctx, j.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if final.Status != models.ValidationJobStatusCompleted {
		t.Errorf("final status = %q, want completed", final.Status)
	}
	if final.CompletedAt == nil {
		t.Errorf("completed_at should be set")
	}
	if !final.IsTerminal() {
		t.Errorf("completed job should report IsTerminal()=true")
	}
}

func TestInteg_ValidationJobs_ClaimNextPending_EmptyReturnsNilNil(t *testing.T) {
	ctx := context.Background()
	repo := newJobRepo(t)
	got, err := repo.ClaimNextPending(ctx, "no-such-project", "w")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Errorf("empty queue should return nil, got %+v", got)
	}
}

func TestInteg_ValidationJobs_ClaimNextPending_OldestFirst(t *testing.T) {
	ctx := context.Background()
	repo := newJobRepo(t)

	projectID := "proj-fifo"
	j1, j2, j3 := newJob(t), newJob(t), newJob(t)
	j1.ProjectID, j2.ProjectID, j3.ProjectID = projectID, projectID, projectID
	// Different doc_ids — otherwise the partial-unique-on-active
	// index rejects the second pending row.
	j1.DocID, j2.DocID, j3.DocID = "doc-1", "doc-2", "doc-3"

	for _, j := range []*models.ValidationJob{j1, j2, j3} {
		if err := repo.Enqueue(ctx, j); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		// Force EnqueuedAt to be strictly ordered so the FIFO claim
		// assertion isn't sensitive to sub-millisecond timing.
		time.Sleep(5 * time.Millisecond)
	}

	for _, expected := range []*models.ValidationJob{j1, j2, j3} {
		got, err := repo.ClaimNextPending(ctx, projectID, "w")
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if got == nil || got.ID != expected.ID {
			t.Errorf("FIFO violated: want %s, got %+v", expected.ID, got)
		}
	}
}

// THE race test. Two goroutines call Enqueue for the same (discovery,
// doc) at the same time. Exactly one must succeed; the other MUST get
// ErrDuplicateActiveJob. This is the durable defence — without the
// partial-unique-on-active index the user could double-click and the
// worker would run validation twice.
func TestInteg_ValidationJobs_Enqueue_PartialUniqueOnActiveRejectsRaceDuplicate(t *testing.T) {
	ctx := context.Background()
	repo := newJobRepo(t)
	template := newJob(t)

	const N = 8
	var wg sync.WaitGroup
	results := make([]error, N)

	for i := 0; i < N; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			j := &models.ValidationJob{
				ID:          uuid.New().String(), // unique _id per goroutine
				ProjectID:   template.ProjectID,
				DiscoveryID: template.DiscoveryID,
				DocKind:     template.DocKind,
				DocID:       template.DocID,
			}
			results[i] = repo.Enqueue(ctx, j)
		}()
	}
	wg.Wait()

	ok, dup, other := 0, 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrDuplicateActiveJob):
			dup++
		default:
			t.Errorf("unexpected error: %v", err)
			other++
		}
	}
	if ok != 1 {
		t.Errorf("exactly one Enqueue should succeed, got %d successes (%d duplicates, %d other)", ok, dup, other)
	}
	if dup != N-1 {
		t.Errorf("the other %d Enqueues should report ErrDuplicateActiveJob, got %d", N-1, dup)
	}
}

// After a job reaches a terminal state, a fresh Enqueue for the same
// (discovery, doc) must succeed — the partial-unique-on-active index
// only fires while the row is pending or running.
func TestInteg_ValidationJobs_Enqueue_TerminalDoesNotBlockReEnqueue(t *testing.T) {
	ctx := context.Background()
	repo := newJobRepo(t)
	j1 := newJob(t)

	if err := repo.Enqueue(ctx, j1); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	claimed, err := repo.ClaimNextPending(ctx, j1.ProjectID, "w")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := repo.MarkCompleted(ctx, j1.ID, claimed.Attempt); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	j2 := &models.ValidationJob{
		ID:          uuid.New().String(),
		ProjectID:   j1.ProjectID,
		DiscoveryID: j1.DiscoveryID,
		DocKind:     j1.DocKind,
		DocID:       j1.DocID,
	}
	if err := repo.Enqueue(ctx, j2); err != nil {
		t.Errorf("re-enqueue after terminal should succeed, got %v", err)
	}
}

func TestInteg_ValidationJobs_Cancel_PendingTransitionsToCancelled(t *testing.T) {
	ctx := context.Background()
	repo := newJobRepo(t)
	j := newJob(t)
	if err := repo.Enqueue(ctx, j); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if err := repo.Cancel(ctx, j.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	got, err := repo.GetByID(ctx, j.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != models.ValidationJobStatusCancelled {
		t.Errorf("status = %q, want cancelled", got.Status)
	}
	if got.CancelledAt == nil {
		t.Errorf("cancelled_at should be set")
	}
	if got.CompletedAt == nil {
		t.Errorf("completed_at should be set (drives the TTL)")
	}
}

func TestInteg_ValidationJobs_Cancel_AlreadyTerminalReturnsErrAlreadyTerminal(t *testing.T) {
	ctx := context.Background()
	repo := newJobRepo(t)
	j := newJob(t)
	if err := repo.Enqueue(ctx, j); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claimed, _ := repo.ClaimNextPending(ctx, j.ProjectID, "w")
	if err := repo.MarkCompleted(ctx, j.ID, claimed.Attempt); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	err := repo.Cancel(ctx, j.ID)
	if !errors.Is(err, ErrAlreadyTerminal) {
		t.Errorf("cancel on terminal: want ErrAlreadyTerminal, got %v", err)
	}
}

func TestInteg_ValidationJobs_Cancel_UnknownReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	repo := newJobRepo(t)
	err := repo.Cancel(ctx, "nope-not-a-real-id")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("cancel on unknown: want ErrNotFound, got %v", err)
	}
}

func TestInteg_ValidationJobs_RequeueStale_FlipsBackToPending(t *testing.T) {
	ctx := context.Background()
	repo := newJobRepo(t)
	j := newJob(t)
	if err := repo.Enqueue(ctx, j); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := repo.ClaimNextPending(ctx, j.ProjectID, "w"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Force heartbeat into the past so the stale sweep picks it up.
	past := time.Now().Add(-10 * time.Minute).UTC()
	_, err := repo.col.UpdateOne(ctx,
		map[string]interface{}{"_id": j.ID},
		map[string]interface{}{"$set": map[string]interface{}{"heartbeat_at": past}},
	)
	if err != nil {
		t.Fatalf("backdate heartbeat: %v", err)
	}

	requeued, failed, err := repo.RequeueStale(ctx, 5*time.Minute, 3)
	if err != nil {
		t.Fatalf("requeue stale: %v", err)
	}
	if requeued != 1 || failed != 0 {
		t.Errorf("requeued=%d failed=%d, want 1/0", requeued, failed)
	}

	got, err := repo.GetByID(ctx, j.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != models.ValidationJobStatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	if got.WorkerID != "" {
		t.Errorf("worker_id should be cleared, got %q", got.WorkerID)
	}
}

func TestInteg_ValidationJobs_RequeueStale_FailsWhenAttemptExceedsMax(t *testing.T) {
	ctx := context.Background()
	repo := newJobRepo(t)
	j := newJob(t)
	j.Attempt = 5 // already past the budget
	j.Status = models.ValidationJobStatusRunning
	j.EnqueuedAt = time.Now().UTC()
	past := time.Now().Add(-10 * time.Minute).UTC()
	j.ClaimedAt = &past
	j.HeartbeatAt = &past
	// Direct insert — bypassing Enqueue which would force pending status.
	if _, err := repo.col.InsertOne(ctx, j); err != nil {
		t.Fatalf("direct insert: %v", err)
	}

	requeued, failed, err := repo.RequeueStale(ctx, 5*time.Minute, 3)
	if err != nil {
		t.Fatalf("requeue stale: %v", err)
	}
	if requeued != 0 || failed != 1 {
		t.Errorf("requeued=%d failed=%d, want 0/1", requeued, failed)
	}

	got, _ := repo.GetByID(ctx, j.ID)
	if got.Status != models.ValidationJobStatusFailed {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if got.Error == "" {
		t.Errorf("error should explain the retry-budget exhaustion")
	}
}

func TestInteg_ValidationJobs_ListByDoc_MostRecentFirst(t *testing.T) {
	ctx := context.Background()
	repo := newJobRepo(t)

	template := newJob(t)
	for i := 0; i < 3; i++ {
		// Complete each job before enqueuing the next so the
		// active-uniqueness invariant lets us insert 3 rows for the
		// same (discovery, doc).
		j := &models.ValidationJob{
			ID:          uuid.New().String(),
			ProjectID:   template.ProjectID,
			DiscoveryID: template.DiscoveryID,
			DocKind:     template.DocKind,
			DocID:       template.DocID,
		}
		if err := repo.Enqueue(ctx, j); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
		claimed, _ := repo.ClaimNextPending(ctx, j.ProjectID, "w")
		if err := repo.MarkCompleted(ctx, j.ID, claimed.Attempt); err != nil {
			t.Fatalf("mark completed: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	jobs, err := repo.ListByDoc(ctx, template.DiscoveryID, template.DocKind, template.DocID, 20)
	if err != nil {
		t.Fatalf("list by doc: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("want 3 jobs, got %d", len(jobs))
	}
	for i := 1; i < len(jobs); i++ {
		if !jobs[i-1].EnqueuedAt.After(jobs[i].EnqueuedAt) {
			t.Errorf("list-by-doc must be most-recent first; broken at index %d", i)
		}
	}
}

func TestInteg_ValidationJobs_Heartbeat_NoOpsWhenAttemptMismatches(t *testing.T) {
	ctx := context.Background()
	repo := newJobRepo(t)
	j := newJob(t)
	if err := repo.Enqueue(ctx, j); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claimed, _ := repo.ClaimNextPending(ctx, j.ProjectID, "w")

	// A stale agent process from a prior attempt heartbeats with the
	// wrong attempt number. The write must silently no-op — otherwise
	// it would smear progress across requeued jobs.
	if err := repo.Heartbeat(ctx, j.ID, claimed.Attempt+5); err != nil {
		t.Errorf("heartbeat on wrong attempt should be a silent no-op, got %v", err)
	}
}

func TestInteg_ValidationJobs_CountActiveByDoc_OnlyCountsNonTerminal(t *testing.T) {
	ctx := context.Background()
	repo := newJobRepo(t)
	template := newJob(t)

	// First job — keep it pending.
	if err := repo.Enqueue(ctx, template); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	count, err := repo.CountActiveByDoc(ctx, template.DiscoveryID, template.DocKind, template.DocID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("pending job should count as active, got count=%d", count)
	}

	claimed, _ := repo.ClaimNextPending(ctx, template.ProjectID, "w")
	if err := repo.MarkCompleted(ctx, template.ID, claimed.Attempt); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	count, err = repo.CountActiveByDoc(ctx, template.DiscoveryID, template.DocKind, template.DocID)
	if err != nil {
		t.Fatalf("count after terminal: %v", err)
	}
	if count != 0 {
		t.Errorf("completed job should not count as active, got count=%d", count)
	}
}
