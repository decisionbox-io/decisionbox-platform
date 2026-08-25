package openai

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// TestCatalog_GatewayAliasMaxOutputTokens pins the DecisionBox AI gateway
// aliases to their underlying model's output cap (Opus 128K, the blurb model
// 64K) rather than the provider's generic 16K default. Callers size their
// request from GetMaxOutputTokens, so a wrong value truncates a large
// generation.
func TestCatalog_GatewayAliasMaxOutputTokens(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		{"decisionbox-analysis-model", 128000},
		{"decisionbox-blurb-model", 64000},
	}
	for _, c := range cases {
		t.Run(c.model, func(t *testing.T) {
			if got := gollm.GetMaxOutputTokens("openai", c.model); got != c.want {
				t.Errorf("GetMaxOutputTokens(openai, %q) = %d, want %d", c.model, got, c.want)
			}
		})
	}

	// A model the catalog doesn't know falls back to the provider default
	// (64K for unknown models, #338).
	if got := gollm.GetMaxOutputTokens("openai", "some-unknown-model"); got != 65536 {
		t.Errorf("unknown model fell back to %d, want the 65536 default", got)
	}
}

// TestCatalog_GatewayAliasesDispatchable confirms the gateway chat aliases are
// catalogued as the OpenAI wire (so the LLM picker shows them and the agent
// dispatches them), while the embedding alias is absent from the LLM catalog.
func TestCatalog_GatewayAliasesDispatchable(t *testing.T) {
	meta, ok := gollm.GetProviderMeta("openai")
	if !ok {
		t.Fatal("openai provider meta not registered")
	}
	for _, id := range []string{"decisionbox-analysis-model", "decisionbox-blurb-model"} {
		e, found := meta.FindModel(id)
		if !found {
			t.Errorf("%q missing from the openai catalog", id)
			continue
		}
		if e.Wire != gollm.WireOpenAICompat {
			t.Errorf("%q wire = %q, want %q", id, e.Wire, gollm.WireOpenAICompat)
		}
	}
	if _, found := meta.FindModel("decisionbox-embed-model"); found {
		t.Error("decisionbox-embed-model must not be in the LLM catalog (it is an embedding model)")
	}
}
