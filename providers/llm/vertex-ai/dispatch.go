package vertexai

import (
	"context"
	"fmt"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// dispatch picks the wire format for req.Model from the registered
// ProviderMeta catalog. Resolution order: catalog (ID + aliases) →
// wireOverride → FamilyInferrer → actionable error.
func (p *VertexAIProvider) dispatch(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	meta, ok := gollm.GetProviderMeta(providerName)
	if !ok {
		return nil, fmt.Errorf("vertex-ai: provider not registered")
	}

	wire, err := meta.ResolveWire(req.Model, p.wireOverride)
	if err != nil {
		return nil, err
	}

	switch wire {
	case gollm.WireGoogleNative:
		return p.chatGoogleNative(ctx, req)
	case gollm.WireAnthropic:
		return p.chatAnthropic(ctx, req)
	case gollm.WireOpenAICompat:
		return p.chatOpenAICompat(ctx, req)
	default:
		return nil, fmt.Errorf(
			"vertex-ai: model %q uses wire %q which is not implemented on Vertex AI (supported: %s, %s, %s)",
			req.Model, wire, gollm.WireGoogleNative, gollm.WireAnthropic, gollm.WireOpenAICompat,
		)
	}
}
