package vertexai

import (
	"context"
	"fmt"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// dispatch picks the wire format for req.Model from the registered
// ProviderMeta catalog. Resolution order: catalog (ID + aliases) →
// wireOverride → FamilyInferrer → actionable error.
//
// A configured endpoint_id short-circuits this: a user-deployed Vertex
// endpoint always serves the OpenAI chat-completions wire and its model
// ID won't resolve through the publisher catalog, so it routes straight
// to chatOpenAICompat with the model passed through verbatim.
func (p *VertexAIProvider) dispatch(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	if p.endpointID != "" {
		return p.chatOpenAICompat(ctx, req)
	}

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
