package handler

import (
	"encoding/json"
	"net/http"

	"github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	"github.com/decisionbox-io/decisionbox/services/api/internal/database"
	"github.com/decisionbox-io/decisionbox/services/api/internal/models"
)

// APIResponse is the standard response wrapper.
type APIResponse struct {
	Data  interface{} `json:"data,omitempty"`
	Error string      `json:"error,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIResponse{Data: data})
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIResponse{Error: msg})
}

func decodeJSON(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

// getProjectWithOrgCheck fetches a project by ID and verifies the requesting
// user belongs to the same org. Returns the project, or nil if not found /
// not authorized (writes the error response automatically).
func getProjectWithOrgCheck(w http.ResponseWriter, r *http.Request, repo database.ProjectRepo, projectID string) *models.Project {
	p, err := repo.GetByID(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get project: "+err.Error())
		return nil
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return nil
	}
	if user, ok := auth.FromContext(r.Context()); ok && p.OrgID != "" && p.OrgID != user.OrgID {
		writeError(w, http.StatusNotFound, "project not found")
		return nil
	}
	return p
}
