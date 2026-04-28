package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	secretsapi "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

func TestProjectsHandler_Create_InvalidJSON(t *testing.T) {
	h := NewProjectsHandler(nil, nil)

	req := httptest.NewRequest("POST", "/api/v1/projects",
		strings.NewReader(`not json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp.Error, "invalid JSON") {
		t.Errorf("error = %q, should contain 'invalid JSON'", resp.Error)
	}
}

func TestProjectsHandler_Create_MissingName(t *testing.T) {
	h := NewProjectsHandler(nil, nil)

	req := httptest.NewRequest("POST", "/api/v1/projects",
		strings.NewReader(`{"domain": "gaming", "category": "match3"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error != "name is required" {
		t.Errorf("error = %q, want 'name is required'", resp.Error)
	}
}

func TestProjectsHandler_Create_MissingDomain(t *testing.T) {
	h := NewProjectsHandler(nil, nil)

	req := httptest.NewRequest("POST", "/api/v1/projects",
		strings.NewReader(`{"name": "Test Project", "category": "match3"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error != "domain is required" {
		t.Errorf("error = %q, want 'domain is required'", resp.Error)
	}
}

func TestProjectsHandler_Create_MissingCategory(t *testing.T) {
	h := NewProjectsHandler(nil, nil)

	req := httptest.NewRequest("POST", "/api/v1/projects",
		strings.NewReader(`{"name": "Test Project", "domain": "gaming"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error != "category is required" {
		t.Errorf("error = %q, want 'category is required'", resp.Error)
	}
}

func TestProjectsHandler_Create_EmptyBody(t *testing.T) {
	h := NewProjectsHandler(nil, nil)

	req := httptest.NewRequest("POST", "/api/v1/projects",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestProjectsHandler_Create_ValidationOrder(t *testing.T) {
	// Verify that name is checked first, then domain, then category
	h := NewProjectsHandler(nil, nil)

	// All missing: name should be reported first
	req := httptest.NewRequest("POST", "/api/v1/projects",
		strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.Create(w, req)

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error != "name is required" {
		t.Errorf("first validation error should be name, got %q", resp.Error)
	}

	// Name present, domain missing: domain should be reported
	req = httptest.NewRequest("POST", "/api/v1/projects",
		strings.NewReader(`{"name": "Test"}`))
	w = httptest.NewRecorder()
	h.Create(w, req)

	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error != "domain is required" {
		t.Errorf("second validation error should be domain, got %q", resp.Error)
	}

	// Name and domain present, category missing: category should be reported
	req = httptest.NewRequest("POST", "/api/v1/projects",
		strings.NewReader(`{"name": "Test", "domain": "gaming"}`))
	w = httptest.NewRecorder()
	h.Create(w, req)

	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error != "category is required" {
		t.Errorf("third validation error should be category, got %q", resp.Error)
	}
}

// --- Mock-based unit tests ---

func TestProjectsHandler_Create_Success_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	body := `{"name":"Test Project","domain":"gaming","category":"match3"}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data, ok := resp.Data.(map[string]interface{})
	if !ok {
		t.Fatal("response data should be a project object")
	}
	if data["id"] == nil || data["id"] == "" {
		t.Error("created project should have an id")
	}
	if data["name"] != "Test Project" {
		t.Errorf("name = %v, want 'Test Project'", data["name"])
	}
	if data["domain"] != "gaming" {
		t.Errorf("domain = %v, want 'gaming'", data["domain"])
	}
	if data["category"] != "match3" {
		t.Errorf("category = %v, want 'match3'", data["category"])
	}

	// Verify the project was stored in the mock repo
	if len(repo.projects) != 1 {
		t.Errorf("repo should have 1 project, got %d", len(repo.projects))
	}
}

func TestProjectsHandler_Create_RepoError_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	repo.createErr = fmt.Errorf("database connection failed")
	h := NewProjectsHandler(repo, nil)

	body := `{"name":"Test","domain":"gaming","category":"match3"}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp.Error, "database connection failed") {
		t.Errorf("error = %q, should contain repo error message", resp.Error)
	}
}

func TestProjectsHandler_List_Success_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	// Seed two projects
	for i := 0; i < 2; i++ {
		p := &models.Project{
			Name:     fmt.Sprintf("Project %d", i+1),
			Domain:   "gaming",
			Category: "match3",
		}
		repo.Create(context.Background(), p)
	}

	req := httptest.NewRequest("GET", "/api/v1/projects", nil)
	w := httptest.NewRecorder()

	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	projects, ok := resp.Data.([]interface{})
	if !ok {
		t.Fatal("response data should be an array")
	}
	if len(projects) != 2 {
		t.Errorf("project count = %d, want 2", len(projects))
	}
}

func TestProjectsHandler_List_Empty_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	req := httptest.NewRequest("GET", "/api/v1/projects", nil)
	w := httptest.NewRecorder()

	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	projects, ok := resp.Data.([]interface{})
	if !ok {
		t.Fatal("response data should be an array")
	}
	if len(projects) != 0 {
		t.Errorf("project count = %d, want 0", len(projects))
	}
}

func TestProjectsHandler_List_RepoError_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	repo.listErr = fmt.Errorf("database timeout")
	h := NewProjectsHandler(repo, nil)

	req := httptest.NewRequest("GET", "/api/v1/projects", nil)
	w := httptest.NewRecorder()

	h.List(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestProjectsHandler_Get_Success_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	// Create a project
	p := &models.Project{Name: "My Project", Domain: "gaming", Category: "match3"}
	repo.Create(context.Background(), p)

	req := httptest.NewRequest("GET", "/api/v1/projects/"+p.ID, nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()

	h.Get(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["name"] != "My Project" {
		t.Errorf("name = %v, want 'My Project'", data["name"])
	}
	if data["id"] != p.ID {
		t.Errorf("id = %v, want %q", data["id"], p.ID)
	}
}

func TestProjectsHandler_Get_NotFound_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	req := httptest.NewRequest("GET", "/api/v1/projects/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()

	h.Get(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error != "project not found" {
		t.Errorf("error = %q, want 'project not found'", resp.Error)
	}
}

func TestProjectsHandler_Get_RepoError_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	repo.getErr = fmt.Errorf("connection refused")
	h := NewProjectsHandler(repo, nil)

	req := httptest.NewRequest("GET", "/api/v1/projects/some-id", nil)
	req.SetPathValue("id", "some-id")
	w := httptest.NewRecorder()

	h.Get(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestProjectsHandler_Update_Success_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	// Create a project first
	p := &models.Project{
		Name:     "Original Name",
		Domain:   "gaming",
		Category: "match3",
		Warehouse: models.WarehouseConfig{Provider: "bigquery"},
	}
	repo.Create(context.Background(), p)

	// Update the name
	body := `{"name":"Updated Name"}`
	req := httptest.NewRequest("PUT", "/api/v1/projects/"+p.ID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()

	h.Update(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["name"] != "Updated Name" {
		t.Errorf("name = %v, want 'Updated Name'", data["name"])
	}

	// Verify warehouse was preserved (merge behavior)
	wh := data["warehouse"].(map[string]interface{})
	if wh["provider"] != "bigquery" {
		t.Errorf("warehouse provider = %v, want 'bigquery' (should be preserved)", wh["provider"])
	}

	// Verify the update persisted in the repo
	updated, _ := repo.GetByID(context.Background(), p.ID)
	if updated.Name != "Updated Name" {
		t.Errorf("repo name = %q, want 'Updated Name'", updated.Name)
	}
}

// TestProjectsHandler_Update_AcceptsPackGenDoneToReady covers the
// only state transition the PUT /projects/{id} endpoint allows on the
// state field — the wizard's "Accept and continue" / "Skip review"
// buttons fire this when the user is happy with the generated pack
// and wants the discovery UI to take over the project page. Without
// this transition the merge silently drops `state: "ready"` from the
// request body and the project is stuck on pack_generation_done
// forever (regression: fizbot project on 2026-04-27 sat in
// pack_generation_done after the user clicked Accept because the
// merge had no state branch).
func TestProjectsHandler_Update_AcceptsPackGenDoneToReady(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	p := &models.Project{
		Name:  "Done Project",
		State: models.ProjectStatePackGenerationDone,
		LLM:   models.LLMConfig{Provider: "openai", Model: "gpt-test"},
	}
	repo.Create(context.Background(), p)

	body := `{"state":"ready"}`
	req := httptest.NewRequest("PUT", "/api/v1/projects/"+p.ID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Update(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	updated, _ := repo.GetByID(context.Background(), p.ID)
	if updated.State != models.ProjectStateReady {
		t.Errorf("state = %q, want %q", updated.State, models.ProjectStateReady)
	}
}

// TestProjectsHandler_Update_DropsArbitraryStateWrites locks in the
// flip side: any state-write attempt that ISN'T the
// pack_generation_done → ready transition is silently dropped (the
// merge ignores it). Critical so a stale dashboard or curl can't
// roll a project back into pack-gen mid-flight.
func TestProjectsHandler_Update_DropsArbitraryStateWrites(t *testing.T) {
	cases := []struct {
		name         string
		startState   string
		incomingBody string
		wantState    string
	}{
		{"ready → pack_generation refused", models.ProjectStateReady, `{"state":"pack_generation"}`, models.ProjectStateReady},
		{"ready → pack_generation_pending refused", models.ProjectStateReady, `{"state":"pack_generation_pending"}`, models.ProjectStateReady},
		{"pack_generation_pending → ready refused", models.ProjectStatePackGenerationPending, `{"state":"ready"}`, models.ProjectStatePackGenerationPending},
		{"pack_generation → ready refused", models.ProjectStatePackGeneration, `{"state":"ready"}`, models.ProjectStatePackGeneration},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newMockProjectRepo()
			h := NewProjectsHandler(repo, nil)
			p := &models.Project{
				Name:  "Guarded",
				State: tc.startState,
				LLM:   models.LLMConfig{Provider: "openai", Model: "gpt-test"},
			}
			repo.Create(context.Background(), p)
			req := httptest.NewRequest("PUT", "/api/v1/projects/"+p.ID, strings.NewReader(tc.incomingBody))
			req.Header.Set("Content-Type", "application/json")
			req.SetPathValue("id", p.ID)
			w := httptest.NewRecorder()
			h.Update(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			updated, _ := repo.GetByID(context.Background(), p.ID)
			if updated.State != tc.wantState {
				t.Errorf("state = %q, want %q (%s)", updated.State, tc.wantState, tc.name)
			}
		})
	}
}

func TestProjectsHandler_Update_NotFound_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	body := `{"name":"Updated"}`
	req := httptest.NewRequest("PUT", "/api/v1/projects/nonexistent", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()

	h.Update(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestProjectsHandler_Update_InvalidJSON_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	// Create a project so GetByID succeeds
	p := &models.Project{Name: "Test", Domain: "gaming", Category: "match3"}
	repo.Create(context.Background(), p)

	req := httptest.NewRequest("PUT", "/api/v1/projects/"+p.ID, strings.NewReader(`not json`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()

	h.Update(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestProjectsHandler_Update_RepoError_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	// Create a project, then inject an update error
	p := &models.Project{Name: "Test", Domain: "gaming", Category: "match3"}
	repo.Create(context.Background(), p)
	repo.updateErr = fmt.Errorf("write conflict")

	body := `{"name":"Updated"}`
	req := httptest.NewRequest("PUT", "/api/v1/projects/"+p.ID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()

	h.Update(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestProjectsHandler_Delete_Success_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	// Create a project
	p := &models.Project{Name: "To Delete", Domain: "gaming", Category: "match3"}
	repo.Create(context.Background(), p)

	req := httptest.NewRequest("DELETE", "/api/v1/projects/"+p.ID, nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()

	h.Delete(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["deleted"] != p.ID {
		t.Errorf("deleted = %v, want %q", data["deleted"], p.ID)
	}

	// Verify project is gone
	got, _ := repo.GetByID(context.Background(), p.ID)
	if got != nil {
		t.Error("project should be deleted from repo")
	}
}

func TestProjectsHandler_Delete_NotFound_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	req := httptest.NewRequest("DELETE", "/api/v1/projects/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()

	h.Delete(w, req)

	// New cascade contract: missing project returns 404 (was 500 under
	// the legacy single-repo Delete, but the new flow does an explicit
	// GetByID pre-check so the user gets a clearer error).
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestProjectsHandler_Delete_RepoError_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	repo.deleteCascadeErr = fmt.Errorf("permission denied")
	h := NewProjectsHandler(repo, nil)

	// Create a project so GetByID passes the pre-check; the cascade
	// step is what fails here.
	p := &models.Project{Name: "Test", Domain: "gaming", Category: "match3"}
	repo.Create(context.Background(), p)

	req := httptest.NewRequest("DELETE", "/api/v1/projects/"+p.ID, nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()

	h.Delete(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

// --- Delete cascade with optional deps wired ---

// fakeProjectDeleterSecretProvider is a secrets.Provider that ALSO
// implements ProjectDeleter — used to verify the handler type-asserts
// correctly and sweeps secrets when the backend supports it.
type fakeProjectDeleterSecretProvider struct {
	deleteCalled []string
	deleteErr    error
}

func (m *fakeProjectDeleterSecretProvider) Get(_ context.Context, _, _ string) (string, error) {
	return "", nil
}
func (m *fakeProjectDeleterSecretProvider) Set(_ context.Context, _, _, _ string) error { return nil }
func (m *fakeProjectDeleterSecretProvider) List(_ context.Context, _ string) ([]secretsapi.SecretEntry, error) {
	return nil, nil
}
func (m *fakeProjectDeleterSecretProvider) DeleteAllForProject(_ context.Context, projectID string) error {
	m.deleteCalled = append(m.deleteCalled, projectID)
	return m.deleteErr
}

// externalSecretProvider is a secrets.Provider that does NOT implement
// ProjectDeleter — represents gcp/aws/azure backends. Delete handler
// must skip secret sweep on these and report secrets_skipped: true.
type externalSecretProvider struct{}

func (m *externalSecretProvider) Get(_ context.Context, _, _ string) (string, error) {
	return "", nil
}
func (m *externalSecretProvider) Set(_ context.Context, _, _, _ string) error { return nil }
func (m *externalSecretProvider) List(_ context.Context, _ string) ([]secretsapi.SecretEntry, error) {
	return nil, nil
}

func TestProjectsHandler_Delete_HappyPath_AllDepsFire(t *testing.T) {
	repo := newMockProjectRepo()
	p := &models.Project{Name: "T", Domain: "gaming", Category: "match3"}
	_ = repo.Create(context.Background(), p)

	dropper := &mockDropper{}
	secretsProv := &fakeProjectDeleterSecretProvider{}
	h := NewProjectsHandler(repo, nil).WithDeleteCascadeDeps(dropper, secretsProv, nil)

	req := httptest.NewRequest("DELETE", "/api/v1/projects/"+p.ID, nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Delete(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", w.Code, w.Body.String())
	}
	if len(dropper.calls) != 1 || dropper.calls[0] != p.ID {
		t.Errorf("dropper called with %v, want [%s]", dropper.calls, p.ID)
	}
	if len(secretsProv.deleteCalled) != 1 || secretsProv.deleteCalled[0] != p.ID {
		t.Errorf("secrets DeleteAllForProject called with %v, want [%s]", secretsProv.deleteCalled, p.ID)
	}
	if len(repo.cascadeCalls) != 1 || repo.cascadeCalls[0] != p.ID {
		t.Errorf("DeleteCascade called with %v, want [%s]", repo.cascadeCalls, p.ID)
	}
	var resp APIResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["secrets_skipped"] != false {
		t.Errorf("secrets_skipped = %v, want false (mongo-backed)", data["secrets_skipped"])
	}
}

func TestProjectsHandler_Delete_IndexingInFlight_409(t *testing.T) {
	repo := newMockProjectRepo()
	p := &models.Project{Name: "T", Domain: "gaming", Category: "match3", SchemaIndexStatus: models.SchemaIndexStatusIndexing}
	_ = repo.Create(context.Background(), p)

	dropper := &mockDropper{}
	secretsProv := &fakeProjectDeleterSecretProvider{}
	h := NewProjectsHandler(repo, nil).WithDeleteCascadeDeps(dropper, secretsProv, nil)

	req := httptest.NewRequest("DELETE", "/api/v1/projects/"+p.ID, nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Delete(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	// Nothing must have been touched.
	if len(dropper.calls) != 0 {
		t.Errorf("dropper called despite 409: %v", dropper.calls)
	}
	if len(secretsProv.deleteCalled) != 0 {
		t.Errorf("secrets DeleteAllForProject called despite 409: %v", secretsProv.deleteCalled)
	}
	if len(repo.cascadeCalls) != 0 {
		t.Errorf("DeleteCascade called despite 409: %v", repo.cascadeCalls)
	}
}

func TestProjectsHandler_Delete_QdrantFailure_StillProceeds(t *testing.T) {
	// Qdrant down at delete time must NOT block: BuildIndex re-drops
	// on next index, so a leftover collection is harmless. The cascade
	// still has to clear Mongo or the user is permanently blocked.
	repo := newMockProjectRepo()
	p := &models.Project{Name: "T", Domain: "gaming", Category: "match3"}
	_ = repo.Create(context.Background(), p)

	dropper := &mockDropper{err: fmt.Errorf("qdrant unreachable")}
	secretsProv := &fakeProjectDeleterSecretProvider{}
	h := NewProjectsHandler(repo, nil).WithDeleteCascadeDeps(dropper, secretsProv, nil)

	req := httptest.NewRequest("DELETE", "/api/v1/projects/"+p.ID, nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Delete(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s); cascade must continue past Qdrant failures", w.Code, w.Body.String())
	}
	if len(repo.cascadeCalls) != 1 {
		t.Errorf("DeleteCascade not invoked after Qdrant failure: %v", repo.cascadeCalls)
	}
	if len(secretsProv.deleteCalled) != 1 {
		t.Errorf("secrets sweep skipped after Qdrant failure: %v", secretsProv.deleteCalled)
	}
}

func TestProjectsHandler_Delete_SecretSweepFailure_StillProceeds(t *testing.T) {
	repo := newMockProjectRepo()
	p := &models.Project{Name: "T", Domain: "gaming", Category: "match3"}
	_ = repo.Create(context.Background(), p)

	secretsProv := &fakeProjectDeleterSecretProvider{deleteErr: fmt.Errorf("mongo blip")}
	h := NewProjectsHandler(repo, nil).WithDeleteCascadeDeps(&mockDropper{}, secretsProv, nil)

	req := httptest.NewRequest("DELETE", "/api/v1/projects/"+p.ID, nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Delete(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (secret-sweep failures are best-effort)", w.Code)
	}
	if len(repo.cascadeCalls) != 1 {
		t.Errorf("Mongo cascade skipped after secret-sweep failure")
	}
	var resp APIResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["secrets_skipped"] != true {
		t.Errorf("secrets_skipped = %v, want true (sweep returned error → effectively skipped)", data["secrets_skipped"])
	}
}

func TestProjectsHandler_Delete_ExternalSecretProvider_SkipsSecrets(t *testing.T) {
	// gcp/aws/azure secret backends do not implement ProjectDeleter.
	// The handler must NOT panic and must report secrets_skipped: true
	// so the UI can prompt the user to clean credentials manually.
	repo := newMockProjectRepo()
	p := &models.Project{Name: "T", Domain: "gaming", Category: "match3"}
	_ = repo.Create(context.Background(), p)

	h := NewProjectsHandler(repo, nil).WithDeleteCascadeDeps(&mockDropper{}, &externalSecretProvider{}, nil)

	req := httptest.NewRequest("DELETE", "/api/v1/projects/"+p.ID, nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Delete(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp APIResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["secrets_skipped"] != true {
		t.Errorf("secrets_skipped = %v, want true (external secret provider)", data["secrets_skipped"])
	}
	if len(repo.cascadeCalls) != 1 {
		t.Error("cascade must still run with external secret provider")
	}
}

func TestProjectsHandler_Delete_NoOptionalDeps_Works(t *testing.T) {
	// Community Qdrant-less / no-secrets build: handler is constructed
	// without WithDeleteCascadeDeps. Deletion must still complete the
	// Mongo cascade; secrets are skipped by definition.
	repo := newMockProjectRepo()
	p := &models.Project{Name: "T", Domain: "gaming", Category: "match3"}
	_ = repo.Create(context.Background(), p)

	h := NewProjectsHandler(repo, nil) // no WithDeleteCascadeDeps

	req := httptest.NewRequest("DELETE", "/api/v1/projects/"+p.ID, nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Delete(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", w.Code, w.Body.String())
	}
	if len(repo.cascadeCalls) != 1 {
		t.Errorf("cascade not invoked: %v", repo.cascadeCalls)
	}
	var resp APIResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["secrets_skipped"] != true {
		t.Errorf("secrets_skipped = %v, want true (no provider wired)", data["secrets_skipped"])
	}
}

func TestProjectsHandler_Delete_EmptyPathID_400(t *testing.T) {
	h := NewProjectsHandler(newMockProjectRepo(), nil)
	req := httptest.NewRequest("DELETE", "/api/v1/projects/", nil)
	req.SetPathValue("id", "")
	w := httptest.NewRecorder()
	h.Delete(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestProjectsHandler_Update_MergeFields_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	// Create a project with LLM and warehouse config
	p := &models.Project{
		Name:      "Test Project",
		Domain:    "gaming",
		Category:  "match3",
		Warehouse: models.WarehouseConfig{Provider: "bigquery", Datasets: []string{"events"}},
		LLM:       models.LLMConfig{Provider: "claude", Model: "claude-sonnet-4"},
	}
	repo.Create(context.Background(), p)

	// Update only LLM provider — warehouse should be preserved
	body := `{"llm":{"provider":"openai","model":"gpt-4o"}}`
	req := httptest.NewRequest("PUT", "/api/v1/projects/"+p.ID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()

	h.Update(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	updated, _ := repo.GetByID(context.Background(), p.ID)
	if updated.LLM.Provider != "openai" {
		t.Errorf("LLM provider = %q, want 'openai'", updated.LLM.Provider)
	}
	if updated.Warehouse.Provider != "bigquery" {
		t.Errorf("warehouse provider = %q, want 'bigquery' (should be preserved)", updated.Warehouse.Provider)
	}
}

func TestProjectsHandler_Create_WithoutDomainPackRepo_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	body := `{"name":"Prompt Test","domain":"gaming","category":"match3"}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}

	// Without domainPackRepo, prompts are not seeded (graceful degradation)
	var storedID string
	for id := range repo.projects {
		storedID = id
	}
	stored, _ := repo.GetByID(context.Background(), storedID)
	if stored.Prompts != nil {
		t.Error("prompts should be nil when domainPackRepo is nil")
	}
}

// --- wire_override validation ---

func TestProjectsHandler_Create_InvalidWireOverride(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	body := `{
		"name":"WireTest","domain":"gaming","category":"match3",
		"llm":{"provider":"bedrock","model":"anthropic.claude-sonnet-4-20250514-v1:0",
		       "config":{"wire_override":"antropik"}}
	}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var resp APIResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp.Error, "wire_override") {
		t.Errorf("error = %q, should mention wire_override", resp.Error)
	}
	// Must not persist the project.
	if len(repo.projects) != 0 {
		t.Errorf("project should not be persisted on validation failure")
	}
}

func TestProjectsHandler_Create_ValidWireOverride(t *testing.T) {
	// Bedrock supports anthropic + openai-compat only — google-native is
	// rejected at the handler now that validation is provider-scoped.
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	for _, wo := range []string{"anthropic", "openai-compat"} {
		body := fmt.Sprintf(`{
			"name":"WO %s","domain":"gaming","category":"match3",
			"llm":{"provider":"bedrock","model":"vendor.future",
			       "config":{"wire_override":%q}}
		}`, wo, wo)
		req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Create(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("wire_override=%q: status = %d, want 201: %s", wo, w.Code, w.Body.String())
		}
	}
}

func TestProjectsHandler_Create_WireOverrideNotSupportedByProvider(t *testing.T) {
	// google-native is a valid wire syntax but bedrock doesn't implement it.
	// The provider-scoped validator must reject it at the handler.
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	body := `{
		"name":"bad","domain":"gaming","category":"match3",
		"llm":{"provider":"bedrock","model":"anthropic.claude-sonnet-4-20250514-v1:0",
		       "config":{"wire_override":"google-native"}}
	}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	var resp APIResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp.Error, "not supported by provider") {
		t.Errorf("error = %q, should mention 'not supported by provider'", resp.Error)
	}
	if !strings.Contains(resp.Error, "bedrock") {
		t.Errorf("error = %q, should name the provider", resp.Error)
	}
	if len(repo.projects) != 0 {
		t.Errorf("project should not be persisted on validation failure")
	}
}

func TestProjectsHandler_Update_InvalidWireOverride(t *testing.T) {
	repo := newMockProjectRepo()
	// Preload directly — Create() rewrites the ID so we can't use it here.
	repo.projects["proj-seed"] = &models.Project{
		ID:       "proj-seed",
		Name:     "pre",
		Domain:   "gaming",
		Category: "match3",
		LLM:      models.LLMConfig{Provider: "bedrock", Model: "anthropic.claude-sonnet-4-20250514-v1:0"},
	}

	h := NewProjectsHandler(repo, nil)

	body := `{"llm":{"provider":"bedrock","model":"anthropic.claude-sonnet-4-20250514-v1:0",
	       "config":{"wire_override":"not-a-wire"}}}`
	req := httptest.NewRequest("PUT", "/api/v1/projects/proj-seed", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "proj-seed")
	w := httptest.NewRecorder()
	h.Update(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
	var resp APIResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp.Error, "wire_override") {
		t.Errorf("error = %q, should mention wire_override", resp.Error)
	}
}
