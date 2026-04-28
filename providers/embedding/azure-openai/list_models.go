package azureopenai

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
// capability so the API's live-list endpoint can enumerate the
// subscription's embedding-capable foundation models for the dashboard.
var _ goembedding.ModelLister = (*provider)(nil)

// ListModels hits Azure OpenAI's data-plane GET /openai/models endpoint
// and returns every foundation model the subscription can serve whose
// capabilities.embeddings flag is true.
//
// We deliberately don't use /openai/deployments because:
//   - It only works on classic "OpenAI"-kind Cognitive Services
//     accounts; modern "AIServices"-kind (Azure AI Foundry) accounts
//     return 404 — Azure routes deployment listing through ARM, which
//     needs AAD auth, not the api-key the user already supplied.
//   - The same api-key that powers /embeddings authenticates /openai/models
//     in both account kinds, so this path works uniformly.
//
// Trade-off: the rows are foundation-model IDs, not deployment names.
// The user types their deployment name in the form's `deployment`
// field; this dropdown surfaces the underlying foundation model the
// deployment maps to. That matches the dashboard's flow today (the
// `model` field is a hint for our static dimension lookup, the
// `deployment` field is what /embeddings actually reads).
func (p *provider) ListModels(ctx context.Context) ([]goembedding.RemoteModel, error) {
	endpoint := strings.TrimSuffix(p.endpoint, "/") + "/openai/models?api-version=" + p.apiVersion
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("azure-openai embedding: build list req: %w", err)
	}
	req.Header.Set("api-key", p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("azure-openai embedding: list models: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("azure-openai embedding: read list body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("azure-openai embedding: list models: status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	// /openai/models response shape (api-version 2024-10-21):
	//   {"data":[{
	//     "id":"text-embedding-3-small-2",
	//     "capabilities":{"embeddings":true,"chat_completion":false,...},
	//     "lifecycle_status":"generally-available",
	//     "status":"succeeded"
	//   }],"object":"list"}
	var listResp struct {
		Data []struct {
			ID              string `json:"id"`
			LifecycleStatus string `json:"lifecycle_status"`
			Capabilities    struct {
				Embeddings bool `json:"embeddings"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil {
		return nil, fmt.Errorf("azure-openai embedding: parse list body: %w", err)
	}

	out := make([]goembedding.RemoteModel, 0, len(listResp.Data))
	for _, m := range listResp.Data {
		if m.ID == "" || !m.Capabilities.Embeddings {
			continue
		}
		if !isModernEmbeddingID(m.ID) {
			// Azure flags legacy similarity / search models
			// (text-similarity-*, text-search-*, code-search-*)
			// as embedding-capable, but they use a different API
			// shape and are deprecated. Restrict to the
			// text-embedding-* family that the /embeddings call
			// expects today.
			continue
		}
		// Strip the trailing "-<n>" version suffix Azure appends to
		// foundation model ids (e.g. "text-embedding-3-small-2") so
		// the dimension lookup against modelDimensions hits.
		bare := stripTrailingVersion(m.ID)
		out = append(out, goembedding.RemoteModel{
			ID:          m.ID,
			DisplayName: m.ID,
			Dimensions:  modelDimensions[bare],
			Lifecycle:   m.LifecycleStatus,
		})
	}
	return out, nil
}

// isModernEmbeddingID matches the OpenAI text-embedding-* family that
// Azure's /embeddings endpoint accepts today. text-similarity-*,
// text-search-*, and code-search-* have embeddings=true on /openai/models
// but use the deprecated similarity/search APIs and are not callable
// through /embeddings — including them in the dropdown would be a
// pit-of-failure.
func isModernEmbeddingID(id string) bool {
	return strings.HasPrefix(id, "text-embedding-")
}

// stripTrailingVersion drops the trailing "-N" or "-N.M" Azure version
// suffix from a foundation model id ("text-embedding-3-small-2" →
// "text-embedding-3-small"). Idempotent on names without a suffix.
func stripTrailingVersion(id string) string {
	i := strings.LastIndexByte(id, '-')
	if i < 0 {
		return id
	}
	tail := id[i+1:]
	if tail == "" {
		return id
	}
	for _, r := range tail {
		if (r < '0' || r > '9') && r != '.' {
			return id
		}
	}
	return id[:i]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
