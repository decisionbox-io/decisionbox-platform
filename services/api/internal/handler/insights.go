package handler

import (
	"net/http"
	"strconv"

	"github.com/decisionbox-io/decisionbox/services/api/database"
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

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "invalid limit parameter")
			return
		}
		limit = parsed
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "invalid offset parameter")
			return
		}
		offset = parsed
	}
	// Priority is the default, and `created_at` stays reachable: changing the
	// default without leaving the old order available would silently reorder
	// every existing client's pages. An unrecognised value is a 400 rather
	// than a quiet fall back to the default -- a client that asked for an
	// order it did not get is a client whose paging is wrong in a way it
	// cannot detect.
	sort := r.URL.Query().Get("sort")
	if _, err := database.ListSort(sort); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	insights, err := h.repo.ListByProject(r.Context(), projectID, limit, offset, sort)
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

	projectID := r.PathValue("id")

	insight, err := h.repo.GetByID(r.Context(), insightID)
	if err != nil {
		writeError(w, http.StatusNotFound, "insight not found")
		return
	}
	if insight.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "insight not found")
		return
	}

	writeJSON(w, http.StatusOK, insight)
}
