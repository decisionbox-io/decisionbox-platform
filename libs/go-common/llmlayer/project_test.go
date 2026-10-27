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

	// The project toggles describe the ANALYSIS pair, and on a legacy
	// document that is all they ever described: schema indexing built the
	// blurb provider straight from blurb_llm.config. Letting the project
	// field govern the blurb slot here turns caching back on for table
	// descriptions the moment the image is upgraded — and the first save
	// turns it off again, because the seed puts the blurb config's own half
	// on that row.
	t.Run("the blurb slot keeps its own toggles on a legacy document", func(t *testing.T) {
		c := legacyOnly()
		c.Legacy.PromptCache = boolPtr(true)
		c.Legacy.Reasoning = boolPtr(true)
		c.Legacy.Blurb = &LegacyBlurb{
			Provider: "openai",
			Model:    "gpt-4.1-nano",
			Config: map[string]string{
				gollm.PromptCacheKey:      "false",
				gollm.ReasoningEnabledKey: "false",
			},
		}
		spec, err := c.SpecUnchecked(LayerTableDescriptions)
		if err != nil {
			t.Fatalf("SpecUnchecked(table_descriptions): %v", err)
		}
		if spec.PromptCache {
			t.Error("table descriptions cached, but blurb_llm.config turned caching off and the project field never reached this slot")
		}
		if spec.Reasoning {
			t.Error("table descriptions ran with reasoning, which blurb_llm.config turned off")
		}

		// The analysis pair is still governed by the project field — that is
		// what it has always meant, and the two must not be swapped.
		analysis, _ := c.SpecUnchecked(LayerAnalysis)
		if !analysis.PromptCache {
			t.Error("the project-wide caching switch stopped applying to the analysis pair")
		}
	})

	// And saving the untouched seed must change nothing — in EITHER
	// direction. The blurb slot does not answer to the project fields
	// before migration, but an absent key on a configured row does, so the
	// seed has to materialise what the layer resolves to today.
	//
	// The second case is the one that bites in practice: a project with
	// caching off project-wide and a blurb slot that never mentioned it ran
	// table descriptions WITH caching, and the first save turned it off —
	// a change in indexing cost and latency nobody asked for.
	t.Run("the seeded row resolves to what the legacy read resolved to", func(t *testing.T) {
		for _, tc := range []struct {
			name         string
			projectCache *bool
			blurbConfig  map[string]string
		}{
			{"the blurb slot states its own", boolPtr(true), map[string]string{gollm.PromptCacheKey: "false"}},
			{"the blurb slot states nothing", boolPtr(false), nil},
			{"the blurb slot states nothing and the project says on", boolPtr(true), nil},
		} {
			t.Run(tc.name, func(t *testing.T) {
				c := legacyOnly()
				c.Legacy.PromptCache = tc.projectCache
				c.Legacy.Blurb = &LegacyBlurb{
					Provider: "openai",
					Model:    "gpt-4.1-nano",
					Config:   tc.blurbConfig,
				}
				before, err := c.SpecUnchecked(LayerTableDescriptions)
				if err != nil {
					t.Fatalf("SpecUnchecked before seeding: %v", err)
				}

				entries, layers := c.SeedFromLegacy()
				seeded := ProjectConfig{Providers: entries, LayerModels: layers, Legacy: c.Legacy}
				after, err := seeded.SpecUnchecked(LayerTableDescriptions)
				if err != nil {
					t.Fatalf("SpecUnchecked after seeding: %v", err)
				}
				if before.PromptCache != after.PromptCache {
					t.Errorf("caching was %v before the seed and %v after; saving an unchanged seed must not change what runs",
						before.PromptCache, after.PromptCache)
				}
				if before.Reasoning != after.Reasoning {
					t.Errorf("reasoning was %v before the seed and %v after", before.Reasoning, after.Reasoning)
				}
			})
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

// Seeding must not lose the model-scoped settings a legacy project already
// has — on either row.
//
// The blurb half is the one that breaks silently: EffectiveLayerModel answers
// with the table-description PAIR and no config, so partitioning that empty
// map while also stripping the model-scoped keys off the entry dropped
// wire_override and the token caps on the floor. A custom Bedrock, Vertex or
// Azure deployment that needs a wire override stops indexing on its first
// save, with nothing in the UI to show what went missing.
func TestSeedFromLegacyKeepsModelSettingsOnBothRows(t *testing.T) {
	cache := false
	c := ProjectConfig{
		Legacy: Legacy{
			Provider: "bedrock",
			Model:    "opus",
			Config: map[string]string{
				"region":            "eu-west-1",
				"max_output_tokens": "8000",
				"wire_override":     "anthropic-messages",
			},
			PromptCache: &cache,
			Blurb: &LegacyBlurb{
				Provider: "litellm",
				Model:    "qwen3-32b",
				Config: map[string]string{
					"base_url":          "https://cheap/v1",
					"max_output_tokens": "1024",
					"wire_override":     "openai-chat",
				},
			},
		},
	}

	entries, layers := c.SeedFromLegacy()

	byID := map[string]ProviderEntry{}
	for _, e := range entries {
		byID[e.ID] = e
	}
	// Connection fields stay on the entries.
	if byID[DefaultProviderID].Config["region"] != "eu-west-1" {
		t.Errorf("default entry = %+v, want its region", byID[DefaultProviderID].Config)
	}
	if byID[BlurbProviderID].Config["base_url"] != "https://cheap/v1" {
		t.Errorf("blurb entry = %+v, want its base_url", byID[BlurbProviderID].Config)
	}
	// Model-scoped fields are on the rows, and on the RIGHT row.
	def := layers[DefaultLayer]
	if def.Config["max_output_tokens"] != "8000" || def.Config["wire_override"] != "anthropic-messages" {
		t.Errorf("default row = %+v, want the analysis model settings", def.Config)
	}
	td := layers[LayerTableDescriptions]
	if td.Config["max_output_tokens"] != "1024" || td.Config["wire_override"] != "openai-chat" {
		t.Errorf("table-description row = %+v, want the BLURB model settings", td.Config)
	}
	// And nothing model-scoped is left behind on an entry, where it would
	// apply to every layer sharing that connection.
	for id, e := range byID {
		for _, k := range ModelScopedKeys {
			if _, present := e.Config[k]; present {
				t.Errorf("entry %q kept the model-scoped key %q", id, k)
			}
		}
	}
	// The resolved pairs still carry the settings, whichever side they came
	// from — the seed is a reshuffle, not a change.
	spec, err := c.withSeed(entries, layers).SpecUnchecked(LayerTableDescriptions)
	if err != nil {
		t.Fatalf("SpecUnchecked: %v", err)
	}
	if spec.Config["wire_override"] != "openai-chat" || spec.Config["base_url"] != "https://cheap/v1" {
		t.Errorf("resolved table-description config = %+v, want the merged connection and model settings", spec.Config)
	}
}

// withSeed returns the configuration a project would have after saving this
// seed, so a test can resolve against it.
func (c ProjectConfig) withSeed(entries []ProviderEntry, layers map[string]LayerModel) ProjectConfig {
	c.Providers = entries
	c.LayerModels = layers
	c.Version = ConfigVersion
	return c
}

// The two legacy toggles keep the precedence they have always had on a
// project that has no per-layer configuration: the FIELD is authoritative
// and llm.config is the fallback.
//
// Documents setting both and disagreeing exist, because the two were written
// by different versions of the settings page. Reading the config map first
// silently flipped them. On a CONFIGURED project the layer's own config wins
// — choosing per layer is the point.
func TestLegacyTogglePrecedenceSurvivesUntilAProjectIsConfigured(t *testing.T) {
	off := false
	legacy := ProjectConfig{
		Legacy: Legacy{
			Provider:    "bedrock",
			Model:       "opus",
			Config:      map[string]string{gollm.PromptCacheKey: "true"},
			PromptCache: &off,
		},
	}
	spec, err := legacy.SpecUnchecked(LayerAnalysis)
	if err != nil {
		t.Fatalf("SpecUnchecked: %v", err)
	}
	if spec.PromptCache {
		t.Error("prompt caching is on; prompt_cache_enabled=false is what this project has always meant")
	}

	configured := legacy
	configured.Providers = []ProviderEntry{{ID: DefaultProviderID, Provider: "bedrock"}}
	configured.LayerModels = map[string]LayerModel{
		DefaultLayer: {ProviderID: DefaultProviderID, Model: "opus", Config: map[string]string{gollm.PromptCacheKey: "true"}},
	}
	configured.Version = ConfigVersion
	spec, err = configured.SpecUnchecked(LayerAnalysis)
	if err != nil {
		t.Fatalf("SpecUnchecked: %v", err)
	}
	if !spec.PromptCache {
		t.Error("a configured project's own row must win over the legacy field")
	}
}
