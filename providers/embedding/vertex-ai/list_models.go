package vertexai

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
// capability so the API's live-list endpoint can enumerate Vertex AI
// embedding publisher models for the dashboard.
var _ goembedding.ModelLister = (*provider)(nil)

// ListModels enumerates Google's published Vertex AI models filtered
// to the embedding family. Uses the regional aiplatform endpoint and
// the same OAuth token chain as the predict call (cloud-platform
// scope), so a workload that can already embed text can also list.
//
// We hit GET /v1/publishers/google/models which returns all of
// Google's first-party publisher models in the region; the result
// includes Gemini chat/vision rows alongside embeddings, so we
// filter by `supportedActions` (embed support is signaled by the
// model's surfaces) AND a name-prefix heuristic for known embedding
// families. The catalog dimension map fills in vector size for
// known IDs.
func (p *provider) ListModels(ctx context.Context) ([]goembedding.RemoteModel, error) {
	// publishers.models.list lives on v1beta1 only — the v1 path 404s
	// across all regions. We expose the underlying foundation models
	// (not the project's tuned models), so a regional aiplatform host
	// is sufficient — no project context required.
	endpoint := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/publishers/google/models", p.location)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai embedding: build list req: %w", err)
	}

	tok, err := p.auth.token(ctx)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai embedding: list models: get token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	// publishers.models.list requires a quota project even when the
	// resource path itself doesn't carry one (it's a publisher-owned
	// resource, not a project-owned one). Send the configured project
	// as the quota project so ADC + service-account flows both work.
	if p.projectID != "" {
		req.Header.Set("X-Goog-User-Project", p.projectID)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai embedding: list models: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai embedding: read list body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vertex-ai embedding: list models: status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	// publishers/google/models response shape: {"publisherModels":[{"name":"publishers/google/models/text-embedding-005","versionId":"...","launchStage":"GA","openSourceCategory":"...",...}]}
	var listResp struct {
		PublisherModels []struct {
			Name        string `json:"name"`
			VersionID   string `json:"versionId"`
			LaunchStage string `json:"launchStage"`
		} `json:"publisherModels"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil {
		return nil, fmt.Errorf("vertex-ai embedding: parse list body: %w", err)
	}

	out := make([]goembedding.RemoteModel, 0, len(listResp.PublisherModels))
	for _, m := range listResp.PublisherModels {
		// Name format: "publishers/google/models/<id>". Strip the prefix.
		id := m.Name
		if i := strings.LastIndexByte(id, '/'); i >= 0 {
			id = id[i+1:]
		}
		if !isEmbeddingPublisherID(id) {
			continue
		}
		out = append(out, goembedding.RemoteModel{
			ID:          id,
			DisplayName: id,
			Dimensions:  modelDimensions[id], // 0 when unknown
			Lifecycle:   strings.ToLower(m.LaunchStage),
		})
	}
	return out, nil
}

// isEmbeddingPublisherID matches the name prefixes Vertex AI uses for
// embedding models. The publishers/google/models endpoint returns
// every Google first-party model (Gemini chat, image, code, audio,
// embedding, ...), and there's no first-class "modality" field on the
// list response — name-prefix is the durable signal.
func isEmbeddingPublisherID(id string) bool {
	return strings.HasPrefix(id, "text-embedding-") ||
		strings.HasPrefix(id, "text-multilingual-embedding-") ||
		strings.HasPrefix(id, "gemini-embedding-")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
