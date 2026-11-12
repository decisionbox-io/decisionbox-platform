package handler

import (
	"net/http"
	"strconv"

	"github.com/decisionbox-io/decisionbox/services/api/database"
)

// RecommendationsHandler handles recommendation listing endpoints.
type RecommendationsHandler struct {
	repo database.RecommendationRepo
}

func NewRecommendationsHandler(repo database.RecommendationRepo) *RecommendationsHandler {
	return &RecommendationsHandler{repo: repo}
}

// List returns paginated recommendations for a project.
// GET /api/v1/projects/{id}/recommendations?limit=50&offset=0
func (h *RecommendationsHandler) List(w http.ResponseWriter, r *http.Request) {
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

	recs, err := h.repo.ListByProject(r.Context(), projectID, limit, offset, sort)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list recommendations")
		return
	}

	writeJSON(w, http.StatusOK, recs)
}

// Get returns a single recommendation by ID.
// GET /api/v1/projects/{id}/recommendations/{recId}
func (h *RecommendationsHandler) Get(w http.ResponseWriter, r *http.Request) {
	recID := r.PathValue("recId")
	if recID == "" {
		writeError(w, http.StatusBadRequest, "recommendation ID is required")
		return
	}

	projectID := r.PathValue("id")

	rec, err := h.repo.GetByID(r.Context(), recID)
	if err != nil {
		writeError(w, http.StatusNotFound, "recommendation not found")
		return
	}
	if rec.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "recommendation not found")
		return
	}

	writeJSON(w, http.StatusOK, rec)
}
