package azurefoundry

import (
	"context"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// TokenCounter implements gollm.TokenCounterProvider for Azure AI
// Foundry. Routing:
//
//  1. Model entry has an `Encoding` declared (currently the
//     OpenAI-wire entries — GPT-5, GPT-4.1, GPT-4o, GPT-4o Mini,
//     all using o200k_base) → tiktoken with that encoding (exact).
//  2. Any other model — Anthropic-wire Claude, Mistral, or an
//     unknown deployment name — falls back to gollm.ApproximateCounter.
//     We can't claim exactness for Claude here because Foundry
//     fronts Anthropic through its own wire and doesn't expose
//     Anthropic's `/messages/count_tokens` API.
//
// Same exactness contract as the direct OpenAI provider: Count()
// is exact for the raw text input but OpenAI's chat-completions
// `prompt_tokens` adds per-message overhead the counter doesn't
// model. The Budget layer's 5% exact-tier safety margin absorbs
// the residual.
func (p *AzureFoundryProvider) TokenCounter(_ context.Context, model string) (gollm.TokenCounter, error) {
	if model == "" {
		model = p.model
	}
	encoding := gollm.GetEncoding("azure-foundry", model)
	if encoding == "" {
		// Anthropic-wire entry, non-OpenAI Mistral, or unknown
		// deployment name. Approximate counter + wider 15%
		// safety margin is the safe choice.
		return gollm.ApproximateCounter{}, nil
	}
	counter, err := gollm.NewTiktokenCounter(encoding)
	if err != nil {
		// Encoding name didn't load — treat as inexact rather
		// than fail the request. Same fallback as the openai
		// provider on encoding-load error.
		return gollm.ApproximateCounter{}, nil
	}
	return counter, nil
}
