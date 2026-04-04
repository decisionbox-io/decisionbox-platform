package handler

import (
	"net/http"
	"strconv"

	"github.com/decisionbox-io/decisionbox/services/api/internal/database"
)

// InsightsHandler handles insight listing endpoints.
type InsightsHandler struct {
	repo database.InsightRepo
}

func NewInsightsHandler(repo database.InsightRepo) *InsightsHandler {
	return &InsightsHandler{repo: repo}
}

// List returns paginated insights for a project.
// GET /api/v1/projects/{id}/insights?limit=50&offset=0
func (h *InsightsHandler) List(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project ID is required")
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	insights, err := h.repo.ListByProject(r.Context(), projectID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list insights")
		return
	}

	writeJSON(w, http.StatusOK, insights)
}

// Get returns a single insight by ID.
// GET /api/v1/projects/{id}/insights/{insightId}
func (h *InsightsHandler) Get(w http.ResponseWriter, r *http.Request) {
	insightID := r.PathValue("insightId")
	if insightID == "" {
		writeError(w, http.StatusBadRequest, "insight ID is required")
		return
	}

	insight, err := h.repo.GetByID(r.Context(), insightID)
	if err != nil {
		writeError(w, http.StatusNotFound, "insight not found")
		return
	}

	writeJSON(w, http.StatusOK, insight)
}
