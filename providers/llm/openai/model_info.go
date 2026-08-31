package openai

import (
	"context"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// providerName is the registry key this provider registers under (see init in
// provider.go). Used to look up the provider's own catalog for alias matching.
const providerName = "openai"

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
	caps := func(m gollm.RemoteModel) gollm.ModelCapabilities {
		return gollm.ModelCapabilities{MaxInputTokens: m.MaxInputTokens, MaxOutputTokens: m.MaxOutputTokens}
	}
	// Exact match first.
	for _, m := range models {
		if m.ID == model {
			return caps(m), nil
		}
	}
	// Alias match: the live-model merge (writeLiveModelsResponse) projects a
	// gateway's alias id onto its canonical catalog id when PreferLiveModelID is
	// false, so a project may be saved as the canonical id (e.g. "gpt-4o") while
	// the gateway lists an alias (e.g. "gpt-4o-2024-08-06") carrying the real
	// window/output. Mirror that projection so the detected capabilities still
	// resolve for the saved canonical id.
	if meta, ok := gollm.GetProviderMeta(providerName); ok {
		if canonical := canonicalCatalogID(meta, model); canonical != "" {
			for _, m := range models {
				if canonicalCatalogID(meta, m.ID) == canonical && (m.MaxInputTokens > 0 || m.MaxOutputTokens > 0) {
					return caps(m), nil
				}
			}
		}
	}
	return gollm.ModelCapabilities{}, nil
}

// canonicalCatalogID returns the canonical catalog id an id resolves to (via ID
// or alias), or "" when the id is not in the catalog.
func canonicalCatalogID(meta gollm.ProviderMeta, id string) string {
	if e, ok := meta.FindModel(id); ok {
		return e.ID
	}
	return ""
}
