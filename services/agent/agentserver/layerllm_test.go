package agentserver

import (
	"context"
	"errors"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// --- counting fakes ---

// countingProviderStub is a POINTER type with a field, so two instances are
// distinguishable: a zero-size struct value would compare equal and make
// "did these layers share a provider?" unanswerable.
type countingProviderStub struct{ id int }

func (*countingProviderStub) Chat(context.Context, gollm.ChatRequest) (*gollm.ChatResponse, error) {
	return &gollm.ChatResponse{}, nil
}
func (*countingProviderStub) Validate(context.Context) error { return nil }

// counters records how many times each expensive thing was built, so the
// dedupe guarantees are asserted rather than read out of the code.
type counters struct {
	providerBuilds int
	budgetResolves int
	windowReads    int
	// specs is every spec a provider was built for, in order.
	specs []llmlayer.LayerSpec
}

func (c *counters) deps() layerSetDeps {
	return layerSetDeps{
		buildProvider: func(_ context.Context, spec llmlayer.LayerSpec) (gollm.Provider, gollm.ProviderConfig, error) {
			c.providerBuilds++
			c.specs = append(c.specs, spec)
			return &countingProviderStub{id: c.providerBuilds}, llmlayer.ProviderConfig(spec, llmlayer.Credential{}, llmlayer.BuildOptions{}), nil
		},
		resolveBudget: func(_ context.Context, _ gollm.Provider, spec llmlayer.LayerSpec, persisted int) (int, int) {
			c.budgetResolves++
			if persisted > 0 {
				return persisted, 8000
			}
			return 100000, 8000
		},
		persistedWindow: func(_ context.Context, _ llmlayer.LayerSpec) int {
			c.windowReads++
			return 0
		},
	}
}

func singleModelProject() *models.Project {
	p := &models.Project{}
	p.LLM = models.LLMConfig{Provider: "bedrock", Model: "opus", Config: map[string]string{"region": "us-east-1"}}
	p.SeedLLMLayersFromLegacy()
	return p
}

// --- the standing guarantee ---

func TestEveryLayerOnTheDefaultCostsOneProviderOneProbeOneBudget(t *testing.T) {
	c := &counters{}
	set, err := buildLayerSet(context.Background(), layerSetInput{
		project:   singleModelProject(),
		projectID: "p1",
		runID:     "r1",
		gate:      true,
		deps:      c.deps(),
	})
	if err != nil {
		t.Fatalf("buildLayerSet: %v", err)
	}

	if c.providerBuilds != 1 {
		t.Errorf("built %d providers for a single-model project, want 1 — each one is an HTTP client, a connection pool and an AWS/GCP credential load", c.providerBuilds)
	}
	if c.budgetResolves != 1 {
		t.Errorf("resolved %d budgets, want 1 — each resolution includes an 8-second live model-info probe", c.budgetResolves)
	}
	if c.windowReads != 1 {
		t.Errorf("read %d persisted windows, want 1", c.windowReads)
	}

	// Clients, by contrast, ARE per layer: they are small, and that is what
	// makes per-layer attribution and per-layer reasoning free.
	seen := map[any]bool{}
	for _, layer := range append([]string{llmlayer.DefaultLayer}, llmlayer.Keys()...) {
		l := set.For(layer)
		if l == nil {
			t.Fatalf("For(%q) returned nil; it must never", layer)
		}
		if l.LLM == nil || l.Client == nil {
			t.Fatalf("layer %q has no provider or client", layer)
		}
		seen[l.Client] = true
	}
	// Only the layers that were BUILT get their own client; the rest fall back
	// to the default's. With no layer list every layer is built.
	if len(seen) != len(llmlayer.Keys())+1 {
		t.Errorf("distinct clients = %d, want one per built layer (%d)", len(seen), len(llmlayer.Keys())+1)
	}
}

func TestOneProviderServesEveryModelOnAnEntry(t *testing.T) {
	// ai.Client sets ChatRequest.Model on every call and every provider treats
	// its construction-time model as a fallback only, so a project running
	// four different models on ONE entry still builds one provider — but four
	// budget resolutions, because the probe is per model.
	p := &models.Project{}
	p.LLM = models.LLMConfig{Provider: "bedrock", Model: "opus"}
	p.LLMProviders = []llmlayer.ProviderEntry{{ID: llmlayer.DefaultProviderID, Provider: "bedrock"}}
	p.LayerModels = map[string]llmlayer.LayerModel{
		llmlayer.DefaultLayer:         {ProviderID: llmlayer.DefaultProviderID, Model: "opus"},
		llmlayer.LayerExploration:     {ProviderID: llmlayer.DefaultProviderID, Model: "haiku"},
		llmlayer.LayerSQLRepair:       {ProviderID: llmlayer.DefaultProviderID, Model: "nova"},
		llmlayer.LayerRecommendations: {ProviderID: llmlayer.DefaultProviderID, Model: "sonnet"},
	}
	p.LLMConfigVersion = llmlayer.ConfigVersion

	c := &counters{}
	if _, err := buildLayerSet(context.Background(), layerSetInput{
		project: p, projectID: "p1", gate: true, deps: c.deps(),
	}); err != nil {
		t.Fatalf("buildLayerSet: %v", err)
	}
	if c.providerBuilds != 1 {
		t.Errorf("built %d providers for four models on one entry, want 1", c.providerBuilds)
	}
	if c.budgetResolves != 4 {
		t.Errorf("resolved %d budgets for four distinct models, want 4 — the catalog lookup and the live probe are per model", c.budgetResolves)
	}
}

func TestMixedPipelineBuildsOneProviderPerEntry(t *testing.T) {
	// The benchmark's recommended configuration: DeepSeek exploring via
	// LiteLLM, Opus analysing and validating on Bedrock, Haiku on the small
	// layers.
	p := &models.Project{}
	p.LLM = models.LLMConfig{Provider: "bedrock", Model: "opus"}
	p.LLMProviders = []llmlayer.ProviderEntry{
		{ID: llmlayer.DefaultProviderID, Provider: "bedrock", Config: map[string]string{"region": "us-east-1"}},
		{ID: "deepinfra", Provider: "litellm", Config: map[string]string{"base_url": "https://deepinfra.test/v1"}},
	}
	p.LayerModels = map[string]llmlayer.LayerModel{
		llmlayer.DefaultLayer:         {ProviderID: llmlayer.DefaultProviderID, Model: "opus"},
		llmlayer.LayerExploration:     {ProviderID: "deepinfra", Model: "deepseek-v4.1-flash"},
		llmlayer.LayerRecommendations: {ProviderID: llmlayer.DefaultProviderID, Model: "haiku"},
		llmlayer.LayerReflection:      {ProviderID: llmlayer.DefaultProviderID, Model: "haiku"},
		llmlayer.LayerSQLRepair:       {ProviderID: llmlayer.DefaultProviderID, Model: "haiku"},
	}
	p.LLMConfigVersion = llmlayer.ConfigVersion

	c := &counters{}
	set, err := buildLayerSet(context.Background(), layerSetInput{
		project: p, projectID: "p1", layers: discoveryLayers, gate: true, deps: c.deps(),
	})
	if err != nil {
		t.Fatalf("buildLayerSet: %v", err)
	}
	if c.providerBuilds != 2 {
		t.Errorf("built %d providers for two entries, want 2", c.providerBuilds)
	}
	// opus (default/validation/analysis), deepseek (exploration), haiku
	// (recommendations/reflection/sql_repair).
	if c.budgetResolves != 3 {
		t.Errorf("resolved %d budgets, want 3 (one per distinct model)", c.budgetResolves)
	}
	if got := set.For(llmlayer.LayerExploration); got.Provider != "litellm" || got.Model != "deepseek-v4.1-flash" {
		t.Errorf("exploration = %s/%s, want litellm/deepseek-v4.1-flash", got.Provider, got.Model)
	}
	if got := set.For(llmlayer.LayerAnalysis); got.Provider != "bedrock" || got.Model != "opus" {
		t.Errorf("analysis = %s/%s, want bedrock/opus", got.Provider, got.Model)
	}
	// The two entries' connection config must not bleed into each other.
	if set.For(llmlayer.LayerExploration).LLM == set.For(llmlayer.LayerAnalysis).LLM {
		t.Error("two entries shared one provider instance")
	}
}

func TestOneGatewayEntryWithAnAliasPerLayerIsOneProviderAndManyBudgets(t *testing.T) {
	// Cloud's shape: a single gateway entry, a distinct alias per layer. One
	// provider, but a budget and a probe per alias — inherent to per-layer
	// aliases, not a defect, so it is pinned rather than left to be
	// "optimised" later into one shared budget that would be wrong.
	p := &models.Project{}
	p.LLM = models.LLMConfig{Provider: "openai", Model: "decisionbox-analysis-model"}
	p.LLMProviders = []llmlayer.ProviderEntry{{
		ID: llmlayer.DefaultProviderID, Provider: "openai",
		Config: map[string]string{"base_url": "https://gw.test/v1"},
	}}
	p.LayerModels = map[string]llmlayer.LayerModel{
		llmlayer.DefaultLayer: {ProviderID: llmlayer.DefaultProviderID, Model: "decisionbox-analysis-model"},
	}
	for _, layer := range llmlayer.Keys() {
		p.LayerModels[layer] = llmlayer.LayerModel{ProviderID: llmlayer.DefaultProviderID, Model: "decisionbox-" + layer + "-model"}
	}
	p.LLMConfigVersion = llmlayer.ConfigVersion

	c := &counters{}
	if _, err := buildLayerSet(context.Background(), layerSetInput{
		project: p, projectID: "p1", gate: true, deps: c.deps(),
	}); err != nil {
		t.Fatalf("buildLayerSet: %v", err)
	}
	if c.providerBuilds != 1 {
		t.Errorf("built %d providers for one gateway entry, want 1", c.providerBuilds)
	}
	// One per distinct alias — and the default row shares the analysis
	// alias, which is the whole point of AI_GATEWAY_ANALYSIS_MODEL staying
	// authoritative for both.
	if want := len(llmlayer.Keys()); c.budgetResolves != want {
		t.Errorf("resolved %d budgets, want one per distinct alias (%d)", c.budgetResolves, want)
	}
}

func TestPromptCachingDifferingOnOneEntryBuildsTwoProviders(t *testing.T) {
	// The D2 consequence, pinned so it is a known cost rather than a
	// surprise: prompt caching is read at CONSTRUCTION by the four
	// Anthropic-wire providers, so it is part of the merged config.
	p := &models.Project{}
	p.LLM = models.LLMConfig{Provider: "bedrock", Model: "opus"}
	p.LLMProviders = []llmlayer.ProviderEntry{{ID: llmlayer.DefaultProviderID, Provider: "bedrock"}}
	p.LayerModels = map[string]llmlayer.LayerModel{
		llmlayer.DefaultLayer:   {ProviderID: llmlayer.DefaultProviderID, Model: "opus"},
		llmlayer.LayerSQLRepair: {ProviderID: llmlayer.DefaultProviderID, Model: "opus", Config: map[string]string{gollm.PromptCacheKey: "false"}},
	}
	p.LLMConfigVersion = llmlayer.ConfigVersion

	c := &counters{}
	set, err := buildLayerSet(context.Background(), layerSetInput{
		project: p, projectID: "p1", layers: []string{llmlayer.LayerSQLRepair, llmlayer.LayerAnalysis}, gate: true, deps: c.deps(),
	})
	if err != nil {
		t.Fatalf("buildLayerSet: %v", err)
	}
	if c.providerBuilds != 2 {
		t.Errorf("built %d providers, want 2 — the two layers must not share a provider built with the wrong caching setting", c.providerBuilds)
	}
	if set.For(llmlayer.LayerSQLRepair).LLM == set.For(llmlayer.LayerAnalysis).LLM {
		t.Error("the caching-off layer shares a provider instance with a caching-on sibling")
	}
}

// --- fallbacks and propagation ---

func TestForNeverReturnsNilAndFallsBackToTheDefault(t *testing.T) {
	c := &counters{}
	set, err := buildLayerSet(context.Background(), layerSetInput{
		project: singleModelProject(), projectID: "p1", layers: []string{llmlayer.LayerAnalysis}, gate: true, deps: c.deps(),
	})
	if err != nil {
		t.Fatalf("buildLayerSet: %v", err)
	}
	// A layer this set did not build — a run that does not use it, or a typo.
	l := set.For("not_a_layer")
	if l == nil {
		t.Fatal("For() must never return nil; a run that dies on a layer-key typo is worse than one on the default model")
	}
	if l.Layer != llmlayer.DefaultLayer {
		t.Errorf("fallback layer = %q, want the default", l.Layer)
	}
}

func TestALayerNamingAMissingEntryStillBuilds(t *testing.T) {
	p := &models.Project{}
	p.LLM = models.LLMConfig{Provider: "bedrock", Model: "opus"}
	p.LLMProviders = []llmlayer.ProviderEntry{{ID: llmlayer.DefaultProviderID, Provider: "bedrock"}}
	p.LayerModels = map[string]llmlayer.LayerModel{
		llmlayer.DefaultLayer:     {ProviderID: llmlayer.DefaultProviderID, Model: "opus"},
		llmlayer.LayerExploration: {ProviderID: "deleted", Model: "deepseek"},
	}
	p.LLMConfigVersion = llmlayer.ConfigVersion

	c := &counters{}
	set, err := buildLayerSet(context.Background(), layerSetInput{
		project: p, projectID: "p1", layers: []string{llmlayer.LayerExploration}, gate: true, deps: c.deps(),
	})
	if err != nil {
		t.Fatalf("a layer naming a deleted entry must fall back, not fail the run: %v", err)
	}
	if got := set.For(llmlayer.LayerExploration); got.ProviderID != llmlayer.DefaultProviderID {
		t.Errorf("exploration resolved to entry %q, want the default", got.ProviderID)
	}
}

func TestTestModeReachesEveryClient(t *testing.T) {
	// Test mode reached only the single client before per-layer models.
	// Applying it to one now would leave sixteen layers running unlimited.
	c := &counters{}
	set, err := buildLayerSet(context.Background(), layerSetInput{
		project: singleModelProject(), projectID: "p1", runID: "r1", gate: true, testMode: true, deps: c.deps(),
	})
	if err != nil {
		t.Fatalf("buildLayerSet: %v", err)
	}
	for _, layer := range append([]string{llmlayer.DefaultLayer}, llmlayer.Keys()...) {
		if !set.For(layer).Client.TestMode() {
			t.Errorf("layer %q did not get test mode", layer)
		}
	}
}

func TestEachLayersClientIsTaggedWithItsPhase(t *testing.T) {
	c := &counters{}
	set, err := buildLayerSet(context.Background(), layerSetInput{
		project: singleModelProject(), projectID: "p1", runID: "r1", gate: true, deps: c.deps(),
	})
	if err != nil {
		t.Fatalf("buildLayerSet: %v", err)
	}
	// Most layer keys ARE the phase constant, so attribution is free.
	if got := set.For(llmlayer.LayerExploration).Client.Phase(); got != models.PhaseExploration {
		t.Errorf("exploration client phase = %q, want %q", got, models.PhaseExploration)
	}
	// The validation pair is the exception: one phase, two models.
	for _, layer := range []string{llmlayer.LayerValidationVerifier, llmlayer.LayerValidationRefuter} {
		if got := set.For(layer).Client.Phase(); got != models.PhaseValidation {
			t.Errorf("%s client phase = %q, want %q", layer, got, models.PhaseValidation)
		}
	}
}

func TestPerLayerReasoningReachesTheClient(t *testing.T) {
	p := &models.Project{}
	p.LLM = models.LLMConfig{Provider: "bedrock", Model: "opus"}
	p.LLMProviders = []llmlayer.ProviderEntry{{ID: llmlayer.DefaultProviderID, Provider: "bedrock"}}
	p.LayerModels = map[string]llmlayer.LayerModel{
		llmlayer.DefaultLayer:     {ProviderID: llmlayer.DefaultProviderID, Model: "opus"},
		llmlayer.LayerExploration: {ProviderID: llmlayer.DefaultProviderID, Model: "opus", Config: map[string]string{gollm.ReasoningEnabledKey: "true"}},
	}
	p.LLMConfigVersion = llmlayer.ConfigVersion

	c := &counters{}
	set, err := buildLayerSet(context.Background(), layerSetInput{
		project: p, projectID: "p1", layers: []string{llmlayer.LayerExploration, llmlayer.LayerAnalysis}, gate: true, deps: c.deps(),
	})
	if err != nil {
		t.Fatalf("buildLayerSet: %v", err)
	}
	if !set.For(llmlayer.LayerExploration).Reasoning {
		t.Error("exploration must carry its own reasoning opt-in")
	}
	if set.For(llmlayer.LayerAnalysis).Reasoning {
		t.Error("analysis must not inherit another layer's reasoning setting")
	}
}

func TestGateRefusesAnUnconfiguredProject(t *testing.T) {
	p := &models.Project{}
	p.LLM = models.LLMConfig{Provider: "bedrock", Model: "opus"}
	// Deliberately not seeded: version 0.

	c := &counters{}
	_, err := buildLayerSet(context.Background(), layerSetInput{
		project: p, projectID: "p1", gate: true, deps: c.deps(),
	})
	if !errors.Is(err, llmlayer.ErrLLMNotConfigured) {
		t.Fatalf("buildLayerSet = %v, want llmlayer.ErrLLMNotConfigured", err)
	}
	if c.providerBuilds != 0 {
		t.Error("a refused project must not build any provider")
	}

	// The ungated form serves schema indexing and the connection test, which
	// have to work before a project has been through settings.
	if _, err := buildLayerSet(context.Background(), layerSetInput{
		project: p, projectID: "p1", gate: false, deps: c.deps(),
	}); err != nil {
		t.Errorf("ungated buildLayerSet = %v, want nil", err)
	}
}

func TestDiscoveryDoesNotBuildLayersItNeverCalls(t *testing.T) {
	c := &counters{}
	set, err := buildLayerSet(context.Background(), layerSetInput{
		project: singleModelProject(), projectID: "p1", layers: discoveryLayers, gate: true, deps: c.deps(),
	})
	if err != nil {
		t.Fatalf("buildLayerSet: %v", err)
	}
	// Ask, pack generation, file ingestion and the executive summary are not
	// discovery's business; resolving them would be providers a run pays for
	// and never calls.
	for _, layer := range []string{llmlayer.LayerAsk, llmlayer.LayerPackGen, llmlayer.LayerFileIngestion, llmlayer.LayerExecSummary} {
		if _, built := set.byLayer[layer]; built {
			t.Errorf("a discovery run built the %q layer", layer)
		}
		if set.For(layer).Layer != llmlayer.DefaultLayer {
			t.Errorf("For(%q) must fall back to the default", layer)
		}
	}
}
