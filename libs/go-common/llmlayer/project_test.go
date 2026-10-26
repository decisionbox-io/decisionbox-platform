package llmlayer

import (
	"errors"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func boolPtr(b bool) *bool { return &b }

// legacyOnly is the shape of every project that exists today: one provider,
// one analysis model, no plural fields, version 0.
func legacyOnly() ProjectConfig {
	return ProjectConfig{
		Legacy: Legacy{
			Provider: "bedrock",
			Model:    "anthropic.claude-opus-4-8",
			Config:   map[string]string{"region": "us-east-1"},
		},
	}
}

func TestLegacyProjectSynthesisesOneDefaultEntry(t *testing.T) {
	c := legacyOnly()
	entries := c.EffectiveProviders()
	if len(entries) != 1 {
		t.Fatalf("EffectiveProviders() = %d entries, want 1", len(entries))
	}
	if entries[0].ID != DefaultProviderID {
		t.Errorf("entry id = %q, want %q — a non-default id would send the agent to a secret key nothing ever wrote", entries[0].ID, DefaultProviderID)
	}
	if entries[0].Provider != "bedrock" || entries[0].Config["region"] != "us-east-1" {
		t.Errorf("entry = %+v, want the legacy provider and config", entries[0])
	}
}

func TestProjectWithNoLLMAtAllResolvesToNothing(t *testing.T) {
	var c ProjectConfig
	if got := c.EffectiveProviders(); got != nil {
		t.Errorf("EffectiveProviders() = %v, want nil", got)
	}
	if _, err := c.SpecUnchecked(LayerAnalysis); !errors.Is(err, ErrNoProviderConfigured) {
		t.Errorf("SpecUnchecked = %v, want ErrNoProviderConfigured", err)
	}
}

func TestBlurbOverrideBecomesASeparateEntryEvenWhenTheProviderMatches(t *testing.T) {
	// Its Config REPLACES the analysis config today rather than merging. A
	// merged map would reintroduce the bug that fed GCP auth_method values
	// into the AWS factory on a Vertex-analysis / Bedrock-blurb project.
	c := legacyOnly()
	c.Legacy.Blurb = &LegacyBlurb{
		Provider: "bedrock",
		Model:    "anthropic.claude-haiku-4-5",
		Config:   map[string]string{"region": "eu-west-1"},
	}
	entries := c.EffectiveProviders()
	if len(entries) != 2 {
		t.Fatalf("EffectiveProviders() = %d entries, want 2", len(entries))
	}
	if entries[1].ID != BlurbProviderID {
		t.Errorf("second entry id = %q, want %q", entries[1].ID, BlurbProviderID)
	}
	spec, err := c.SpecUnchecked(LayerTableDescriptions)
	if err != nil {
		t.Fatalf("SpecUnchecked: %v", err)
	}
	if spec.Config["region"] != "eu-west-1" {
		t.Errorf("table-description region = %q, want the blurb entry's own value", spec.Config["region"])
	}
}

func TestLegacyBlurbThreeBranches(t *testing.T) {
	cases := []struct {
		name          string
		blurb         *LegacyBlurb
		wantProvider  string
		wantModel     string
		wantEntryKind string
	}{
		{
			// Branch 1: no blurb provider — the blurb object's model and
			// config are ignored ENTIRELY, not partially.
			name:          "nil blurb inherits the analysis pair",
			blurb:         nil,
			wantProvider:  "bedrock",
			wantModel:     "anthropic.claude-opus-4-8",
			wantEntryKind: DefaultProviderID,
		},
		{
			name:          "empty blurb provider ignores the blurb model too",
			blurb:         &LegacyBlurb{Provider: "", Model: "ignored-model"},
			wantProvider:  "bedrock",
			wantModel:     "anthropic.claude-opus-4-8",
			wantEntryKind: DefaultProviderID,
		},
		{
			// Branch 2: a provider, a blank model, no endpoint — keep the
			// blurb provider, inherit the analysis model.
			name:          "blank model with no endpoint inherits the analysis model",
			blurb:         &LegacyBlurb{Provider: "litellm", Model: ""},
			wantProvider:  "litellm",
			wantModel:     "anthropic.claude-opus-4-8",
			wantEntryKind: BlurbProviderID,
		},
		{
			// Branch 3: endpoint-backed — pass the model through verbatim,
			// blank or explicit. An endpoint serves its own model and must
			// never be sent the analysis one.
			name:          "endpoint-backed blank model stays blank",
			blurb:         &LegacyBlurb{Provider: "litellm", Model: "", Config: map[string]string{EndpointIDConfigKey: "ep-1"}},
			wantProvider:  "litellm",
			wantModel:     "",
			wantEntryKind: BlurbProviderID,
		},
		{
			name:          "endpoint-backed explicit model is passed through",
			blurb:         &LegacyBlurb{Provider: "litellm", Model: "qwen3-32b", Config: map[string]string{EndpointIDConfigKey: "ep-1"}},
			wantProvider:  "litellm",
			wantModel:     "qwen3-32b",
			wantEntryKind: BlurbProviderID,
		},
		{
			// A whitespace-only endpoint_id is not an endpoint, so branch 2
			// applies and the analysis model is inherited.
			name:          "whitespace-only endpoint_id is not an endpoint",
			blurb:         &LegacyBlurb{Provider: "litellm", Model: "", Config: map[string]string{EndpointIDConfigKey: "   "}},
			wantProvider:  "litellm",
			wantModel:     "anthropic.claude-opus-4-8",
			wantEntryKind: BlurbProviderID,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := legacyOnly()
			c.Legacy.Blurb = tc.blurb
			lm := c.EffectiveLayerModel(LayerTableDescriptions)
			if lm.ProviderID != tc.wantEntryKind {
				t.Errorf("provider entry = %q, want %q", lm.ProviderID, tc.wantEntryKind)
			}
			if lm.Model != tc.wantModel {
				t.Errorf("model = %q, want %q", lm.Model, tc.wantModel)
			}
			spec, err := c.SpecUnchecked(LayerTableDescriptions)
			if err != nil {
				t.Fatalf("SpecUnchecked: %v", err)
			}
			if spec.Provider != tc.wantProvider {
				t.Errorf("provider slug = %q, want %q", spec.Provider, tc.wantProvider)
			}
		})
	}
}

func TestEveryOtherLegacyLayerUsesTheAnalysisPair(t *testing.T) {
	c := legacyOnly()
	c.Legacy.Blurb = &LegacyBlurb{Provider: "litellm", Model: "cheap"}
	for _, key := range Keys() {
		if key == LayerTableDescriptions {
			continue
		}
		lm := c.EffectiveLayerModel(key)
		if lm.ProviderID != DefaultProviderID || lm.Model != "anthropic.claude-opus-4-8" {
			t.Errorf("layer %q resolved to %+v, want the analysis pair — only table descriptions had an override before this feature", key, lm)
		}
	}
}

func TestPluralLayerModelsAreAuthoritativeAndDoNotRediscoverTheBlurbMirror(t *testing.T) {
	// The rule that matters on a real project: a user who RESETS the
	// table-description override must not have the stale blurb_llm mirror
	// silently reinstate the model they just reset.
	c := ProjectConfig{
		Version:   ConfigVersion,
		Providers: []ProviderEntry{{ID: DefaultProviderID, Provider: "bedrock"}},
		LayerModels: map[string]LayerModel{
			DefaultLayer: {ProviderID: DefaultProviderID, Model: "opus"},
		},
		Legacy: Legacy{
			Provider: "bedrock",
			Model:    "opus",
			// Still present because the dual-write mirrors it; must be ignored.
			Blurb: &LegacyBlurb{Provider: "litellm", Model: "stale-haiku"},
		},
	}
	lm := c.EffectiveLayerModel(LayerTableDescriptions)
	if lm.Model != "opus" || lm.ProviderID != DefaultProviderID {
		t.Fatalf("table descriptions resolved to %+v; with layer_models present the legacy blurb mirror must not be consulted", lm)
	}
}

func TestALayerWithoutARowInheritsTheDefaultRow(t *testing.T) {
	// This is what makes "adding a layer does not bump ConfigVersion" true.
	c := ProjectConfig{
		Version:   ConfigVersion,
		Providers: []ProviderEntry{{ID: DefaultProviderID, Provider: "bedrock"}, {ID: "cheap", Provider: "litellm"}},
		LayerModels: map[string]LayerModel{
			DefaultLayer:         {ProviderID: DefaultProviderID, Model: "opus"},
			LayerRecommendations: {ProviderID: "cheap", Model: "haiku"},
		},
	}
	if lm := c.EffectiveLayerModel(LayerExploration); lm.Model != "opus" {
		t.Errorf("exploration = %+v, want the default row", lm)
	}
	if lm := c.EffectiveLayerModel(LayerRecommendations); lm.Model != "haiku" || lm.ProviderID != "cheap" {
		t.Errorf("recommendations = %+v, want its own row", lm)
	}
	if lm := c.EffectiveLayerModel("a_layer_from_a_future_release"); lm.Model != "opus" {
		t.Errorf("an unknown layer must inherit the default row, got %+v", lm)
	}
}

func TestEmptyProviderIDNormalisesToTheDefaultEntry(t *testing.T) {
	c := ProjectConfig{
		Version:     ConfigVersion,
		Providers:   []ProviderEntry{{Provider: "bedrock"}}, // no id
		LayerModels: map[string]LayerModel{DefaultLayer: {Model: "opus"}},
	}
	if got := c.EffectiveProviders()[0].ID; got != DefaultProviderID {
		t.Errorf("stored id-less entry read as %q, want %q", got, DefaultProviderID)
	}
	if _, ok := c.ProviderByID(DefaultProviderID); !ok {
		t.Error(`ProviderByID("default") must find a stored id-less entry`)
	}
	spec, err := c.SpecUnchecked(LayerAnalysis)
	if err != nil {
		t.Fatalf("SpecUnchecked: %v", err)
	}
	if spec.ProviderID != DefaultProviderID {
		t.Errorf("spec provider id = %q, want %q", spec.ProviderID, DefaultProviderID)
	}
	if CredentialsKey(spec.ProviderID) != LegacyCredentialsKey {
		t.Error("an id-less default entry must still read the legacy credential key")
	}
}

func TestALayerNamingADeletedEntryFallsBackToTheDefaultWithAWarning(t *testing.T) {
	c := ProjectConfig{
		Version:   ConfigVersion,
		Providers: []ProviderEntry{{ID: DefaultProviderID, Provider: "bedrock"}},
		LayerModels: map[string]LayerModel{
			DefaultLayer:     {ProviderID: DefaultProviderID, Model: "opus"},
			LayerExploration: {ProviderID: "deleted", Model: "deepseek"},
		},
	}
	spec, err := c.SpecUnchecked(LayerExploration)
	if err != nil {
		t.Fatalf("a layer naming a missing entry must fall back, not fail the run: %v", err)
	}
	if spec.ProviderID != DefaultProviderID || spec.Provider != "bedrock" {
		t.Errorf("spec = %+v, want the default entry", spec)
	}
	if spec.FellBackFromProviderID != "deleted" {
		t.Errorf("FellBackFromProviderID = %q, want %q so the caller can log it", spec.FellBackFromProviderID, "deleted")
	}
}

func TestLayerConfigIsMergedOverTheEntryConfig(t *testing.T) {
	c := ProjectConfig{
		Version: ConfigVersion,
		Providers: []ProviderEntry{{
			ID:       DefaultProviderID,
			Provider: "bedrock",
			Config:   map[string]string{"region": "us-east-1", "max_output_tokens": "4096"},
		}},
		LayerModels: map[string]LayerModel{
			DefaultLayer: {ProviderID: DefaultProviderID, Model: "opus"},
			LayerExploration: {ProviderID: DefaultProviderID, Model: "opus", Config: map[string]string{
				"max_output_tokens": "32000",
				"wire_override":     "converse",
			}},
		},
	}
	spec, err := c.SpecUnchecked(LayerExploration)
	if err != nil {
		t.Fatalf("SpecUnchecked: %v", err)
	}
	if spec.Config["region"] != "us-east-1" {
		t.Errorf("region = %q, want the entry's connection field", spec.Config["region"])
	}
	if spec.Config["max_output_tokens"] != "32000" {
		t.Errorf("max_output_tokens = %q, want the layer's override to win", spec.Config["max_output_tokens"])
	}
	if spec.Config["wire_override"] != "converse" {
		t.Errorf("wire_override = %q, want the layer's value", spec.Config["wire_override"])
	}
	// The entry's config must not be mutated by the merge.
	if _, leaked := c.Providers[0].Config["wire_override"]; leaked {
		t.Error("the merge wrote into the provider entry's own config map")
	}
}

// --- per-layer reasoning and prompt caching ---

func TestPerLayerReasoning(t *testing.T) {
	base := func() ProjectConfig {
		return ProjectConfig{
			Version:   ConfigVersion,
			Providers: []ProviderEntry{{ID: DefaultProviderID, Provider: "bedrock"}},
			LayerModels: map[string]LayerModel{
				DefaultLayer: {ProviderID: DefaultProviderID, Model: "opus"},
			},
		}
	}

	t.Run("a layer's own setting wins", func(t *testing.T) {
		c := base()
		c.Legacy.Reasoning = boolPtr(false)
		c.LayerModels[LayerExploration] = LayerModel{
			ProviderID: DefaultProviderID, Model: "opus",
			Config: map[string]string{gollm.ReasoningEnabledKey: "true"},
		}
		on, _ := c.SpecUnchecked(LayerExploration)
		off, _ := c.SpecUnchecked(LayerAnalysis)
		if !on.Reasoning {
			t.Error("exploration must have reasoning on")
		}
		if off.Reasoning {
			t.Error("analysis must have reasoning off")
		}
	})

	t.Run("the project field applies where no layer says anything", func(t *testing.T) {
		c := base()
		c.Legacy.Reasoning = boolPtr(true)
		spec, _ := c.SpecUnchecked(LayerAnalysis)
		if !spec.Reasoning {
			t.Error("a layer that says nothing must keep honouring the project field, or the operator's intent is silently dropped")
		}
	})

	t.Run("a legacy llm.config entry still resolves", func(t *testing.T) {
		c := legacyOnly()
		c.Legacy.Config[gollm.ReasoningEnabledKey] = "true"
		spec, _ := c.SpecUnchecked(LayerAnalysis)
		if !spec.Reasoning {
			t.Error("the pre-existing llm.config reasoning_enabled entry must keep working")
		}
	})

	t.Run("absent everywhere is off", func(t *testing.T) {
		spec, _ := legacyOnly().SpecUnchecked(LayerAnalysis)
		if spec.Reasoning {
			t.Error("reasoning is opt-in; absent must be off")
		}
	})
}

func TestPerLayerPromptCaching(t *testing.T) {
	base := func() ProjectConfig {
		return ProjectConfig{
			Version:   ConfigVersion,
			Providers: []ProviderEntry{{ID: DefaultProviderID, Provider: "bedrock"}},
			LayerModels: map[string]LayerModel{
				DefaultLayer: {ProviderID: DefaultProviderID, Model: "opus"},
			},
		}
	}

	t.Run("absent means on, per layer", func(t *testing.T) {
		for _, key := range Keys() {
			spec, err := base().SpecUnchecked(key)
			if err != nil {
				t.Fatalf("SpecUnchecked(%q): %v", key, err)
			}
			if !spec.PromptCache {
				t.Errorf("layer %q prompt caching = false; caching has always been opt-out", key)
			}
		}
	})

	t.Run("one layer off, a sibling on the same entry still on", func(t *testing.T) {
		c := base()
		c.LayerModels[LayerSQLRepair] = LayerModel{
			ProviderID: DefaultProviderID, Model: "haiku",
			Config: map[string]string{gollm.PromptCacheKey: "false"},
		}
		off, _ := c.SpecUnchecked(LayerSQLRepair)
		on, _ := c.SpecUnchecked(LayerAnalysis)
		if off.PromptCache {
			t.Error("sql_repair must have caching off")
		}
		if !on.PromptCache {
			t.Error("analysis must keep caching on")
		}
		// The D2 consequence, pinned: prompt caching is read at CONSTRUCTION,
		// so these two layers are two provider instances on one entry.
		offCfg := ProviderConfig(off, Credential{}, BuildOptions{})
		onCfg := ProviderConfig(on, Credential{}, BuildOptions{})
		if ConfigKey(offCfg) == ConfigKey(onCfg) {
			t.Error("two layers on one entry with different caching must NOT dedupe to one provider — they would share the wrong construction-time setting")
		}
	})

	t.Run("the project field applies where no layer says anything", func(t *testing.T) {
		c := base()
		c.Legacy.PromptCache = boolPtr(false)
		spec, _ := c.SpecUnchecked(LayerAnalysis)
		if spec.PromptCache {
			t.Error("an operator who turned caching off project-wide must keep it off until they change a layer")
		}
	})

	t.Run("a legacy llm.config entry still resolves", func(t *testing.T) {
		c := legacyOnly()
		c.Legacy.Config[gollm.PromptCacheKey] = "false"
		spec, _ := c.SpecUnchecked(LayerAnalysis)
		if spec.PromptCache {
			t.Error("the pre-existing llm.config prompt_cache entry must keep working")
		}
	})
}

// --- the configuration gate ---

func TestSpecRefusesAnUnconfiguredProject(t *testing.T) {
	c := legacyOnly()
	if c.Configured() {
		t.Fatal("a project at version 0 must not count as configured")
	}
	if _, err := c.Spec(LayerAnalysis); !errors.Is(err, ErrLLMNotConfigured) {
		t.Errorf("Spec() = %v, want ErrLLMNotConfigured", err)
	}
	// The ungated path must still work: the settings pre-fill, the per-layer
	// connection test, schema indexing and the support bundle all read an
	// unconfigured project.
	if _, err := c.SpecUnchecked(LayerAnalysis); err != nil {
		t.Errorf("SpecUnchecked() = %v, want nil on an unconfigured project", err)
	}
}

func TestConfiguredAtOrAboveTheCurrentVersion(t *testing.T) {
	cases := []struct {
		version int
		want    bool
	}{
		{0, false},
		{ConfigVersion, true},
		{ConfigVersion + 1, true}, // configured by a newer API; do not re-gate
	}
	for _, tc := range cases {
		c := legacyOnly()
		c.Version = tc.version
		if c.Configured() != tc.want {
			t.Errorf("version %d: Configured() = %t, want %t", tc.version, c.Configured(), tc.want)
		}
	}
}

// --- seeding ---

func TestSeedFromLegacyMakesTheMigrationAReviewNotADataEntryExercise(t *testing.T) {
	c := legacyOnly()
	c.Legacy.Reasoning = boolPtr(true)
	c.Legacy.PromptCache = boolPtr(false)
	c.Legacy.Blurb = &LegacyBlurb{Provider: "litellm", Model: "qwen3-32b", Config: map[string]string{"base_url": "http://x"}}

	entries, layers := c.SeedFromLegacy()
	if len(entries) != 2 {
		t.Fatalf("seeded %d entries, want 2 (default + blurb)", len(entries))
	}
	def, ok := layers[DefaultLayer]
	if !ok {
		t.Fatal("the seed must write a default row — it is what every layer inherits")
	}
	if def.ProviderID != DefaultProviderID || def.Model != "anthropic.claude-opus-4-8" {
		t.Errorf("default row = %+v, want the analysis pair", def)
	}
	if def.Config[gollm.ReasoningEnabledKey] != "true" {
		t.Error("the project's reasoning toggle must carry onto the default row, or moving it per layer silently drops the operator's choice")
	}
	if def.Config[gollm.PromptCacheKey] != "false" {
		t.Error("the project's prompt-cache toggle must carry onto the default row")
	}
	td, ok := layers[LayerTableDescriptions]
	if !ok {
		t.Fatal("a blurb override must seed its own table-description row")
	}
	if td.ProviderID != BlurbProviderID || td.Model != "qwen3-32b" {
		t.Errorf("table-description row = %+v, want the blurb pair", td)
	}
	if len(layers) != 2 {
		t.Errorf("seeded %d rows, want 2 — every other layer inherits the default rather than being written out", len(layers))
	}
}

func TestSeedFromLegacyWritesNoTableDescriptionRowWhenItMatchesTheDefault(t *testing.T) {
	c := legacyOnly()
	_, layers := c.SeedFromLegacy()
	if _, ok := layers[LayerTableDescriptions]; ok {
		t.Error("a project with no blurb override must not get a redundant table-description row")
	}
	// But the seeded configuration must still resolve table descriptions to
	// the same model it uses today.
	c.Version = ConfigVersion
	c.LayerModels = layers
	spec, err := c.Spec(LayerTableDescriptions)
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	if spec.Model != "anthropic.claude-opus-4-8" {
		t.Errorf("seeded table-description model = %q, want the analysis model it uses today", spec.Model)
	}
}

func TestSeededConfigurationReproducesTodaysBehaviourForEveryLayer(t *testing.T) {
	// The acceptance test for the migration: a project with every layer on
	// the default must behave exactly as it does now.
	before := legacyOnly()
	after := legacyOnly()
	after.Providers, after.LayerModels = before.SeedFromLegacy()
	after.Version = ConfigVersion

	for _, key := range Keys() {
		b, err := before.SpecUnchecked(key)
		if err != nil {
			t.Fatalf("before %q: %v", key, err)
		}
		a, err := after.Spec(key)
		if err != nil {
			t.Fatalf("after %q: %v", key, err)
		}
		if a.Provider != b.Provider || a.Model != b.Model || a.ProviderID != b.ProviderID {
			t.Errorf("layer %q: seeded %s/%s on %q, was %s/%s on %q", key, a.Provider, a.Model, a.ProviderID, b.Provider, b.Model, b.ProviderID)
		}
		if a.PromptCache != b.PromptCache || a.Reasoning != b.Reasoning {
			t.Errorf("layer %q: seeded cache=%t reasoning=%t, was cache=%t reasoning=%t", key, a.PromptCache, a.Reasoning, b.PromptCache, b.Reasoning)
		}
	}
}

func TestSeedFromLegacyOnAProjectWithNothingToSeed(t *testing.T) {
	entries, layers := ProjectConfig{}.SeedFromLegacy()
	if entries != nil || layers != nil {
		t.Errorf("SeedFromLegacy on an empty project = %v, %v; want nil, nil", entries, layers)
	}
}

// --- guards and dedupe helpers ---

func TestLayersUsingProviderNamesTheReferencingLayers(t *testing.T) {
	c := ProjectConfig{
		Version: ConfigVersion,
		LayerModels: map[string]LayerModel{
			DefaultLayer:     {ProviderID: DefaultProviderID, Model: "opus"},
			LayerExploration: {ProviderID: "deepinfra", Model: "deepseek"},
			LayerQuestions:   {ProviderID: "deepinfra", Model: "deepseek"},
		},
	}
	using := c.LayersUsingProvider("deepinfra")
	if len(using) != 2 {
		t.Fatalf("LayersUsingProvider = %v, want the two referencing layers", using)
	}
	if len(c.LayersUsingProvider("nobody")) != 0 {
		t.Error("an unreferenced entry must report no layers")
	}
}

func TestDistinctTargetsCollapsesEveryLayerOnTheDefaultToOne(t *testing.T) {
	c := legacyOnly()
	c.Version = ConfigVersion
	c.Providers, c.LayerModels = c.SeedFromLegacy()
	if got := c.DistinctTargets(); len(got) != 1 {
		t.Fatalf("DistinctTargets() = %d, want 1 — a project on one model must not pay for seventeen budget resolutions and probes", len(got))
	}

	// The benchmark's mixed pipeline: three entries, four distinct models.
	c.Providers = append(c.Providers, ProviderEntry{ID: "deepinfra", Provider: "litellm"}, ProviderEntry{ID: "cheap", Provider: "bedrock"})
	c.LayerModels[LayerExploration] = LayerModel{ProviderID: "deepinfra", Model: "deepseek-v4.1-flash"}
	c.LayerModels[LayerRecommendations] = LayerModel{ProviderID: "cheap", Model: "haiku"}
	c.LayerModels[LayerReflection] = LayerModel{ProviderID: "cheap", Model: "haiku"}
	if got := c.DistinctTargets(); len(got) != 3 {
		t.Errorf("DistinctTargets() = %d, want 3 (opus on default, deepseek on deepinfra, haiku on cheap)", len(got))
	}
}

func TestValidateLayerKeyAcceptsTheDefaultRowAndRejectsTypos(t *testing.T) {
	if !ValidateLayerKey(DefaultLayer) {
		t.Error("the reserved default row must be a legal layer_models key")
	}
	if !ValidateLayerKey(LayerAsk) {
		t.Error("a real layer key must be legal")
	}
	if ValidateLayerKey("validation") {
		t.Error(`"validation" is a phase, not a layer; storing it would silently do nothing forever`)
	}
	if ValidateLayerKey("") {
		t.Error("an empty layer key must be rejected")
	}
	if IsLayer(DefaultLayer) {
		t.Error("the default row must not appear in Layers() — a UI iterating the set would render it twice")
	}
}
