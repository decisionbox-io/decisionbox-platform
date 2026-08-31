package discovery

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// TestOrchestrator_EffectiveReasoning covers the model-agnostic "Enable
// reasoning" resolution: the per-project toggle turns it on for ANY model, the
// legacy llm.config flag still works (back-compat), and with everything off an
// uncatalogued non-reasoning model is NOT reasoning-effective (so big models
// keep today's exploration ceiling).
func TestOrchestrator_EffectiveReasoning(t *testing.T) {
	// Per-project toggle on → reasoning-effective regardless of provider/model.
	if !(&Orchestrator{reasoningEnabled: true}).effectiveReasoning() {
		t.Errorf("reasoningEnabled=true must be reasoning-effective")
	}
	// Everything off + uncatalogued model → not reasoning-effective.
	if (&Orchestrator{llmProvider: "litellm", llmModel: "kimi-k2.7-coden"}).effectiveReasoning() {
		t.Errorf("toggle off + uncatalogued model must not be reasoning-effective")
	}
	// Back-compat: a legacy llm.config reasoning_enabled flag still counts.
	o := &Orchestrator{llmConfig: gollm.ProviderConfig{gollm.ReasoningEnabledKey: "true"}}
	if !o.effectiveReasoning() {
		t.Errorf("legacy llm.config reasoning_enabled must still be honored")
	}
}
