package azurefoundry

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestInferAzureWire(t *testing.T) {
	tests := []struct {
		id   string
		want gollm.Wire
	}{
		// Claude deployments on Azure Foundry
		{"claude-opus-4-6", gollm.WireAnthropic},
		{"claude-sonnet-4-6", gollm.WireAnthropic},
		{"claude-99-future", gollm.WireAnthropic},

		// OpenAI-compat families
		{"gpt-5", gollm.WireOpenAICompat},
		{"gpt-4o", gollm.WireOpenAICompat},
		{"gpt-4.1", gollm.WireOpenAICompat},
		{"o3", gollm.WireOpenAICompat},
		{"o4-mini", gollm.WireOpenAICompat},
		{"mistral-large-2411", gollm.WireOpenAICompat},
		{"phi-4", gollm.WireOpenAICompat},
		{"llama-3-70b", gollm.WireOpenAICompat},

		// Unknown / custom deployment name
		{"my-custom-alias", gollm.WireUnknown},
		{"", gollm.WireUnknown},
		// Family-only short forms — must NOT be inferred (catalog
		// alias provides the right wire+cap).
		{"opus-4-7", gollm.WireUnknown},
	}
	for _, tt := range tests {
		if got := inferAzureWire(tt.id); got != tt.want {
			t.Errorf("inferAzureWire(%q) = %q, want %q", tt.id, got, tt.want)
		}
	}
}

func TestInferAzureWire_WiredIntoProviderMeta(t *testing.T) {
	meta, ok := gollm.GetProviderMeta(providerName)
	if !ok {
		t.Fatal("azure-foundry not registered")
	}
	if meta.FamilyInferrer == nil {
		t.Fatal("FamilyInferrer is nil")
	}
	wire, err := meta.ResolveWire("claude-99", gollm.WireUnknown)
	if err != nil || wire != gollm.WireAnthropic {
		t.Errorf("ResolveWire(claude-99) = (%q, %v)", wire, err)
	}
	wire, err = meta.ResolveWire("gpt-6", gollm.WireUnknown)
	if err != nil || wire != gollm.WireOpenAICompat {
		t.Errorf("ResolveWire(gpt-6) = (%q, %v)", wire, err)
	}
}
