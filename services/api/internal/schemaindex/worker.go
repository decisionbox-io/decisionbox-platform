// Package schemaindex contains the API-side background loop that
// spawns agent --mode index-schema processes for projects in
// schema_index_status="pending_indexing". One worker per API process
// (plan §3 — deployments are single-node and single-tenant), so no
// cross-instance lock coordination is needed. The atomic
// FindOneAndUpdate claim on the projects collection is still required
// to serialise between the worker loop and concurrent user-triggered
// Re-index clicks.
package schemaindex

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/internal/runner"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// DefaultPollInterval is how often the worker polls for pending
// projects. 5 seconds keeps the "create project" → "indexing started"
// latency short without hammering Mongo when the queue is idle.
const DefaultPollInterval = 5 * time.Second

// WorkerConfig parameterises the background loop.
type WorkerConfig struct {
	Projects     *database.ProjectRepository
	Progress     *database.SchemaIndexProgressRepository
	Logs         *database.SchemaIndexLogRepository // optional — enables per-run log capture into Mongo
	Runner       runner.Runner
	PollInterval time.Duration // 0 → DefaultPollInterval
}

// Worker claims one pending-indexing project at a time and runs it
// through the agent. Stateless enough to be restarted cleanly; anything
// that was in-flight when the API crashed stays "indexing" in Mongo and
// needs a manual retry (next user click on "Retry indexing" will
// transition it back to pending_indexing). We deliberately do NOT
// auto-reset stale "indexing" rows on startup: the agent might still
// be running as a subprocess on another API instance in a misconfigured
// deployment, and flipping status from under it would double-count.
type Worker struct {
	cfg WorkerConfig

	// inflight maps projectID → a cancel func that aborts the current
	// indexing run. Populated in tick() for the duration of a run and
	// removed in the defer path. The Cancel() method reads this map to
	// signal a user-initiated abort from the dashboard.
	mu       sync.Mutex
	inflight map[string]context.CancelFunc
}

// New constructs a Worker. Validates dependencies so configuration errors
// surface at startup.
func New(cfg WorkerConfig) (*Worker, error) {
	if cfg.Projects == nil {
		return nil, errors.New("schemaindex: Projects repo is required")
	}
	if cfg.Progress == nil {
		return nil, errors.New("schemaindex: Progress repo is required")
	}
	if cfg.Runner == nil {
		return nil, errors.New("schemaindex: Runner is required")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	return &Worker{cfg: cfg, inflight: make(map[string]context.CancelFunc)}, nil
}

// Cancel aborts the in-flight indexing run for projectID, if any.
// Returns true when a run was in flight and the cancel signal was
// delivered; false when nothing was running for that project (e.g.
// the user clicked Cancel just as the run completed — race-safe).
//
// The actual status transition to "cancelled" happens in tick() when
// the subprocess exits with context.Canceled; Cancel only signals.
func (w *Worker) Cancel(projectID string) bool {
	w.mu.Lock()
	cancel, ok := w.inflight[projectID]
	w.mu.Unlock()
	if !ok {
		return false
	}
	cancel()
	apilog.WithField("project_id", projectID).Info("Schema-index run cancel requested")
	return true
}

// IsRunning reports whether the worker currently has an agent subprocess
// running for projectID. Useful for the API to 409 a cancel request
// that races a run completion.
func (w *Worker) IsRunning(projectID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.inflight[projectID]
	return ok
}

func (w *Worker) register(projectID string, cancel context.CancelFunc) {
	w.mu.Lock()
	w.inflight[projectID] = cancel
	w.mu.Unlock()
}

func (w *Worker) deregister(projectID string) {
	w.mu.Lock()
	delete(w.inflight, projectID)
	w.mu.Unlock()
}

// Start runs the worker loop until ctx is cancelled. Blocking — intended
// to be launched from a goroutine in apiserver.Run. Safe to call only
// once per Worker instance.
func (w *Worker) Start(ctx context.Context) {
	apilog.WithField("poll_ms", w.cfg.PollInterval.Milliseconds()).Info("Schema-index worker started")
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	// First tick runs immediately so a project created just before API
	// boot doesn't wait a full poll interval.
	w.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			apilog.Info("Schema-index worker stopping")
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

// tick claims at most one project and runs it to completion. Single-
// project-per-tick pacing keeps indexing runs from starving the
// discovery-run spawner sitting on the same API process — a 2K-table
// warehouse can occupy the worker for ~6 minutes, during which any new
// pending projects just wait their turn.
func (w *Worker) tick(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	p, err := w.cfg.Projects.ClaimNextPendingIndex(ctx)
	if err != nil {
		apilog.WithError(err).Warn("schemaindex: claim failed")
		return
	}
	if p == nil {
		return // idle queue
	}

	apilog.WithFields(apilog.Fields{
		"project_id": p.ID,
		"project":    p.Name,
	}).Info("Schema-index worker claimed project")

	// Run-id matches the claim timestamp, so progress docs + logs line
	// up. Not persisted on the project — the worker owns it and the
	// progress doc carries it.
	runID := time.Now().UTC().Format("20060102T150405.000Z")

	// Log capture fan-out: every agent stderr line also lands in
	// project_schema_index_logs so the dashboard can show an in-UI
	// debug tail. The callback is synchronous on purpose — Mongo
	// InsertOne takes ~1ms locally, and ordering matters for a tail
	// view. If latency ever becomes a concern we'd swap to a channel-
	// fed worker here rather than racing goroutines.
	var onLine func(string)
	if w.cfg.Logs != nil {
		projectID := p.ID
		capRunID := runID
		onLine = func(line string) {
			// Use a short-lived detached context: the subprocess
			// ctx can be cancelled mid-write on shutdown, but we
			// still want the last line persisted.
			writeCtx, writeCancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = w.cfg.Logs.Append(writeCtx, projectID, capRunID, line)
			writeCancel()
		}
	}

	// Derive a per-run context so the dashboard can cancel this one
	// run without bringing down the whole worker loop. register()
	// captures the cancel func so Worker.Cancel can reach it.
	runCtx, runCancel := context.WithCancel(ctx)
	w.register(p.ID, runCancel)
	defer func() {
		w.deregister(p.ID)
		runCancel()
	}()

	err = w.cfg.Runner.RunIndexSchema(runCtx, runner.IndexSchemaOptions{
		ProjectID: p.ID,
		RunID:     runID,
		OnLogLine: onLine,
	})
	// Use a detached context for the status transition — ctx may be
	// cancelled by Start's return (clean shutdown) and we still want
	// the final state to make it to Mongo.
	transitionCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err != nil {
		// Distinguish user-initiated cancellation from a real failure
		// so the UI can show a "Cancelled" pill instead of an error.
		// runCtx.Err being set means Worker.Cancel fired; the parent
		// ctx.Err check covers API shutdown, which we also treat as
		// cancellation (the worker will resume-or-retry on next boot
		// via ResetStaleIndexingProjects).
		if runCtx.Err() != nil {
			apilog.WithField("project_id", p.ID).Info("Schema-index run cancelled")
			if setErr := w.cfg.Projects.SetSchemaIndexStatus(transitionCtx, p.ID, models.SchemaIndexStatusCancelled, "cancelled by user"); setErr != nil {
				apilog.WithError(setErr).Error("schemaindex: set cancelled-status")
			}
			return
		}
		apilog.WithFields(apilog.Fields{
			"project_id": p.ID,
			"error":      err.Error(),
		}).Warn("Schema-index run failed")
		if setErr := w.cfg.Projects.SetSchemaIndexStatus(transitionCtx, p.ID, models.SchemaIndexStatusFailed, err.Error()); setErr != nil {
			apilog.WithError(setErr).Error("schemaindex: set failed-status")
		}
		return
	}

	apilog.WithField("project_id", p.ID).Info("Schema-index run completed")
	if setErr := w.cfg.Projects.SetSchemaIndexStatus(transitionCtx, p.ID, models.SchemaIndexStatusReady, ""); setErr != nil {
		apilog.WithError(setErr).Error("schemaindex: set ready-status")
	}
}
