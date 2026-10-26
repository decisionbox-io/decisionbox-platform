package agentserver

import (
	"context"
	"errors"
	"slices"
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
	providerBuilds  int
	budgetResolves  int
	windowReads     int
	credentialReads int
	// specs is every spec a provider was built for, in order.
	specs []llmlayer.LayerSpec
	// secrets, when set, maps a secret key to its stored value; a chain
	// resolves to the first key present. Empty means every chain resolves to
	// nothing, which is the single-credential case.
	secrets map[string]string
}

func (c *counters) deps() layerSetDeps {
	return layerSetDeps{
		resolveCredential: func(_ context.Context, spec llmlayer.LayerSpec) (llmlayer.Credential, error) {
			c.credentialReads++
			for _, ref := range llmlayer.CredentialChain(spec.Layer, spec.ProviderID, spec.Config) {
				if v, ok := c.secrets[ref.SecretKey]; ok {
					return llmlayer.Credential{Value: v, SecretKey: ref.SecretKey, Source: "dashboard"}, nil
				}
			}
			return llmlayer.Credential{Source: "none"}, nil
		},
		buildProvider: func(_ context.Context, spec llmlayer.LayerSpec, cred llmlayer.Credential) (gollm.Provider, gollm.ProviderConfig, error) {
			c.providerBuilds++
			c.specs = append(c.specs, spec)
			return &countingProviderStub{id: c.providerBuilds}, llmlayer.ProviderConfig(spec, cred, llmlayer.BuildOptions{}), nil
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
		project: p, projectID: "p1", deps: c.deps(),
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
		project: p, projectID: "p1", layers: discoveryLayers(allPhases), deps: c.deps(),
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
		project: p, projectID: "p1", deps: c.deps(),
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
		project: p, projectID: "p1", layers: []string{llmlayer.LayerSQLRepair, llmlayer.LayerAnalysis}, deps: c.deps(),
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
		project: singleModelProject(), projectID: "p1", layers: []string{llmlayer.LayerAnalysis}, deps: c.deps(),
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
		project: p, projectID: "p1", layers: []string{llmlayer.LayerExploration}, deps: c.deps(),
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
		project: singleModelProject(), projectID: "p1", runID: "r1", testMode: true, deps: c.deps(),
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
		project: singleModelProject(), projectID: "p1", runID: "r1", deps: c.deps(),
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
		project: p, projectID: "p1", layers: []string{llmlayer.LayerExploration, llmlayer.LayerAnalysis}, deps: c.deps(),
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

// The agent does not carry the per-layer configuration gate; the API does,
// because the API is the only side that knows whether the gate can be true.
//
// Two shapes that reach the agent with no version stamp and MUST run:
// an existing MANAGED project (the gateway owns its configuration and
// managedai.Apply only stamps it on the next write), and the pre-#407
// single-model project that schema indexing and the connection test have
// always built from. The unstamped project here stands for both.
func TestTheAgentRunsAProjectTheAPIAlreadyLetThrough(t *testing.T) {
	p := &models.Project{}
	p.LLM = models.LLMConfig{Provider: "bedrock", Model: "opus"}
	// Deliberately not seeded: version 0.

	c := &counters{}
	set, err := buildLayerSet(context.Background(), layerSetInput{
		project: p, projectID: "p1", deps: c.deps(),
	})
	if err != nil {
		t.Fatalf("buildLayerSet = %v, want the legacy fields to resolve", err)
	}
	if got := set.For(llmlayer.LayerAnalysis); got == nil || got.Model != "opus" {
		t.Fatalf("analysis layer = %+v, want the legacy model", got)
	}
	if c.providerBuilds != 1 {
		t.Errorf("built %d providers for a single-model project, want 1", c.providerBuilds)
	}
}

// A project with no LLM at all is still refused — by the resolver, not by a
// gate, so the refusal survives the gate living elsewhere.
func TestAProjectWithNoProviderAtAllIsStillRefused(t *testing.T) {
	c := &counters{}
	_, err := buildLayerSet(context.Background(), layerSetInput{
		project: &models.Project{}, projectID: "p1", deps: c.deps(),
	})
	if !errors.Is(err, llmlayer.ErrNoProviderConfigured) {
		t.Fatalf("buildLayerSet = %v, want llmlayer.ErrNoProviderConfigured", err)
	}
	if c.providerBuilds != 0 {
		t.Error("a refused project must not build any provider")
	}
}

// allPhases is a run with every optional phase on: the widest layer list.
var allPhases = discoveryPhases{validation: true, refuter: true, questions: true, reflection: true}

// A phase that is switched off must not have its provider built.
//
// Building one reads a credential and opens a connection, so a broken
// override on a layer the run will never call aborted the run before
// exploration started — which left VALIDATION_REFUTER_ENABLED=false unable to
// route around a broken refuter endpoint, the one thing it exists for.
func TestADisabledPhaseBuildsNoProvider(t *testing.T) {
	cases := []struct {
		name   string
		phases discoveryPhases
		absent []string
	}{
		{
			"validation off takes the refuter with it",
			discoveryPhases{refuter: true, questions: true, reflection: true},
			[]string{llmlayer.LayerValidationVerifier, llmlayer.LayerValidationRefuter},
		},
		{
			"the refuter alone",
			discoveryPhases{validation: true, questions: true, reflection: true},
			[]string{llmlayer.LayerValidationRefuter},
		},
		{
			"questions and reflection off",
			discoveryPhases{validation: true, refuter: true},
			[]string{llmlayer.LayerQuestions, llmlayer.LayerReflection},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := discoveryLayers(tc.phases)
			for _, layer := range tc.absent {
				if slices.Contains(got, layer) {
					t.Errorf("%q is in the layer list for a run that will not call it: %v", layer, got)
				}
			}
			// What a run always calls stays in, whatever is off.
			for _, layer := range []string{
				llmlayer.LayerExploration, llmlayer.LayerAnalysis,
				llmlayer.LayerRecommendations, llmlayer.LayerSQLRepair,
			} {
				if !slices.Contains(got, layer) {
					t.Errorf("%q is missing from the layer list: %v", layer, got)
				}
			}
		})
	}

	// And the whole point: a broken override on a disabled layer does not
	// stop the run.
	c := &countersFailingOn{layer: llmlayer.LayerValidationRefuter, counters: &counters{}}
	if _, err := buildLayerSet(context.Background(), layerSetInput{
		project:   refuterOverrideProject(),
		projectID: "p1",
		layers:    discoveryLayers(discoveryPhases{validation: true, questions: true, reflection: true}),
		deps:      c.deps(),
	}); err != nil {
		t.Fatalf("buildLayerSet = %v; a disabled refuter's broken provider must not abort the run", err)
	}
	// With it enabled the same project does fail, so the stub is honest.
	c2 := &countersFailingOn{layer: llmlayer.LayerValidationRefuter, counters: &counters{}}
	if _, err := buildLayerSet(context.Background(), layerSetInput{
		project:   refuterOverrideProject(),
		projectID: "p1",
		layers:    discoveryLayers(allPhases),
		deps:      c2.deps(),
	}); err == nil {
		t.Error("an ENABLED refuter with a broken provider must fail the run")
	}
}

// refuterOverrideProject puts the refuter on its own entry, so a failure can
// be scoped to it.
func refuterOverrideProject() *models.Project {
	p := singleModelProject()
	p.LLMProviders = append(p.LLMProviders, llmlayer.ProviderEntry{
		ID: "refuter-box", Provider: "litellm", Config: map[string]string{"base_url": "http://broken"},
	})
	p.LayerModels[llmlayer.LayerValidationRefuter] = llmlayer.LayerModel{ProviderID: "refuter-box", Model: "qwen3-32b"}
	return p
}

// countersFailingOn is counters whose provider construction fails for one
// layer's entry.
type countersFailingOn struct {
	layer string
	*counters
}

func (c *countersFailingOn) deps() layerSetDeps {
	d := c.counters.deps()
	inner := d.buildProvider
	d.buildProvider = func(ctx context.Context, spec llmlayer.LayerSpec, cred llmlayer.Credential) (gollm.Provider, gollm.ProviderConfig, error) {
		if spec.Layer == c.layer {
			return nil, nil, errors.New("connection refused")
		}
		return inner(ctx, spec, cred)
	}
	return d
}

func TestDiscoveryDoesNotBuildLayersItNeverCalls(t *testing.T) {
	c := &counters{}
	set, err := buildLayerSet(context.Background(), layerSetInput{
		project: singleModelProject(), projectID: "p1", layers: discoveryLayers(allPhases), deps: c.deps(),
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

// Two layers on ONE entry can authenticate as different principals.
//
// Table descriptions resolves blurb-llm-credentials before the default key —
// that is today's behaviour and a project can really be configured that way.
// Keyed on (entry, config) alone, the first layer built won the shared
// provider and table descriptions indexed on the ANALYSIS key, which
// resolveBlurbLLM used to get right by building its own provider. A single
// model everywhere made it invisible.
func TestASeparateBlurbCredentialGetsItsOwnProvider(t *testing.T) {
	c := &counters{secrets: map[string]string{
		llmlayer.LegacyCredentialsKey: "analysis-key",
		llmlayer.BlurbCredentialsKey:  "blurb-key",
	}}
	set, err := buildLayerSet(context.Background(), layerSetInput{
		project:   singleModelProject(),
		projectID: "p1",
		layers:    []string{llmlayer.LayerAnalysis, llmlayer.LayerTableDescriptions},
		deps:      c.deps(),
	})
	if err != nil {
		t.Fatalf("buildLayerSet: %v", err)
	}

	analysis := set.For(llmlayer.LayerAnalysis)
	td := set.For(llmlayer.LayerTableDescriptions)
	if analysis.Config["credentials_json"] != "analysis-key" {
		t.Errorf("analysis credential = %q, want the default key's value", analysis.Config["credentials_json"])
	}
	if td.Config["credentials_json"] != "blurb-key" {
		t.Errorf("table-description credential = %q, want the blurb key's value", td.Config["credentials_json"])
	}
	if analysis.LLM == td.LLM {
		t.Error("two layers authenticating as different principals shared one provider instance")
	}
	if c.providerBuilds != 2 {
		t.Errorf("built %d providers, want 2 — one per credential", c.providerBuilds)
	}
}

// And the converse, which is what keeps the single-model guarantee true: a
// project with NO blurb secret has its blurb chain fall through to the
// default key, so every layer is still one provider. Keying on the chain
// rather than on the credential it resolves to would have split this.
func TestABlurbChainThatFallsThroughStillSharesOneProvider(t *testing.T) {
	c := &counters{secrets: map[string]string{llmlayer.LegacyCredentialsKey: "the-only-key"}}
	set, err := buildLayerSet(context.Background(), layerSetInput{
		project:   singleModelProject(),
		projectID: "p1",
		layers:    llmlayer.Keys(),
		deps:      c.deps(),
	})
	if err != nil {
		t.Fatalf("buildLayerSet: %v", err)
	}
	if c.providerBuilds != 1 {
		t.Errorf("built %d providers for one model and one credential, want 1", c.providerBuilds)
	}
	if got := set.For(llmlayer.LayerTableDescriptions).Config["credentials_json"]; got != "the-only-key" {
		t.Errorf("table-description credential = %q, want the default key it falls through to", got)
	}
	// One read per CHAIN, not per layer: seventeen layers, two chains.
	if c.credentialReads > 2 {
		t.Errorf("resolved credentials %d times for two distinct chains", c.credentialReads)
	}
}
