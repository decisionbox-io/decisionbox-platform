package bedrock

import (
	"context"
	"fmt"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// dispatch picks the wire format for req.Model by consulting the
// provider's registered ProviderMeta catalog. Resolution order:
//  1. ModelEntry.Wire (canonical ID or alias)
//  2. p.wireOverride from project config
//  3. ProviderMeta.FamilyInferrer (prefix-based)
//  4. Actionable error
//
// The dispatch switch then rejects wires Bedrock does not implement
// (today: every wire other than Anthropic and OpenAICompat).
func (p *BedrockProvider) dispatch(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	meta, ok := gollm.GetProviderMeta(providerName)
	if !ok {
		// init() guarantees the provider is registered; reaching this
		// branch means a test forgot to import the package.
		return nil, fmt.Errorf("bedrock: provider not registered")
	}

	wire, err := meta.ResolveWire(req.Model, p.wireOverride)
	if err != nil {
		return nil, err
	}

	switch wire {
	case gollm.WireAnthropic:
		return p.chatAnthropic(ctx, req)
	case gollm.WireOpenAICompat:
		return p.chatOpenAICompat(ctx, req)
	default:
		return nil, fmt.Errorf(
			"bedrock: model %q uses wire %q which is not implemented on Bedrock (supported: %s, %s)",
			req.Model, wire, gollm.WireAnthropic, gollm.WireOpenAICompat,
		)
	}
}
