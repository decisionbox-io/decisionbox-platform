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
	got := resolveRunScoringConfig(context.Background(), projectWithRisk())

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
			got := resolveRunScoringConfig(context.Background(), tc.project)
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

	got := resolveRunScoringConfig(context.Background(), project)
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
	pinned := resolveRunScoringConfig(context.Background(), project)
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

// --- The deployment's own configuration, over the top ----------------------

// stubScoringProvider answers the registry with a fixed override, or fails.
type stubScoringProvider struct {
	out          agentplugin.ScoringOverride
	err          error
	gotProject   string
	gotWarehouse string
}

func (s *stubScoringProvider) Name() string { return "stub" }

func (s *stubScoringProvider) ScoringConfig(_ context.Context, projectID, warehouseID string) (agentplugin.ScoringOverride, error) {
	s.gotProject, s.gotWarehouse = projectID, warehouseID
	return s.out, s.err
}

// TestTheOverrideWinsOverThePackAndTheProject is the whole point of the
// layer: a deployment states a priority and it beats what the pack declared
// and what the project saved, per category, without restating either.
func TestTheOverrideWinsOverThePackAndTheProject(t *testing.T) {
	stub := &stubScoringProvider{out: agentplugin.ScoringOverride{
		CategoryWeights: map[string]float64{"compliance": 1.0},
		AmountBands:     []gomodels.MagnitudeBand{{At: 2_000_000, Band: gomodels.SeverityBandHigh}},
		AmountCurrency:  "₺",
	}}
	agentplugin.RegisterScoringConfigProvider(stub)
	t.Cleanup(agentplugin.ResetScoringForTest)

	project := projectWithRisk()
	project.ID = "proj-7"
	project.Warehouses = []models.WarehouseConfig{{ID: "wh_snowflake"}}

	got := resolveRunScoringConfig(context.Background(), project)

	want := map[string]float64{
		"compliance":   1.0, // the override's, over the pack's 3.0
		"revenue_leak": 2.5, // the project's, which the override says nothing about
	}
	if !reflect.DeepEqual(got.CategoryWeights, want) {
		t.Errorf("CategoryWeights = %v, want %v", got.CategoryWeights, want)
	}
	if len(got.AmountBands) != 1 || got.AmountBands[0].At != 2_000_000 {
		t.Errorf("AmountBands = %v, want the override's single threshold", got.AmountBands)
	}
	if got.AmountCurrency != "₺" {
		t.Errorf("AmountCurrency = %q, want ₺", got.AmountCurrency)
	}

	// Asked for the project and for the datasource whose pack supplied this
	// run's areas -- the primary. A registry handed the wrong datasource
	// would apply one warehouse's money thresholds to another's amounts.
	if stub.gotProject != "proj-7" || stub.gotWarehouse != "wh_snowflake" {
		t.Errorf("provider asked for (%q, %q), want (%q, %q)",
			stub.gotProject, stub.gotWarehouse, "proj-7", "wh_snowflake")
	}
}

// TestAProviderFailureStillPinsThePackAndProjectConfiguration.
//
// The run must complete, and it must complete WEIGHTED by whatever the pack
// and the project already said -- not unweighted. Pinning an empty
// configuration here would be the worse failure of the two: the run would
// finish, look fine, and rank its findings by nothing.
func TestAProviderFailureStillPinsThePackAndProjectConfiguration(t *testing.T) {
	agentplugin.RegisterScoringConfigProvider(&stubScoringProvider{
		out: agentplugin.ScoringOverride{CategoryWeights: map[string]float64{"compliance": 1.0}},
		err: errors.New("mongo is down"),
	})
	t.Cleanup(agentplugin.ResetScoringForTest)

	got := resolveRunScoringConfig(context.Background(), projectWithRisk())

	want := map[string]float64{"compliance": 3.0, "revenue_leak": 2.5}
	if !reflect.DeepEqual(got.CategoryWeights, want) {
		t.Errorf("CategoryWeights = %v, want the pack and project resolution %v", got.CategoryWeights, want)
	}
	if got.AmountBands != nil {
		t.Errorf("AmountBands = %v, want none from a provider that failed", got.AmountBands)
	}
}

// TestWithNoProviderThePinIsExactlyWhatItWasBefore -- the compatibility
// criterion at this seam. A community deployment, and every enterprise one
// until someone saves a weight, must pin the same document it pinned before
// this layer existed.
func TestWithNoProviderThePinIsExactlyWhatItWasBefore(t *testing.T) {
	agentplugin.ResetScoringForTest()

	got := resolveRunScoringConfig(context.Background(), projectWithRisk())

	want := map[string]float64{"compliance": 3.0, "revenue_leak": 2.5}
	if !reflect.DeepEqual(got.CategoryWeights, want) {
		t.Errorf("CategoryWeights = %v, want %v", got.CategoryWeights, want)
	}
	if got.AmountBands != nil || got.AmountCurrency != "" {
		t.Errorf("thresholds reached the pin with nothing configured: %v / %q", got.AmountBands, got.AmountCurrency)
	}
}

// TestAResumeDoesNotResolveTheOverrideAgain is §S10's second hazard, at the
// seam that would cause it: a threshold or a weight changed between two
// attempts must not reach the second one, or one run's result is half ranked
// one way and half the other.
func TestAResumeDoesNotResolveTheOverrideAgain(t *testing.T) {
	stub := &stubScoringProvider{out: agentplugin.ScoringOverride{
		CategoryWeights: map[string]float64{"compliance": 3.0},
	}}
	agentplugin.RegisterScoringConfigProvider(stub)
	t.Cleanup(agentplugin.ResetScoringForTest)

	// Attempt 1 pinned a configuration with no weights at all, which is what
	// a run started before any of this existed carries.
	store := &fakeScoringStore{run: &models.DiscoveryRun{
		ScoringConfig: &gomodels.RunScoringConfig{},
	}}

	got, err := pinRunScoringConfig(context.Background(), store, "run-1", projectWithRisk(), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || len(got.CategoryWeights) != 0 {
		t.Errorf("resume read %+v, want the empty configuration attempt 1 pinned", got)
	}
	if store.pinCall != 0 {
		t.Errorf("a resume wrote the pin %d times, want 0", store.pinCall)
	}
	if stub.gotProject != "" {
		t.Error("a resume asked the provider for a configuration it must not use")
	}
}
