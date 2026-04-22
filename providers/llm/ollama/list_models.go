package ollama

import (
	"context"
	"fmt"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// ListModels returns every model the local Ollama instance has pulled,
// via GET /api/tags. No auth needed — Ollama is localhost.
func (p *OllamaProvider) ListModels(ctx context.Context) ([]gollm.RemoteModel, error) {
	resp, err := p.client.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("ollama: list models: %w", err)
	}
	out := make([]gollm.RemoteModel, 0, len(resp.Models))
	for _, m := range resp.Models {
		out = append(out, gollm.RemoteModel{
			ID:          m.Name,
			DisplayName: m.Name,
		})
	}
	return out, nil
}
