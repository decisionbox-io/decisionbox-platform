package litellm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// ListModels returns every model the LiteLLM proxy is configured to
// route, via GET /v1/models (LiteLLM's OpenAI-compatible endpoint).
// Runs through the custom-TLS transport so the dashboard picker
// populates from the proxy's real model list even behind a private CA.
func (p *LiteLLMProvider) ListModels(ctx context.Context) ([]gollm.RemoteModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("litellm: list models: build request: %w", err)
	}
	p.setAuth(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("litellm: list models: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("litellm: list models: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("litellm: list models: status %d: %s", resp.StatusCode, gollm.SanitizeErrorBody(body, 500))
	}

	var decoded struct {
		Data []struct {
			ID string `json:"id"`
			// LiteLLM's /v1/models includes a non-standard max_output_tokens
			// per row when its model-cost map knows the model.
			MaxOutputTokens int `json:"max_output_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("litellm: list models: parse: %w", err)
	}

	// Best-effort: /model/info additionally reports the context window
	// (max_input_tokens), which /v1/models does not. A failure here (e.g. the
	// key lacks admin scope) leaves the window unknown but never blocks the
	// listing — the dashboard falls back to catalog/manual entry.
	info, _ := p.fetchModelInfo(ctx)

	out := make([]gollm.RemoteModel, 0, len(decoded.Data))
	for _, m := range decoded.Data {
		if m.ID == "" {
			continue
		}
		rm := gollm.RemoteModel{ID: m.ID, DisplayName: m.ID, MaxOutputTokens: m.MaxOutputTokens}
		if caps, ok := info[m.ID]; ok {
			if caps.MaxInputTokens > 0 {
				rm.MaxInputTokens = caps.MaxInputTokens
			}
			if caps.MaxOutputTokens > 0 {
				rm.MaxOutputTokens = caps.MaxOutputTokens
			}
		}
		out = append(out, rm)
	}
	return out, nil
}
