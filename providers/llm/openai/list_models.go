package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// ListModels returns every model available under the configured API key
// via GET /v1/models. Works against OpenAI directly and against
// OpenAI-compatible endpoints that implement the /models route (most do).
func (p *OpenAIProvider) ListModels(ctx context.Context) ([]gollm.RemoteModel, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("openai: list models: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai: list models: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai: list models: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai: list models: status %d: %s", resp.StatusCode, gollm.SanitizeErrorBody(body, 500))
	}

	var decoded struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
			// OpenAI-compatible gateways expose the model's context window under
			// varying keys: vLLM uses max_model_len; LiteLLM-style rows use
			// max_input_tokens; some add a non-standard max_output_tokens. All
			// are absent (0) on OpenAI proper, where the catalog carries them.
			MaxModelLen     int `json:"max_model_len"`
			MaxInputTokens  int `json:"max_input_tokens"`
			MaxOutputTokens int `json:"max_output_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("openai: list models: parse: %w", err)
	}

	out := make([]gollm.RemoteModel, 0, len(decoded.Data))
	for _, m := range decoded.Data {
		window := m.MaxInputTokens
		if window == 0 {
			window = m.MaxModelLen
		}
		out = append(out, gollm.RemoteModel{
			ID:              m.ID,
			DisplayName:     m.ID,
			MaxInputTokens:  window,
			MaxOutputTokens: m.MaxOutputTokens,
		})
	}
	return out, nil
}
