package azurefoundry

import (
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// inferAzureWire is Azure AI Foundry's FamilyInferrer.
//
// Foundry deployment names typically match the model family:
//   - claude-* → routed through {endpoint}/anthropic/v1/messages
//   - gpt-* / o1 / o3 / o4 / mistral-* / phi-* / llama / meta-llama →
//     routed through {endpoint}/openai/v1/chat/completions
//
// Customer-renamed deployments that don't follow either prefix
// convention return WireUnknown — the user is expected to set
// wire_override explicitly rather than have us guess.
func inferAzureWire(model string) gollm.Wire {
	if strings.HasPrefix(model, "claude-") {
		return gollm.WireAnthropic
	}
	for _, pfx := range []string{"gpt-", "gpt4", "gpt3", "o1", "o3", "o4", "text-", "mistral", "phi-", "llama", "meta-llama"} {
		if strings.HasPrefix(model, pfx) {
			return gollm.WireOpenAICompat
		}
	}
	return gollm.WireUnknown
}
