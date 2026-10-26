package llmlayer

import (
	"errors"
	"testing"
)

func TestCheckLayerModelRefusesReasoningClassTableDescriptions(t *testing.T) {
	refused := []string{
		"deepseek-r1",
		"us.deepseek.r1-distill-llama-70b",
		"o1-preview",
		"o3",
		"o3-mini",
		"o4-mini",
		"gpt-5",
		"gpt-5-mini",
		"gpt-5-2025-08-07",
		"anthropic.claude-opus-4-1-extended-thinking",
	}
	for _, model := range refused {
		err := CheckLayerModel(LayerTableDescriptions, "bedrock", model)
		if err == nil {
			t.Errorf("CheckLayerModel(table_descriptions, %q) = nil; a reasoning-class model returns an empty description within the small blurb budget", model)
			continue
		}
		if !errors.Is(err, ErrReasoningModelNotSupported) {
			t.Errorf("CheckLayerModel(table_descriptions, %q) = %v, want ErrReasoningModelNotSupported", model, err)
		}
	}
}

func TestCheckLayerModelAcceptsFastModelsForTableDescriptions(t *testing.T) {
	accepted := []string{
		"anthropic.claude-haiku-4-5-20251001-v1:0",
		"gpt-4.1-nano",
		"gpt-4o-mini",
		"gemini-2.5-flash",
		"qwen.qwen3-32b-v1:0",
		"", // an endpoint-backed entry serves its own model
	}
	for _, model := range accepted {
		if err := CheckLayerModel(LayerTableDescriptions, "bedrock", model); err != nil {
			t.Errorf("CheckLayerModel(table_descriptions, %q) = %v, want nil", model, err)
		}
	}
}

func TestCheckLayerModelHasNoRuleForTheOtherSixteenLayers(t *testing.T) {
	// "Opus 5.5 refuses pack generation" is a RUNTIME refusal
	// (gollm.ErrModelRefused), handled where the call is made. Turning it
	// into a pre-flight denylist here would block a model the next release
	// accepts, for every project, with no way to override.
	for _, d := range Layers() {
		if d.Key == LayerTableDescriptions {
			continue
		}
		for _, model := range []string{"gpt-5", "o3", "deepseek-r1", "claude-opus-5-5-extended-thinking"} {
			if err := CheckLayerModel(d.Key, "bedrock", model); err != nil {
				t.Errorf("CheckLayerModel(%q, %q) = %v, want nil — only table_descriptions has a pre-flight rule", d.Key, model, err)
			}
		}
	}
}

func TestCheckLayerModelIgnoresUnknownLayers(t *testing.T) {
	if err := CheckLayerModel("not_a_layer", "bedrock", "gpt-5"); err != nil {
		t.Errorf("an unknown layer key must carry no rule, got %v", err)
	}
}

func TestDescriptorRuleFlagAgreesWithCheckLayerModel(t *testing.T) {
	// The dashboard warns from Descriptor.RefusesReasoningModels while the
	// API enforces with CheckLayerModel; a disagreement shows as a save that
	// the UI said was fine being rejected.
	for _, d := range Layers() {
		err := CheckLayerModel(d.Key, "bedrock", "gpt-5")
		if (err != nil) != d.RefusesReasoningModels {
			t.Errorf("layer %q: RefusesReasoningModels = %t but CheckLayerModel error = %v", d.Key, d.RefusesReasoningModels, err)
		}
	}
}

// The substring-pattern half of the rule, moved here with the list it reads.
// The catalog-flag half needs registered provider metadata and is exercised in
// the agent's blurb package, where the providers are imported.
func TestIsReasoningClassModelMatches(t *testing.T) {
	reasoning := []string{
		"deepseek.deepseek-r1-v1:0",
		"r1-distill-llama",
		"o1-preview",
		"o3-mini-high",
		"o4-mini",
		"some-model-extended-thinking",
		"DeepSeek-R1", // case-insensitive
		// GPT-5 family — reasoning by default, so a small description budget
		// returns empty content.
		"gpt-5",
		"gpt-5-mini",
		"gpt-5-nano",
		"gpt-5-2025-08-07",
		"GPT-5",
	}
	for _, m := range reasoning {
		if !IsReasoningClassModel(m) {
			t.Errorf("%q should be reasoning-class", m)
		}
	}
}

func TestIsReasoningClassModelNoFalsePositives(t *testing.T) {
	normal := []string{
		"gpt-4o",
		"gpt-4.1-nano",
		"claude-haiku-4-5",
		"claude-sonnet-4-6",
		"qwen.qwen3-32b-v1:0",
		"ollama3-70b",
	}
	for _, m := range normal {
		if IsReasoningClassModel(m) {
			t.Errorf("%q should NOT be reasoning-class", m)
		}
	}
}
