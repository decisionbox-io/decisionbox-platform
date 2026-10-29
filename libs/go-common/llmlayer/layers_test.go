package llmlayer

import (
	"strings"
	"testing"
)

func TestLayersCoverTheWholeProductAndNothingElse(t *testing.T) {
	ls := Layers()
	if len(ls) != 17 {
		t.Fatalf("Layers() returned %d descriptors, want 17 — a layer was added or removed without updating the test (and check whether ConfigVersion should bump: adding one must not)", len(ls))
	}

	want := map[string]string{
		LayerExploration:        GroupMain,
		LayerAnalysis:           GroupMain,
		LayerValidationVerifier: GroupMain,
		LayerValidationRefuter:  GroupMain,
		LayerRecommendations:    GroupMain,
		LayerAsk:                GroupMain,
		LayerQuestions:          GroupSmall,
		LayerReflection:         GroupSmall,
		LayerSQLRepair:          GroupSmall,
		LayerAskRouter:          GroupSmall,
		LayerAskSuggestions:     GroupSmall,
		LayerTableDescriptions:  GroupSmall,
		LayerSourceSummary:      GroupSmall,
		LayerPackGen:            GroupSmall,
		LayerFileIngestion:      GroupSmall,
		LayerExecSummary:        GroupSmall,
		LayerReportStyle:        GroupSmall,
	}

	seen := map[string]bool{}
	for _, d := range ls {
		if seen[d.Key] {
			t.Errorf("duplicate layer key %q", d.Key)
		}
		seen[d.Key] = true
		group, ok := want[d.Key]
		if !ok {
			t.Errorf("unexpected layer key %q", d.Key)
			continue
		}
		if d.Group != group {
			t.Errorf("layer %q group = %q, want %q", d.Key, d.Group, group)
		}
		if d.Label == "" || d.Description == "" {
			t.Errorf("layer %q has an empty label or description — the dashboard renders both", d.Key)
		}
	}
	for key := range want {
		if !seen[key] {
			t.Errorf("layer %q missing from Layers()", key)
		}
	}
}

func TestMainLayersComeFirstSoTheUIGroupsWithoutSorting(t *testing.T) {
	ls := Layers()
	sawSmall := false
	for _, d := range ls {
		if d.Group == GroupSmall {
			sawSmall = true
			continue
		}
		if sawSmall {
			t.Fatalf("main layer %q appears after a small layer; Layers() must be ordered main-then-small", d.Key)
		}
	}
}

func TestLayersIsACopy(t *testing.T) {
	ls := Layers()
	ls[0].Label = "mutated"
	if Layers()[0].Label == "mutated" {
		t.Fatal("Layers() handed out the package's own slice; a caller can corrupt the layer table")
	}
}

func TestKeysMatchDescriptorOrder(t *testing.T) {
	keys := Keys()
	ls := Layers()
	if len(keys) != len(ls) {
		t.Fatalf("Keys() has %d entries, Layers() %d", len(keys), len(ls))
	}
	for i := range keys {
		if keys[i] != ls[i].Key {
			t.Errorf("Keys()[%d] = %q, Layers()[%d].Key = %q", i, keys[i], i, ls[i].Key)
		}
	}
}

func TestGetAndIsLayer(t *testing.T) {
	d, ok := Get(LayerTableDescriptions)
	if !ok {
		t.Fatal("Get(table_descriptions) not found")
	}
	if !d.RefusesReasoningModels {
		t.Error("table_descriptions must advertise its reasoning-model rule so the dashboard can warn before a save")
	}
	if _, ok := Get("validation"); ok {
		t.Error(`Get("validation") must not resolve — the phase is called validation but the layers are validation_verifier and validation_refuter`)
	}
	if IsLayer("nope") {
		t.Error(`IsLayer("nope") = true`)
	}
	if !IsLayer(LayerAsk) {
		t.Error("IsLayer(ask) = false")
	}
}

func TestOnlyTableDescriptionsAdvertisesARule(t *testing.T) {
	for _, d := range Layers() {
		want := d.Key == LayerTableDescriptions
		if d.RefusesReasoningModels != want {
			t.Errorf("layer %q RefusesReasoningModels = %t, want %t — a pre-flight denylist for any other layer would block a model the next release accepts", d.Key, d.RefusesReasoningModels, want)
		}
	}
}

func TestLayerKeysArePersistedIdentifiers(t *testing.T) {
	// The keys are stored on the project document, appear in the API and
	// derive the managed-AI gateway aliases ("decisionbox-<layer>-model"),
	// which must be a legal alias token.
	for _, d := range Layers() {
		if d.Key != strings.ToLower(d.Key) {
			t.Errorf("layer key %q is not lower-case", d.Key)
		}
		for _, r := range d.Key {
			switch {
			case r >= 'a' && r <= 'z', r == '_':
			default:
				t.Errorf("layer key %q contains %q; keys must be [a-z_] so the derived gateway alias is legal", d.Key, r)
			}
		}
	}
}

func TestConfigVersionIsOneSoEveryExistingProjectIsUnconfigured(t *testing.T) {
	if ConfigVersion != 1 {
		t.Fatalf("ConfigVersion = %d, want 1 — bumping it re-gates every configured project, which is only correct when an existing configuration would be WRONG", ConfigVersion)
	}
	// A project document with no llm_config_version decodes as 0.
	if 0 >= ConfigVersion {
		t.Fatal("a project with no llm_config_version must not count as configured")
	}
}
