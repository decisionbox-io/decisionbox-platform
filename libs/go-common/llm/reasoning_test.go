package llm

import (
	"sync"
	"testing"
)

// onceReasoningCatalog keeps the registration single — RegisterWithMeta
// panics on a duplicate and the registry is package-global.
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

// A model the catalog KNOWS is not a reasoning model must not be sent an
// effort: those reject the field outright, which turns a quality setting into
// a 400 on a request that used to work.
func TestNormalizeReasoningEffortDropsItForAKnownNonReasoningModel(t *testing.T) {
	registerReasoningCatalog(t)
	got := NormalizeReasoningEffort(reasoningCatalogProvider, "",
		ChatRequest{Model: "plain-model", ReasoningEffort: ReasoningEffortHigh})
	if got.ReasoningEffort != ReasoningEffortDefault {
		t.Errorf("effort = %q, want it dropped for a known non-reasoning model", got.ReasoningEffort)
	}
}

// A model the catalog knows DOES reason keeps what the caller asked for.
func TestNormalizeReasoningEffortKeepsItForAReasoningModel(t *testing.T) {
	registerReasoningCatalog(t)
	got := NormalizeReasoningEffort(reasoningCatalogProvider, "",
		ChatRequest{Model: "thinking-model", ReasoningEffort: ReasoningEffortHigh})
	if got.ReasoningEffort != ReasoningEffortHigh {
		t.Errorf("effort = %q, want high", got.ReasoningEffort)
	}
}

// An UNKNOWN model keeps it. This is the case that matters behind a managed
// gateway: the alias hides a reasoning model the catalog cannot see through,
// and dropping the field whenever the catalog cannot answer is exactly how an
// operator's override silently loses to the gateway's own default.
func TestNormalizeReasoningEffortKeepsItForAnUnknownModel(t *testing.T) {
	registerReasoningCatalog(t)
	got := NormalizeReasoningEffort(reasoningCatalogProvider, "",
		ChatRequest{Model: "decisionbox-validation-refuter-model", ReasoningEffort: ReasoningEffortLow})
	if got.ReasoningEffort != ReasoningEffortLow {
		t.Errorf("effort = %q, want it kept for a model the catalog does not know", got.ReasoningEffort)
	}
	// Same for a provider with no catalog at all.
	got = NormalizeReasoningEffort("no-such-provider", "",
		ChatRequest{Model: "whatever", ReasoningEffort: ReasoningEffortLow})
	if got.ReasoningEffort != ReasoningEffortLow {
		t.Errorf("effort = %q, want it kept when the provider has no catalog", got.ReasoningEffort)
	}
}

// The decision is made about the model that actually goes on the wire: the
// request's when it names one, the provider's fallback otherwise.
func TestNormalizeReasoningEffortUsesTheEffectiveModel(t *testing.T) {
	registerReasoningCatalog(t)
	got := NormalizeReasoningEffort(reasoningCatalogProvider, "plain-model",
		ChatRequest{ReasoningEffort: ReasoningEffortHigh})
	if got.ReasoningEffort != ReasoningEffortDefault {
		t.Errorf("effort = %q, want it dropped — the fallback model is the one being sent", got.ReasoningEffort)
	}
}

// ResolveReasoningEffort is what turns the "on" sentinel into a level that can
// travel. The OpenAI-compatible wire cannot express "on" and omits the field,
// so a caller that left it as the sentinel lost the deployment's override at
// the first hop.
func TestResolveReasoningEffortTurnsTheSentinelIntoALevel(t *testing.T) {
	t.Setenv(ReasoningEffortEnvVar, "medium")
	if got := ResolveReasoningEffort(ReasoningEffortOn); got != ReasoningEffortMedium {
		t.Errorf("on resolved to %q, want the deployment's medium", got)
	}
	if got := ResolveReasoningEffort(ReasoningEffortHigh); got != ReasoningEffortHigh {
		t.Errorf("an explicit level must survive, got %q", got)
	}
}
