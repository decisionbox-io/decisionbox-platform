package models

import (
	"strings"
	"testing"
)

// testDomainPack returns a minimal valid pack. Test cases mutate one
// field, call ValidateDomainPack, and assert on the result.
func testDomainPack(slug, category string) *DomainPack {
	return &DomainPack{
		Slug:        slug,
		Name:        slug,
		IsPublished: true,
		Categories: []PackCategory{
			{ID: category, Name: category, Description: "test category"},
		},
		Prompts: PackPrompts{
			Base: BasePrompts{
				BaseContext:     "Context: {{PROFILE}}\n{{PREVIOUS_CONTEXT}}",
				Exploration:     "Explore {{DATASET}} using {{SCHEMA_INFO}} areas: {{ANALYSIS_AREAS}}",
				Recommendations: "Recommend based on {{INSIGHTS_DATA}} summary: {{INSIGHTS_SUMMARY}} date: {{DISCOVERY_DATE}}",
			},
			Categories: map[string]CategoryPrompts{
				category: {ExplorationContext: "Category-specific context for " + category},
			},
		},
		AnalysisAreas: PackAnalysisAreas{
			Base: []PackAnalysisArea{
				{
					ID: "test_area", Name: "Test Area", Description: "Test analysis area",
					Keywords: []string{"test"}, Priority: 1,
					Prompt: "Analyze {{DATASET}}: {{QUERY_RESULTS}}",
				},
			},
			Categories: map[string][]PackAnalysisArea{},
		},
		ProfileSchema: PackProfileSchema{
			Base:       map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
			Categories: map[string]map[string]interface{}{},
		},
	}
}

func TestValidateDomainPack_Valid(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	if err := ValidateDomainPack(pack); err != nil {
		t.Errorf("valid pack should pass: %v", err)
	}
}

func TestValidateDomainPack_MissingSlug(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Slug = ""
	if err := ValidateDomainPack(pack); err == nil {
		t.Error("should require slug")
	}
}

func TestValidateDomainPack_SlugRegexShape(t *testing.T) {
	cases := []struct {
		name  string
		slug  string
		valid bool
	}{
		{"valid lowercase", "gaming", true},
		{"valid with digits", "1stplace", true},
		{"valid with hyphens", "e-commerce", true},
		{"valid all digits", "12345", true},
		{"too short", "a", false},
		{"trailing hyphen", "gaming-", false},
		{"leading hyphen", "-gaming", false},
		{"uppercase", "Gaming", false},
		{"contains space", "two words", false},
		{"contains underscore", "snake_case", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pack := testDomainPack(tc.slug, "match3")
			err := ValidateDomainPack(pack)
			if tc.valid && err != nil {
				t.Errorf("%q should be valid, got %v", tc.slug, err)
			}
			if !tc.valid && err == nil {
				t.Errorf("%q should be rejected", tc.slug)
			}
		})
	}
}

func TestValidateDomainPack_MissingName(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Name = ""
	if err := ValidateDomainPack(pack); err == nil {
		t.Error("should require name")
	}
}

func TestValidateDomainPack_MissingCategories(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Categories = nil
	if err := ValidateDomainPack(pack); err == nil {
		t.Error("should require at least one category")
	}
}

func TestValidateDomainPack_CategoryMissingID(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Categories[0].ID = ""
	if err := ValidateDomainPack(pack); err == nil {
		t.Error("should require category id")
	}
}

func TestValidateDomainPack_CategoryMissingName(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Categories[0].Name = ""
	if err := ValidateDomainPack(pack); err == nil {
		t.Error("should require category name")
	}
}

func TestValidateDomainPack_MissingBaseContext(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Prompts.Base.BaseContext = ""
	if err := ValidateDomainPack(pack); err == nil {
		t.Error("should require base_context")
	}
}

func TestValidateDomainPack_MissingExploration(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Prompts.Base.Exploration = ""
	if err := ValidateDomainPack(pack); err == nil {
		t.Error("should require exploration")
	}
}

func TestValidateDomainPack_MissingRecommendations(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Prompts.Base.Recommendations = ""
	if err := ValidateDomainPack(pack); err == nil {
		t.Error("should require recommendations")
	}
}

func TestValidateDomainPack_MissingProfileTemplate(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Prompts.Base.BaseContext = "no profile variable"
	err := ValidateDomainPack(pack)
	if err == nil || !strings.Contains(err.Error(), "{{PROFILE}}") {
		t.Errorf("should require {{PROFILE}} in base_context, got %v", err)
	}
}

func TestValidateDomainPack_ExplorationMissingDataset(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Prompts.Base.Exploration = "Explore using {{SCHEMA_INFO}} areas: {{ANALYSIS_AREAS}}"
	err := ValidateDomainPack(pack)
	if err == nil || !strings.Contains(err.Error(), "{{DATASET}}") {
		t.Errorf("should require {{DATASET}} in exploration, got %v", err)
	}
}

func TestValidateDomainPack_ExplorationMissingSchemaInfo(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Prompts.Base.Exploration = "Explore {{DATASET}} with {{ANALYSIS_AREAS}}"
	err := ValidateDomainPack(pack)
	if err == nil || !strings.Contains(err.Error(), "{{SCHEMA_INFO}}") {
		t.Errorf("should require {{SCHEMA_INFO}} in exploration, got %v", err)
	}
}

func TestValidateDomainPack_ExplorationMissingAnalysisAreas(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Prompts.Base.Exploration = "Explore {{DATASET}} using {{SCHEMA_INFO}}"
	err := ValidateDomainPack(pack)
	if err == nil || !strings.Contains(err.Error(), "{{ANALYSIS_AREAS}}") {
		t.Errorf("should require {{ANALYSIS_AREAS}} in exploration, got %v", err)
	}
}

func TestValidateDomainPack_RecommendationsMissingInsightsData(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Prompts.Base.Recommendations = "Generate recommendations"
	err := ValidateDomainPack(pack)
	if err == nil || !strings.Contains(err.Error(), "{{INSIGHTS_DATA}}") {
		t.Errorf("should require {{INSIGHTS_DATA}} in recommendations, got %v", err)
	}
}

func TestValidateDomainPack_MissingAnalysisAreas(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.AnalysisAreas.Base = nil
	if err := ValidateDomainPack(pack); err == nil {
		t.Error("should require at least one base analysis area")
	}
}

func TestValidateDomainPack_AreaMissingID(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.AnalysisAreas.Base[0].ID = ""
	if err := ValidateDomainPack(pack); err == nil {
		t.Error("should require id in analysis area")
	}
}

func TestValidateDomainPack_AreaMissingName(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.AnalysisAreas.Base[0].Name = ""
	if err := ValidateDomainPack(pack); err == nil {
		t.Error("should require name in analysis area")
	}
}

func TestValidateDomainPack_AreaMissingPrompt(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.AnalysisAreas.Base[0].Prompt = ""
	if err := ValidateDomainPack(pack); err == nil {
		t.Error("should require prompt in analysis area")
	}
}

func TestValidateDomainPack_AreaMissingKeywords(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.AnalysisAreas.Base[0].Keywords = nil
	if err := ValidateDomainPack(pack); err == nil {
		t.Error("should require keywords in analysis area")
	}
}

func TestValidateDomainPack_CategoryAreaValidation(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.AnalysisAreas.Categories["match3"] = []PackAnalysisArea{
		{ID: "", Name: "Bad", Keywords: []string{"x"}, Prompt: "x"},
	}
	if err := ValidateDomainPack(pack); err == nil {
		t.Error("should reject category area with empty ID")
	}
}
