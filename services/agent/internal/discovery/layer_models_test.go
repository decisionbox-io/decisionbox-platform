package discovery

import (
	"context"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
)

// fakeLayerSet is a minimal ai.LayerSet for the orchestrator's layer plumbing.
type fakeLayerSet struct {
	byLayer  map[string]*ai.LayerLLM
	fallback *ai.LayerLLM
}

func (f *fakeLayerSet) For(layer string) *ai.LayerLLM {
	if l, ok := f.byLayer[layer]; ok {
		return l
	}
	return f.fallback
}

func layerLLM(layer, providerID, provider, model string, window, cap int) *ai.LayerLLM {
	client, _ := ai.New(nil, model)
	return &ai.LayerLLM{
		Layer: layer, ProviderID: providerID, Provider: provider, Model: model,
		Window: window, OutputCap: cap, Client: client,
	}
}

// A nil layer set is the single-model path: every layer resolves to the one
// client, and the no-argument wrappers keep working. This is what makes the
// existing suite valid without a layer set.
func TestNilLayerSetKeepsTheSingleClientPath(t *testing.T) {
	client, _ := ai.New(nil, "opus")
	o := &Orchestrator{
		aiClient:       client,
		llmProvider:    "bedrock",
		llmModel:       "opus",
		llmInputWindow: 200000,
		llmOutputCap:   32000,
	}
	for _, layer := range append([]string{llmlayer.DefaultLayer}, llmlayer.Keys()...) {
		if got := o.ai(layer); got != client {
			t.Fatalf("layer %q resolved to a different client with no layer set", layer)
		}
		w, c := o.budgetFor(layer)
		if w != 200000 || c != 32000 {
			t.Errorf("layer %q budget = %d/%d, want the run-start budget", layer, w, c)
		}
	}
	if o.layer(llmlayer.LayerAsk) != nil {
		t.Error("layer() must be nil with no layer set")
	}
}

func TestPerLayerBudgetsComeFromTheLayersOwnModel(t *testing.T) {
	deflt := layerLLM(llmlayer.DefaultLayer, llmlayer.DefaultProviderID, "bedrock", "opus", 200000, 32000)
	cheap := layerLLM(llmlayer.LayerSQLRepair, "cheap", "bedrock", "haiku", 200000, 8192)
	o := &Orchestrator{
		aiClient: deflt.Client,
		layers: &fakeLayerSet{
			byLayer:  map[string]*ai.LayerLLM{llmlayer.DefaultLayer: deflt, llmlayer.LayerSQLRepair: cheap},
			fallback: deflt,
		},
	}
	if w, c := o.budgetFor(llmlayer.LayerSQLRepair); w != 200000 || c != 8192 {
		t.Errorf("sql_repair budget = %d/%d, want 200000/8192 — the layer's own cap, not the analysis model's", w, c)
	}
	if w, c := o.budgetFor(llmlayer.LayerAnalysis); w != 200000 || c != 32000 {
		t.Errorf("analysis budget = %d/%d, want the default layer's", w, c)
	}
	if o.ai(llmlayer.LayerSQLRepair) != cheap.Client {
		t.Error("sql_repair must use its own client")
	}
}

// A window learned from one model must not change another's budget. With one
// model this could not show, which is why it was wrong and invisible.
func TestCalibrationIsScopedToTheTargetThatLearnedIt(t *testing.T) {
	deflt := layerLLM(llmlayer.DefaultLayer, llmlayer.DefaultProviderID, "bedrock", "opus", 200000, 32000)
	cheap := layerLLM(llmlayer.LayerSQLRepair, "cheap", "litellm", "qwen3-32b", 128000, 8192)
	store := &fakeWindowStore{}
	o := &Orchestrator{
		aiClient:        deflt.Client,
		modelWindowRepo: store,
		layers: &fakeLayerSet{
			byLayer:  map[string]*ai.LayerLLM{llmlayer.DefaultLayer: deflt, llmlayer.LayerSQLRepair: cheap},
			fallback: deflt,
		},
	}

	o.applyCalibratedWindowFor(context.Background(), cheap, "qwen3-32b", 40960)

	if w, _ := o.budgetFor(llmlayer.LayerSQLRepair); w != 40960 {
		t.Errorf("sql_repair window = %d, want the calibrated 40960", w)
	}
	if w, _ := o.budgetFor(llmlayer.LayerAnalysis); w != 200000 {
		t.Errorf("analysis window = %d, want its own 200000 — a window learned on the cheap model must not shrink the analysis budget", w)
	}
	if len(store.saved) != 1 {
		t.Fatalf("persisted %d windows, want 1", len(store.saved))
	}
	if store.saved[0].providerID != "cheap" || store.saved[0].provider != "litellm" {
		t.Errorf("persisted under %+v, want the entry that learned it", store.saved[0])
	}
}

// Two entries pointing at different deployments can name the same model
// alias; a window learned on one must not be applied to the other.
func TestTwoEntriesSharingAModelAliasDoNotContaminateEachOther(t *testing.T) {
	a := layerLLM(llmlayer.LayerExploration, "deploy-a", "litellm", "qwen3-32b", 128000, 8192)
	b := layerLLM(llmlayer.LayerAnalysis, "deploy-b", "litellm", "qwen3-32b", 128000, 8192)
	o := &Orchestrator{
		aiClient: a.Client,
		layers: &fakeLayerSet{
			byLayer:  map[string]*ai.LayerLLM{llmlayer.LayerExploration: a, llmlayer.LayerAnalysis: b, llmlayer.DefaultLayer: a},
			fallback: a,
		},
	}
	o.applyCalibratedWindowFor(context.Background(), a, "qwen3-32b", 40960)

	if w, _ := o.budgetFor(llmlayer.LayerExploration); w != 40960 {
		t.Errorf("deploy-a window = %d, want the calibrated 40960", w)
	}
	if w, _ := o.budgetFor(llmlayer.LayerAnalysis); w != 128000 {
		t.Errorf("deploy-b window = %d, want its own 128000 — the two deployments share only an alias", w)
	}
}

func TestPerLayerReasoningDrivesTheExplorationHeadroom(t *testing.T) {
	reasoningLayer := layerLLM(llmlayer.LayerExploration, llmlayer.DefaultProviderID, "bedrock", "opus", 200000, 32000)
	reasoningLayer.Reasoning = true
	plain := layerLLM(llmlayer.LayerAnalysis, llmlayer.DefaultProviderID, "bedrock", "opus", 200000, 32000)
	o := &Orchestrator{
		aiClient: plain.Client,
		layers: &fakeLayerSet{
			byLayer:  map[string]*ai.LayerLLM{llmlayer.LayerExploration: reasoningLayer, llmlayer.LayerAnalysis: plain, llmlayer.DefaultLayer: plain},
			fallback: plain,
		},
	}
	if !o.effectiveReasoningFor(llmlayer.LayerExploration) {
		t.Error("exploration must read its own reasoning opt-in")
	}
	if o.effectiveReasoningFor(llmlayer.LayerAnalysis) {
		t.Error("analysis must not inherit another layer's reasoning setting")
	}
}

func TestBudgetFallsBackToTheCatalogWithNoResolvedValues(t *testing.T) {
	// The path the unit tests take when an Orchestrator is built without
	// pre-resolved budgets.
	o := &Orchestrator{llmProvider: "bedrock", llmModel: "anthropic.claude-haiku-4-5", llmConfig: gollm.ProviderConfig{}}
	w, c := o.budgetFor(llmlayer.LayerAnalysis)
	if w <= 0 || c <= 0 {
		t.Errorf("catalog fallback gave %d/%d, want positive values", w, c)
	}
}
