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
		rm := gollm.RemoteModel{ID: m.Name, DisplayName: m.Name}
		// Best-effort: enrich each row with the model's real context window
		// (from /api/show) so the dashboard can prefill the context-window
		// field. A Show failure or a cancelled context leaves the window
		// unknown (0) and never blocks the listing — Show is a localhost call
		// so the added latency is small for a typical handful of pulled models.
		if ctx.Err() == nil {
			if caps, err := p.ResolveModelInfo(ctx, m.Name); err == nil && caps.MaxInputTokens > 0 {
				rm.MaxInputTokens = caps.MaxInputTokens
			}
		}
		out = append(out, rm)
	}
	return out, nil
}
