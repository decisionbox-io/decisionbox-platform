package handler

import (
	"net/http"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// FeedbackHandler handles feedback endpoints.
type FeedbackHandler struct {
	repo database.FeedbackRepo
}

func NewFeedbackHandler(repo database.FeedbackRepo) *FeedbackHandler {
	return &FeedbackHandler{repo: repo}
}

// Submit creates or updates feedback for a target.
// POST /api/v1/discoveries/{runId}/feedback
func (h *FeedbackHandler) Submit(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runId")

	var body struct {
		ProjectID  string `json:"project_id"`
		TargetType string `json:"target_type"` // "insight" | "recommendation"
		TargetID   string `json:"target_id"`
		Rating     string `json:"rating"` // "like" | "dislike"
		Comment    string `json:"comment"`
	}

	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.TargetType == "" || body.TargetID == "" || body.Rating == "" {
		writeError(w, http.StatusBadRequest, "target_type, target_id, and rating are required")
		return
	}

	if body.Rating != "like" && body.Rating != "dislike" {
		writeError(w, http.StatusBadRequest, "rating must be 'like' or 'dislike'")
		return
	}

	if body.TargetType != "insight" && body.TargetType != "recommendation" && body.TargetType != "exploration_step" {
		writeError(w, http.StatusBadRequest, "target_type must be 'insight', 'recommendation', or 'exploration_step'")
		return
	}

	fb := &models.Feedback{
		ProjectID:   body.ProjectID,
		DiscoveryID: runID,
		TargetType:  body.TargetType,
		TargetID:    body.TargetID,
		Rating:      body.Rating,
		Comment:     body.Comment,
		CreatedAt:   time.Now(),
	}

	result, err := h.repo.Upsert(r.Context(), fb)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save feedback: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// List returns all feedback for a discovery run.
// GET /api/v1/discoveries/{runId}/feedback
func (h *FeedbackHandler) List(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runId")

	results, err := h.repo.ListByDiscovery(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list feedback: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, results)
}

// Delete removes a feedback entry.
// DELETE /api/v1/feedback/{id}
func (h *FeedbackHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if err := h.repo.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete feedback: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
