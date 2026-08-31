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
	// When num_ctx is unset, Chat deliberately omits options["num_ctx"], so
	// Ollama enforces its SERVER default context window (OLLAMA_CONTEXT_LENGTH,
	// commonly 2K–8K), NOT the GGUF architectural context_length from /api/show.
	// Reporting the (much larger) architectural value would size analysis prompts
	// above what the chat request actually gets, so we report "unknown" and let
	// budgeting fall through to the conservative catalog/default. We only trust a
	// concrete effective window when num_ctx is set (what Chat actually sends).
	if p.numCtx <= 0 {
		return gollm.ModelCapabilities{}, nil
	}
	resp, err := p.client.Show(ctx, &ollamaapi.ShowRequest{Model: model})
	if err != nil {
		return gollm.ModelCapabilities{}, err
	}
	// Effective window = min(architectural context_length, num_ctx): num_ctx is
	// what Chat sends, but the model can't exceed its own architectural max even
	// if the operator set num_ctx higher.
	window := p.numCtx
	if arch := contextLengthFromModelInfo(resp.ModelInfo); arch > 0 && arch < window {
		window = arch
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
