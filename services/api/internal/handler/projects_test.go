package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/internal/models"
)

func TestProjectsHandler_Create_InvalidJSON(t *testing.T) {
	h := NewProjectsHandler(nil)

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
	h := NewProjectsHandler(nil)

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
	h := NewProjectsHandler(nil)

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
	h := NewProjectsHandler(nil)

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
	h := NewProjectsHandler(nil)

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
	h := NewProjectsHandler(nil)

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
	h := NewProjectsHandler(repo)

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
	h := NewProjectsHandler(repo)

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
	h := NewProjectsHandler(repo)

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
	h := NewProjectsHandler(repo)

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
	h := NewProjectsHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/projects", nil)
	w := httptest.NewRecorder()

	h.List(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestProjectsHandler_Get_Success_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo)

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
	h := NewProjectsHandler(repo)

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
	h := NewProjectsHandler(repo)

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
	h := NewProjectsHandler(repo)

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

func TestProjectsHandler_Update_NotFound_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo)

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
	h := NewProjectsHandler(repo)

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
	h := NewProjectsHandler(repo)

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
	h := NewProjectsHandler(repo)

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
	h := NewProjectsHandler(repo)

	req := httptest.NewRequest("DELETE", "/api/v1/projects/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()

	h.Delete(w, req)

	// Delete handler returns 500 when repo returns an error for missing ID
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestProjectsHandler_Delete_RepoError_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	repo.deleteErr = fmt.Errorf("permission denied")
	h := NewProjectsHandler(repo)

	// Create a project — the deleteErr will override
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

func TestProjectsHandler_Update_MergeFields_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo)

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

func TestProjectsHandler_Create_SeedsPrompts_MockRepo(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo)

	body := `{"name":"Prompt Test","domain":"gaming","category":"match3"}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}

	// Check that the project stored in repo has prompts seeded
	var storedID string
	for id := range repo.projects {
		storedID = id
	}
	stored, _ := repo.GetByID(context.Background(), storedID)
	if stored.Prompts == nil {
		t.Error("prompts should be seeded on create for gaming domain")
	}
}
