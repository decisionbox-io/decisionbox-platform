package ollama

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
// capability so the API's live-list endpoint can enumerate locally
// pulled Ollama tags for the dashboard.
var _ goembedding.ModelLister = (*provider)(nil)

// ListModels hits Ollama's GET /api/tags and returns every locally
// pulled model. Ollama doesn't expose a modality field on tags, so
// we return everything — the user picks an embedding tag from the
// list (or types one). For known embedding model name prefixes we
// surface the catalog dimension so the dropdown shows the vector
// size; unknown tags carry dim=0 and the dashboard shows
// "dimensions unknown" until the user supplies a custom dim.
func (p *provider) ListModels(ctx context.Context) ([]goembedding.RemoteModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.host+"/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("ollama embedding: build list req: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embedding: list models (is Ollama running at %s?): %w", p.host, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ollama embedding: read list body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama embedding: list models: status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var listResp struct {
		Models []struct {
			Name       string `json:"name"`
			ModifiedAt string `json:"modified_at"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil {
		return nil, fmt.Errorf("ollama embedding: parse list body: %w", err)
	}

	out := make([]goembedding.RemoteModel, 0, len(listResp.Models))
	for _, m := range listResp.Models {
		if m.Name == "" {
			continue
		}
		// Tags ship as "<model>:<tag>" (e.g. "nomic-embed-text:latest").
		// modelDimensions is keyed on the bare model id; strip the
		// tag suffix when looking up.
		bare := m.Name
		if i := strings.IndexByte(bare, ':'); i > 0 {
			bare = bare[:i]
		}
		out = append(out, goembedding.RemoteModel{
			ID:          m.Name,
			DisplayName: m.Name,
			Dimensions:  modelDimensions[bare],
		})
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
