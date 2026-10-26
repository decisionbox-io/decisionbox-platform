package discovery

import (
	"context"
	"testing"

	agentplugin "github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
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

// layerLLMOn is layerLLM with a real provider behind the layer's client, so a
// test can read the request a phase actually put on the wire.
func layerLLMOn(t *testing.T, layer, providerID, provider, model string, window, outputCap int, p gollm.Provider) *ai.LayerLLM {
	t.Helper()
	client, err := ai.New(p, model)
	if err != nil {
		t.Fatalf("ai.New: %v", err)
	}
	client.SetProvenance("p", "r", provider)
	return &ai.LayerLLM{
		Layer: layer, ProviderID: providerID, Provider: provider, Model: model,
		Window: window, OutputCap: outputCap, Client: client,
	}
}

// Every phase must size its request against the model it is about to call.
//
// This is the bug the per-layer split creates and nothing else catches: the
// phase takes its CLIENT from its own layer and its BUDGET from a helper, and
// a helper that answers for the default layer sends a 32,000-token max_tokens
// to a model that accepts 1,024 — a 4xx on some providers and a silently
// truncated answer on others. Four call sites had it; two of them (questions
// and reflection) are drivable from here, and they fail the moment a phase
// goes back to budgeting against the default.
func TestEachPhaseBudgetsAgainstItsOwnLayer(t *testing.T) {
	const tinyCap = 1024

	cases := []struct {
		name  string
		layer string
		// run drives the phase on an orchestrator whose layer set gives the
		// phase's layer tinyCap and the default layer a generous one.
		run func(t *testing.T, o *Orchestrator)
	}{
		{
			name:  "questions",
			layer: llmlayer.LayerQuestions,
			run: func(t *testing.T, o *Orchestrator) {
				o.clarifyingQuestionsEnabled = true
				o.questionRepo = &fakeQuestionRepo{}
				items := []uncertaintyItem{{TargetType: "insight", TargetID: "i-1", Label: "churn", Reason: "unverifiable"}}
				if _, err := o.generateQuestions(context.Background(), items, nil, map[string]bool{"i-1": true}, 3); err != nil {
					t.Fatalf("generateQuestions: %v", err)
				}
			},
		},
		{
			name:  "reflection",
			layer: llmlayer.LayerReflection,
			run: func(t *testing.T, o *Orchestrator) {
				o.findingRepo = &fakeFindingRepo{}
				if _, err := o.generateReflection(context.Background(),
					&models.DiscoveryResult{Schemas: map[string]models.TableSchema{"ds.orders": {}}},
					agentplugin.DiscoveryPolicy{EvolutionMode: agentplugin.EvolutionModeSuggestOnly, FrontierPolicy: agentplugin.FrontierBalanced},
					consolidationResult{},
				); err != nil {
					t.Fatalf("generateReflection: %v", err)
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			small := testutil.NewMockLLMProvider()
			small.DefaultResponse = &gollm.ChatResponse{Content: `{"questions":[],"coverage_summary":"covered","learnings":[],"next_tasks":[]}`}
			big := testutil.NewMockLLMProvider()
			big.DefaultResponse = small.DefaultResponse

			deflt := layerLLMOn(t, llmlayer.DefaultLayer, llmlayer.DefaultProviderID, "bedrock", "opus", 200000, 32000, big)
			phase := layerLLMOn(t, c.layer, "cheap", "litellm", "qwen3-32b", 200000, tinyCap, small)

			o := &Orchestrator{
				aiClient:  deflt.Client,
				projectID: "p", runID: "r", datasets: []string{"ds"},
				llmInputWindow: 200000, llmOutputCap: 32000,
				layers: &fakeLayerSet{
					byLayer:  map[string]*ai.LayerLLM{llmlayer.DefaultLayer: deflt, c.layer: phase},
					fallback: deflt,
				},
			}
			c.run(t, o)

			if len(big.Calls) > 0 {
				t.Fatalf("the %s phase called the DEFAULT layer's model", c.name)
			}
			if len(small.Calls) == 0 {
				t.Fatalf("the %s phase never called its own layer's model", c.name)
			}
			if got := small.Calls[0].Request.MaxTokens; got > tinyCap {
				t.Errorf("max_tokens = %d on a layer that accepts %d — the phase budgeted against the default layer", got, tinyCap)
			}
		})
	}
}

// A window learned by overflowing one endpoint must not move another's
// budget, even when both name the same model on the same provider entry.
//
// A user-deployed Vertex or Bedrock endpoint is the real case: the model id
// identifies the family, endpoint_id identifies the deployment, and the two
// deployments can have different context windows. Provider construction and
// budget resolution already tell them apart; the calibration key did not.
func TestCalibrationIsScopedToTheEndpointThatLearnedIt(t *testing.T) {
	cfgA := map[string]string{"project_id": "gcp", "location": "us-central1", "endpoint_id": "ep-a"}
	cfgB := map[string]string{"project_id": "gcp", "location": "us-central1", "endpoint_id": "ep-b"}

	a := layerLLM(llmlayer.LayerExploration, llmlayer.DefaultProviderID, "vertex-ai", "gemini-2.5-pro", 200000, 8192)
	a.Config = gollm.ProviderConfig(cfgA)
	b := layerLLM(llmlayer.LayerAnalysis, llmlayer.DefaultProviderID, "vertex-ai", "gemini-2.5-pro", 200000, 8192)
	b.Config = gollm.ProviderConfig(cfgB)

	store := &fakeWindowStore{}
	o := &Orchestrator{
		aiClient:        a.Client,
		modelWindowRepo: store,
		layers: &fakeLayerSet{
			byLayer:  map[string]*ai.LayerLLM{llmlayer.LayerExploration: a, llmlayer.LayerAnalysis: b, llmlayer.DefaultLayer: a},
			fallback: a,
		},
	}

	o.applyCalibratedWindowFor(context.Background(), a, "gemini-2.5-pro", 40960)

	if w, _ := o.budgetFor(llmlayer.LayerExploration); w != 40960 {
		t.Errorf("ep-a window = %d, want the calibrated 40960", w)
	}
	if w, _ := o.budgetFor(llmlayer.LayerAnalysis); w != 200000 {
		t.Errorf("ep-b window = %d, want its own 200000 — the two endpoints share only a model id", w)
	}
	// And the persisted key separates them, so the next run reloads them apart.
	if len(store.saved) != 1 {
		t.Fatalf("persisted %d windows, want 1", len(store.saved))
	}
	keyA := ModelWindowKey("gemini-2.5-pro", gollm.ProviderConfig(cfgA))
	keyB := ModelWindowKey("gemini-2.5-pro", gollm.ProviderConfig(cfgB))
	if keyA == keyB {
		t.Errorf("ModelWindowKey is %q for both endpoints", keyA)
	}
	if store.saved[0].model != keyA {
		t.Errorf("persisted under %q, want %q", store.saved[0].model, keyA)
	}
}

// The validation output budget is a deployment setting, but it cannot exceed
// what the layer's model will accept.
//
// Per-layer models made this reachable: a validation layer can now be pointed
// at a small model, and a 4,000-token request to a model that accepts 1,024 is
// a 4xx on every validation call. The deployment setting still wins when it is
// the lower of the two — tuning the budget down is a legitimate thing to do.
func TestValidationOutputBudgetIsClampedToTheLayerModel(t *testing.T) {
	small := layerLLM(llmlayer.LayerValidationVerifier, "cheap", "litellm", "qwen3-32b", 128000, 1024)
	big := layerLLM(llmlayer.LayerValidationRefuter, llmlayer.DefaultProviderID, "bedrock", "opus", 200000, 32000)

	if got := clampToLayerCap(4000, small); got != 1024 {
		t.Errorf("clamped budget = %d, want the layer's 1024", got)
	}
	if got := clampToLayerCap(3000, big); got != 3000 {
		t.Errorf("budget = %d, want the deployment's 3000 — the layer accepts more", got)
	}
	// An unresolved layer, or one with no cap, leaves the budget alone rather
	// than collapsing it to zero.
	if got := clampToLayerCap(4000, nil); got != 4000 {
		t.Errorf("budget with no layer = %d, want 4000", got)
	}
	noCap := layerLLM(llmlayer.LayerValidationVerifier, llmlayer.DefaultProviderID, "bedrock", "opus", 200000, 0)
	if got := clampToLayerCap(4000, noCap); got != 4000 {
		t.Errorf("budget with no resolved cap = %d, want 4000", got)
	}
}
