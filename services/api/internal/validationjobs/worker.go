// Package validationjobs contains the API-side background loop that
// processes manual-validation requests. The handler in
// services/api/internal/handler/validation_jobs.go enqueues a
// ValidationJob; this worker claims it, spawns the agent in
// --mode=validate-doc via runner.Runner, and transitions the row to
// completed | failed when the agent exits.
//
// Architecture mirrors schemaindex.Worker — same poll/claim/run shape,
// same Cancel() + inflight-map for user-initiated aborts, same
// stale-recovery sweep on a separate cadence. Differences are limited
// to the keying (per-job vs per-project) and the heartbeat-driven
// stale threshold (the validation pipeline writes heartbeat_at on
// every step change, so we don't need the project-keyed
// elapsed-since-start heuristic).
package validationjobs

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

// DefaultPollInterval — 2 seconds keeps "click Run" → "agent
// starting" latency short without hammering Mongo when the queue is
// idle. The dashboard polls the job state at the same cadence.
const DefaultPollInterval = 2 * time.Second

// DefaultStaleAfter is the heartbeat-age threshold past which the
// stale-recovery sweep flips a running job back to pending. The agent
// writes heartbeat_at every ~60 seconds + on every step change, so
// 5 minutes is generous enough to ride out a slow LLM call without
// false-positives but short enough that a real crash gets recovered
// promptly.
const DefaultStaleAfter = 5 * time.Minute

// DefaultMaxAttempts caps how many times a job can be requeued by
// stale recovery before it gets marked failed with a clear "exceeded
// retry budget" error. Three attempts cover the common pod-eviction
// or transient-LLM-cancellation cases without making genuinely-broken
// jobs loop forever.
const DefaultMaxAttempts = 3

// DefaultStaleSweepInterval is how often the worker walks
// validation_jobs looking for rows whose heartbeat has gone cold.
// Mongo runs the scan against the `(project_id, status)` index so the
// cost is bounded; the sweep frequency just bounds the user-visible
// "is this stuck?" latency.
const DefaultStaleSweepInterval = 30 * time.Second

// WorkerConfig parameterises the worker. ProjectIDProvider returns
// the set of project ids the worker should consider on each tick —
// in single-tenant deployments this is "all projects with a pending
// job"; the multi-tenant cloud uses a tighter, lease-scoped variant.
// The default ProjectIDProvider hits the database for any project_id
// with at least one pending job.
//
// Jobs takes the concrete repository (not the interface) — the worker
// touches every method including ClaimNextPending / MarkFailed /
// RequeueStale, which are deliberately not on the handler-facing
// ValidationJobRepo interface. Mirrors the schemaindex.Worker pattern
// where workers bind to the concrete repo and unit tests inject
// mock concrete repos through this struct.
type WorkerConfig struct {
	Jobs              *database.ValidationJobRepository
	Runner            runner.Runner
	WorkerID          string
	PollInterval      time.Duration
	StaleAfter        time.Duration
	MaxAttempts       int
	StaleSweepEvery   time.Duration
	ProjectIDProvider func(ctx context.Context) ([]string, error)
}

// Worker claims at most one validation job per project per tick and
// runs it through the agent. Single-poll-loop per API process keeps
// the inflight map honest; in multi-replica deployments the partial-
// unique-on-active index in Mongo prevents two workers from claiming
// the same job, and ClaimNextPending is atomic.
type Worker struct {
	cfg WorkerConfig

	mu       sync.Mutex
	inflight map[string]context.CancelFunc // job_id → cancel fn
}

// New constructs a Worker with the supplied config. Validates
// dependencies so configuration errors surface at startup.
func New(cfg WorkerConfig) (*Worker, error) {
	if cfg.Jobs == nil {
		return nil, errors.New("validationjobs: Jobs repo is required")
	}
	if cfg.Runner == nil {
		return nil, errors.New("validationjobs: Runner is required")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.StaleAfter <= 0 {
		cfg.StaleAfter = DefaultStaleAfter
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultMaxAttempts
	}
	if cfg.StaleSweepEvery <= 0 {
		cfg.StaleSweepEvery = DefaultStaleSweepInterval
	}
	if cfg.WorkerID == "" {
		cfg.WorkerID = "validation-worker-local"
	}
	return &Worker{cfg: cfg, inflight: make(map[string]context.CancelFunc)}, nil
}

// Cancel signals an in-flight agent process to abort. The actual job
// state transition to "cancelled" happens via the handler's repo
// Cancel() call; this method only delivers the OS-level abort.
// Returns true when a cancel signal was delivered, false when the
// job wasn't in this worker's inflight map (e.g. handled by another
// API replica, or already terminal).
func (w *Worker) Cancel(jobID string) bool {
	w.mu.Lock()
	cancel, ok := w.inflight[jobID]
	w.mu.Unlock()
	if !ok {
		return false
	}
	cancel()
	apilog.WithField("job_id", jobID).Info("Validation job cancel signal delivered")
	return true
}

// IsRunning reports whether the worker is currently driving an agent
// process for the given job_id.
func (w *Worker) IsRunning(jobID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.inflight[jobID]
	return ok
}

func (w *Worker) register(jobID string, cancel context.CancelFunc) {
	w.mu.Lock()
	w.inflight[jobID] = cancel
	w.mu.Unlock()
}

func (w *Worker) deregister(jobID string) {
	w.mu.Lock()
	delete(w.inflight, jobID)
	w.mu.Unlock()
}

// Start runs the worker until ctx is cancelled. Blocking; intended
// to be launched from a goroutine in apiserver.Run.
func (w *Worker) Start(ctx context.Context) {
	apilog.WithFields(apilog.Fields{
		"poll_ms":     w.cfg.PollInterval.Milliseconds(),
		"stale_after": w.cfg.StaleAfter.String(),
		"worker_id":   w.cfg.WorkerID,
	}).Info("Validation-jobs worker started")

	// Recovery sweep on startup — any "running" rows from a previous
	// process incarnation get requeued so we don't strand work.
	w.staleSweep(ctx)

	poll := time.NewTicker(w.cfg.PollInterval)
	defer poll.Stop()
	sweep := time.NewTicker(w.cfg.StaleSweepEvery)
	defer sweep.Stop()

	for {
		select {
		case <-ctx.Done():
			apilog.Info("Validation-jobs worker stopping")
			return
		case <-poll.C:
			w.tick(ctx)
		case <-sweep.C:
			w.staleSweep(ctx)
		}
	}
}

// tick claims and runs at most one job per known project. Walking
// projects keeps a single greedy project (e.g. one with 20 queued
// docs after the user clicked through every insight in a discovery)
// from starving other projects' jobs. The ProjectIDProvider seam
// keeps the worker single-tenant-aware in OSS while letting the
// cloud control plane lease project IDs to specific workers.
func (w *Worker) tick(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if w.cfg.ProjectIDProvider == nil {
		return
	}
	projectIDs, err := w.cfg.ProjectIDProvider(ctx)
	if err != nil {
		apilog.WithError(err).Warn("validationjobs: project provider failed")
		return
	}
	for _, projectID := range projectIDs {
		if ctx.Err() != nil {
			return
		}
		w.tickOneProject(ctx, projectID)
	}
}

func (w *Worker) tickOneProject(ctx context.Context, projectID string) {
	job, err := w.cfg.Jobs.ClaimNextPending(ctx, projectID, w.cfg.WorkerID)
	if err != nil {
		apilog.WithFields(apilog.Fields{
			"project_id": projectID, "error": err.Error(),
		}).Warn("validationjobs: claim failed")
		return
	}
	if job == nil {
		return // idle queue for this project
	}
	// Run the agent in its own goroutine so this tick returns
	// immediately and the worker's stale-sweep ticker keeps cadence
	// while a long validation is in flight. Concurrency is
	// naturally bounded by the partial-unique-on-active index
	// (one active job per doc) + per-project sequential claim —
	// a caller cannot fan out more than (num_projects × queue_depth)
	// agents.
	go w.runClaimedJob(ctx, job)
}

func (w *Worker) runClaimedJob(ctx context.Context, job *models.ValidationJob) {
	logFields := apilog.Fields{
		"job_id":       job.ID,
		"project_id":   job.ProjectID,
		"discovery_id": job.DiscoveryID,
		"doc_kind":     job.DocKind,
		"doc_id":       job.DocID,
		"attempt":      job.Attempt,
	}
	apilog.WithFields(logFields).Info("Validation job claimed")

	runCtx, cancel := context.WithCancel(ctx)
	w.register(job.ID, cancel)
	defer func() {
		cancel()
		w.deregister(job.ID)
	}()

	err := w.cfg.Runner.RunValidateDoc(runCtx, runner.ValidateDocOptions{
		JobID:       job.ID,
		ProjectID:   job.ProjectID,
		DiscoveryID: job.DiscoveryID,
		DocKind:     job.DocKind,
		DocID:       job.DocID,
	})
	switch {
	case err == nil:
		// Agent process exited cleanly. The agent's own MarkCompleted
		// write is the source of truth; the worker only logs.
		apilog.WithFields(logFields).Info("Validation job agent completed")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// User-initiated cancel, or worker shutdown. The handler /
		// shutdown path is responsible for the row transition; we
		// don't double-write here.
		apilog.WithFields(logFields).Info("Validation job agent cancelled")
	default:
		// Agent process failed before it could write MarkFailed (e.g.
		// died before connecting to Mongo). Write the failure marker
		// from the worker so the dashboard doesn't sit on a stale
		// "running" row.
		mfCtx, mfCancel := context.WithTimeout(context.Background(), 5*time.Second)
		mfErr := w.cfg.Jobs.MarkFailed(mfCtx, job.ID, job.Attempt, err.Error())
		mfCancel()
		failFields := apilog.Fields{}
		for k, v := range logFields {
			failFields[k] = v
		}
		failFields["error"] = err.Error()
		if mfErr != nil {
			failFields["mark_failed_error"] = mfErr.Error()
		}
		apilog.WithFields(failFields).Warn("Validation job agent failed")
	}
}

// staleSweep walks running rows whose heartbeat has gone cold and
// either requeues them (under the retry budget) or marks them failed
// (over the retry budget). Uses the repo's RequeueStale method which
// performs both transitions atomically per row.
func (w *Worker) staleSweep(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	requeued, failed, err := w.cfg.Jobs.RequeueStale(ctx, w.cfg.StaleAfter, w.cfg.MaxAttempts)
	if err != nil {
		apilog.WithFields(apilog.Fields{"error": err.Error()}).Warn("validationjobs: stale sweep failed")
		return
	}
	if requeued > 0 || failed > 0 {
		apilog.WithFields(apilog.Fields{
			"requeued": requeued, "failed": failed,
		}).Info("Validation-jobs stale sweep")
	}
}

// DefaultProjectIDProvider returns the set of project_ids that
// currently have at least one pending job. Indexed via
// (project_id, status). When the dashboard isn't actively triggering
// validations this returns an empty slice and tick() short-circuits.
func DefaultProjectIDProvider(jobs *database.ValidationJobRepository) func(ctx context.Context) ([]string, error) {
	return jobs.DistinctProjectIDsWithPendingJobs
}
