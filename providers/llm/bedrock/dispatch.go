package bedrock

import (
	"context"
	"fmt"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/modelcatalog"
)

// dispatch picks the wire format for req.Model by consulting the model
// catalog; if the model is uncatalogued it falls back to the provider's
// wireOverride (set from project.llm.wire_override at factory time). When
// neither resolves, it returns a clear error that names the cloud, the
// model, and the three valid wire values a user can set.
func (p *BedrockProvider) dispatch(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	wire, err := modelcatalog.ResolveWire("bedrock", req.Model, p.wireOverride)
	if err != nil {
		return nil, err
	}

	switch wire {
	case modelcatalog.Anthropic:
		return p.chatAnthropic(ctx, req)
	case modelcatalog.OpenAICompat:
		return p.chatOpenAICompat(ctx, req)
	default:
		// Catalog registered a wire we do not implement on this cloud.
		// Do not silently fall back — list what bedrock supports.
		return nil, fmt.Errorf(
			"bedrock: model %q uses wire %q which is not implemented on Bedrock (supported: %s, %s)",
			req.Model, wire, modelcatalog.Anthropic, modelcatalog.OpenAICompat,
		)
	}
}
