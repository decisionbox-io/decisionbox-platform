package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/policy"
	"github.com/decisionbox-io/decisionbox/libs/go-common/telemetry"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/internal/discoverytrigger"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/internal/runner"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

func getEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// DiscoveriesHandler handles discovery result endpoints.
type DiscoveriesHandler struct {
	repo             database.DiscoveryRepo
	projectRepo      database.ProjectRepo
	runRepo          database.RunRepo
	debugLogRepo     database.DebugLogRepo
	discoveryLogRepo database.DiscoveryLogRepo
	runStepRepo      database.RunStepRepo
	// checkpointRepo backs the resume endpoint and the cancel-time purge of
	// a run's exploration checkpoints. May be nil — resume then refuses with
	// "checkpointing is not available in this deployment" and cancel skips
	// the purge, which is what a build without the agent's checkpoint
	// collection wants.
	checkpointRepo database.CheckpointRepo
	agentRunner    runner.Runner
}

// NewDiscoveriesHandler wires the handler. `debugLogRepo` may be nil — in
// that case the debug-logs endpoint returns an empty list (useful for tests
// and for builds that ship without the agent's debug log collection).
// discoveryLogRepo and runStepRepo back the paginated split-log endpoints
// (the embedded log fields are gone — see services/api/database/discovery_log_repo.go).
// The checkpoint repository is attached separately via WithCheckpoints.
func NewDiscoveriesHandler(
	repo database.DiscoveryRepo,
	projectRepo database.ProjectRepo,
	runRepo database.RunRepo,
	debugLogRepo database.DebugLogRepo,
	discoveryLogRepo database.DiscoveryLogRepo,
	runStepRepo database.RunStepRepo,
	r runner.Runner,
) *DiscoveriesHandler {
	return &DiscoveriesHandler{
		repo:             repo,
		projectRepo:      projectRepo,
		runRepo:          runRepo,
		debugLogRepo:     debugLogRepo,
		discoveryLogRepo: discoveryLogRepo,
		runStepRepo:      runStepRepo,
		agentRunner:      r,
	}
}

// WithCheckpoints attaches the exploration-checkpoint repository, enabling
// the resume endpoint and the cancel-time purge. Returns the same handler
// for chaining, matching how the other handlers take their optional
// dependencies.
//
// A builder rather than another positional parameter: the constructor
// already takes seven, and every one of the existing read-path tests passes
// nils through it — growing it again would mean editing twenty call sites to
// say nothing.
func (h *DiscoveriesHandler) WithCheckpoints(checkpointRepo database.CheckpointRepo) *DiscoveriesHandler {
	h.checkpointRepo = checkpointRepo
	return h
}

// List returns discovery results for a project.
// GET /api/v1/projects/{id}/discoveries
func (h *DiscoveriesHandler) List(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")

	p, err := h.projectRepo.GetByID(r.Context(), projectID)
	if err != nil || p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	results, err := h.repo.List(r.Context(), projectID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list discoveries: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, results)
}

// GetDiscoveryByID returns a specific discovery by its ID.
// GET /api/v1/discoveries/{id}
func (h *DiscoveriesHandler) GetDiscoveryByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	result, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get discovery: "+err.Error())
		return
	}
	if result == nil {
		writeError(w, http.StatusNotFound, "discovery not found")
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// GetLatest returns the most recent discovery for a project.
// GET /api/v1/projects/{id}/discoveries/latest
func (h *DiscoveriesHandler) GetLatest(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")

	result, err := h.repo.GetLatest(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get discovery: "+err.Error())
		return
	}
	if result == nil {
		writeError(w, http.StatusNotFound, "no discoveries found")
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// GetByDate returns a discovery for a specific date.
// GET /api/v1/projects/{id}/discoveries/{date}
func (h *DiscoveriesHandler) GetByDate(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	dateStr := r.PathValue("date")

	date, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid date format, use YYYY-MM-DD")
		return
	}

	result, err := h.repo.GetByDate(r.Context(), projectID, date)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get discovery: "+err.Error())
		return
	}
	if result == nil {
		writeError(w, http.StatusNotFound, "no discovery found for date "+dateStr)
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// TriggerDiscovery triggers a discovery run for a project.
// POST /api/v1/projects/{id}/discover
//
// This is a thin HTTP adapter over StartRun: it parses the optional
// request body and maps StartRun's typed errors onto status codes. All
// gating, run reservation, policy enforcement, and agent spawn live in
// StartRun so the HTTP endpoint and in-process callers (the
// discoverytrigger seam) share one implementation.
func (h *DiscoveriesHandler) TriggerDiscovery(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")

	// Parse optional request body.
	//
	// MinSteps is a pointer so the handler can distinguish three cases:
	//   nil        → field omitted, apply 60%-of-MaxSteps default
	//   *val == 0  → user explicitly disabled the floor
	//   *val  > 0  → user-provided floor
	var body struct {
		Areas    []string `json:"areas"`               // optional: run only these areas
		Effort   string   `json:"effort,omitempty"`    // optional: discovery intensity (lower/low/medium/high/higher)
		MaxSteps int      `json:"max_steps,omitempty"` // optional: override exploration steps (default 100)
		MinSteps *int     `json:"min_steps,omitempty"` // optional: reject premature completion (default 60% of max_steps)
	}
	_ = decodeJSON(r, &body) // body is optional

	res, err := h.StartRun(r.Context(), discoverytrigger.Options{
		ProjectID: projectID,
		Areas:     body.Areas,
		Effort:    body.Effort,
		MaxSteps:  body.MaxSteps,
		MinSteps:  body.MinSteps,
		Source:    "manual",
	})
	if err != nil {
		var alreadyRunning *discoverytrigger.AlreadyRunningError
		var conflict *discoverytrigger.ConflictError
		var invalid *discoverytrigger.InvalidParamsError
		switch {
		case errors.As(err, &alreadyRunning):
			writeJSON(w, http.StatusConflict, map[string]string{
				"status":  "already_running",
				"run_id":  alreadyRunning.RunID,
				"message": "A discovery is already running for this project",
			})
		case errors.As(err, &conflict):
			writeError(w, http.StatusConflict, conflict.Message)
		case errors.As(err, &invalid):
			writeError(w, http.StatusBadRequest, invalid.Message)
		case errors.Is(err, discoverytrigger.ErrProjectNotFound):
			writeError(w, http.StatusNotFound, "project not found")
		case writePolicyError(w, err):
			// writePolicyError wrote the structured 402/403 body.
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  res.Status,
		"run_id":  res.RunID,
		"message": "Discovery agent started",
	})
}

// gateProjectForRun applies every precondition a discovery run must satisfy:
// the datasource set can anchor an analysis, the project lifecycle is ready,
// and the schema index is built.
//
// Extracted so StartRun and ResumeRun cannot drift apart. They must not: a
// resume re-enters exploration, so it queries the warehouse and reads the
// schema index exactly as a fresh run does, and a resume path that skipped
// these checks would be a way to run discovery against a project the normal
// route refuses.
//
// Returns a *discoverytrigger.ConflictError (HTTP 409) naming what is wrong,
// or nil when the project may run.
func gateProjectForRun(p *models.Project) error {
	// Gate on the datasource set: discovery over sources that can only be
	// correlated against something else, with nothing to correlate against,
	// produces a confident restatement of what those sources' own reporting
	// already shows. That is worse than no run — it looks like analysis.
	//
	// Checked here as well as at configuration time because a project can
	// reach this state without passing through a route that refuses it: an
	// existing project predates the rule, and a datasource's provider can
	// change what it declares between one release and the next.
	if whs := p.EffectiveWarehouses(); len(whs) > 0 && !models.AnyAnchors(whs) {
		// Counted apart from the configuration refusals on purpose. Those are
		// the rule working; this one means a project REACHED a state no
		// configuration route should have allowed — it predates the rule, or a
		// provider changed what it declares between releases. A refusal here
		// is the signal that something upstream is missing a check.
		recordAnchoringRefusal(telemetry.AnchoringAtDiscoveryRun, p.ID, whs)
		return &discoverytrigger.ConflictError{Message: "this project has no data source that can carry an analysis on its own — discovery would only restate what those sources already report; add a system-of-record data source first"}
	}

	// Gate on lifecycle state. Discovery is only valid for projects
	// in the ready (or legacy-empty) state. Plugins may transition
	// projects into their own opaque states (e.g. while a
	// long-running setup flow is in progress) and own the
	// transition back to ready — discovery must refuse to run while
	// those states are active even though the schema index might
	// already be ready. A direct API call from a stale dashboard
	// or curl that bypassed the UI gate must not be able to start
	// the agent.
	if effectiveState := p.EffectiveState(); effectiveState != models.ProjectStateReady {
		return &discoverytrigger.ConflictError{Message: "project is in state \"" + effectiveState + "\" — discovery cannot run until the managing plugin transitions it to \"" + models.ProjectStateReady + "\""}
	}

	// Gate on schema-index lifecycle: discovery requires a ready
	// index. Empty status means the project was created before
	// schema indexing shipped and never migrated — treat it the
	// same as pending_indexing so the migration path kicks in on
	// first run. The dashboard polls /schema-index/status to tell
	// the user what to do next.
	switch p.SchemaIndexStatus {
	case models.SchemaIndexStatusReady:
		// ok — proceed
	case models.SchemaIndexStatusPendingIndexing, models.SchemaIndexStatusIndexing:
		return &discoverytrigger.ConflictError{Message: "schema index is not ready yet — poll /api/v1/projects/" + p.ID + "/schema-index/status"}
	case models.SchemaIndexStatusFailed:
		return &discoverytrigger.ConflictError{Message: "schema indexing failed: " + p.SchemaIndexError + " — click Retry indexing in project settings"}
	case models.SchemaIndexStatusNeedsReindex:
		return &discoverytrigger.ConflictError{Message: "schema cache was cleared; re-indexing is required before discovery — trigger POST /api/v1/projects/" + p.ID + "/reindex"}
	case models.SchemaIndexStatusCancelled:
		return &discoverytrigger.ConflictError{Message: "previous schema-indexing run was cancelled — trigger POST /api/v1/projects/" + p.ID + "/reindex to rebuild"}
	default:
		// empty status — pre-existing project not yet migrated
		return &discoverytrigger.ConflictError{Message: "project has not been indexed yet — trigger POST /api/v1/projects/" + p.ID + "/reindex first"}
	}
	return nil
}

// StartRun performs a discovery-run trigger: lifecycle/schema-index
// gating, run-record reservation, plan-policy enforcement, and agent
// spawn. It is the single implementation shared by the HTTP endpoint
// (TriggerDiscovery) and in-process callers reaching it through
// apiserver.TriggerDiscovery (registered via discoverytrigger.Register).
//
// On rejection it returns one of the discoverytrigger typed errors
// (ErrProjectNotFound, *ConflictError, *AlreadyRunningError,
// *InvalidParamsError), a *policy.PolicyError for plan denials, or a
// generic wrapped error for infrastructure failures.
func (h *DiscoveriesHandler) StartRun(ctx context.Context, opts discoverytrigger.Options) (discoverytrigger.Result, error) {
	p, err := h.projectRepo.GetByID(ctx, opts.ProjectID)
	if err != nil {
		// A lookup failure (e.g. a transient Mongo error) is distinct from
		// a genuinely missing project: return it as a generic error so
		// callers don't treat an infrastructure blip as a confirmed
		// deletion. ErrProjectNotFound is reserved for p == nil.
		return discoverytrigger.Result{}, fmt.Errorf("look up project: %w", err)
	}
	if p == nil {
		return discoverytrigger.Result{}, discoverytrigger.ErrProjectNotFound
	}

	// Every gate a run must clear. Shared with the resume path, which
	// re-enters exploration and so needs exactly the same guarantees — a
	// resumed run that re-queries a warehouse whose schema index was
	// cleared in the meantime would be worse than one that never started.
	if err := gateProjectForRun(p); err != nil {
		return discoverytrigger.Result{}, err
	}

	// An effort level (cloud's customer-facing intensity) resolves to a
	// max_steps budget that takes precedence over a raw MaxSteps — cloud never
	// exposes step counts. Self-hosted callers may still pass MaxSteps.
	if opts.Effort != "" {
		steps, ok := policy.StepsForEffort(opts.Effort)
		if !ok {
			return discoverytrigger.Result{}, &discoverytrigger.InvalidParamsError{Message: "invalid effort level: " + opts.Effort}
		}
		opts.MaxSteps = steps
	}

	// Resolve MaxSteps for the min-steps default computation below. The
	// agent CLI enforces its own default (100) when zero reaches it, so we
	// mirror that here to keep the on-the-wire default and the computed
	// min-steps default consistent.
	effectiveMaxSteps := opts.MaxSteps
	if effectiveMaxSteps <= 0 {
		effectiveMaxSteps = 100
	}

	// Compute MinSteps.
	// Omitted → default = floor(0.6 * max_steps). Reasoning-model discoveries
	// (Qwen3, DeepSeek-R1, GPT-OSS on Bedrock) terminated in 2-18 steps
	// before the min-steps floor existed; 60% is a conservative baseline
	// that still leaves headroom for genuinely short runs.
	// Explicit zero → user disabled the floor; forward as 0.
	// Negative or > max_steps → reject.
	var minSteps int
	if opts.MinSteps == nil {
		minSteps = (effectiveMaxSteps * 6) / 10
	} else {
		minSteps = *opts.MinSteps
		if minSteps < 0 {
			return discoverytrigger.Result{}, &discoverytrigger.InvalidParamsError{Message: "min_steps must be >= 0"}
		}
		if minSteps > effectiveMaxSteps {
			return discoverytrigger.Result{}, &discoverytrigger.InvalidParamsError{Message: fmt.Sprintf("min_steps (%d) cannot exceed max_steps (%d)", minSteps, effectiveMaxSteps)}
		}
	}

	// Create a run record first — we need a stable runID for the policy
	// reservation and the repo-level "already running" invariant is
	// re-enforced here (Create only returns an ID; race is closed by
	// the policy reservation on cloud and by the runRepo uniqueness on
	// self-hosted).
	source := opts.Source
	if source == "" {
		source = "manual"
	}

	// The run's own parameters go on the document. Nothing recorded them
	// before, so a resumed run spawned from the document alone would have
	// silently taken the agent's defaults instead of the budget chosen here.
	runID, err := h.runRepo.Create(ctx, opts.ProjectID, models.RunParams{
		MaxSteps: opts.MaxSteps,
		MinSteps: minSteps,
		Areas:    opts.Areas,
		Effort:   opts.Effort,
		Source:   source,
	})
	if err != nil {
		return discoverytrigger.Result{}, fmt.Errorf("failed to create run: %w", err)
	}

	apilog.WithFields(apilog.Fields{
		"project_id": opts.ProjectID, "run_id": runID, "trigger_source": source,
	}).Info("Starting discovery run")

	// Plan-gate: concurrent-runs-per-project AND runs-per-period. The
	// self-hosted NoopChecker allows everything; the cloud plugin
	// atomically reserves both counters in a single round-trip. On
	// self-hosted we also keep the repo-level "already running" check
	// below so the OSS UX does not regress.
	//
	// The repo-level overlap check also runs when the caller sets
	// SkipIfRunning (the scheduler), because not every registered checker
	// enforces per-project concurrency: the enterprise LicenseChecker is
	// advisory and returns success, so without this an in-process trigger
	// would have no overlap guarantee at all. Cloud's manual path leaves
	// SkipIfRunning false and relies on its atomic reservation, so this
	// adds no extra query (and no 1-per-project cap) there.
	ck := policy.GetChecker()
	_, isNoop := ck.(policy.NoopChecker)
	if opts.SkipIfRunning || isNoop {
		running, _ := h.runRepo.GetRunningByProject(ctx, opts.ProjectID)
		if running != nil && running.ID != runID {
			if err := h.runRepo.Cancel(ctx, runID); err != nil {
				apilog.WithError(err).Warn("failed to clean up runID reserved before already-running check")
			}
			return discoverytrigger.Result{}, &discoverytrigger.AlreadyRunningError{RunID: running.ID}
		}
	}

	res, err := ck.CheckStartDiscoveryRun(ctx, "", opts.ProjectID, runID)
	if err != nil {
		if failErr := h.runRepo.Fail(ctx, runID, "plan denied: "+err.Error()); failErr != nil {
			apilog.WithError(failErr).Warn("failed to mark policy-denied run as failed")
		}
		// Return the (policy) error verbatim so the HTTP adapter can
		// render the structured upgrade body via writePolicyError.
		return discoverytrigger.Result{}, err
	}

	reservationID := ""
	if res != nil {
		reservationID = res.ID
	}
	if reservationID != "" {
		if err := h.runRepo.SetPolicyReservationID(ctx, runID, reservationID); err != nil {
			apilog.WithError(err).Warn("failed to persist policy reservation id on run; cancel/crash recovery will fall through to sweeper")
		}
	}

	// Meter the run at its effort price. Free on self-hosted (no metering
	// checker); on cloud this debits the plan's credit balance and blocks
	// (typed *PolicyError → HTTP 402) when it is exhausted. The runID is the
	// idempotency + refund handle. On a block, roll back the cap reservation
	// and fail the run so nothing is left half-reserved.
	// Metering prices discovery by effort; default to medium when the caller
	// didn't pick one (a cloud client always sends an effort, but this keeps
	// the debit well-formed regardless). No-op on self-hosted.
	chargeEffort := opts.Effort
	if chargeEffort == "" {
		chargeEffort = policy.DefaultEffort
	}
	if _, err := policy.ChargeIfMetered(ctx, "", policy.Operation{
		Name:      policy.OpDiscoveryRun,
		Effort:    chargeEffort,
		Reference: runID,
	}); err != nil {
		if reservationID != "" {
			if relErr := ck.Release(ctx, reservationID); relErr != nil {
				apilog.WithError(relErr).Warn("failed to release reservation after metering block")
			}
		}
		if failErr := h.runRepo.Fail(ctx, runID, "metering denied: "+err.Error()); failErr != nil {
			apilog.WithError(failErr).Warn("failed to mark metering-denied run as failed")
		}
		return discoverytrigger.Result{}, err
	}

	// Spawn the agent via the configured runner (subprocess, docker, or K8s Job)
	runErr := h.agentRunner.Run(ctx, runner.RunOptions{
		ProjectID: opts.ProjectID,
		RunID:     runID,
		Areas:     opts.Areas,
		MaxSteps:  opts.MaxSteps,
		MinSteps:  minSteps,
		OnFailure: func(failedRunID string, errMsg string) {
			apilog.WithFields(apilog.Fields{
				"run_id": failedRunID, "error": errMsg,
			}).Error("Agent failed — updating run status")
			// Guarded on the attempt this callback belongs to. The watcher
			// behind it outlives its attempt, so once a run can be resumed
			// an unguarded Fail here could mark a LIVE resumed attempt
			// failed on behalf of the dead one. A fresh run is attempt 1.
			//
			// The guard governs the STATUS WRITE only; the reservation is
			// confirmed either way. A write that does not apply says
			// nothing about whether the run is over — the ordinary case is
			// that the agent wrote its own `failed` before the watcher
			// noticed the dead workload, so `applied` is false and the run
			// is very much finished. Returning here would hold the
			// concurrent-run slot until the periodic confirmer repaired it,
			// which on cloud means an ordinary in-agent failure temporarily
			// blocking the project's next run.
			//
			// In the one case where the attempt really has moved on, the
			// resume that moved it already confirmed this same reservation,
			// so confirming again is a duplicate the control plane already
			// tolerates — the background confirmer does exactly that — and
			// both outcomes are a failure, so the aggregate does not change.
			if applied, err := h.runRepo.FailAttempt(context.Background(), failedRunID, 1, errMsg); err != nil {
				apilog.WithError(err).Error("failed to mark run as failed")
			} else if !applied {
				apilog.WithFields(apilog.Fields{
					"run_id": failedRunID, "attempt": 1,
				}).Info("failure callback did not change the run status; it is already terminal or on a later attempt")
			}
			if reservationID != "" {
				if err := policy.GetChecker().ConfirmDiscoveryRunEnded(context.Background(), reservationID, policy.RunOutcome{
					Status:  "failure",
					EndedAt: time.Now().UTC(),
					Error:   errMsg,
				}); err != nil {
					apilog.WithError(err).Warn("failed to confirm run ended to policy checker")
				}
			}
		},
	})
	if runErr != nil {
		// Detached from the request for the reason the resume path detaches
		// its post-flip writes: the run document already exists, so a
		// client that disconnected while the spawn was failing would
		// otherwise leave it `pending` for ever — not terminal, so not
		// resumable, and blocking the project's concurrency until the API
		// restarts. The refund and the reservation release are on the same
		// context because they are the same kind of write: owed to the
		// operator whether or not anyone is still holding the connection.
		//
		// Pre-existing rather than introduced here, and fixed alongside the
		// resume path because it is the identical hole one function over.
		withCleanup(ctx, func(ctx context.Context) {
			if err := h.runRepo.Fail(ctx, runID, "failed to start: "+runErr.Error()); err != nil {
				apilog.WithError(err).Error("failed to mark run as failed")
			}
		})
		// The agent never launched — a defined SYSTEM failure, so refund the
		// run's metered charge (no-op on self-hosted / when nothing was
		// charged; idempotent on cloud). Its own budget: a slow Fail above
		// must not cost the operator the refund.
		withCleanup(ctx, func(ctx context.Context) { policy.RefundIfMetered(ctx, "", runID) })
		if reservationID != "" {
			withCleanup(ctx, func(ctx context.Context) {
				if relErr := ck.Release(ctx, reservationID); relErr != nil {
					apilog.WithError(relErr).Warn("failed to release discovery-run reservation after agent spawn failed")
				} else if err := h.runRepo.ClearPolicyReservationID(ctx, runID); err != nil {
					apilog.WithError(err).Warn("released discovery-run reservation after agent spawn failed, but failed to clear persisted reservation id on run (post-completion confirmer will retry Confirm on an already-Released reservation until the doc TTLs)")
				}
			})
		}
		return discoverytrigger.Result{}, fmt.Errorf("failed to start agent: %w", runErr)
	}

	return discoverytrigger.Result{RunID: runID, Status: "started"}, nil
}

// GetStatus returns the live discovery status for a project.
// GET /api/v1/projects/{id}/status
func (h *DiscoveriesHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")

	p, err := h.projectRepo.GetByID(r.Context(), projectID)
	if err != nil || p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	// Get the latest run (for live status)
	latestRun, _ := h.runRepo.GetLatestByProject(r.Context(), projectID)

	status := map[string]interface{}{
		"project_id": projectID,
	}

	if latestRun != nil {
		status["run"] = latestRun
	}

	// Also include latest completed discovery stats
	latest, _ := h.repo.GetLatest(r.Context(), projectID)
	if latest != nil {
		status["last_discovery"] = map[string]interface{}{
			"date":           latest.DiscoveryDate,
			"insights_count": len(latest.Insights),
			"total_steps":    latest.TotalSteps,
		}
	}

	writeJSON(w, http.StatusOK, status)
}

// GetRun returns a specific discovery run by ID.
// GET /api/v1/runs/{runId}
func (h *DiscoveriesHandler) GetRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runId")

	run, err := h.runRepo.GetByID(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get run: "+err.Error())
		return
	}
	if run == nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}

	writeJSON(w, http.StatusOK, run)
}

// cleanupTimeout bounds a detached tail-end write. Generous enough for a
// Mongo round-trip under load, short enough that a wedged control plane
// cannot pin a goroutine indefinitely.
//
// A var rather than a const only so tests can shrink it; nothing in
// production reassigns it.
var cleanupTimeout = 30 * time.Second

// cleanupContext detaches a request's tail-end writes from the request's own
// cancellation, the way the orchestrator's persistContext does for the
// agent's.
//
// net/http cancels the request context the moment a client disconnects, and
// some of this handler's writes are what put a run back into a state anyone
// can act on. Running those on the request context means a browser tab
// closing at the wrong moment decides whether a run is recoverable: the
// stand-down never lands, and the run sits `running` with no agent behind
// it — not terminal, so not resumable, and visible to the concurrency check
// that then blocks every new run for the project until the API restarts and
// its startup sweep clears it.
//
// Deliberately NOT used for the spawn itself. A client that disconnected is
// not waiting for a 202, so standing the attempt down is the least
// surprising outcome and leaves the run resumable; detaching the spawn would
// instead launch a run nobody is listening for, which is a behaviour change
// rather than a fix.
func cleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), cleanupTimeout)
}

// withCleanup runs one tail-end operation on its own detached, freshly
// budgeted context.
//
// Per operation, not per block: a shared deadline lets the first slow call
// eat the allowance of everything after it, and what comes after is usually
// the write that recovers from the slow one. Each call gets its own 30s
// because each is independently worth that wait.
func withCleanup(parent context.Context, fn func(ctx context.Context)) {
	ctx, cancel := cleanupContext(parent)
	defer cancel()
	fn(ctx)
}

// standDownResume puts a resumed attempt back to `failed` after the flip has
// landed but before any agent was spawned for it.
//
// Recorded as a real failure rather than rolled back silently: the run gets
// an accurate reason, its checkpoints are untouched so it stays resumable,
// and the attempt counter keeps its increment — which is correct, because
// this attempt happened and it did not start. A failure to write that is
// logged and nothing more; the caller is already returning an error, and
// the run is no worse off than before the flip.
func (h *DiscoveriesHandler) standDownResume(parent context.Context, runID string, attempt int, reason string) {
	// Its own budget, never a caller's leftovers. This function exists to
	// make one write land, and the commonest reason it is called is that
	// something else just timed out — so sharing that deadline would mean
	// the stand-down fails exactly when it is needed most, leaving the run
	// `running` with no agent behind it. cleanupContext strips the parent's
	// deadline along with its cancellation, so an exhausted one in gets a
	// fresh one out.
	ctx, cancel := cleanupContext(parent)
	defer cancel()

	if _, err := h.runRepo.FailAttempt(ctx, runID, attempt, reason); err != nil {
		apilog.WithFields(apilog.Fields{
			"run_id": runID, "attempt": attempt, "error": err.Error(),
		}).Error("failed to stand down a resume that did not start")
	}
}

// ResumeRun restarts a failed discovery run from its last exploration
// checkpoint, re-entering the SAME run id.
// POST /api/v1/runs/{runId}/resume
//
// Exploration is where a discovery run's cost sits — N agentic LLM calls and
// N warehouse queries — and before checkpointing existed a process that died
// anywhere before the persistence tail lost all of it with no way back in.
// This is the way back in: the agent replays the steps already executed and
// continues from the next one, and a run that died after exploration
// finished goes straight to analysis without a single query.
//
// Nothing here meters, charges, refunds, or opens a policy reservation, and
// that is a design decision rather than an omission:
//
//   - The run's metered charge is keyed on its runID, and a resume re-enters
//     the same runID. So resume is free BY CONSTRUCTION; there is no second
//     debit to suppress and no refund path to leak through.
//   - Opening a CheckStartDiscoveryRun reservation would consume another
//     runs-per-period slot, which is a hidden charge for work already paid
//     for. The per-project concurrency invariant is instead enforced at the
//     repository level below, unconditionally.
//
// The cost of that choice, stated plainly because it is not fully closable
// from this side: a resumed run holds NO reservation, so it is invisible to
// anything that enforces concurrency through reservations. The repo-level
// check below (and its re-check after the flip) catches a competing run that
// is already visible, which covers resume-versus-resume and the self-hosted
// path. It does not serialise a resume against a FRESH trigger on a
// deployment whose per-project concurrency is governed by the reservation
// rather than by that check — the fresh trigger's reservation has nothing to
// see.
//
// Closing it needs a CheckResumeDiscoveryRun on the policy checker that
// reserves concurrency WITHOUT consuming a runs-per-period slot — a new seam
// in a shared interface, and a pricing decision, because it would make a
// resume refusable on plan grounds for a run that is already paid for.
//
// Reviewed and deliberately left as it stands, to be revisited with the v4
// (operator Pause) increment. `attempt` is persisted so a future per-attempt
// price can key on runID:attempt rather than silently no-op'ing against the
// original charge, and the seam above is where it goes.
func (h *DiscoveriesHandler) ResumeRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runId")
	ctx := r.Context()

	run, err := h.runRepo.GetByID(ctx, runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get run: "+err.Error())
		return
	}
	if run == nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}

	// `failed` is the only resumable status, and the message says what the
	// run actually is so a stale dashboard's user is not left guessing.
	// `cancelled` is a deliberate hard kill and stays terminal.
	if run.Status != "failed" {
		writeError(w, http.StatusConflict, "run is not resumable (status: "+run.Status+") — only a failed run can be resumed")
		return
	}

	p, err := h.projectRepo.GetByID(ctx, run.ProjectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to look up project: "+err.Error())
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	// The same preconditions a fresh run must clear. A resume re-enters
	// exploration, so it needs them all — notably a ready schema index.
	if gateErr := gateProjectForRun(p); gateErr != nil {
		writeError(w, http.StatusConflict, gateErr.Error())
		return
	}

	// Nothing to resume from is a real answer, not a bug: checkpoints are
	// bounded by DISCOVERY_CHECKPOINT_RETENTION, and a run that died before
	// its first step never wrote one.
	if h.checkpointRepo == nil {
		writeError(w, http.StatusConflict, "no checkpoint to resume from — checkpointing is not available in this deployment")
		return
	}
	prefixLen, explorationComplete, err := h.checkpointRepo.ResumeState(ctx, runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read checkpoints: "+err.Error())
		return
	}
	if prefixLen == 0 && !explorationComplete {
		writeError(w, http.StatusConflict, "no checkpoint to resume from — it expired or was never written; start a new run instead")
		return
	}

	// One active run per project. Applied unconditionally here, rather than
	// only under the self-hosted no-op checker as the start path does,
	// because resume opens no policy reservation — so this is the only thing
	// bounding concurrency for the project.
	running, err := h.runRepo.GetOtherRunningByProject(ctx, run.ProjectID, runID)
	if err != nil {
		// Fail closed. Nothing has been mutated yet, so refusing costs the
		// operator a retry; proceeding would spend a whole run to find out.
		writeError(w, http.StatusInternalServerError, "failed to check for a competing run: "+err.Error())
		return
	}
	if running != nil {
		// writeError, not writeJSON: the dashboard's request helper reads
		// only the top-level `error` on a non-2xx, so a body under `data`
		// would surface as a bare "API error: 409" and lose the one detail
		// that makes this actionable — which run is in the way.
		writeError(w, http.StatusConflict,
			"a discovery run is already in progress for this project (run "+running.ID+")")
		return
	}

	// The atomic flip. Its filter is the race guard: a double-clicked Resume
	// matches nothing the second time, so two agents can never be spawned
	// onto one run.
	resumed, err := h.runRepo.BeginResume(ctx, runID)
	if err != nil {
		if errors.Is(err, database.ErrNoResumableRun) {
			writeError(w, http.StatusConflict, "run is no longer resumable — another request got there first")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to begin resume: "+err.Error())
		return
	}

	// Past the flip the run is `running`, so everything that can put it back
	// runs detached from the request — see cleanupContext. Before the flip
	// nothing had been mutated and a cancelled request simply left the run
	// alone, which is why this starts here and not at the top.
	//
	// One budget PER operation, deliberately, rather than one shared across
	// them: a shared deadline makes the first slow call eat the allowance of
	// the write that is supposed to recover from it.

	// Re-check for a competing run now that this one is visibly `running`.
	//
	// The check above the flip is a pre-check, and two requests can both pass
	// it before either write is visible — a resume and a fresh trigger, say,
	// which do not serialise against each other at all: the fresh trigger's
	// plan reservation does not see a resume, because resume opens none.
	// Re-checking after the flip means at least one of the two sees the
	// other, which is what the one-active-run-per-project invariant needs.
	//
	// Excluding this run explicitly: it is `running` now, so a query that did
	// not exclude it could hand it back and report no competitor.
	//
	// An ERROR stands the attempt down just as losing the race does, rather
	// than being swallowed into "no competitor". This check is the ONLY
	// thing bounding concurrency for a resume — no reservation is opened, so
	// nothing sits behind it — which makes "I could not tell" equivalent to
	// "do not start". Both stand-downs are mild: see standDownResume.
	recheckCtx, cancelRecheck := cleanupContext(ctx)
	running, err = h.runRepo.GetOtherRunningByProject(recheckCtx, run.ProjectID, runID)
	cancelRecheck()
	if err != nil {
		h.standDownResume(ctx, runID, resumed.Attempt,
			"resume aborted: could not verify that no other discovery run is active for this project")
		writeError(w, http.StatusInternalServerError, "failed to check for a competing run: "+err.Error())
		return
	}
	if running != nil {
		h.standDownResume(ctx, runID, resumed.Attempt,
			"resume aborted: another discovery run for this project started at the same time")
		writeError(w, http.StatusConflict,
			"a discovery run is already in progress for this project (run "+running.ID+")")
		return
	}

	apilog.WithFields(apilog.Fields{
		"project_id": run.ProjectID, "run_id": runID,
		"attempt": resumed.Attempt, "replayable_steps": prefixLen,
		"exploration_complete": explorationComplete,
	}).Info("Resuming discovery run")

	// End the previous attempt's plan reservation.
	//
	// Confirm rather than Release: the period counter was consumed when the
	// run started and a resume does not refund it — the concurrent-runs
	// counter is what needs to come down. It has to be ended at all because
	// resume opens NO reservation of its own: left in place, the resumed
	// attempt would be reported against one it never made, by the
	// post-completion confirmer, with its own outcome.
	//
	// Confirm FIRST, then clear the id — never the other way round. The id is
	// the only handle anyone has on the reservation, so clearing it before
	// the confirm lands would turn a crash in between into a leaked
	// concurrent-run slot with nothing left to reconcile from. Same order
	// StartRun uses for Release.
	//
	// Both halves are best-effort: a reservation that cannot be ended is an
	// accounting problem for the control plane, and refusing to resume the
	// run over it would be the wrong trade. Leaving the id in place on
	// failure is deliberate — the background confirmer retries from it.
	if run.PolicyReservationID != "" {
		resCtx, cancelRes := cleanupContext(ctx)
		defer cancelRes()
		if err := policy.GetChecker().ConfirmDiscoveryRunEnded(resCtx, run.PolicyReservationID, policy.RunOutcome{
			Status:  "failure",
			EndedAt: time.Now().UTC(),
			Error:   models.SupersededByResumeReason,
		}); err != nil {
			apilog.WithError(err).Warn("failed to confirm the superseded attempt's reservation; leaving its id on the run so the confirmer can retry")
		} else if err := h.runRepo.ClearPolicyReservationID(resCtx, runID); err != nil {
			apilog.WithError(err).Warn("confirmed the superseded attempt's reservation but failed to clear its id; the confirmer will retry an already-confirmed reservation until the run ages out")
		}
	}

	// Replay the run's OWN parameters, not the current defaults. Runs
	// created before these were persisted read as zero, which the agent
	// resolves to its documented defaults — the same thing that happens
	// today for a run whose parameters were never recorded anywhere.
	// Captured for the failure callback below, which may fire long after
	// this attempt has been superseded by another resume.
	attempt := resumed.Attempt
	runErr := h.agentRunner.Run(ctx, runner.RunOptions{
		ProjectID: run.ProjectID,
		RunID:     runID,
		Areas:     resumed.Areas,
		MaxSteps:  resumed.MaxSteps,
		MinSteps:  resumed.MinSteps,
		Resume:    true,
		Attempt:   attempt,
		OnFailure: func(failedRunID string, errMsg string) {
			apilog.WithFields(apilog.Fields{
				"run_id": failedRunID, "error": errMsg, "attempt": attempt,
			}).Error("Resumed agent failed — updating run status")
			// Guarded on THIS attempt. Without it, this attempt's watcher
			// could outlive it and mark the NEXT resume failed — the same
			// race on every subsequent attempt.
			if applied, err := h.runRepo.FailAttempt(context.Background(), failedRunID, attempt, errMsg); err != nil {
				apilog.WithError(err).Error("failed to mark resumed run as failed")
			} else if !applied {
				apilog.WithFields(apilog.Fields{
					"run_id": failedRunID, "attempt": attempt,
				}).Info("ignored a failure callback for an attempt that is no longer the live one")
			}
		},
	})
	if runErr != nil {
		// Back to `failed`, which leaves the run resumable again — the
		// checkpoints are untouched, so a spawn failure costs nothing but
		// the attempt counter.
		failCtx, cancelFail := cleanupContext(ctx)
		defer cancelFail()
		if err := h.runRepo.Fail(failCtx, runID, "failed to start resume: "+runErr.Error()); err != nil {
			apilog.WithError(err).Error("failed to mark run as failed after a resume spawn failure")
		}
		writeError(w, http.StatusInternalServerError, "failed to start agent: "+runErr.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"status":  "resumed",
		"run_id":  runID,
		"attempt": resumed.Attempt,
	})
}

// CancelRun cancels a running discovery.
// DELETE /api/v1/runs/{runId}
func (h *DiscoveriesHandler) CancelRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runId")

	run, err := h.runRepo.GetByID(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get run: "+err.Error())
		return
	}
	if run == nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}

	if run.Status != "running" && run.Status != "pending" {
		writeError(w, http.StatusBadRequest, "run is not active (status: "+run.Status+")")
		return
	}

	// Cancel via runner (kills subprocess or deletes K8s Job)
	if err := h.agentRunner.Cancel(r.Context(), runID); err != nil {
		apilog.WithFields(apilog.Fields{"run_id": runID, "error": err.Error()}).Warn("Runner cancel returned error")
	}

	// Mark as cancelled in MongoDB
	if err := h.runRepo.Cancel(r.Context(), runID); err != nil {
		apilog.WithError(err).Warn("failed to cancel run in database")
	}

	// Cancellation is a deliberate hard kill and stays terminal, so the
	// checkpoints have no one left to serve. Dropped eagerly rather than
	// left to the retention TTL, both to reclaim the rows and so the
	// boot-time orphan sweep stops treating the run as live and keeping its
	// per-run vector collection alive. Best-effort: the TTL is the backstop.
	if h.checkpointRepo != nil {
		if deleted, err := h.checkpointRepo.DeleteByRun(r.Context(), runID); err != nil {
			apilog.WithError(err).Warn("failed to delete checkpoints of a cancelled run; the retention TTL will reclaim them")
		} else if deleted > 0 {
			apilog.WithFields(apilog.Fields{"run_id": runID, "deleted": deleted}).Info("deleted the checkpoints of a cancelled run")
		}
	}

	// Confirm the policy reservation ended. We call Confirm rather than
	// Release so the period counter (already incremented when the run
	// started) stays consumed — cancellation does not refund the run
	// budget. The concurrent-runs counter decrements. Noop is a no-op.
	if run.PolicyReservationID != "" {
		// A reservation can still be here because it belongs to an attempt a
		// resume superseded and that confirm failed — resume opens none of
		// its own. Closing it as `cancelled` would record THIS cancellation
		// against the dead attempt's reservation; the same misattribution
		// the background confirmer avoids, and it has to be avoided in both
		// places or the outcome depends on which one gets there first.
		cancelOutcome := policy.RunOutcome{
			Status:  "cancelled",
			EndedAt: time.Now().UTC(),
		}
		if run.ReservationBelongsToASupersededAttempt() {
			cancelOutcome.Status = "failure"
			cancelOutcome.Error = models.SupersededByResumeReason
		}
		if err := policy.GetChecker().ConfirmDiscoveryRunEnded(r.Context(), run.PolicyReservationID, cancelOutcome); err != nil {
			apilog.WithError(err).Warn("failed to confirm cancelled run to policy checker")
		}
	}

	apilog.WithField("run_id", runID).Info("Discovery run cancelled")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "cancelled",
		"message": "Run cancelled",
	})
}

// GetDebugLogs streams the agent's debug log entries for a single run. The
// dashboard polls this endpoint every few seconds while a run is active to
// show a live view of what the agent is doing (LLM calls, SQL executions,
// retries, errors). The response is deliberately a lean projection — it
// does NOT include full LLM prompts, raw query result rows, or analysis
// input/output blobs. Those stay in Mongo.
//
// GET /api/v1/runs/{runId}/debug-logs?since=<RFC3339>&limit=<n>
//
//   - `since` (optional): RFC3339 timestamp. Only entries created strictly
//     after it are returned. The client passes the `created_at` of the most
//     recent entry it has already rendered, so polling becomes idempotent.
//   - `limit` (optional): max rows. Defaults to 200, capped at 1000.
func (h *DiscoveriesHandler) GetDebugLogs(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runId")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "runId is required")
		return
	}

	if h.debugLogRepo == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	var since time.Time
	if s := r.URL.Query().Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid 'since' timestamp (expected RFC3339): "+err.Error())
			return
		}
		since = t
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit > 1000 {
		limit = 1000
	}

	entries, err := h.debugLogRepo.ListByRun(r.Context(), runID, since, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list debug logs: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, entries)
}

// maxExplorationStepsPerRequest caps how many exploration step rows a
// single GET /exploration-steps response may carry. Each row holds a
// full LLM request/response pair plus query results — the whole point
// of the split is to keep individual responses bounded.
const maxExplorationStepsPerRequest = 1000

// maxRunStepsPerRequest caps how many live run-step rows a single GET
// /runs/{runId}/steps response may carry. Run-step docs are smaller than
// exploration steps (no LLM dialog), so the cap is higher — but we still
// clamp on missing/zero/negative limits so a long-running discovery
// can't surface its full history in one shot.
const maxRunStepsPerRequest = 5000

// ListExplorationSteps returns the per-step exploration log for a single
// discovery. Backed by the discovery_exploration_steps collection (split
// out of the discoveries doc to dodge the 16MB BSON limit).
//
// GET /api/v1/discoveries/{id}/exploration-steps?limit=<n>
//
// `limit` defaults to maxExplorationStepsPerRequest and is clamped to
// the same value — exploration step rows are large (full LLM dialog +
// query results), and an unbounded request defeats the purpose of the
// split.
func (h *DiscoveriesHandler) ListExplorationSteps(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "discovery id is required")
		return
	}
	if h.discoveryLogRepo == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > maxExplorationStepsPerRequest {
		limit = maxExplorationStepsPerRequest
	}
	steps, err := h.discoveryLogRepo.ListExplorationSteps(r.Context(), id, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list exploration steps: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, steps)
}

// ListAnalysisSteps returns the per-area analysis log for a discovery.
//
// GET /api/v1/discoveries/{id}/analysis-steps
func (h *DiscoveriesHandler) ListAnalysisSteps(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "discovery id is required")
		return
	}
	if h.discoveryLogRepo == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	steps, err := h.discoveryLogRepo.ListAnalysisSteps(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list analysis steps: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, steps)
}

// ListValidationResults returns the warehouse-verification rows for a
// discovery.
//
// GET /api/v1/discoveries/{id}/validation-results
func (h *DiscoveriesHandler) ListValidationResults(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "discovery id is required")
		return
	}
	if h.discoveryLogRepo == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	results, err := h.discoveryLogRepo.ListValidationResults(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list validation results: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, results)
}

// GetRecommendationLog returns the recommendation-phase summary row for a
// discovery (or 404 when the run produced no recommendations).
//
// GET /api/v1/discoveries/{id}/recommendation-log
func (h *DiscoveriesHandler) GetRecommendationLog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "discovery id is required")
		return
	}
	if h.discoveryLogRepo == nil {
		writeError(w, http.StatusNotFound, "recommendation log not found")
		return
	}
	entry, err := h.discoveryLogRepo.GetRecommendationLog(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recommendation log: "+err.Error())
		return
	}
	if entry == nil {
		writeError(w, http.StatusNotFound, "recommendation log not found")
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// ListRunSteps returns the live run-step log for a discovery run with an
// opaque cursor (`since` = last row's `id`) for streaming polls. Replaces
// the embedded `steps` array that previously lived on the
// discovery_runs document.
//
// GET /api/v1/runs/{runId}/steps?since=<id>&limit=<n>
//
// `since` is the `id` field of the last row the caller has already
// rendered; the dashboard treats it as opaque and just echoes it back.
// See run_step_repo.go for why ObjectID, not timestamp — ms-precision
// timestamp cursors silently drop colliding rows.
func (h *DiscoveriesHandler) ListRunSteps(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runId")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "runId is required")
		return
	}
	if h.runStepRepo == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	sinceID := r.URL.Query().Get("since")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > maxRunStepsPerRequest {
		limit = maxRunStepsPerRequest
	}
	steps, err := h.runStepRepo.ListByRun(r.Context(), runID, sinceID, limit)
	if err != nil {
		if errors.Is(err, database.ErrInvalidCursor) {
			writeError(w, http.StatusBadRequest, "invalid 'since' cursor (expected an opaque id from a prior response)")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to list run steps: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, steps)
}
