package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/database"
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

// TestSeedProjectPromptsCarriesTheRiskConfiguration -- the taxonomy, the area
// mappings and the per-area override reach the project.
//
// This is the only moment they can. A project snapshots its area
// configuration at creation and pack seeds are never re-applied on upgrade,
// so a field this function drops is one the project can never acquire -- and
// the acceptance criterion for the whole feature is a FRESHLY CREATED project,
// for exactly that reason.
func TestSeedProjectPromptsCarriesTheRiskConfiguration(t *testing.T) {
	override := 1.5
	pack := testDomainPack("healthcare", "clinic")
	pack.RiskTaxonomy = []models.PackRiskCategory{
		{ID: "compliance", Label: "Regulatory compliance", Weight: 3.0},
		{ID: "revenue_leak", Label: "Revenue leakage", Weight: 2.0},
	}
	pack.AnalysisAreas.Base[0].RiskCategories = []string{"compliance", "data_quality"}
	pack.AnalysisAreas.Base[0].SeverityWeight = &override
	pack.AnalysisAreas.Categories["clinic"] = []models.PackAnalysisArea{{
		ID: "billing", Name: "Billing", Description: "Billing analysis",
		Keywords: []string{"billing"}, Priority: 2,
		Prompt:         "Analyze billing: {{QUERY_RESULTS}}",
		RiskCategories: []string{"revenue_leak"},
	}}

	p := &models.Project{Domain: "healthcare", Category: "clinic"}
	SeedProjectPrompts(p, pack)

	if p.Prompts == nil {
		t.Fatal("Prompts should not be nil")
	}
	if len(p.Prompts.RiskTaxonomy) != 2 {
		t.Fatalf("RiskTaxonomy length = %d, want 2 — the project cannot acquire it later",
			len(p.Prompts.RiskTaxonomy))
	}
	if p.Prompts.RiskTaxonomy[0].ID != "compliance" || p.Prompts.RiskTaxonomy[0].Weight != 3.0 {
		t.Errorf("RiskTaxonomy[0] = %+v, want compliance at 3.0", p.Prompts.RiskTaxonomy[0])
	}

	base, ok := p.Prompts.AnalysisAreas["test_area"]
	if !ok {
		t.Fatal("the base area was not seeded")
	}
	if len(base.RiskCategories) != 2 || base.RiskCategories[0] != "compliance" {
		t.Errorf("base area RiskCategories = %v, want [compliance data_quality]", base.RiskCategories)
	}
	if base.SeverityWeight == nil || *base.SeverityWeight != override {
		t.Errorf("base area SeverityWeight = %v, want %v", base.SeverityWeight, override)
	}

	cat, ok := p.Prompts.AnalysisAreas["billing"]
	if !ok {
		t.Fatal("the per-category area was not seeded")
	}
	if len(cat.RiskCategories) != 1 || cat.RiskCategories[0] != "revenue_leak" {
		t.Errorf("category area RiskCategories = %v, want [revenue_leak]", cat.RiskCategories)
	}
	// Not overridden, so nil rather than a zero that would be a rejected
	// weight if anyone stated it.
	if cat.SeverityWeight != nil {
		t.Errorf("category area SeverityWeight = %v, want nil", *cat.SeverityWeight)
	}
}

// TestSeedProjectPromptsLeavesAnUnconfiguredPackAlone -- a pack with no risk
// configuration seeds a project with none, rather than with empty slices that
// would read as "configured, to nothing".
func TestSeedProjectPromptsLeavesAnUnconfiguredPackAlone(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	p := &models.Project{Domain: "gaming", Category: "match3"}

	SeedProjectPrompts(p, pack)

	if p.Prompts.RiskTaxonomy != nil {
		t.Errorf("RiskTaxonomy = %v, want nil", p.Prompts.RiskTaxonomy)
	}
	area := p.Prompts.AnalysisAreas["test_area"]
	if area.RiskCategories != nil {
		t.Errorf("RiskCategories = %v, want nil", area.RiskCategories)
	}
	if area.SeverityWeight != nil {
		t.Errorf("SeverityWeight = %v, want nil", *area.SeverityWeight)
	}
}

// --- Round 1 review: the prompts route is the weight editor's write -------

// recordingProjectRepo is stubProjectRepo with the Update it received.
type recordingProjectRepo struct {
	stubProjectRepo
	updated *models.Project
}

func (r *recordingProjectRepo) Update(_ context.Context, _ string, p *models.Project) error {
	r.updated = p
	return nil
}

func putPrompts(t *testing.T, repo database.ProjectRepo, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/projects/p1/prompts", strings.NewReader(body))
	req.SetPathValue("id", "p1")
	w := httptest.NewRecorder()
	UpdatePrompts(repo)(w, req)
	return w
}

// TestUpdatePromptsRefusesAnInvalidRiskConfiguration.
//
// The playbook page's weight editor writes through this route. The engine
// IGNORES an illegal weight and an unknown category id, so accepting them
// stores nothing wrong — it just answers 200 to a reviewer who typed 4.0 and
// then scores the area at 1.0 forever. A save that silently does nothing is
// worse than a rejected one.
func TestUpdatePromptsRefusesAnInvalidRiskConfiguration(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "an override above the ceiling",
			body: `{"analysis_areas":{"consent":{"name":"Consent","severity_weight":4}}}`,
			want: "severity_weight",
		},
		{
			name: "a stated zero override",
			body: `{"analysis_areas":{"consent":{"name":"Consent","severity_weight":0}}}`,
			want: "severity_weight",
		},
		{
			name: "a category id nothing declares",
			body: `{"analysis_areas":{"consent":{"name":"Consent","risk_categories":["invented_here"]}}}`,
			want: "invented_here",
		},
		{
			name: "a taxonomy weight above the ceiling",
			body: `{"risk_taxonomy":[{"id":"compliance","label":"Compliance","weight":9}]}`,
			want: "outside",
		},
		{
			name: "a taxonomy id that is not a slug",
			body: `{"risk_taxonomy":[{"id":"Compliance","label":"Compliance","weight":2}]}`,
			want: "slug",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &recordingProjectRepo{stubProjectRepo: stubProjectRepo{project: &models.Project{ID: "p1"}}}
			w := putPrompts(t, repo, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.want) {
				t.Errorf("the response does not say what is wrong (%q): %s", tc.want, w.Body.String())
			}
			if repo.updated != nil {
				t.Error("the project was written despite the refusal")
			}
		})
	}
}

// TestUpdatePromptsAcceptsAValidRiskConfiguration -- including the two cases
// that must NOT be refused: a global category id on a project whose pack
// declares no taxonomy, and prompts carrying no risk configuration at all.
func TestUpdatePromptsAcceptsAValidRiskConfiguration(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "a declared taxonomy and a mapped area",
			body: `{"risk_taxonomy":[{"id":"compliance","label":"Compliance","weight":3}],` +
				`"analysis_areas":{"consent":{"name":"Consent","risk_categories":["compliance"],"severity_weight":1.5}}}`,
		},
		{
			name: "a global id with no taxonomy",
			body: `{"analysis_areas":{"consent":{"name":"Consent","risk_categories":["data_quality"]}}}`,
		},
		{
			name: "no risk configuration at all",
			body: `{"exploration":"e","analysis_areas":{"consent":{"name":"Consent"}}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &recordingProjectRepo{stubProjectRepo: stubProjectRepo{project: &models.Project{ID: "p1"}}}
			w := putPrompts(t, repo, tc.body)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
			}
			if repo.updated == nil {
				t.Fatal("the project was not written")
			}
		})
	}
}

// TestUpdatePromptsValidatesAgainstTheProjectsOwnCategoryWeights -- the two
// halves of the configuration are checked against each other, so a weight
// already stored on the project cannot be left invalid by a prompts write.
func TestUpdatePromptsValidatesAgainstTheProjectsOwnCategoryWeights(t *testing.T) {
	repo := &recordingProjectRepo{stubProjectRepo: stubProjectRepo{project: &models.Project{
		ID:              "p1",
		CategoryWeights: map[string]float64{"compliance": 9},
	}}}
	w := putPrompts(t, repo, `{"analysis_areas":{"consent":{"name":"Consent"}}}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (the project carries an illegal category weight); body = %s",
			w.Code, w.Body.String())
	}
}
