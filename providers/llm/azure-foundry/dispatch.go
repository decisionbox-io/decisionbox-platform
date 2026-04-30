package azurefoundry

import (
	"context"
	"fmt"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// dispatch picks the wire format for req.Model from the registered
// ProviderMeta catalog. Resolution: catalog (ID + aliases) →
// wireOverride → FamilyInferrer → actionable error.
//
// Azure Foundry serves Claude behind {endpoint}/anthropic/v1/messages
// and OpenAI-compatible models behind
// {endpoint}/openai/v1/chat/completions on the same endpoint host.
func (p *AzureFoundryProvider) dispatch(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	meta, ok := gollm.GetProviderMeta(providerName)
	if !ok {
		return nil, fmt.Errorf("azure-foundry: provider not registered")
	}

	wire, err := meta.ResolveWire(req.Model, p.wireOverride)
	if err != nil {
		return nil, err
	}

	switch wire {
	case gollm.WireAnthropic:
		return p.claudeChat(ctx, req)
	case gollm.WireOpenAICompat:
		return p.openaiChat(ctx, req)
	default:
		return nil, fmt.Errorf(
			"azure-foundry: model %q uses wire %q which is not implemented on Azure Foundry (supported: %s, %s)",
			req.Model, wire, gollm.WireAnthropic, gollm.WireOpenAICompat,
		)
	}
}
