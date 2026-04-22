package vertexai

import (
	"context"
	"fmt"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/modelcatalog"
)

// dispatch picks the wire format for req.Model from the model catalog;
// if the model is uncatalogued it falls back to the provider's
// wireOverride (set from project.llm.wire_override at factory time).
// Returns a clear error when neither resolves.
func (p *VertexAIProvider) dispatch(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	wire, err := modelcatalog.ResolveWire("vertex-ai", req.Model, p.wireOverride)
	if err != nil {
		return nil, err
	}

	switch wire {
	case modelcatalog.GoogleNative:
		return p.chatGoogleNative(ctx, req)
	case modelcatalog.Anthropic:
		return p.chatAnthropic(ctx, req)
	case modelcatalog.OpenAICompat:
		return p.chatOpenAICompat(ctx, req)
	default:
		return nil, fmt.Errorf(
			"vertex-ai: model %q uses wire %q which is not implemented on Vertex AI (supported: %s, %s, %s)",
			req.Model, wire, modelcatalog.GoogleNative, modelcatalog.Anthropic, modelcatalog.OpenAICompat,
		)
	}
}
