package handler

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

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

	// Check if there's already a running discovery
	running, _ := h.runRepo.GetRunningByProject(r.Context(), projectID)
	if running != nil {
		writeJSON(w, http.StatusConflict, map[string]string{
			"status":  "already_running",
			"run_id":  running.ID,
			"message": "A discovery is already running for this project",
		})
		return
	}

	// Parse optional request body
	var body struct {
		Areas    []string `json:"areas"`     // optional: run only these areas
		MaxSteps int      `json:"max_steps"` // optional: override exploration steps (default 100)
	}
	_ = decodeJSON(r, &body) // body is optional

	// Create a run record
	runID, err := h.runRepo.Create(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create run: "+err.Error())
		return
	}

	// Spawn the agent via the configured runner (subprocess or K8s Job)
	runErr := h.agentRunner.Run(r.Context(), runner.RunOptions{
		ProjectID: projectID,
		RunID:     runID,
		Areas:     body.Areas,
		MaxSteps:  body.MaxSteps,
		OnFailure: func(failedRunID string, errMsg string) {
			apilog.WithFields(apilog.Fields{
				"run_id": failedRunID, "error": errMsg,
			}).Error("Agent failed — updating run status")
			if err := h.runRepo.Fail(context.Background(), failedRunID, errMsg); err != nil {
				apilog.WithError(err).Error("failed to mark run as failed")
			}
		},
	})
	if runErr != nil {
		if err := h.runRepo.Fail(r.Context(), runID, "failed to start: "+runErr.Error()); err != nil {
			apilog.WithError(err).Error("failed to mark run as failed")
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

	apilog.WithField("run_id", runID).Info("Discovery run cancelled")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "cancelled",
		"message": "Run cancelled",
	})
}
