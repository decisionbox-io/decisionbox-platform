package openai

import (
	"context"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// ResolveModelInfo implements gollm.ModelInfoResolver: for an OpenAI-compatible
// gateway (vLLM, LiteLLM-style, …) it reads the model's context window / output
// cap out of GET /v1/models (max_model_len / max_input_tokens /
// max_output_tokens — see ListModels). OpenAI proper reports none of these, so
// this returns (zero, nil) there and the caller falls through to the catalog.
//
// This is what lets the agent's run-start window resolver auto-detect an
// uncatalogued gateway model without the operator typing its window — the
// resolver type-asserts ModelInfoResolver, which ListModels alone did not
// satisfy.
func (p *OpenAIProvider) ResolveModelInfo(ctx context.Context, model string) (gollm.ModelCapabilities, error) {
	if model == "" {
		return gollm.ModelCapabilities{}, nil
	}
	models, err := p.ListModels(ctx)
	if err != nil {
		return gollm.ModelCapabilities{}, err
	}
	for _, m := range models {
		if m.ID == model {
			return gollm.ModelCapabilities{
				MaxInputTokens:  m.MaxInputTokens,
				MaxOutputTokens: m.MaxOutputTokens,
			}, nil
		}
	}
	return gollm.ModelCapabilities{}, nil
}
