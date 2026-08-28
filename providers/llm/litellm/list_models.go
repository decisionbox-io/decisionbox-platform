package litellm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// fetchV1Models returns the capabilities LiteLLM exposes on GET /v1/models
// (its OpenAI-compatible endpoint, accessible to non-admin virtual keys),
// keyed by model id. /v1/models carries a non-standard max_output_tokens per
// row from the proxy's model-cost map; it does not report the context window.
// Runs through the custom-TLS transport so it works behind a private CA.
func (p *LiteLLMProvider) fetchV1Models(ctx context.Context) (map[string]gollm.ModelCapabilities, []string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("litellm: list models: build request: %w", err)
	}
	p.setAuth(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("litellm: list models: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("litellm: list models: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("litellm: list models: status %d: %s", resp.StatusCode, gollm.SanitizeErrorBody(body, 500))
	}

	var decoded struct {
		Data []struct {
			ID              string `json:"id"`
			MaxOutputTokens int    `json:"max_output_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, nil, fmt.Errorf("litellm: list models: parse: %w", err)
	}

	caps := make(map[string]gollm.ModelCapabilities, len(decoded.Data))
	ids := make([]string, 0, len(decoded.Data))
	for _, m := range decoded.Data {
		if m.ID == "" {
			continue
		}
		ids = append(ids, m.ID)
		caps[m.ID] = gollm.ModelCapabilities{MaxOutputTokens: m.MaxOutputTokens}
	}
	return caps, ids, nil
}

// ListModels returns every model the LiteLLM proxy is configured to
// route, via GET /v1/models (LiteLLM's OpenAI-compatible endpoint).
// Runs through the custom-TLS transport so the dashboard picker
// populates from the proxy's real model list even behind a private CA.
func (p *LiteLLMProvider) ListModels(ctx context.Context) ([]gollm.RemoteModel, error) {
	basic, ids, err := p.fetchV1Models(ctx)
	if err != nil {
		return nil, err
	}

	// Best-effort: /model/info additionally reports the context window
	// (max_input_tokens), which /v1/models does not. A failure here (e.g. the
	// key lacks admin scope) leaves the window unknown but never blocks the
	// listing — the dashboard falls back to catalog/manual entry.
	info, _ := p.fetchModelInfo(ctx)

	out := make([]gollm.RemoteModel, 0, len(ids))
	for _, id := range ids {
		rm := gollm.RemoteModel{ID: id, DisplayName: id, MaxOutputTokens: basic[id].MaxOutputTokens}
		if caps, ok := info[id]; ok {
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
