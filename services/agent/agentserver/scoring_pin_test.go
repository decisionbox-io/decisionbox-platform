package agentserver

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
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
	got := resolveRunScoringConfig(projectWithRisk(), nil)

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
			got := resolveRunScoringConfig(tc.project, nil)
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

	got := resolveRunScoringConfig(project, nil)
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
	pinned := resolveRunScoringConfig(project, nil)
	if w := pinned.Areas["billing"].SeverityWeight; w == nil || *w != 0 {
		t.Errorf("billing override = %v, want the stated 0 recorded as stated", w)
	}
}

// --- Round 1 review: a resume must not pin --------------------------------

// fakeScoringStore records which of the two repository calls the seam made.
type fakeScoringStore struct {
	run     *models.DiscoveryRun
	getErr  error
	pinned  *gomodels.RunScoringConfig
	gets    int
	pinCall int
}

func (f *fakeScoringStore) GetByID(_ context.Context, _ string) (*models.DiscoveryRun, error) {
	f.gets++
	return f.run, f.getErr
}

func (f *fakeScoringStore) PinScoringConfig(_ context.Context, _ string, want gomodels.RunScoringConfig) (*gomodels.RunScoringConfig, error) {
	f.pinCall++
	f.pinned = &want
	return &want, nil
}

// TestAResumeReadsThePinAndNeverWritesOne is the round-1 finding at the seam
// that decides it.
//
// A run started before `scoring_config` existed was scored with no weights at
// all, and resuming it RESTORES its completed areas rather than re-scoring
// them. Pinning the project's current weights on the way back in would score
// the rest of that run against them and leave one result half unweighted and
// half weighted — the exact outcome the pin exists to prevent, arrived at by
// the pin itself. And because the write is conditional on absence, it would
// succeed.
func TestAResumeReadsThePinAndNeverWritesOne(t *testing.T) {
	ctx := context.Background()
	project := projectWithRisk()

	t.Run("a resume of a run that pinned nothing stays unweighted", func(t *testing.T) {
		store := &fakeScoringStore{run: &models.DiscoveryRun{}}
		got, err := pinRunScoringConfig(ctx, store, "run-1", project, true)
		if err != nil {
			t.Fatalf("pinRunScoringConfig: %v", err)
		}
		if got != nil {
			t.Errorf("got %+v, want nil — the run recorded no weights", got)
		}
		if store.pinCall != 0 {
			t.Errorf("the resume wrote a pin (%d calls); it must only read", store.pinCall)
		}
		if store.gets != 1 {
			t.Errorf("GetByID calls = %d, want 1", store.gets)
		}
	})

	t.Run("a resume of a pinned run replays that pin", func(t *testing.T) {
		pinned := &gomodels.RunScoringConfig{CategoryWeights: map[string]float64{"compliance": 1.0}}
		store := &fakeScoringStore{run: &models.DiscoveryRun{ScoringConfig: pinned}}
		got, err := pinRunScoringConfig(ctx, store, "run-1", project, true)
		if err != nil {
			t.Fatalf("pinRunScoringConfig: %v", err)
		}
		// Attempt 1's weights, NOT the project's current 3.0.
		if got == nil || got.CategoryWeights["compliance"] != 1.0 {
			t.Errorf("got %+v, want the pinned 1.0", got)
		}
		if store.pinCall != 0 {
			t.Errorf("the resume wrote a pin (%d calls)", store.pinCall)
		}
	})

	t.Run("a fresh run pins the project's configuration", func(t *testing.T) {
		store := &fakeScoringStore{run: &models.DiscoveryRun{}}
		got, err := pinRunScoringConfig(ctx, store, "run-1", project, false)
		if err != nil {
			t.Fatalf("pinRunScoringConfig: %v", err)
		}
		if store.pinCall != 1 {
			t.Fatalf("PinScoringConfig calls = %d, want 1", store.pinCall)
		}
		if got == nil || got.CategoryWeights["compliance"] != 3.0 {
			t.Errorf("got %+v, want the project's compliance weight", got)
		}
	})

	t.Run("a resume of a run that no longer exists is reported", func(t *testing.T) {
		store := &fakeScoringStore{run: nil}
		if _, err := pinRunScoringConfig(ctx, store, "run-gone", project, true); err == nil {
			t.Error("a missing run should be an error rather than an unweighted run")
		}
	})
}

// --- Deployment-wide weights from a registered provider --------------------

// countingScoringProvider is a scoring-configuration provider that counts how
// often it is asked, and can fail either way the seam has to survive.
type countingScoringProvider struct {
	weights map[string]float64
	err     error
	panics  bool
	calls   int
	project string
}

func (p *countingScoringProvider) ScoringConfig(_ context.Context, projectID string) (agentplugin.ScoringOverride, error) {
	p.calls++
	p.project = projectID
	if p.panics {
		panic("provider exploded")
	}
	return agentplugin.ScoringOverride{CategoryWeights: p.weights}, p.err
}

func (*countingScoringProvider) Name() string { return "counting" }

func registerScoringProvider(t *testing.T, p agentplugin.ScoringConfigProvider) {
	t.Helper()
	agentplugin.ResetScoringConfigProviderForTest()
	t.Cleanup(agentplugin.ResetScoringConfigProviderForTest)
	agentplugin.RegisterScoringConfigProvider(p)
}

// TestTheFirstAttemptPinsTheDeploymentLayerBetweenThePackAndTheProject -- the
// provider's weights are folded in at attempt 1, under the project's own and
// over the pack's, and are asked for this project.
func TestTheFirstAttemptPinsTheDeploymentLayerBetweenThePackAndTheProject(t *testing.T) {
	project := projectWithRisk() // pack: compliance 3, revenue_leak 2; project: revenue_leak 2.5
	project.ID = "proj-7"
	provider := &countingScoringProvider{weights: map[string]float64{
		"compliance":   1.5, // over the pack's 3
		"revenue_leak": 0.5, // under the project's 2.5, which wins
		"security":     2,   // a global id nothing else weights
	}}
	registerScoringProvider(t, provider)

	store := &fakeScoringStore{run: &models.DiscoveryRun{}}
	got, err := pinRunScoringConfig(context.Background(), store, "run-1", project, false)
	if err != nil {
		t.Fatalf("pinRunScoringConfig: %v", err)
	}
	if provider.calls != 1 || provider.project != "proj-7" {
		t.Errorf("provider asked %d times about %q, want once about proj-7", provider.calls, provider.project)
	}
	want := map[string]float64{"compliance": 1.5, "revenue_leak": 2.5, "security": 2}
	if got == nil || !reflect.DeepEqual(got.CategoryWeights, want) {
		t.Errorf("pinned %+v, want category weights %v", got, want)
	}
}

// TestAResumeNeverAsksTheProvider -- a weight changed between two attempts of
// one run must not reach the second: the resume replays the pin and the
// provider is not consulted at all.
func TestAResumeNeverAsksTheProvider(t *testing.T) {
	provider := &countingScoringProvider{weights: map[string]float64{"compliance": 0.1}}
	registerScoringProvider(t, provider)

	pinned := &gomodels.RunScoringConfig{CategoryWeights: map[string]float64{"compliance": 3}}
	store := &fakeScoringStore{run: &models.DiscoveryRun{ScoringConfig: pinned}}
	got, err := pinRunScoringConfig(context.Background(), store, "run-1", projectWithRisk(), true)
	if err != nil {
		t.Fatalf("pinRunScoringConfig: %v", err)
	}
	if provider.calls != 0 {
		t.Errorf("the resume asked the provider %d times", provider.calls)
	}
	if got == nil || got.CategoryWeights["compliance"] != 3 {
		t.Errorf("got %+v, want attempt 1's compliance 3", got)
	}
}

// TestAFailingProviderCostsTheRunOnlyItsWeights -- an error or a panic pins
// exactly what the pack and the project resolve to, and the run goes on.
func TestAFailingProviderCostsTheRunOnlyItsWeights(t *testing.T) {
	want := resolveRunScoringConfig(projectWithRisk(), nil)

	for name, provider := range map[string]*countingScoringProvider{
		"an error": {weights: map[string]float64{"compliance": 0.1}, err: errors.New("settings store unreachable")},
		"a panic":  {panics: true},
	} {
		t.Run(name, func(t *testing.T) {
			registerScoringProvider(t, provider)
			store := &fakeScoringStore{run: &models.DiscoveryRun{}}
			got, err := pinRunScoringConfig(context.Background(), store, "run-1", projectWithRisk(), false)
			if err != nil {
				t.Fatalf("the provider's failure failed the run: %v", err)
			}
			if got == nil || !reflect.DeepEqual(*got, want) {
				t.Errorf("pinned %+v, want the pack and project resolution %+v", got, want)
			}
		})
	}
}

// TestNoProviderPinsWhatV3Pinned -- with nothing registered the pin is the
// pack and project resolution, exactly; the golden pins its bytes.
func TestNoProviderPinsWhatV3Pinned(t *testing.T) {
	agentplugin.ResetScoringConfigProviderForTest()
	store := &fakeScoringStore{run: &models.DiscoveryRun{}}
	got, err := pinRunScoringConfig(context.Background(), store, "run-1", projectWithRisk(), false)
	if err != nil {
		t.Fatalf("pinRunScoringConfig: %v", err)
	}
	if want := resolveRunScoringConfig(projectWithRisk(), nil); got == nil || !reflect.DeepEqual(*got, want) {
		t.Errorf("pinned %+v, want %+v", got, want)
	}
}
