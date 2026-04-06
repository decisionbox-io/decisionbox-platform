package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/decisionbox-io/decisionbox/services/api/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// EstimateHandler handles cost estimation endpoints.
type EstimateHandler struct {
	projectRepo database.ProjectRepo
}

func NewEstimateHandler(projectRepo database.ProjectRepo) *EstimateHandler {
	return &EstimateHandler{projectRepo: projectRepo}
}

// Estimate runs a cost estimation for a project.
// POST /api/v1/projects/{id}/discover/estimate
func (h *EstimateHandler) Estimate(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")

	p, err := h.projectRepo.GetByID(r.Context(), projectID)
	if err != nil || p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	// Parse optional request body
	var body struct {
		Areas    []string `json:"areas"`
		MaxSteps int      `json:"max_steps"`
	}
	_ = decodeJSON(r, &body) // body is optional

	if body.MaxSteps <= 0 {
		body.MaxSteps = 100
	}

	// Spawn agent with --estimate flag (synchronous — captures stdout)
	args := []string{
		"--project-id", projectID,
		"--estimate",
		"--max-steps", strconv.Itoa(body.MaxSteps),
	}
	if len(body.Areas) > 0 {
		args = append(args, "--areas", strings.Join(body.Areas, ","))
	}

	// Estimation always runs as subprocess (synchronous, captures stdout).
	// Unlike discovery which uses the Runner interface (subprocess or K8s Job),
	// estimation is fast (~10s) and needs the JSON result immediately.
	cmd := exec.Command("decisionbox-agent", args...) //nolint:gosec // controlled binary name
	cmd.Env = append(os.Environ(),
		"MONGODB_URI="+getEnvOrDefault("MONGODB_URI", "mongodb://localhost:27017"),
		"MONGODB_DB="+getEnvOrDefault("MONGODB_DB", "decisionbox"),
	)

	apilog.WithFields(apilog.Fields{
		"project_id": projectID,
		"max_steps":  body.MaxSteps,
		"areas":      body.Areas,
	}).Info("Running cost estimation")

	output, err := cmd.Output()
	if err != nil {
		errMsg := fmt.Sprintf("estimation failed: %s", err.Error())
		if exitErr, ok := err.(*exec.ExitError); ok {
			errMsg += "\n" + string(exitErr.Stderr)
		}
		apilog.WithFields(apilog.Fields{
			"project_id": projectID, "error": errMsg,
		}).Error("Cost estimation failed")
		writeError(w, http.StatusInternalServerError, errMsg)
		return
	}

	// Extract JSON from output (agent logs may be mixed in on stdout)
	jsonBytes := extractJSONObject(output)
	if jsonBytes == nil {
		writeError(w, http.StatusInternalServerError, "no JSON found in agent output")
		return
	}

	var estimate models.CostEstimate
	if err := json.Unmarshal(jsonBytes, &estimate); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to parse estimate: "+err.Error())
		return
	}

	apilog.WithFields(apilog.Fields{
		"project_id": projectID,
		"total_usd":  fmt.Sprintf("$%.4f", estimate.TotalUSD),
		"llm_usd":    fmt.Sprintf("$%.4f", estimate.LLM.CostUSD),
	}).Info("Cost estimation completed")

	writeJSON(w, http.StatusOK, estimate)
}

// extractJSONObject finds the first top-level JSON object in mixed output.
func extractJSONObject(data []byte) []byte {
	s := string(data)
	start := strings.Index(s, "{")
	if start == -1 {
		return nil
	}
	// Find matching closing brace
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return []byte(s[start : i+1])
			}
		}
	}
	return nil
}
