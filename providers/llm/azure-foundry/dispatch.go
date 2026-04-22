package azurefoundry

import (
	"context"
	"fmt"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/modelcatalog"
)

// dispatch picks the wire format for req.Model from the model catalog; if
// the model is uncatalogued it falls back to the provider's wireOverride
// (project.llm.wire_override). Returns a clear error when neither resolves.
//
// Azure Foundry serves Claude models behind {endpoint}/anthropic/v1/messages
// and OpenAI-compatible models behind {endpoint}/openai/v1/chat/completions
// on the same endpoint host.
func (p *AzureFoundryProvider) dispatch(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	wire, err := modelcatalog.ResolveWire("azure-foundry", req.Model, p.wireOverride)
	if err != nil {
		return nil, err
	}

	switch wire {
	case modelcatalog.Anthropic:
		return p.claudeChat(ctx, req)
	case modelcatalog.OpenAICompat:
		return p.openaiChat(ctx, req)
	default:
		return nil, fmt.Errorf(
			"azure-foundry: model %q uses wire %q which is not implemented on Azure Foundry (supported: %s, %s)",
			req.Model, wire, modelcatalog.Anthropic, modelcatalog.OpenAICompat,
		)
	}
}
