package agentserver

import (
	"reflect"
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func weightOf(v float64) *float64 { return &v }

// projectWithRisk is a project whose pack declared a taxonomy and whose areas
// map to it.
func projectWithRisk() *models.Project {
	return &models.Project{
		CategoryWeights: map[string]float64{"revenue_leak": 2.5},
		Prompts: &models.ProjectPrompts{
			RiskTaxonomy: []gomodels.RiskCategory{
				{ID: "compliance", Label: "Compliance", Weight: 3.0},
				{ID: "revenue_leak", Label: "Revenue leakage", Weight: 2.0},
			},
			AnalysisAreas: map[string]models.AnalysisAreaConfig{
				"consent":    {Name: "Consent", RiskCategories: []string{"compliance"}},
				"billing":    {Name: "Billing", RiskCategories: []string{"revenue_leak"}, SeverityWeight: weightOf(1.5)},
				"engagement": {Name: "Engagement"},
			},
		},
	}
}

// TestResolveRunScoringConfigFoldsTheProjectIntoWhatTheEngineReads.
func TestResolveRunScoringConfigFoldsTheProjectIntoWhatTheEngineReads(t *testing.T) {
	got := resolveRunScoringConfig(projectWithRisk())

	want := map[string]float64{"compliance": 3.0, "revenue_leak": 2.5}
	if !reflect.DeepEqual(got.CategoryWeights, want) {
		t.Errorf("CategoryWeights = %v, want %v (the project's 2.5 over the pack's 2.0)", got.CategoryWeights, want)
	}

	if len(got.Areas) != 2 {
		t.Fatalf("Areas = %v, want only the two areas that configure something", got.Areas)
	}
	if !reflect.DeepEqual(got.Areas["consent"].RiskCategories, []string{"compliance"}) {
		t.Errorf("consent categories = %v", got.Areas["consent"].RiskCategories)
	}
	if w := got.Areas["billing"].SeverityWeight; w == nil || *w != 1.5 {
		t.Errorf("billing override = %v, want 1.5", w)
	}
	// An area that configures nothing is left out, so the pinned document
	// records what was configured rather than listing every area the project
	// has. The engine scores an absent area at the default, which is the same
	// answer.
	if _, present := got.Areas["engagement"]; present {
		t.Error("an area with no risk configuration was pinned anyway")
	}
}

// TestResolveRunScoringConfigOnAnUnconfiguredProject -- an empty
// configuration, and not a nil one. It is still written to the run document,
// because it records that this run started with nothing configured, which is
// what a resume after someone adds a taxonomy has to honour.
func TestResolveRunScoringConfigOnAnUnconfiguredProject(t *testing.T) {
	for _, tc := range []struct {
		name    string
		project *models.Project
	}{
		{"no project at all", nil},
		{"no prompts", &models.Project{}},
		{"prompts with no risk config", &models.Project{Prompts: &models.ProjectPrompts{
			AnalysisAreas: map[string]models.AnalysisAreaConfig{"a": {Name: "A"}},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveRunScoringConfig(tc.project)
			if got.CategoryWeights != nil {
				t.Errorf("CategoryWeights = %v, want nil", got.CategoryWeights)
			}
			if got.Areas != nil {
				t.Errorf("Areas = %v, want nil", got.Areas)
			}
		})
	}
}

// TestResolveRunScoringConfigRejectsAnIllegalWeight -- the resolution runs
// through the engine's own rules, so a weight nobody could have saved through
// validation does not reach the pin either.
func TestResolveRunScoringConfigRejectsAnIllegalWeight(t *testing.T) {
	project := projectWithRisk()
	project.Prompts.RiskTaxonomy[0].Weight = 0
	project.CategoryWeights = map[string]float64{"revenue_leak": -3}

	got := resolveRunScoringConfig(project)
	if _, present := got.CategoryWeights["compliance"]; present {
		t.Error("a zero-weighted category reached the pin")
	}
	if w := got.CategoryWeights["revenue_leak"]; w != 2.0 {
		t.Errorf("revenue_leak = %v, want the pack's 2.0 — the project's -3 is not a legal weight", w)
	}

	// The per-area override is NOT filtered here, deliberately: the engine
	// ignores an illegal one when it scores, and pinning it verbatim keeps
	// the record of what the project said. Asserted so the decision is
	// visible rather than inferred from its absence.
	project.Prompts.AnalysisAreas["billing"] = models.AnalysisAreaConfig{
		Name: "Billing", RiskCategories: []string{"revenue_leak"}, SeverityWeight: weightOf(0),
	}
	pinned := resolveRunScoringConfig(project)
	if w := pinned.Areas["billing"].SeverityWeight; w == nil || *w != 0 {
		t.Errorf("billing override = %v, want the stated 0 recorded as stated", w)
	}
}
