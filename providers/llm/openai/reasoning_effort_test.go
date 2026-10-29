package openai

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// Against the REAL catalog, because the question is what this catalog can and
// cannot answer.
//
// No row here claims Reasoning, so no OpenAI-compatible request from this
// provider carries reasoning_effort. That is the safe direction: the field is
// rejected outright by gpt-4o and gpt-4.1, a customer's deployment id is
// unknowable, and a model that does take an effort setting simply applies its
// own default — which is what it did before the field was wired at all.
func TestNoEffortLeavesThisProviderWithoutPositiveEvidence(t *testing.T) {
	for _, model := range []string{
		"gpt-4o", "gpt-4.1", "gpt-4o-mini", // reject the field outright
		"o3", "o4-mini", "gpt-5", // accept it, but the catalog does not say so
		"decisionbox-analysis-model", // a gateway alias: per-org backend
		"gpt-4o-my-own-deployment",   // a name only the customer knows
	} {
		got := gollm.NormalizeReasoningEffort(providerName, "",
			gollm.ChatRequest{Model: model, ReasoningEffort: gollm.ReasoningEffortHigh})
		if got.ReasoningEffort != gollm.ReasoningEffortDefault {
			t.Errorf("%s: effort = %q, want it omitted — the catalog gives no positive evidence",
				model, got.ReasoningEffort)
		}
	}
}

// The gateway aliases must stay un-marked: their backend is chosen per
// organisation, so this catalog cannot speak for their reasoning, and the
// gateway resolves effort itself against the real model id.
func TestGatewayAliasesDoNotClaimReasoning(t *testing.T) {
	meta, ok := gollm.GetProviderMeta(providerName)
	if !ok {
		t.Fatalf("provider %q is not registered", providerName)
	}
	for _, alias := range []string{"decisionbox-analysis-model", "decisionbox-blurb-model"} {
		e, found := meta.FindModel(alias)
		if !found {
			t.Errorf("%s is not catalogued — its token limits would fall back to the generic default", alias)
			continue
		}
		if e.Reasoning {
			t.Errorf("%s claims Reasoning; this row cannot know what the alias routes to", alias)
		}
	}
}
