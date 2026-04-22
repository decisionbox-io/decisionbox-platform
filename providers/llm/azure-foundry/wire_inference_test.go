package azurefoundry

import (
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/modelcatalog"
)

func TestInferAzureWire(t *testing.T) {
	tests := []struct {
		id   string
		want modelcatalog.Wire
	}{
		// Claude deployments on Azure Foundry
		{"claude-opus-4-6", modelcatalog.Anthropic},
		{"claude-sonnet-4-6", modelcatalog.Anthropic},
		{"claude-99-future", modelcatalog.Anthropic},

		// OpenAI-compat families
		{"gpt-5", modelcatalog.OpenAICompat},
		{"gpt-4o", modelcatalog.OpenAICompat},
		{"gpt-4.1", modelcatalog.OpenAICompat},
		{"o3", modelcatalog.OpenAICompat},
		{"o4-mini", modelcatalog.OpenAICompat},
		{"mistral-large-2411", modelcatalog.OpenAICompat},
		{"phi-4", modelcatalog.OpenAICompat},
		{"llama-3-70b", modelcatalog.OpenAICompat},

		// Unknown / custom deployment name
		{"my-custom-alias", modelcatalog.Unknown},
		{"", modelcatalog.Unknown},
	}
	for _, tt := range tests {
		if got := inferAzureWire(tt.id); got != tt.want {
			t.Errorf("inferAzureWire(%q) = %q, want %q", tt.id, got, tt.want)
		}
	}
}

func TestInferAzureWire_RegisteredIntoCatalog(t *testing.T) {
	if got := modelcatalog.InferWire("azure-foundry", "claude-99"); got != modelcatalog.Anthropic {
		t.Errorf("InferWire(azure-foundry, claude-99) = %q", got)
	}
	if got := modelcatalog.InferWire("azure-foundry", "gpt-6"); got != modelcatalog.OpenAICompat {
		t.Errorf("InferWire(azure-foundry, gpt-6) = %q", got)
	}
}
