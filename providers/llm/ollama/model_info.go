package ollama

import (
	"context"
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	ollamaapi "github.com/ollama/ollama/api"
)

// ResolveModelInfo implements gollm.ModelInfoResolver: it reads the model's
// real context window from Ollama's /api/show model_info (the
// "<arch>.context_length" field, e.g. "qwen3.context_length"). Ollama does not
// report a separate output cap — generation is bounded by the context and
// num_predict — so MaxOutputTokens is left unknown (0). Returns (zero, nil)
// when the field is absent so callers fall through to the catalog/default.
func (p *OllamaProvider) ResolveModelInfo(ctx context.Context, model string) (gollm.ModelCapabilities, error) {
	if model == "" {
		return gollm.ModelCapabilities{}, nil
	}
	resp, err := p.client.Show(ctx, &ollamaapi.ShowRequest{Model: model})
	if err != nil {
		return gollm.ModelCapabilities{}, err
	}
	window := contextLengthFromModelInfo(resp.ModelInfo)
	// Respect an operator-configured num_ctx: Chat sends at most num_ctx tokens
	// of context, so the effective request window is min(architectural
	// context_length, num_ctx). Reporting the larger architectural value would
	// let budgeting size prompts above what Chat actually sends, reintroducing
	// context-length failures. Mirrors ollamaEffectiveInputWindow's downward
	// clamp.
	if p.numCtx > 0 && (window == 0 || p.numCtx < window) {
		window = p.numCtx
	}
	return gollm.ModelCapabilities{MaxInputTokens: window}, nil
}

// contextLengthFromModelInfo pulls the "<arch>.context_length" value out of an
// Ollama model_info map. The key is architecture-prefixed and the value decodes
// as float64 (encoding/json) or int; both are handled. Returns 0 when no such
// key is present.
func contextLengthFromModelInfo(mi map[string]any) int {
	if mi == nil {
		return 0
	}
	for k, v := range mi {
		if !strings.HasSuffix(k, ".context_length") {
			continue
		}
		switch n := v.(type) {
		case float64:
			if n > 0 {
				return int(n)
			}
		case int:
			if n > 0 {
				return n
			}
		case int64:
			if n > 0 {
				return int(n)
			}
		}
	}
	return 0
}
