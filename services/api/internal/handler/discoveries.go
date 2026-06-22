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
	repo            database.DiscoveryRepo
	projectRepo     database.ProjectRepo
	runRepo         database.RunRepo
	debugLogRepo    database.DebugLogRepo
	discoveryLogRepo database.DiscoveryLogRepo
	runStepRepo     database.RunStepRepo
	agentRunner     runner.Runner
}

// NewDiscoveriesHandler wires the handler. `debugLogRepo` may be nil — in
// that case the debug-logs endpoint returns an empty list (useful for tests
// and for builds that ship without the agent's debug log collection).
// discoveryLogRepo and runStepRepo back the paginated split-log endpoints
// (the embedded log fields are gone — see services/api/database/discovery_log_repo.go).
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
		MaxSteps int      `json:"max_steps,omitempty"` // optional: override exploration steps (default 100)
		MinSteps *int     `json:"min_steps,omitempty"` // optional: reject premature completion (default 60% of max_steps)
	}
	_ = decodeJSON(r, &body) // body is optional

	res, err := h.StartRun(r.Context(), discoverytrigger.Options{
		ProjectID: projectID,
		Areas:     body.Areas,
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
		return discoverytrigger.Result{}, &discoverytrigger.ConflictError{Message: "project is in state \"" + effectiveState + "\" — discovery cannot run until the managing plugin transitions it to \"" + models.ProjectStateReady + "\""}
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
		return discoverytrigger.Result{}, &discoverytrigger.ConflictError{Message: "schema index is not ready yet — poll /api/v1/projects/" + opts.ProjectID + "/schema-index/status"}
	case models.SchemaIndexStatusFailed:
		return discoverytrigger.Result{}, &discoverytrigger.ConflictError{Message: "schema indexing failed: " + p.SchemaIndexError + " — click Retry indexing in project settings"}
	case models.SchemaIndexStatusNeedsReindex:
		return discoverytrigger.Result{}, &discoverytrigger.ConflictError{Message: "schema cache was cleared; re-indexing is required before discovery — trigger POST /api/v1/projects/" + opts.ProjectID + "/reindex"}
	case models.SchemaIndexStatusCancelled:
		return discoverytrigger.Result{}, &discoverytrigger.ConflictError{Message: "previous schema-indexing run was cancelled — trigger POST /api/v1/projects/" + opts.ProjectID + "/reindex to rebuild"}
	default:
		// empty status — pre-existing project not yet migrated
		return discoverytrigger.Result{}, &discoverytrigger.ConflictError{Message: "project has not been indexed yet — trigger POST /api/v1/projects/" + opts.ProjectID + "/reindex first"}
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
	runID, err := h.runRepo.Create(ctx, opts.ProjectID)
	if err != nil {
		return discoverytrigger.Result{}, fmt.Errorf("failed to create run: %w", err)
	}

	source := opts.Source
	if source == "" {
		source = "manual"
	}
	apilog.WithFields(apilog.Fields{
		"project_id": opts.ProjectID, "run_id": runID, "trigger_source": source,
	}).Info("Starting discovery run")

	// Plan-gate: concurrent-runs-per-project AND runs-per-period. The
	// self-hosted NoopChecker allows everything; the cloud plugin
	// atomically reserves both counters in a single round-trip. On
	// self-hosted we also keep the repo-level "already running" check
	// below so the OSS UX does not regress.
	ck := policy.GetChecker()
	if _, isNoop := ck.(policy.NoopChecker); isNoop {
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
			if err := h.runRepo.Fail(context.Background(), failedRunID, errMsg); err != nil {
				apilog.WithError(err).Error("failed to mark run as failed")
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
		if err := h.runRepo.Fail(ctx, runID, "failed to start: "+runErr.Error()); err != nil {
			apilog.WithError(err).Error("failed to mark run as failed")
		}
		if reservationID != "" {
			if relErr := ck.Release(ctx, reservationID); relErr != nil {
				apilog.WithError(relErr).Warn("failed to release discovery-run reservation after agent spawn failed")
			} else if err := h.runRepo.ClearPolicyReservationID(ctx, runID); err != nil {
				apilog.WithError(err).Warn("released discovery-run reservation after agent spawn failed, but failed to clear persisted reservation id on run (post-completion confirmer will retry Confirm on an already-Released reservation until the doc TTLs)")
			}
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
			"date":            latest.DiscoveryDate,
			"insights_count":  len(latest.Insights),
			"total_steps":     latest.TotalSteps,
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

	// Confirm the policy reservation ended. We call Confirm rather than
	// Release so the period counter (already incremented when the run
	// started) stays consumed — cancellation does not refund the run
	// budget. The concurrent-runs counter decrements. Noop is a no-op.
	if run.PolicyReservationID != "" {
		if err := policy.GetChecker().ConfirmDiscoveryRunEnded(r.Context(), run.PolicyReservationID, policy.RunOutcome{
			Status:  "cancelled",
			EndedAt: time.Now().UTC(),
		}); err != nil {
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
