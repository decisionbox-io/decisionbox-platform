package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// getWithPrincipal drives ProjectsHandler.Get for project id with the given
// principal attached to the request context (as the auth middleware would).
func getWithPrincipal(t *testing.T, h *ProjectsHandler, id string, p *auth.UserPrincipal) int {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/projects/"+id, nil)
	req.SetPathValue("id", id)
	if p != nil {
		req = req.WithContext(auth.WithUser(req.Context(), p))
	}
	w := httptest.NewRecorder()
	h.Get(w, req)
	return w.Code
}

func TestProjectsHandler_Get_RoleBasedAccess(t *testing.T) {
	repo := newMockProjectRepo()
	repo.projects["open1"] = &models.Project{ID: "open1", Name: "Open"}
	repo.projects["hr1"] = &models.Project{ID: "hr1", Name: "HR", AllowedRoles: []string{"hr-analyst"}}
	h := NewProjectsHandler(repo, nil)

	tests := []struct {
		name string
		id   string
		p    *auth.UserPrincipal
		want int
	}{
		{"open project, viewer allowed", "open1", &auth.UserPrincipal{Roles: []string{"viewer"}}, http.StatusOK},
		{"restricted project, matching custom role allowed", "hr1", &auth.UserPrincipal{Roles: []string{"hr-analyst"}}, http.StatusOK},
		{"restricted project, non-matching role -> 404 (not 403)", "hr1", &auth.UserPrincipal{Roles: []string{"viewer"}}, http.StatusNotFound},
		{"restricted project, admin bypass", "hr1", &auth.UserPrincipal{Roles: []string{"admin"}}, http.StatusOK},
		{"restricted project, no principal (off auth chain) allowed", "hr1", nil, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getWithPrincipal(t, h, tt.id, tt.p); got != tt.want {
				t.Errorf("Get(%s) status = %d, want %d", tt.id, got, tt.want)
			}
		})
	}
}
