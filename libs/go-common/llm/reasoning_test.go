package llm

import (
	"sync"
	"testing"
)

var onceReasoningCatalog sync.Once

const reasoningCatalogProvider = "test-reasoning-catalog"

func registerReasoningCatalog(t *testing.T) {
	t.Helper()
	onceReasoningCatalog.Do(func() {
		RegisterWithMeta(reasoningCatalogProvider, mockFactory, ProviderMeta{
			Name: "Reasoning catalog test",
			Models: []ModelEntry{
				{ID: "plain-model", Wire: WireOpenAICompat, MaxOutputTokens: 16384, MaxInputTokens: 128000},
				{ID: "thinking-model", Wire: WireOpenAICompat, MaxOutputTokens: 64000, MaxInputTokens: 200000, Reasoning: true},
			},
		})
	})
}

// A model the catalog identifies as a reasoning model keeps the effort.
func TestNormalizeReasoningEffortKeepsItForAReasoningModel(t *testing.T) {
	registerReasoningCatalog(t)
	got := NormalizeReasoningEffort(reasoningCatalogProvider, "",
		ChatRequest{Model: "thinking-model", ReasoningEffort: ReasoningEffortHigh})
	if got.ReasoningEffort != ReasoningEffortHigh {
		t.Errorf("effort = %q, want high", got.ReasoningEffort)
	}
}

// Everything else loses it. The gate is positive evidence because the set of
// targets whose capability we cannot know is open — a customer-named Azure
// deployment, a managed gateway's alias — and the field is rejected outright
// by the chat models that predate it. Omitting it costs only the model's own
// default, which is what it used before this existed; sending it to the wrong
// target costs a 400 on every call.
func TestNormalizeReasoningEffortDropsItWithoutPositiveEvidence(t *testing.T) {
	registerReasoningCatalog(t)
	cases := []struct{ provider, model, why string }{
		{reasoningCatalogProvider, "plain-model", "a catalogued non-reasoning model"},
		{reasoningCatalogProvider, "decisionbox-analysis-model", "a gateway alias this catalog does not list"},
		{reasoningCatalogProvider, "gpt-4o-my-deployment-2024", "a customer-named deployment id"},
		{"no-such-provider", "anything", "a provider with no catalog"},
	}
	for _, c := range cases {
		got := NormalizeReasoningEffort(c.provider, "", ChatRequest{Model: c.model, ReasoningEffort: ReasoningEffortHigh})
		if got.ReasoningEffort != ReasoningEffortDefault {
			t.Errorf("%s (%s): effort = %q, want it omitted", c.model, c.why, got.ReasoningEffort)
		}
	}
}

// The decision is made about the model that actually goes on the wire: the
// request's when it names one, the provider's fallback otherwise.
func TestNormalizeReasoningEffortUsesTheEffectiveModel(t *testing.T) {
	registerReasoningCatalog(t)
	got := NormalizeReasoningEffort(reasoningCatalogProvider, "thinking-model",
		ChatRequest{ReasoningEffort: ReasoningEffortHigh})
	if got.ReasoningEffort != ReasoningEffortHigh {
		t.Errorf("effort = %q, want high — the fallback model is the one being sent", got.ReasoningEffort)
	}
	got = NormalizeReasoningEffort(reasoningCatalogProvider, "plain-model",
		ChatRequest{ReasoningEffort: ReasoningEffortHigh})
	if got.ReasoningEffort != ReasoningEffortDefault {
		t.Errorf("effort = %q, want it omitted for the fallback model", got.ReasoningEffort)
	}
}

// ResolveReasoningEffort is what turns the "on" sentinel into a level that can
// travel, and what applies the deployment override.
func TestResolveReasoningEffortTurnsTheSentinelIntoALevel(t *testing.T) {
	t.Setenv(ReasoningEffortEnvVar, "medium")
	if got := ResolveReasoningEffort(ReasoningEffortOn); got != ReasoningEffortMedium {
		t.Errorf("on resolved to %q, want the deployment's medium", got)
	}
	if got := ResolveReasoningEffort(ReasoningEffortHigh); got != ReasoningEffortHigh {
		t.Errorf("an explicit level must survive, got %q", got)
	}
}
