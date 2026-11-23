package agentserver

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// What a run pins for a project, as a golden file.
//
// The pin is what a run is scored under for its whole life, resumes included,
// so a change to how it is resolved changes every future run's numbers. This
// golden was captured before any change to the resolution and committed on
// its own: a project configured the v3 way must keep pinning exactly this.

// pinGoldenProjects is each project shape worth pinning, named for what it
// exercises.
func pinGoldenProjects() []struct {
	name    string
	project *models.Project
} {
	illegal := projectWithRisk()
	illegal.Prompts.RiskTaxonomy[0].Weight = 0
	illegal.CategoryWeights = map[string]float64{"revenue_leak": -3, "data_quality": 2}

	globalOnly := &models.Project{
		CategoryWeights: map[string]float64{"compliance": 2.5},
		Prompts: &models.ProjectPrompts{
			AnalysisAreas: map[string]models.AnalysisAreaConfig{
				"consent": {Name: "Consent", RiskCategories: []string{"compliance", "security"}},
			},
		},
	}

	return []struct {
		name    string
		project *models.Project
	}{
		{"a taxonomy, project weights and an area override", projectWithRisk()},
		{"illegal weights at both levels", illegal},
		{"global categories weighted by the project alone", globalOnly},
		{"an unconfigured project", &models.Project{Prompts: &models.ProjectPrompts{
			AnalysisAreas: map[string]models.AnalysisAreaConfig{"a": {Name: "A"}},
		}}},
		{"no project", nil},
	}
}

func renderPinGolden(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, c := range pinGoldenProjects() {
		raw, err := json.MarshalIndent(pinGoldenResolve(c.project), "", "  ")
		if err != nil {
			t.Fatalf("%s: marshal: %v", c.name, err)
		}
		b.WriteString("## " + c.name + "\n")
		b.Write(raw)
		b.WriteString("\n\n")
	}
	return b.String()
}

// TestTheRunPinIsUnchangedForAV3Configuration -- byte for byte.
func TestTheRunPinIsUnchangedForAV3Configuration(t *testing.T) {
	const golden = "testdata/run_scoring_pin.golden"
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read %s: %v", golden, err)
	}
	got := renderPinGolden(t)
	if got == string(want) {
		return
	}
	actual := golden + ".actual"
	if err := os.WriteFile(actual, []byte(got), 0o600); err != nil {
		t.Logf("could not write %s: %v", actual, err)
	}
	t.Errorf("the pinned scoring configuration changed for a v3 project.\n"+
		"Compare %s with %s. If the change is deliberate, re-capture the golden and say so on the PR.", golden, actual)
}

// pinGoldenResolve is the resolution a v3 run made: the project alone.
func pinGoldenResolve(p *models.Project) gomodels.RunScoringConfig {
	return resolveRunScoringConfig(p, nil)
}
