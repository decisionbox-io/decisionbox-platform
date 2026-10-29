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
	registerRejectCatalog(t)
	got := NormalizeReasoningEffort(rejectCatalogProvider, "legacy-chat",
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

// Silence is not a denial. Most catalog rows never set Reasoning — OpenAI's
// own o-series and GPT-5 among them — so a model that merely fails to claim
// reasoning must still receive the effort the caller asked for. Reading that
// silence as a rejection stripped the field from models that accept it, with
// nothing visible but a worse answer.
func TestNormalizeReasoningEffortKeepsItForAModelThatSimplyDoesNotClaimReasoning(t *testing.T) {
	registerReasoningCatalog(t)
	got := NormalizeReasoningEffort(reasoningCatalogProvider, "",
		ChatRequest{Model: "plain-model", ReasoningEffort: ReasoningEffortHigh})
	if got.ReasoningEffort != ReasoningEffortHigh {
		t.Errorf("effort = %q, want it kept — the row does not declare a rejection", got.ReasoningEffort)
	}
}

// Only an explicit declaration drops it.
func TestNormalizeReasoningEffortDropsItForADeclaredRejecter(t *testing.T) {
	registerRejectCatalog(t)
	got := NormalizeReasoningEffort(rejectCatalogProvider, "",
		ChatRequest{Model: "legacy-chat", ReasoningEffort: ReasoningEffortHigh})
	if got.ReasoningEffort != ReasoningEffortDefault {
		t.Errorf("effort = %q, want it dropped for a declared rejecter", got.ReasoningEffort)
	}
}

var onceRejectCatalog sync.Once

const rejectCatalogProvider = "test-reject-effort"

// registerRejectCatalog registers a provider with one row that declares it
// rejects a reasoning-effort field — the only thing that drops the effort now.
func registerRejectCatalog(t *testing.T) {
	t.Helper()
	onceRejectCatalog.Do(func() {
		RegisterWithMeta(rejectCatalogProvider, mockFactory, ProviderMeta{
			Name: "Reject-effort test",
			Models: []ModelEntry{
				{ID: "legacy-chat", Wire: WireOpenAICompat, MaxOutputTokens: 4096, MaxInputTokens: 128000,
					RejectsReasoningEffort: true},
			},
		})
	})
}
