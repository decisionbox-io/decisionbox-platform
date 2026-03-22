package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/internal/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/internal/runner"
)

// TestConnectionHandler handles provider test endpoints.
// Tests run via the Runner interface (subprocess or K8s Job).
type TestConnectionHandler struct {
	projectRepo *database.ProjectRepository
	runner      runner.Runner
}

func NewTestConnectionHandler(projectRepo *database.ProjectRepository, r runner.Runner) *TestConnectionHandler {
	return &TestConnectionHandler{projectRepo: projectRepo, runner: r}
}

// TestWarehouse tests the warehouse connection for a project.
// POST /api/v1/projects/{id}/test/warehouse
func (h *TestConnectionHandler) TestWarehouse(w http.ResponseWriter, r *http.Request) {
	h.runTest(w, r, "warehouse")
}

// TestLLM tests the LLM provider connection for a project.
// POST /api/v1/projects/{id}/test/llm
func (h *TestConnectionHandler) TestLLM(w http.ResponseWriter, r *http.Request) {
	h.runTest(w, r, "llm")
}

func (h *TestConnectionHandler) runTest(w http.ResponseWriter, r *http.Request, target string) {
	projectID := r.PathValue("id")

	p, err := h.projectRepo.GetByID(r.Context(), projectID)
	if err != nil || p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	apilog.WithFields(apilog.Fields{
		"project_id": projectID, "target": target,
	}).Info("Running connection test")

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	result, err := h.runner.RunSync(ctx, runner.RunSyncOptions{
		ProjectID: projectID,
		Args:      []string{"--test-connection", target},
	})

	if err != nil {
		// Agent exited with error — try to parse JSON from stdout
		if result != nil && len(result.Output) > 0 {
			jsonBytes := extractJSONObject(result.Output)
			if jsonBytes != nil {
				var parsed map[string]interface{}
				if json.Unmarshal(jsonBytes, &parsed) == nil {
					writeJSON(w, http.StatusOK, parsed)
					return
				}
			}
		}

		errMsg := "connection test failed"
		if result != nil && result.Error != "" {
			errMsg = result.Error
		}

		apilog.WithFields(apilog.Fields{
			"project_id": projectID, "target": target, "error": errMsg,
		}).Warn("Connection test failed")

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   errMsg,
		})
		return
	}

	// Parse success JSON from agent stdout
	jsonBytes := extractJSONObject(result.Output)
	if jsonBytes == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   "no result from agent",
		})
		return
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   "failed to parse agent result: " + err.Error(),
		})
		return
	}

	apilog.WithFields(apilog.Fields{
		"project_id": projectID, "target": target, "success": parsed["success"],
	}).Info("Connection test completed")

	writeJSON(w, http.StatusOK, parsed)
}
