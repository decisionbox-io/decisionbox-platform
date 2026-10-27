package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/internal/runner"
)

// TestConnectionHandler handles provider test endpoints.
// Tests run via the Runner interface (subprocess or K8s Job).
type TestConnectionHandler struct {
	projectRepo database.ProjectRepo
	runner      runner.Runner
}

func NewTestConnectionHandler(projectRepo database.ProjectRepo, r runner.Runner) *TestConnectionHandler {
	return &TestConnectionHandler{projectRepo: projectRepo, runner: r}
}

// TestWarehouse tests the warehouse connection for a project.
// POST /api/v1/projects/{id}/test/warehouse
func (h *TestConnectionHandler) TestWarehouse(w http.ResponseWriter, r *http.Request) {
	h.runTest(w, r, "warehouse")
}

// TestLLM tests the LLM provider connection for a project.
//
// An optional ?layer=<key> tests one layer's own provider entry and model
// (the ?warehouse_id= precedent on the warehouse target). Absent, it tests
// the default layer — exactly today's behaviour.
//
// POST /api/v1/projects/{id}/test/llm[?layer=exploration]
func (h *TestConnectionHandler) TestLLM(w http.ResponseWriter, r *http.Request) {
	h.runTest(w, r, "llm")
}

// TestEmbedding tests the embedding provider connection for a project.
// POST /api/v1/projects/{id}/test/embedding
func (h *TestConnectionHandler) TestEmbedding(w http.ResponseWriter, r *http.Request) {
	h.runTest(w, r, "embedding")
}

// TestBlurbLLM tests the blurb LLM connection for a project. Falls
// back to the analysis LLM credential when the blurb provider matches
// the analysis provider — same path discovery uses.
//
// Kept as its own target rather than folded into ?layer=table_descriptions:
// it is a published API the dashboard and scripts already call, and the agent
// side treats the two as equivalent.
//
// POST /api/v1/projects/{id}/test/blurb-llm
func (h *TestConnectionHandler) TestBlurbLLM(w http.ResponseWriter, r *http.Request) {
	h.runTest(w, r, "blurb-llm")
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

	args := []string{"--test-connection", target}
	// For the warehouse target, an optional ?warehouse_id= selects a specific
	// datasource (the Data Sources UI passes it to test a secondary); empty
	// tests the primary, preserving the original single-warehouse behavior.
	if target == "warehouse" {
		if whid := r.URL.Query().Get("warehouse_id"); whid != "" {
			args = append(args, "--warehouse-id", whid)
		}
	}
	// For the llm target, an optional ?layer= selects one layer's provider
	// entry and model.
	//
	// The flag is OMITTED for the default layer, and the blurb-llm target
	// never carries it: no shipped agent image has a --layer flag, so a
	// per-layer test against an older image has to degrade to the default
	// rather than die on an unknown flag. An unknown layer key is refused
	// here rather than passed through, so the user gets a real message
	// instead of an agent usage error.
	// layerRequested records a non-default layer so a flag-parse failure
	// from an older agent can be reported as what it is.
	var layerRequested string
	if target == "llm" {
		if layer := r.URL.Query().Get("layer"); layer != "" {
			if !llmlayer.ValidateLayerKey(layer) {
				writeError(w, http.StatusBadRequest, "unknown layer: "+layer)
				return
			}
			if layer != llmlayer.DefaultLayer {
				args = append(args, "--layer", layer)
				layerRequested = layer
			}
		}
	}

	result, err := h.runner.RunSync(ctx, runner.RunSyncOptions{
		ProjectID: projectID,
		Args:      args,
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
		// An agent image older than the API does not know --layer and dies
		// in flag.Parse, which reads as an unrelated failure. Retrying
		// without the flag would be worse than this message: it would test
		// the DEFAULT layer and report the answer as if it were this
		// layer's.
		if layerRequested != "" && mentionsUnknownLayerFlag(result) {
			errMsg = "this deployment's agent image is older than the API and cannot test one layer on its own. " +
				"Upgrade the agent, or use the default model's connection test."
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

// mentionsUnknownLayerFlag reports whether an agent's failure looks like
// Go's flag package rejecting --layer, which is what an image older than
// this API does with it.
func mentionsUnknownLayerFlag(result *runner.RunSyncResult) bool {
	if result == nil {
		return false
	}
	haystack := strings.ToLower(result.Error + "\n" + string(result.Output))
	return strings.Contains(haystack, "flag provided but not defined") &&
		strings.Contains(haystack, "layer")
}
