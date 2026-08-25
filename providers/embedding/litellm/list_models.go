package litellm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
)

// ListModels implements goembedding.ModelLister: it returns the models the
// LiteLLM proxy exposes via GET /v1/models (through the custom-TLS
// transport). LiteLLM's /v1/models does not distinguish chat from
// embedding models, so the picker shows every configured model and the
// operator selects the embedding one.
func (p *provider) ListModels(ctx context.Context) ([]goembedding.RemoteModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("litellm embedding: list models: build request: %w", err)
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("litellm embedding: list models: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("litellm embedding: list models: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("litellm embedding: list models: status %d", resp.StatusCode)
	}

	var decoded struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("litellm embedding: list models: parse: %w", err)
	}

	out := make([]goembedding.RemoteModel, 0, len(decoded.Data))
	for _, m := range decoded.Data {
		if m.ID == "" {
			continue
		}
		out = append(out, goembedding.RemoteModel{ID: m.ID, DisplayName: m.ID})
	}
	return out, nil
}
