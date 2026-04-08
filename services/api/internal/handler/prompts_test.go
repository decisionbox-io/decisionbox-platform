package handler

import (
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/models"
)

func TestSeedProjectPrompts_Basic(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	p := &models.Project{Domain: "gaming", Category: "match3"}

	SeedProjectPrompts(p, pack)

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
	if len(p.Prompts.AnalysisAreas) == 0 {
		t.Fatal("AnalysisAreas should not be empty")
	}
}

func TestSeedProjectPrompts_WithCategoryAreas(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.AnalysisAreas.Categories["match3"] = []models.PackAnalysisArea{
		{
			ID: "levels", Name: "Level Design", Description: "Match3 levels",
			Keywords: []string{"level", "stage"}, Priority: 4,
			Prompt: "Analyze levels...",
		},
		{
			ID: "boosters", Name: "Booster Usage", Description: "Match3 boosters",
			Keywords: []string{"booster", "power_up"}, Priority: 5,
			Prompt: "Analyze boosters...",
		},
	}

	p := &models.Project{Domain: "gaming", Category: "match3"}
	SeedProjectPrompts(p, pack)

	if p.Prompts == nil {
		t.Fatal("Prompts should not be nil")
	}

	// 1 base + 2 category
	if len(p.Prompts.AnalysisAreas) != 3 {
		t.Errorf("AnalysisAreas count = %d, want 3", len(p.Prompts.AnalysisAreas))
	}

	expectedAreas := []string{"test_area", "levels", "boosters"}
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

func TestSeedProjectPrompts_NilPack(t *testing.T) {
	p := &models.Project{Domain: "nonexistent", Category: "unknown"}

	SeedProjectPrompts(p, nil)

	if p.Prompts != nil {
		t.Error("Prompts should remain nil for nil pack")
	}
}

func TestSeedProjectPrompts_AreaProperties(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.AnalysisAreas.Categories["match3"] = []models.PackAnalysisArea{
		{
			ID: "levels", Name: "Level Design", Description: "Match3 levels",
			Keywords: []string{"level"}, Priority: 4,
			Prompt: "Analyze levels...",
		},
	}

	p := &models.Project{Domain: "gaming", Category: "match3"}
	SeedProjectPrompts(p, pack)

	if p.Prompts == nil {
		t.Fatal("Prompts should not be nil")
	}

	// Check base area properties
	baseArea := p.Prompts.AnalysisAreas["test_area"]
	if !baseArea.IsBase {
		t.Error("base area should have IsBase=true")
	}
	if baseArea.IsCustom {
		t.Error("seeded area should not be custom")
	}
	if !baseArea.Enabled {
		t.Error("area should be enabled")
	}
	if baseArea.Description == "" {
		t.Error("area should have description")
	}

	// Check category area properties
	catArea := p.Prompts.AnalysisAreas["levels"]
	if catArea.IsBase {
		t.Error("category area should have IsBase=false")
	}
	if catArea.IsCustom {
		t.Error("seeded area should not be custom")
	}
}

func TestSeedProjectPrompts_CategoryContext(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	p := &models.Project{Domain: "gaming", Category: "match3"}

	SeedProjectPrompts(p, pack)

	if p.Prompts == nil {
		t.Fatal("Prompts should not be nil")
	}

	// Category exploration context should be appended to base exploration
	if len(p.Prompts.Exploration) <= len(pack.Prompts.Base.Exploration) {
		t.Error("exploration should include category context")
	}
}

func TestSeedProjectPrompts_PromptContent(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	p := &models.Project{Domain: "gaming", Category: "match3"}

	SeedProjectPrompts(p, pack)

	if p.Prompts == nil {
		t.Fatal("Prompts should not be nil")
	}

	for id, area := range p.Prompts.AnalysisAreas {
		if area.Prompt == "" {
			t.Errorf("area %s has empty Prompt", id)
		}
	}
}

func TestSeedProjectPrompts_AreaKeywords(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	p := &models.Project{Domain: "gaming", Category: "match3"}

	SeedProjectPrompts(p, pack)

	if p.Prompts == nil {
		t.Fatal("Prompts should not be nil")
	}

	for id, area := range p.Prompts.AnalysisAreas {
		if len(area.Keywords) == 0 {
			t.Errorf("area %s has no Keywords", id)
		}
	}
}

func TestGetPrompts_ReturnsHandlerFunc(t *testing.T) {
	handler := GetPrompts(nil, nil)
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
