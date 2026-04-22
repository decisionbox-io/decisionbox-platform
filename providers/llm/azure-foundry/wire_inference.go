package azurefoundry

import (
	"strings"

	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/modelcatalog"
)

// inferAzureWire is Azure AI Foundry's wire-inference rule.
// Foundry deployment names typically match the model family:
//   - claude-* → routed through {endpoint}/anthropic/v1/messages
//   - everything else → routed through {endpoint}/openai/v1/chat/completions
//
// Foundry-hosted Claude deployment names always start with "claude-",
// so a prefix match is reliable. Any other deployment name (gpt-*,
// mistral-*, o-series, or a user-custom alias) is routed through the
// OpenAI-compat endpoint.
func inferAzureWire(id string) modelcatalog.Wire {
	if strings.HasPrefix(id, "claude-") {
		return modelcatalog.Anthropic
	}
	// Known OpenAI-shape model families on Azure Foundry.
	for _, pfx := range []string{"gpt-", "gpt4", "gpt3", "o1", "o3", "o4", "text-", "mistral", "phi-", "llama", "meta-llama"} {
		if strings.HasPrefix(id, pfx) {
			return modelcatalog.OpenAICompat
		}
	}
	// Unknown deployment name — let the caller set wire_override
	// rather than guess.
	return modelcatalog.Unknown
}

func init() {
	modelcatalog.SetWireInferrer("azure-foundry", inferAzureWire)
}
