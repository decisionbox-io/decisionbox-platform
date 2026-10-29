package openai

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// Against the REAL catalog, not a stub, because the whole question is what
// this catalog does and does not know.
//
// A managed gateway's alias must keep the effort the layer resolved. The
// catalog is keyed on (provider, model) and the alias deliberately hides the
// backend, so it is absent — and absent must mean "the caller knows more than
// I do", not "not a reasoning model". Reading it as the latter is how an
// operator's LLM_REASONING_EFFORT silently loses to the gateway's own default
// on every managed call.
func TestGatewayAliasKeepsItsResolvedEffort(t *testing.T) {
	for _, alias := range []string{
		"decisionbox-analysis-model",
		"decisionbox-validation-refuter-model",
		"decisionbox-exploration-model",
	} {
		got := gollm.NormalizeReasoningEffort(providerName, "",
			gollm.ChatRequest{Model: alias, ReasoningEffort: gollm.ReasoningEffortLow})
		if got.ReasoningEffort != gollm.ReasoningEffortLow {
			t.Errorf("%s: effort = %q, want the resolved low to survive", alias, got.ReasoningEffort)
		}
	}
}

// The non-reasoning chat families reject reasoning_effort outright, so
// forwarding it would turn a quality setting into a 400 on a request that
// worked. Those rows declare the rejection explicitly.
func TestDeclaredRejectersLoseTheEffort(t *testing.T) {
	for _, model := range []string{"gpt-4o", "gpt-4o-mini", "gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano"} {
		if _, found := catalogEntry(t, model); !found {
			t.Errorf("%s is not in this catalog — the premise of this test", model)
			continue
		}
		got := gollm.NormalizeReasoningEffort(providerName, "",
			gollm.ChatRequest{Model: model, ReasoningEffort: gollm.ReasoningEffortHigh})
		if got.ReasoningEffort != gollm.ReasoningEffortDefault {
			t.Errorf("%s: effort = %q, want it dropped — the model rejects the field", model, got.ReasoningEffort)
		}
	}
}

// catalogEntry reports whether this provider's catalog knows a model, so the
// test above states its premise instead of assuming it.
func catalogEntry(t *testing.T, model string) (gollm.ModelEntry, bool) {
	t.Helper()
	meta, ok := gollm.GetProviderMeta(providerName)
	if !ok {
		t.Fatalf("provider %q is not registered", providerName)
	}
	return meta.FindModel(model)
}

// OpenAI's own reasoning families accept reasoning_effort, so the newly wired
// setting must reach them. They do not set Reasoning in this catalog — most
// rows never do — which is exactly why a missing flag cannot be read as a
// rejection.
func TestNativeReasoningModelsKeepTheirEffort(t *testing.T) {
	for _, model := range []string{"o3", "o4-mini", "gpt-5", "gpt-5-mini", "gpt-5-nano"} {
		if _, found := catalogEntry(t, model); !found {
			t.Errorf("%s is not in this catalog — the premise of this test", model)
			continue
		}
		got := gollm.NormalizeReasoningEffort(providerName, "",
			gollm.ChatRequest{Model: model, ReasoningEffort: gollm.ReasoningEffortHigh})
		if got.ReasoningEffort != gollm.ReasoningEffortHigh {
			t.Errorf("%s: effort = %q, want high — this model accepts the field", model, got.ReasoningEffort)
		}
	}
}
