package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/internal/models"
)

// Org-scoping tests for handlers that use getProjectWithOrgCheck.
// Tests verify that a user from org-B cannot access org-A's resources.

// --- Estimate ---

func TestEstimateHandler_OtherOrg_Blocked(t *testing.T) {
	projRepo := newMockProjectRepo()
	projRepo.projects["proj-1"] = &models.Project{ID: "proj-1", OrgID: "acme", Name: "Acme"}
	h := NewEstimateHandler(projRepo)

	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/discover/estimate", strings.NewReader(`{}`))
	req.SetPathValue("id", "proj-1")
	req = withOrgUser(req, "attacker", "beta", "admin")
	w := httptest.NewRecorder()

	h.Estimate(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (cross-org estimate blocked)", w.Code)
	}
}

// --- Secrets ---

func TestSecretsHandler_Set_OtherOrg_Blocked(t *testing.T) {
	projRepo := newMockProjectRepo()
	projRepo.projects["proj-1"] = &models.Project{ID: "proj-1", OrgID: "acme", Name: "Acme"}
	h := NewSecretsHandler(nil, projRepo)

	body := `{"value":"secret-value"}`
	req := httptest.NewRequest("PUT", "/api/v1/projects/proj-1/secrets/llm-api-key", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "proj-1")
	req.SetPathValue("key", "llm-api-key")
	req = withOrgUser(req, "attacker", "beta", "admin")
	w := httptest.NewRecorder()

	h.Set(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (cross-org secret set blocked)", w.Code)
	}
}

func TestSecretsHandler_List_OtherOrg_Blocked(t *testing.T) {
	projRepo := newMockProjectRepo()
	projRepo.projects["proj-1"] = &models.Project{ID: "proj-1", OrgID: "acme", Name: "Acme"}
	h := NewSecretsHandler(nil, projRepo)

	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/secrets", nil)
	req.SetPathValue("id", "proj-1")
	req = withOrgUser(req, "attacker", "beta", "admin")
	w := httptest.NewRecorder()

	h.List(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (cross-org secret list blocked)", w.Code)
	}
}

// --- Prompts ---

func TestPromptsHandler_Get_OtherOrg_Blocked(t *testing.T) {
	projRepo := newMockProjectRepo()
	projRepo.projects["proj-1"] = &models.Project{ID: "proj-1", OrgID: "acme", Name: "Acme"}
	h := GetPrompts(projRepo)

	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/prompts", nil)
	req.SetPathValue("id", "proj-1")
	req = withOrgUser(req, "attacker", "beta", "admin")
	w := httptest.NewRecorder()

	h(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (cross-org prompts get blocked)", w.Code)
	}
}

func TestPromptsHandler_Update_OtherOrg_Blocked(t *testing.T) {
	projRepo := newMockProjectRepo()
	projRepo.projects["proj-1"] = &models.Project{ID: "proj-1", OrgID: "acme", Name: "Acme"}
	h := UpdatePrompts(projRepo)

	body := `{"exploration":"hacked prompt"}`
	req := httptest.NewRequest("PUT", "/api/v1/projects/proj-1/prompts", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "proj-1")
	req = withOrgUser(req, "attacker", "beta", "admin")
	w := httptest.NewRecorder()

	h(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (cross-org prompts update blocked)", w.Code)
	}
}
