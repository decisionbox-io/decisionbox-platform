package handler

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/policy"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/internal/runner"
)

func getEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// DiscoveriesHandler handles discovery result endpoints.
type DiscoveriesHandler struct {
	repo        database.DiscoveryRepo
	projectRepo database.ProjectRepo
	runRepo     database.RunRepo
	agentRunner runner.Runner
}

func NewDiscoveriesHandler(repo database.DiscoveryRepo, projectRepo database.ProjectRepo, runRepo database.RunRepo, r runner.Runner) *DiscoveriesHandler {
	return &DiscoveriesHandler{repo: repo, projectRepo: projectRepo, runRepo: runRepo, agentRunner: r}
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
func (h *DiscoveriesHandler) TriggerDiscovery(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")

	p, err := h.projectRepo.GetByID(r.Context(), projectID)
	if err != nil || p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

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

	// Resolve MaxSteps for the min-steps default computation below. The
	// agent CLI enforces its own default (100) when zero reaches it, so we
	// mirror that here to keep the on-the-wire default and the computed
	// min-steps default consistent.
	effectiveMaxSteps := body.MaxSteps
	if effectiveMaxSteps <= 0 {
		effectiveMaxSteps = 100
	}

	// Compute MinSteps.
	// Omitted → default = floor(0.6 * max_steps). Reasoning-model discoveries
	// (Qwen3, DeepSeek-R1, GPT-OSS on Bedrock) terminated in 2-18 steps
	// before the min-steps floor existed; 60% is a conservative baseline
	// that still leaves headroom for genuinely short runs.
	// Explicit zero → user disabled the floor; forward as 0.
	// Negative or > max_steps → reject with 400.
	var minSteps int
	if body.MinSteps == nil {
		minSteps = (effectiveMaxSteps * 6) / 10
	} else {
		minSteps = *body.MinSteps
		if minSteps < 0 {
			writeError(w, http.StatusBadRequest, "min_steps must be >= 0")
			return
		}
		if minSteps > effectiveMaxSteps {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("min_steps (%d) cannot exceed max_steps (%d)", minSteps, effectiveMaxSteps))
			return
		}
	}

	// Create a run record first — we need a stable runID for the policy
	// reservation and the repo-level "already running" invariant is
	// re-enforced here (Create only returns an ID; race is closed by
	// the policy reservation on cloud and by the runRepo uniqueness on
	// self-hosted).
	runID, err := h.runRepo.Create(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create run: "+err.Error())
		return
	}

	// Plan-gate: concurrent-runs-per-project AND runs-per-period. The
	// self-hosted NoopChecker allows everything; the cloud plugin
	// atomically reserves both counters in a single round-trip. On
	// self-hosted we also keep the repo-level "already running" check
	// below so the OSS UX does not regress.
	ck := policy.GetChecker()
	if _, isNoop := ck.(policy.NoopChecker); isNoop {
		running, _ := h.runRepo.GetRunningByProject(r.Context(), projectID)
		if running != nil && running.ID != runID {
			if err := h.runRepo.Cancel(r.Context(), runID); err != nil {
				apilog.WithError(err).Warn("failed to clean up runID reserved before already-running check")
			}
			writeJSON(w, http.StatusConflict, map[string]string{
				"status":  "already_running",
				"run_id":  running.ID,
				"message": "A discovery is already running for this project",
			})
			return
		}
	}

	res, err := ck.CheckStartDiscoveryRun(r.Context(), "", projectID, runID)
	if err != nil {
		if failErr := h.runRepo.Fail(r.Context(), runID, "plan denied: "+err.Error()); failErr != nil {
			apilog.WithError(failErr).Warn("failed to mark policy-denied run as failed")
		}
		if writePolicyError(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "policy check failed: "+err.Error())
		return
	}

	reservationID := ""
	if res != nil {
		reservationID = res.ID
	}
	if reservationID != "" {
		if err := h.runRepo.SetPolicyReservationID(r.Context(), runID, reservationID); err != nil {
			apilog.WithError(err).Warn("failed to persist policy reservation id on run; cancel/crash recovery will fall through to sweeper")
		}
	}

	// Spawn the agent via the configured runner (subprocess or K8s Job)
	runErr := h.agentRunner.Run(r.Context(), runner.RunOptions{
		ProjectID: projectID,
		RunID:     runID,
		Areas:     body.Areas,
		MaxSteps:  body.MaxSteps,
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
		if err := h.runRepo.Fail(r.Context(), runID, "failed to start: "+runErr.Error()); err != nil {
			apilog.WithError(err).Error("failed to mark run as failed")
		}
		if reservationID != "" {
			if relErr := ck.Release(r.Context(), reservationID); relErr != nil {
				apilog.WithError(relErr).Warn("failed to release discovery-run reservation after agent spawn failed")
			} else if err := h.runRepo.ClearPolicyReservationID(r.Context(), runID); err != nil {
				apilog.WithError(err).Warn("released discovery-run reservation after agent spawn failed, but failed to clear persisted reservation id on run (post-completion confirmer will retry Confirm on an already-Released reservation until the doc TTLs)")
			}
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to start agent: %s", runErr.Error()))
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status": "started",
		"run_id": runID,
		"message": "Discovery agent started",
	})
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
