package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/internal/models"
)

func TestSeedProjectPrompts_Gaming(t *testing.T) {
	p := &models.Project{
		Domain:   "gaming",
		Category: "match3",
	}

	SeedProjectPrompts(p)

	if p.Prompts == nil {
		t.Fatal("Prompts should not be nil after seeding")
	}

	if p.Prompts.Exploration == "" {
		t.Error("Exploration prompt should not be empty")
	}
	if p.Prompts.Recommendations == "" {
		t.Error("Recommendations prompt should not be empty")
	}
	if p.Prompts.BaseContext == "" {
		t.Error("BaseContext prompt should not be empty")
	}

	// Should have analysis areas
	if len(p.Prompts.AnalysisAreas) == 0 {
		t.Fatal("AnalysisAreas should not be empty")
	}

	// Check for expected areas (3 base + 2 match3-specific)
	expectedAreas := []string{"churn", "engagement", "monetization", "levels", "boosters"}
	for _, id := range expectedAreas {
		area, ok := p.Prompts.AnalysisAreas[id]
		if !ok {
			t.Errorf("missing analysis area: %s", id)
			continue
		}
		if area.Name == "" {
			t.Errorf("area %s has empty Name", id)
		}
		if !area.Enabled {
			t.Errorf("area %s should be enabled", id)
		}
	}
}

func TestSeedProjectPrompts_Social(t *testing.T) {
	p := &models.Project{
		Domain:   "social",
		Category: "content_sharing",
	}

	SeedProjectPrompts(p)

	if p.Prompts == nil {
		t.Fatal("Prompts should not be nil after seeding")
	}

	if p.Prompts.Exploration == "" {
		t.Error("Exploration prompt should not be empty")
	}
	if len(p.Prompts.AnalysisAreas) == 0 {
		t.Error("AnalysisAreas should not be empty")
	}
}

func TestSeedProjectPrompts_UnknownDomain(t *testing.T) {
	p := &models.Project{
		Domain:   "nonexistent-domain",
		Category: "unknown",
	}

	SeedProjectPrompts(p)

	// Should not panic, prompts should remain nil
	if p.Prompts != nil {
		t.Error("Prompts should remain nil for unknown domain")
	}
}

func TestSeedProjectPrompts_AreaProperties(t *testing.T) {
	p := &models.Project{
		Domain:   "gaming",
		Category: "match3",
	}

	SeedProjectPrompts(p)

	if p.Prompts == nil {
		t.Fatal("Prompts should not be nil")
	}

	// Check that base areas have IsBase=true
	for id, area := range p.Prompts.AnalysisAreas {
		// Base areas: churn, engagement, monetization
		// Category areas: levels, boosters
		if id == "churn" || id == "engagement" || id == "monetization" {
			if !area.IsBase {
				t.Errorf("area %s should be base", id)
			}
		}
		if id == "levels" || id == "boosters" {
			if area.IsBase {
				t.Errorf("area %s should not be base", id)
			}
		}

		// All areas should not be custom (they come from domain pack)
		if area.IsCustom {
			t.Errorf("area %s should not be custom", id)
		}

		// All areas should be enabled
		if !area.Enabled {
			t.Errorf("area %s should be enabled", id)
		}

		// All areas should have a description
		if area.Description == "" {
			t.Errorf("area %s has empty Description", id)
		}
	}
}

func TestGetPrompts_NilRepo(t *testing.T) {
	handler := GetPrompts(nil)

	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/prompts", nil)
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()

	// Will panic on nil repo — expected
	defer func() { recover() }()
	handler(w, req)
}

func TestUpdatePrompts_NilRepo(t *testing.T) {
	handler := UpdatePrompts(nil)

	req := httptest.NewRequest("PUT", "/api/v1/projects/proj-1/prompts",
		strings.NewReader(`{"exploration": "test"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()

	// Will panic on nil repo — expected
	defer func() { recover() }()
	handler(w, req)
}

func TestGetPrompts_ReturnsHandlerFunc(t *testing.T) {
	handler := GetPrompts(nil)
	if handler == nil {
		t.Fatal("GetPrompts should return non-nil handler")
	}
}

func TestUpdatePrompts_ReturnsHandlerFunc(t *testing.T) {
	handler := UpdatePrompts(nil)
	if handler == nil {
		t.Fatal("UpdatePrompts should return non-nil handler")
	}
}

func TestSeedProjectPrompts_GamingIdle(t *testing.T) {
	p := &models.Project{
		Domain:   "gaming",
		Category: "idle",
	}

	SeedProjectPrompts(p)

	if p.Prompts == nil {
		t.Fatal("Prompts should not be nil after seeding")
	}

	// Idle category has its own specific areas
	if len(p.Prompts.AnalysisAreas) == 0 {
		t.Error("AnalysisAreas should not be empty for idle category")
	}

	// Should have the 3 base areas
	baseAreas := []string{"churn", "engagement", "monetization"}
	for _, id := range baseAreas {
		if _, ok := p.Prompts.AnalysisAreas[id]; !ok {
			t.Errorf("missing base area: %s", id)
		}
	}
}

func TestSeedProjectPrompts_GamingCasual(t *testing.T) {
	p := &models.Project{
		Domain:   "gaming",
		Category: "casual",
	}

	SeedProjectPrompts(p)

	if p.Prompts == nil {
		t.Fatal("Prompts should not be nil after seeding")
	}

	if len(p.Prompts.AnalysisAreas) == 0 {
		t.Error("AnalysisAreas should not be empty for casual category")
	}
}

func TestTestConnectionHandler_TestWarehouse_NilRepo(t *testing.T) {
	h := NewTestConnectionHandler(nil, nil)

	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/test/warehouse", nil)
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()

	// Will panic on nil repo — expected
	defer func() { recover() }()
	h.TestWarehouse(w, req)
}

func TestTestConnectionHandler_TestLLM_NilRepo(t *testing.T) {
	h := NewTestConnectionHandler(nil, nil)

	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/test/llm", nil)
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()

	// Will panic on nil repo — expected
	defer func() { recover() }()
	h.TestLLM(w, req)
}

func TestPricingHandler_Update_ValidJSON_NilRepo(t *testing.T) {
	h := NewPricingHandler(nil)

	req := httptest.NewRequest("PUT", "/api/v1/pricing",
		strings.NewReader(`{"llm": {}, "warehouse": {}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	// Valid JSON passes decoding, then panics on nil repo — expected
	defer func() { recover() }()
	h.Update(w, req)

	// If we get here, it didn't panic on decoding (good)
	if w.Code == http.StatusBadRequest {
		t.Error("valid JSON should not return 400")
	}
}

func TestFeedbackHandler_Delete_NilRepo(t *testing.T) {
	h := NewFeedbackHandler(nil)

	req := httptest.NewRequest("DELETE", "/api/v1/feedback/fb-123", nil)
	req.SetPathValue("id", "fb-123")
	w := httptest.NewRecorder()

	// Will panic on nil repo — expected
	defer func() { recover() }()
	h.Delete(w, req)
}

func TestDomainsHandler_GetProfileSchema_NotFound(t *testing.T) {
	h := NewDomainsHandler()
	req := httptest.NewRequest("GET", "/api/v1/domains/nonexistent/categories/cat/schema", nil)
	req.SetPathValue("domain", "nonexistent")
	req.SetPathValue("category", "cat")
	w := httptest.NewRecorder()

	h.GetProfileSchema(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestDomainsHandler_GetAnalysisAreas_NotFound(t *testing.T) {
	h := NewDomainsHandler()
	req := httptest.NewRequest("GET", "/api/v1/domains/nonexistent/categories/cat/areas", nil)
	req.SetPathValue("domain", "nonexistent")
	req.SetPathValue("category", "cat")
	w := httptest.NewRecorder()

	h.GetAnalysisAreas(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestDomainsHandler_GetProfileSchema_SocialDomain(t *testing.T) {
	h := NewDomainsHandler()
	req := httptest.NewRequest("GET", "/api/v1/domains/social/categories/content_sharing/schema", nil)
	req.SetPathValue("domain", "social")
	req.SetPathValue("category", "content_sharing")
	w := httptest.NewRecorder()

	h.GetProfileSchema(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestDomainsHandler_GetAnalysisAreas_SocialDomain(t *testing.T) {
	h := NewDomainsHandler()
	req := httptest.NewRequest("GET", "/api/v1/domains/social/categories/content_sharing/areas", nil)
	req.SetPathValue("domain", "social")
	req.SetPathValue("category", "content_sharing")
	w := httptest.NewRecorder()

	h.GetAnalysisAreas(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestDomainsHandler_ListCategories_Social(t *testing.T) {
	h := NewDomainsHandler()
	req := httptest.NewRequest("GET", "/api/v1/domains/social/categories", nil)
	req.SetPathValue("domain", "social")
	w := httptest.NewRecorder()

	h.ListCategories(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestSeedProjectPrompts_PromptContent(t *testing.T) {
	p := &models.Project{
		Domain:   "gaming",
		Category: "match3",
	}

	SeedProjectPrompts(p)

	if p.Prompts == nil {
		t.Fatal("Prompts should not be nil")
	}

	// Verify analysis areas have prompts populated
	for id, area := range p.Prompts.AnalysisAreas {
		if area.Prompt == "" {
			t.Errorf("area %s has empty Prompt", id)
		}
	}
}

func TestSeedProjectPrompts_AreaKeywords(t *testing.T) {
	p := &models.Project{
		Domain:   "gaming",
		Category: "match3",
	}

	SeedProjectPrompts(p)

	if p.Prompts == nil {
		t.Fatal("Prompts should not be nil")
	}

	for id, area := range p.Prompts.AnalysisAreas {
		if len(area.Keywords) == 0 {
			t.Errorf("area %s has no Keywords", id)
		}
	}
}
