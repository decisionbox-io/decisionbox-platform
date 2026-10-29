package models

import (
	"errors"
	"reflect"
	"testing"

	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
)

// The API and the agent each carry a copy of Project and decode the SAME
// document. A tag typo in one copy is silent — the field simply never loads —
// so the three per-layer fields' storage names are pinned in both copies.
// Change one and this test fails in the other package too.
func TestPerLayerFieldStorageNames(t *testing.T) {
	want := map[string]string{
		"LLMProviders":     "llm_providers,omitempty",
		"LayerModels":      "layer_models,omitempty",
		"LLMConfigVersion": "llm_config_version,omitempty",
	}
	rt := reflect.TypeOf(Project{})
	for name, tag := range want {
		f, ok := rt.FieldByName(name)
		if !ok {
			t.Errorf("Project has no field %s", name)
			continue
		}
		if got := f.Tag.Get("bson"); got != tag {
			t.Errorf("%s bson tag = %q, want %q", name, got, tag)
		}
	}
}

// legacyLLMProject is the shape of every project that exists today.
func legacyLLMProject() *Project {
	p := &Project{}
	p.LLM = LLMConfig{Provider: "bedrock", Model: "opus", Config: map[string]string{"region": "us-east-1"}}
	return p
}

func TestLegacyProjectSynthesisesOneDefaultProviderEntry(t *testing.T) {
	p := legacyLLMProject()
	entries := p.EffectiveLLMProviders()
	if len(entries) != 1 || entries[0].ID != llmlayer.DefaultProviderID || entries[0].Provider != "bedrock" {
		t.Fatalf("EffectiveLLMProviders() = %+v, want one default bedrock entry", entries)
	}
	if got := p.DefaultLLMProvider().Provider; got != "bedrock" {
		t.Errorf("DefaultLLMProvider().Provider = %q, want bedrock", got)
	}
	if _, ok := p.LLMProviderByID(""); !ok {
		t.Error(`LLMProviderByID("") must resolve to the default entry`)
	}
}

func TestBlurbOverrideIsReadThroughTheAccessors(t *testing.T) {
	p := legacyLLMProject()
	p.BlurbLLM = &BlurbLLMConfig{Provider: "litellm", Model: "qwen3-32b", Config: map[string]string{"base_url": "http://x"}}

	if got := len(p.EffectiveLLMProviders()); got != 2 {
		t.Fatalf("EffectiveLLMProviders() = %d entries, want 2 (default + blurb)", got)
	}
	lm := p.EffectiveLayerModel(llmlayer.LayerTableDescriptions)
	if lm.ProviderID != llmlayer.BlurbProviderID || lm.Model != "qwen3-32b" {
		t.Errorf("table-description layer = %+v, want the blurb pair", lm)
	}
	spec, err := p.LayerSpecUnchecked(llmlayer.LayerTableDescriptions)
	if err != nil {
		t.Fatalf("LayerSpecUnchecked: %v", err)
	}
	if spec.Config["base_url"] != "http://x" {
		t.Errorf("spec config = %+v, want the blurb entry's own connection fields", spec.Config)
	}
	if spec.Config["region"] == "us-east-1" {
		t.Error("the blurb entry's config must REPLACE the analysis config, not merge with it — a Vertex/Bedrock mix would otherwise feed GCP auth fields into the AWS factory")
	}
}

func TestNilBlurbLLMIsNotDereferenced(t *testing.T) {
	p := legacyLLMProject()
	if lm := p.EffectiveLayerModel(llmlayer.LayerTableDescriptions); lm.Model != "opus" {
		t.Errorf("table-description layer = %+v, want the analysis pair", lm)
	}
}

func TestLayerSpecGatesAnUnconfiguredProject(t *testing.T) {
	p := legacyLLMProject()
	if p.LLMConfigured() {
		t.Fatal("a project with no llm_config_version must not count as configured")
	}
	if _, err := p.LayerSpec(llmlayer.LayerAnalysis); !errors.Is(err, llmlayer.ErrLLMNotConfigured) {
		t.Errorf("LayerSpec() = %v, want llmlayer.ErrLLMNotConfigured", err)
	}
	if _, err := p.LayerSpecUnchecked(llmlayer.LayerAnalysis); err != nil {
		t.Errorf("LayerSpecUnchecked() = %v, want nil — the settings pre-fill and the connection test read an unconfigured project", err)
	}
}

func TestSeedLLMLayersFromLegacyStampsTheVersionAndPreservesBehaviour(t *testing.T) {
	p := legacyLLMProject()
	yes := true
	p.ReasoningEnabled = &yes

	before, err := p.LayerSpecUnchecked(llmlayer.LayerExploration)
	if err != nil {
		t.Fatalf("LayerSpecUnchecked: %v", err)
	}

	p.SeedLLMLayersFromLegacy()

	if p.LLMConfigVersion != llmlayer.ConfigVersion {
		t.Errorf("LLMConfigVersion = %d, want %d", p.LLMConfigVersion, llmlayer.ConfigVersion)
	}
	if !p.LLMConfigured() {
		t.Error("a seeded project must count as configured")
	}
	after, err := p.LayerSpec(llmlayer.LayerExploration)
	if err != nil {
		t.Fatalf("LayerSpec after seed: %v", err)
	}
	if after.Provider != before.Provider || after.Model != before.Model {
		t.Errorf("seeding changed exploration from %s/%s to %s/%s", before.Provider, before.Model, after.Provider, after.Model)
	}
	if !after.Reasoning {
		t.Error("the project's reasoning toggle must carry onto the seeded default row")
	}
}

func TestSeedLLMLayersFromLegacyIsANoOpWithNothingToSeed(t *testing.T) {
	p := &Project{}
	p.SeedLLMLayersFromLegacy()
	if p.LLMConfigVersion != 0 || p.LLMProviders != nil || p.LayerModels != nil {
		t.Error("a project with no LLM provider must not be stamped as configured")
	}
}

func TestNilProjectAccessorsDoNotPanic(t *testing.T) {
	var p *Project
	if p.LLMConfigured() {
		t.Error("a nil project is not configured")
	}
	if got := p.EffectiveLLMProviders(); got != nil {
		t.Errorf("EffectiveLLMProviders() on nil = %v, want nil", got)
	}
}
