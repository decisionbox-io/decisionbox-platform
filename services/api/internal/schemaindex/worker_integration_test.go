//go:build integration

package schemaindex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/internal/runner"
	"github.com/decisionbox-io/decisionbox/services/api/models"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
)

// These tests verify the worker's lifecycle contract end-to-end without
// spawning a real agent subprocess. A fake Runner stands in for the
// agent; we assert that:
//   * ClaimNextPendingIndex is invoked (implicit — only pending projects
//     get picked up; ready/failed/indexing don't)
//   * RunIndexSchema is called with the claimed project_id
//   * On success, status transitions to ready
//   * On failure, status transitions to failed with the error message
//   * Idle queue → no RunIndexSchema calls
//   * Exactly one concurrent run per tick (the queue serialises)

var testDB *database.DB

func TestMain(m *testing.M) {
	ctx := context.Background()
	container, err := tcmongo.Run(ctx, "mongo:7.0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "MongoDB start failed: %v\n", err)
		os.Exit(1)
	}
	defer container.Terminate(ctx)

	uri, _ := container.ConnectionString(ctx)
	cfg := gomongo.DefaultConfig()
	cfg.URI = uri
	cfg.Database = "schemaindex_worker_integration_test"

	client, err := gomongo.NewClient(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "MongoDB connect failed: %v\n", err)
		os.Exit(1)
	}
	defer client.Disconnect(ctx)

	testDB = database.New(client)
	if err := database.InitDatabase(ctx, testDB); err != nil {
		fmt.Fprintf(os.Stderr, "InitDatabase failed: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

type recorderRunner struct {
	calls []runner.IndexSchemaOptions
	// scripted[i] → error for the i-th RunIndexSchema call. Missing/nil
	// entries → success.
	scripted []error
	cursor   int
}

func (r *recorderRunner) Run(_ context.Context, _ runner.RunOptions) error { return nil }
func (r *recorderRunner) RunSync(_ context.Context, _ runner.RunSyncOptions) (*runner.RunSyncResult, error) {
	return nil, nil
}
func (r *recorderRunner) Cancel(_ context.Context, _ string) error { return nil }
func (r *recorderRunner) RunIndexSchema(_ context.Context, opts runner.IndexSchemaOptions) error {
	r.calls = append(r.calls, opts)
	if r.cursor < len(r.scripted) {
		err := r.scripted[r.cursor]
		r.cursor++
		return err
	}
	return nil
}

func newWorker(t *testing.T, run runner.Runner) *Worker {
	t.Helper()
	w, err := New(WorkerConfig{
		Projects:     database.NewProjectRepository(testDB),
		Progress:     database.NewSchemaIndexProgressRepository(testDB),
		Runner:       run,
		PollInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func makeProject(t *testing.T, name, status string) *models.Project {
	t.Helper()
	repo := database.NewProjectRepository(testDB)
	p := &models.Project{
		Name:              name,
		Domain:            "gaming",
		Category:          "match3",
		SchemaIndexStatus: status,
	}
	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("Create %s: %v", name, err)
	}
	t.Cleanup(func() { _ = repo.Delete(context.Background(), p.ID) })
	return p
}

func TestInteg_Worker_SuccessTransitionsToReady(t *testing.T) {
	ctx := context.Background()
	p := makeProject(t, "worker-ok", models.SchemaIndexStatusPendingIndexing)

	run := &recorderRunner{}
	w := newWorker(t, run)
	w.tick(ctx)

	if len(run.calls) != 1 {
		t.Fatalf("RunIndexSchema called %d times", len(run.calls))
	}
	if run.calls[0].ProjectID != p.ID {
		t.Errorf("claimed project_id = %q", run.calls[0].ProjectID)
	}

	got, _ := database.NewProjectRepository(testDB).GetByID(ctx, p.ID)
	if got.SchemaIndexStatus != "ready" {
		t.Errorf("status = %q, want ready", got.SchemaIndexStatus)
	}
	if got.SchemaIndexUpdatedAt == nil {
		t.Error("updated_at should be stamped on ready transition")
	}
}

func TestInteg_Worker_FailureTransitionsToFailedWithMessage(t *testing.T) {
	ctx := context.Background()
	p := makeProject(t, "worker-fail", models.SchemaIndexStatusPendingIndexing)

	run := &recorderRunner{scripted: []error{errors.New("agent: qwen quota exhausted")}}
	w := newWorker(t, run)
	w.tick(ctx)

	got, _ := database.NewProjectRepository(testDB).GetByID(ctx, p.ID)
	if got.SchemaIndexStatus != "failed" {
		t.Errorf("status = %q, want failed", got.SchemaIndexStatus)
	}
	if got.SchemaIndexError == "" {
		t.Error("error message should be stamped on failed transition")
	}
}

func TestInteg_Worker_IdleQueueIsNoop(t *testing.T) {
	ctx := context.Background()
	run := &recorderRunner{}
	w := newWorker(t, run)
	w.tick(ctx) // no pending projects
	if len(run.calls) != 0 {
		t.Errorf("idle queue spawned %d runs", len(run.calls))
	}
}

func TestInteg_Worker_IgnoresNonPendingProjects(t *testing.T) {
	ctx := context.Background()
	makeProject(t, "worker-ready", models.SchemaIndexStatusReady)
	makeProject(t, "worker-indexing", models.SchemaIndexStatusIndexing)
	makeProject(t, "worker-failed", models.SchemaIndexStatusFailed)

	run := &recorderRunner{}
	w := newWorker(t, run)
	w.tick(ctx)
	if len(run.calls) != 0 {
		t.Errorf("non-pending projects triggered runs: %+v", run.calls)
	}
}

func TestInteg_Worker_ProcessesOneAtATime(t *testing.T) {
	ctx := context.Background()
	p1 := makeProject(t, "worker-fifo-1", models.SchemaIndexStatusPendingIndexing)
	time.Sleep(10 * time.Millisecond)
	p2 := makeProject(t, "worker-fifo-2", models.SchemaIndexStatusPendingIndexing)

	run := &recorderRunner{}
	w := newWorker(t, run)

	w.tick(ctx)
	if len(run.calls) != 1 || run.calls[0].ProjectID != p1.ID {
		t.Errorf("first tick: %+v (want p1=%s)", run.calls, p1.ID)
	}
	w.tick(ctx)
	if len(run.calls) != 2 || run.calls[1].ProjectID != p2.ID {
		t.Errorf("second tick: %+v (want p2=%s)", run.calls, p2.ID)
	}
	// Both should be ready now.
	g1, _ := database.NewProjectRepository(testDB).GetByID(ctx, p1.ID)
	g2, _ := database.NewProjectRepository(testDB).GetByID(ctx, p2.ID)
	if g1.SchemaIndexStatus != "ready" || g2.SchemaIndexStatus != "ready" {
		t.Errorf("final statuses: p1=%q p2=%q", g1.SchemaIndexStatus, g2.SchemaIndexStatus)
	}
}

func TestInteg_Worker_StartStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := makeProject(t, "worker-lifecycle", models.SchemaIndexStatusPendingIndexing)

	run := &recorderRunner{}
	w := newWorker(t, run)

	done := make(chan struct{})
	go func() {
		w.Start(ctx)
		close(done)
	}()

	// Wait for the worker to pick up the project.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := database.NewProjectRepository(testDB).GetByID(ctx, p.ID)
		if got.SchemaIndexStatus == "ready" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	// Cancel and wait for graceful return.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop on context cancel")
	}

	// Final status must be ready — the transition uses a detached context.
	got, _ := database.NewProjectRepository(testDB).GetByID(context.Background(), p.ID)
	if got.SchemaIndexStatus != "ready" {
		t.Errorf("final status = %q", got.SchemaIndexStatus)
	}
}
