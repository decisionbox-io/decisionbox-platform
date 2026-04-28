package voyage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
)

// Compile-time check: provider satisfies the optional ModelLister
// capability so the API's live-list endpoint can enumerate Voyage AI
// embedding models for the dashboard.
var _ goembedding.ModelLister = (*provider)(nil)

// ListModels hits Voyage's GET /v1/models endpoint (OpenAI-compatible
// shape). Voyage doesn't document this endpoint as stable but does
// expose it on the same hostname; if it 404s the caller surfaces the
// error to the dashboard and the user can still type a model ID
// manually. We filter to ids that look like embedding models
// (voyage-* family) so chat or rerank ids don't crowd the dropdown.
func (p *provider) ListModels(ctx context.Context) ([]goembedding.RemoteModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("voyage embedding: build list req: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voyage embedding: list models: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("voyage embedding: read list body: %w", err)
	}
	// Voyage AI doesn't document a stable list-models endpoint, and as
	// of 2026-04 /v1/models returns 404 for valid keys. Treat 404 as
	// "this provider doesn't support live-list" rather than a hard
	// error — the dashboard then renders an empty dropdown and the
	// user types the model id directly. A 401 / 429 still surfaces
	// because those signal the credentials, not the capability.
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("voyage embedding: list models: status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var listResp struct {
		Data []struct {
			ID     string `json:"id"`
			Object string `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil {
		return nil, fmt.Errorf("voyage embedding: parse list body: %w", err)
	}

	out := make([]goembedding.RemoteModel, 0, len(listResp.Data))
	for _, m := range listResp.Data {
		if !isEmbeddingModelID(m.ID) {
			continue
		}
		out = append(out, goembedding.RemoteModel{
			ID:          m.ID,
			DisplayName: m.ID,
			Dimensions:  modelDimensions[m.ID], // 0 when unknown
		})
	}
	return out, nil
}

// isEmbeddingModelID matches Voyage's embedding-model naming family.
// Their reranker ids are "rerank-*" and chat ids would be similarly
// distinct, so keying on "voyage-" + excluding "voyage-rerank" keeps
// us future-proof for new embedding ids without surfacing rerankers.
func isEmbeddingModelID(id string) bool {
	if !strings.HasPrefix(id, "voyage-") {
		return false
	}
	if strings.HasPrefix(id, "voyage-rerank") {
		return false
	}
	return true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
