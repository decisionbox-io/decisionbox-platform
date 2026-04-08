package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// --- DomainPacksHandler CRUD ---

func TestDomainPacksHandler_List_Empty(t *testing.T) {
	repo := newMockDomainPackRepo()
	h := NewDomainPacksHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/domain-packs", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	packs := resp.Data.([]interface{})
	if len(packs) != 0 {
		t.Errorf("packs = %d, want 0", len(packs))
	}
}

func TestDomainPacksHandler_List_WithPacks(t *testing.T) {
	repo := newMockDomainPackRepo()
	repo.add(testDomainPack("gaming", "match3"))
	repo.add(testDomainPack("ecommerce", "multi_category"))
	h := NewDomainPacksHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/domain-packs", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	packs := resp.Data.([]interface{})
	if len(packs) != 2 {
		t.Errorf("packs = %d, want 2", len(packs))
	}
}

func TestDomainPacksHandler_List_RepoError(t *testing.T) {
	repo := newMockDomainPackRepo()
	repo.listErr = fmt.Errorf("db connection lost")
	h := NewDomainPacksHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/domain-packs", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestDomainPacksHandler_Get_Found(t *testing.T) {
	repo := newMockDomainPackRepo()
	repo.add(testDomainPack("gaming", "match3"))
	h := NewDomainPacksHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/domain-packs/gaming", nil)
	req.SetPathValue("slug", "gaming")
	w := httptest.NewRecorder()
	h.Get(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["slug"] != "gaming" {
		t.Errorf("slug = %v", data["slug"])
	}
}

func TestDomainPacksHandler_Get_NotFound(t *testing.T) {
	repo := newMockDomainPackRepo()
	h := NewDomainPacksHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/domain-packs/nonexistent", nil)
	req.SetPathValue("slug", "nonexistent")
	w := httptest.NewRecorder()
	h.Get(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestDomainPacksHandler_Create_Success(t *testing.T) {
	repo := newMockDomainPackRepo()
	h := NewDomainPacksHandler(repo)

	pack := testDomainPack("fintech", "banking")
	body, _ := json.Marshal(pack)

	req := httptest.NewRequest("POST", "/api/v1/domain-packs", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", w.Code, w.Body.String())
	}

	// Verify stored
	stored, _ := repo.GetBySlug(context.Background(), "fintech")
	if stored == nil {
		t.Fatal("pack should be stored")
	}
}

func TestDomainPacksHandler_Create_InvalidJSON(t *testing.T) {
	repo := newMockDomainPackRepo()
	h := NewDomainPacksHandler(repo)

	req := httptest.NewRequest("POST", "/api/v1/domain-packs", strings.NewReader(`not json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestDomainPacksHandler_Create_ValidationError(t *testing.T) {
	repo := newMockDomainPackRepo()
	h := NewDomainPacksHandler(repo)

	// Missing required fields
	body := `{"slug":"test","name":"Test"}`
	req := httptest.NewRequest("POST", "/api/v1/domain-packs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestDomainPacksHandler_Create_DuplicateSlug(t *testing.T) {
	repo := newMockDomainPackRepo()
	repo.add(testDomainPack("gaming", "match3"))
	h := NewDomainPacksHandler(repo)

	pack := testDomainPack("gaming", "match3")
	body, _ := json.Marshal(pack)

	req := httptest.NewRequest("POST", "/api/v1/domain-packs", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
}

func TestDomainPacksHandler_Update_Success(t *testing.T) {
	repo := newMockDomainPackRepo()
	repo.add(testDomainPack("gaming", "match3"))
	h := NewDomainPacksHandler(repo)

	updated := testDomainPack("gaming", "match3")
	updated.Name = "Updated Gaming"
	body, _ := json.Marshal(updated)

	req := httptest.NewRequest("PUT", "/api/v1/domain-packs/gaming", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("slug", "gaming")
	w := httptest.NewRecorder()
	h.Update(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
}

func TestDomainPacksHandler_Update_NotFound(t *testing.T) {
	repo := newMockDomainPackRepo()
	h := NewDomainPacksHandler(repo)

	body := `{"name":"test"}`
	req := httptest.NewRequest("PUT", "/api/v1/domain-packs/nonexistent", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("slug", "nonexistent")
	w := httptest.NewRecorder()
	h.Update(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestDomainPacksHandler_Delete_Success(t *testing.T) {
	repo := newMockDomainPackRepo()
	repo.add(testDomainPack("gaming", "match3"))
	h := NewDomainPacksHandler(repo)

	req := httptest.NewRequest("DELETE", "/api/v1/domain-packs/gaming", nil)
	req.SetPathValue("slug", "gaming")
	w := httptest.NewRecorder()
	h.Delete(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	// Verify deleted
	stored, _ := repo.GetBySlug(context.Background(), "gaming")
	if stored != nil {
		t.Error("pack should be deleted")
	}
}

func TestDomainPacksHandler_Delete_NotFound(t *testing.T) {
	repo := newMockDomainPackRepo()
	h := NewDomainPacksHandler(repo)

	req := httptest.NewRequest("DELETE", "/api/v1/domain-packs/nonexistent", nil)
	req.SetPathValue("slug", "nonexistent")
	w := httptest.NewRecorder()
	h.Delete(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestDomainPacksHandler_Import_Success(t *testing.T) {
	repo := newMockDomainPackRepo()
	h := NewDomainPacksHandler(repo)

	pack := testDomainPack("fintech", "banking")
	portable := map[string]interface{}{
		"format":         "decisionbox-domain-pack",
		"format_version": 1,
		"pack":           pack,
	}
	body, _ := json.Marshal(portable)

	req := httptest.NewRequest("POST", "/api/v1/domain-packs/import", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Import(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", w.Code, w.Body.String())
	}
}

func TestDomainPacksHandler_Import_InvalidFormat(t *testing.T) {
	repo := newMockDomainPackRepo()
	h := NewDomainPacksHandler(repo)

	body := `{"format":"wrong-format","format_version":1,"pack":{}}`
	req := httptest.NewRequest("POST", "/api/v1/domain-packs/import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Import(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestDomainPacksHandler_Export_Success(t *testing.T) {
	repo := newMockDomainPackRepo()
	repo.add(testDomainPack("gaming", "match3"))
	h := NewDomainPacksHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/domain-packs/gaming/export", nil)
	req.SetPathValue("slug", "gaming")
	w := httptest.NewRecorder()
	h.Export(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	// Response is wrapped in {"data": ...} by writeJSON
	var resp struct {
		Data struct {
			Format        string            `json:"format"`
			FormatVersion int               `json:"format_version"`
			Pack          models.DomainPack `json:"pack"`
		} `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&resp)
	portable := resp.Data

	if portable.Format != "decisionbox-domain-pack" {
		t.Errorf("format = %q", portable.Format)
	}
	if portable.FormatVersion != 1 {
		t.Errorf("format_version = %d", portable.FormatVersion)
	}
	if portable.Pack.Slug != "gaming" {
		t.Errorf("slug = %q", portable.Pack.Slug)
	}
	if portable.Pack.ID != "" {
		t.Error("exported pack should not have MongoDB ID")
	}
}

func TestDomainPacksHandler_Export_NotFound(t *testing.T) {
	repo := newMockDomainPackRepo()
	h := NewDomainPacksHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/domain-packs/nonexistent/export", nil)
	req.SetPathValue("slug", "nonexistent")
	w := httptest.NewRecorder()
	h.Export(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// --- DomainsHandler (project creation flow) ---

func TestDomainsHandler_ListDomains(t *testing.T) {
	repo := newMockDomainPackRepo()
	gaming := testDomainPack("gaming", "match3")
	gaming.IsPublished = true
	repo.add(gaming)

	unpublished := testDomainPack("draft", "cat1")
	unpublished.IsPublished = false
	repo.add(unpublished)

	h := NewDomainsHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/domains", nil)
	w := httptest.NewRecorder()
	h.ListDomains(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	domains := resp.Data.([]interface{})
	// Only published packs should appear
	if len(domains) != 1 {
		t.Errorf("domains = %d, want 1 (only published)", len(domains))
	}
}

func TestDomainsHandler_ListCategories(t *testing.T) {
	repo := newMockDomainPackRepo()
	repo.add(testDomainPack("gaming", "match3"))
	h := NewDomainsHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/domains/gaming/categories", nil)
	req.SetPathValue("domain", "gaming")
	w := httptest.NewRecorder()
	h.ListCategories(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	cats := resp.Data.([]interface{})
	if len(cats) != 1 {
		t.Errorf("categories = %d, want 1", len(cats))
	}
}

func TestDomainsHandler_ListCategories_NotFound(t *testing.T) {
	repo := newMockDomainPackRepo()
	h := NewDomainsHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/domains/nonexistent/categories", nil)
	req.SetPathValue("domain", "nonexistent")
	w := httptest.NewRecorder()
	h.ListCategories(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestDomainsHandler_GetAnalysisAreas(t *testing.T) {
	repo := newMockDomainPackRepo()
	pack := testDomainPack("gaming", "match3")
	pack.AnalysisAreas.Categories["match3"] = []models.PackAnalysisArea{
		{ID: "levels", Name: "Levels", Description: "Level analysis", Keywords: []string{"level"}, Priority: 4, Prompt: "..."},
	}
	repo.add(pack)
	h := NewDomainsHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/domains/gaming/categories/match3/areas", nil)
	req.SetPathValue("domain", "gaming")
	req.SetPathValue("category", "match3")
	w := httptest.NewRecorder()
	h.GetAnalysisAreas(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	areas := resp.Data.([]interface{})
	// 1 base + 1 category-specific
	if len(areas) != 2 {
		t.Errorf("areas = %d, want 2", len(areas))
	}
}

func TestDomainsHandler_GetAnalysisAreas_NotFound(t *testing.T) {
	repo := newMockDomainPackRepo()
	h := NewDomainsHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/domains/nonexistent/categories/cat/areas", nil)
	req.SetPathValue("domain", "nonexistent")
	req.SetPathValue("category", "cat")
	w := httptest.NewRecorder()
	h.GetAnalysisAreas(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestDomainsHandler_GetProfileSchema(t *testing.T) {
	repo := newMockDomainPackRepo()
	pack := testDomainPack("gaming", "match3")
	pack.ProfileSchema.Base = map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"genre": map[string]interface{}{"type": "string"},
		},
	}
	pack.ProfileSchema.Categories = map[string]map[string]interface{}{
		"match3": {
			"properties": map[string]interface{}{
				"boosters": map[string]interface{}{"type": "boolean"},
			},
		},
	}
	repo.add(pack)
	h := NewDomainsHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/domains/gaming/categories/match3/schema", nil)
	req.SetPathValue("domain", "gaming")
	req.SetPathValue("category", "match3")
	w := httptest.NewRecorder()
	h.GetProfileSchema(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	schema := resp.Data.(map[string]interface{})
	props := schema["properties"].(map[string]interface{})
	// Should have both base and category properties merged
	if _, ok := props["genre"]; !ok {
		t.Error("should have base property 'genre'")
	}
	if _, ok := props["boosters"]; !ok {
		t.Error("should have category property 'boosters' merged in")
	}
}

func TestDomainsHandler_GetProfileSchema_NotFound(t *testing.T) {
	repo := newMockDomainPackRepo()
	h := NewDomainsHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/domains/nonexistent/categories/cat/schema", nil)
	req.SetPathValue("domain", "nonexistent")
	req.SetPathValue("category", "cat")
	w := httptest.NewRecorder()
	h.GetProfileSchema(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// --- Projects Create with domain pack seeding ---

func TestProjectsHandler_Create_SeedsFromDomainPack(t *testing.T) {
	projectRepo := newMockProjectRepo()
	domainPackRepo := newMockDomainPackRepo()
	domainPackRepo.add(testDomainPack("gaming", "match3"))

	h := NewProjectsHandler(projectRepo, domainPackRepo)

	body := `{"name":"Test","domain":"gaming","category":"match3"}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", w.Code, w.Body.String())
	}

	// Verify prompts were seeded
	var storedID string
	for id := range projectRepo.projects {
		storedID = id
	}
	stored, _ := projectRepo.GetByID(context.Background(), storedID)
	if stored.Prompts == nil {
		t.Fatal("prompts should be seeded from domain pack")
	}
	if stored.Prompts.Exploration == "" {
		t.Error("exploration prompt should be populated")
	}
	if len(stored.Prompts.AnalysisAreas) == 0 {
		t.Error("analysis areas should be seeded")
	}
}

func TestProjectsHandler_Create_DomainPackNotFound(t *testing.T) {
	projectRepo := newMockProjectRepo()
	domainPackRepo := newMockDomainPackRepo()
	// No packs in repo

	h := NewProjectsHandler(projectRepo, domainPackRepo)

	body := `{"name":"Test","domain":"nonexistent","category":"cat"}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp.Error, "domain pack not found") {
		t.Errorf("error = %q, should mention domain pack not found", resp.Error)
	}
}

// --- Seed validation edge cases ---

func TestValidateDomainPack_CategoryAreaValidation(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.AnalysisAreas.Categories["match3"] = []models.PackAnalysisArea{
		{ID: "", Name: "Bad", Keywords: []string{"x"}, Prompt: "x"},
	}
	err := ValidateDomainPack(pack)
	if err == nil {
		t.Error("should reject category area with empty ID")
	}
}

func TestValidateDomainPack_ExplorationMissingSchemaInfo(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Prompts.Base.Exploration = "Explore {{DATASET}} with {{ANALYSIS_AREAS}}"
	err := ValidateDomainPack(pack)
	if err == nil {
		t.Error("should require {{SCHEMA_INFO}} in exploration")
	}
}

func TestValidateDomainPack_RecommendationsMissingInsightsData(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Prompts.Base.Recommendations = "Generate recommendations"
	err := ValidateDomainPack(pack)
	if err == nil {
		t.Error("should require {{INSIGHTS_DATA}} in recommendations")
	}
}

func TestValidateDomainPack_CategoryMissingName(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Categories[0].Name = ""
	err := ValidateDomainPack(pack)
	if err == nil {
		t.Error("should require category name")
	}
}

func TestMergeProfileSchema_NilBase(t *testing.T) {
	pack := &models.DomainPack{
		ProfileSchema: models.PackProfileSchema{
			Base:       nil,
			Categories: map[string]map[string]interface{}{},
		},
	}
	schema := mergeProfileSchema(pack, "match3")
	if schema == nil {
		t.Error("should return empty map for nil base")
	}
}

// --- Error path coverage ---

func TestDomainPacksHandler_Get_RepoError(t *testing.T) {
	repo := newMockDomainPackRepo()
	repo.getErr = fmt.Errorf("db error")
	h := NewDomainPacksHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/domain-packs/gaming", nil)
	req.SetPathValue("slug", "gaming")
	w := httptest.NewRecorder()
	h.Get(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestDomainPacksHandler_Update_InvalidJSON(t *testing.T) {
	repo := newMockDomainPackRepo()
	repo.add(testDomainPack("gaming", "match3"))
	h := NewDomainPacksHandler(repo)

	req := httptest.NewRequest("PUT", "/api/v1/domain-packs/gaming", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("slug", "gaming")
	w := httptest.NewRecorder()
	h.Update(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestDomainPacksHandler_Update_ValidationError(t *testing.T) {
	repo := newMockDomainPackRepo()
	repo.add(testDomainPack("gaming", "match3"))
	h := NewDomainPacksHandler(repo)

	// Pack with missing categories
	body := `{"slug":"gaming","name":"Gaming","categories":[]}`
	req := httptest.NewRequest("PUT", "/api/v1/domain-packs/gaming", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("slug", "gaming")
	w := httptest.NewRecorder()
	h.Update(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestDomainPacksHandler_Update_RepoError(t *testing.T) {
	repo := newMockDomainPackRepo()
	repo.add(testDomainPack("gaming", "match3"))
	repo.updateErr = fmt.Errorf("write conflict")
	h := NewDomainPacksHandler(repo)

	pack := testDomainPack("gaming", "match3")
	body, _ := json.Marshal(pack)
	req := httptest.NewRequest("PUT", "/api/v1/domain-packs/gaming", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("slug", "gaming")
	w := httptest.NewRecorder()
	h.Update(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestDomainPacksHandler_Import_InvalidJSON(t *testing.T) {
	repo := newMockDomainPackRepo()
	h := NewDomainPacksHandler(repo)

	req := httptest.NewRequest("POST", "/api/v1/domain-packs/import", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Import(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestDomainPacksHandler_Import_ValidationError(t *testing.T) {
	repo := newMockDomainPackRepo()
	h := NewDomainPacksHandler(repo)

	// Valid format but invalid pack (missing categories)
	body := `{"format":"decisionbox-domain-pack","format_version":1,"pack":{"slug":"bad","name":"Bad"}}`
	req := httptest.NewRequest("POST", "/api/v1/domain-packs/import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Import(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestDomainPacksHandler_Export_RepoError(t *testing.T) {
	repo := newMockDomainPackRepo()
	repo.getErr = fmt.Errorf("db error")
	h := NewDomainPacksHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/domain-packs/gaming/export", nil)
	req.SetPathValue("slug", "gaming")
	w := httptest.NewRecorder()
	h.Export(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestDomainsHandler_ListDomains_RepoError(t *testing.T) {
	repo := newMockDomainPackRepo()
	repo.listErr = fmt.Errorf("db error")
	h := NewDomainsHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/domains", nil)
	w := httptest.NewRecorder()
	h.ListDomains(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestDomainsHandler_ListCategories_RepoError(t *testing.T) {
	repo := newMockDomainPackRepo()
	repo.getErr = fmt.Errorf("db error")
	h := NewDomainsHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/domains/gaming/categories", nil)
	req.SetPathValue("domain", "gaming")
	w := httptest.NewRecorder()
	h.ListCategories(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestDomainsHandler_GetAnalysisAreas_EmptyCategory(t *testing.T) {
	repo := newMockDomainPackRepo()
	repo.add(testDomainPack("gaming", "match3"))
	h := NewDomainsHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/domains/gaming/categories/nonexistent/areas", nil)
	req.SetPathValue("domain", "gaming")
	req.SetPathValue("category", "nonexistent")
	w := httptest.NewRecorder()
	h.GetAnalysisAreas(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	// Should return only base areas
	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	areas := resp.Data.([]interface{})
	if len(areas) != 1 {
		t.Errorf("areas = %d, want 1 (base only)", len(areas))
	}
}

// --- SeedBuiltInPacks ---

func TestSeedBuiltInPacks_SeedsFromEmbed(t *testing.T) {
	repo := newMockDomainPackRepo()
	SeedBuiltInPacks(context.Background(), repo)

	packs, _ := repo.List(context.Background(), false)
	if len(packs) == 0 {
		t.Fatal("should have seeded at least one pack from embedded JSON")
	}

	// Check that gaming pack was seeded
	gaming, _ := repo.GetBySlug(context.Background(), "gaming")
	if gaming == nil {
		t.Fatal("gaming pack should be seeded")
	}
	if len(gaming.Categories) == 0 {
		t.Error("gaming pack should have categories")
	}
	if len(gaming.AnalysisAreas.Base) == 0 {
		t.Error("gaming pack should have base analysis areas")
	}
}

func TestSeedBuiltInPacks_SkipsExisting(t *testing.T) {
	repo := newMockDomainPackRepo()

	// Pre-populate with a custom gaming pack
	customGaming := testDomainPack("gaming", "custom")
	customGaming.Name = "My Custom Gaming"
	repo.add(customGaming)

	SeedBuiltInPacks(context.Background(), repo)

	// The custom pack should NOT be overwritten
	gaming, _ := repo.GetBySlug(context.Background(), "gaming")
	if gaming.Name != "My Custom Gaming" {
		t.Errorf("name = %q, want 'My Custom Gaming' (should not overwrite)", gaming.Name)
	}
}

func TestMergeProfileSchema_EmptyCategory(t *testing.T) {
	pack := &models.DomainPack{
		ProfileSchema: models.PackProfileSchema{
			Base:       map[string]interface{}{"type": "object"},
			Categories: map[string]map[string]interface{}{},
		},
	}
	schema := mergeProfileSchema(pack, "nonexistent")
	if schema["type"] != "object" {
		t.Error("should return base schema for unknown category")
	}
}
