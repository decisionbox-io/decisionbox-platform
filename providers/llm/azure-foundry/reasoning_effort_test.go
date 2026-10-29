package azurefoundry

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// Azure serves the same GPT families as OpenAI, over the same
// OpenAI-compatible wire, so the non-reasoning ones reject reasoning_effort
// here too. The gate reads the catalog of the provider actually being called,
// so marking only OpenAI's rows left a reasoning-enabled Azure project
// sending a field its model rejects — a 400 on requests that used to work.
func TestAzureNonReasoningModelsRejectTheEffort(t *testing.T) {
	for _, model := range []string{"gpt-4.1", "gpt-4o", "gpt-4o-mini"} {
		if !gollm.RejectsReasoningEffort(providerName, model) {
			t.Errorf("%s: catalog does not declare the rejection", model)
		}
		got := gollm.NormalizeReasoningEffort(providerName, "",
			gollm.ChatRequest{Model: model, ReasoningEffort: gollm.ReasoningEffortHigh})
		if got.ReasoningEffort != gollm.ReasoningEffortDefault {
			t.Errorf("%s: effort = %q, want it dropped", model, got.ReasoningEffort)
		}
	}
}

// Azure's GPT-5 rows do accept it, so they must keep what the caller asked
// for — the point of the explicit flag is that it marks rejecters only.
func TestAzureReasoningModelsKeepTheEffort(t *testing.T) {
	for _, model := range []string{"gpt-5", "gpt-5-mini"} {
		got := gollm.NormalizeReasoningEffort(providerName, "",
			gollm.ChatRequest{Model: model, ReasoningEffort: gollm.ReasoningEffortHigh})
		if got.ReasoningEffort != gollm.ReasoningEffortHigh {
			t.Errorf("%s: effort = %q, want high", model, got.ReasoningEffort)
		}
	}
}
