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

// A model this catalog DOES know and does not mark as reasoning must not be
// sent the field: OpenAI rejects reasoning_effort on those outright, so
// forwarding it turns a quality setting into a 400 on a request that worked.
func TestKnownNonReasoningModelLosesTheEffort(t *testing.T) {
	const model = "gpt-4o"
	if _, found := catalogEntry(t, model); !found {
		t.Skipf("%s is not in this catalog; nothing to assert", model)
	}
	got := gollm.NormalizeReasoningEffort(providerName, "",
		gollm.ChatRequest{Model: model, ReasoningEffort: gollm.ReasoningEffortHigh})
	if got.ReasoningEffort != gollm.ReasoningEffortDefault {
		t.Errorf("%s: effort = %q, want it dropped — the model rejects the field", model, got.ReasoningEffort)
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
