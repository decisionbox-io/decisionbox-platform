// Package ollama provides an embedding.Provider backed by a local Ollama instance.
// Ollama runs open-source embedding models locally (nomic-embed-text, mxbai-embed-large, etc.).
//
// Register via init():
//
//	import _ "github.com/decisionbox-io/decisionbox/providers/embedding/ollama"
//
// Supported models:
//   - nomic-embed-text (768 dims)
//   - mxbai-embed-large (1024 dims)
//   - all-minilm (384 dims)
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
)

const defaultHost = "http://localhost:11434"

// probeTimeout caps the dimension-discovery call at the factory. Cold
// starts on a freshly loaded model can take 30–60s on a 32B server, so
// the budget here is the full Embed() ceiling. The probe only runs
// once per factory construction (i.e. once per index run).
const probeTimeout = 120 * time.Second

func init() {
	goembedding.RegisterWithMeta("ollama", func(cfg goembedding.ProviderConfig) (goembedding.Provider, error) {
		host := cfg["host"]
		if host == "" {
			host = defaultHost
		}

		model := cfg["model"]
		if model == "" {
			return nil, fmt.Errorf("ollama embedding: model is required (pull one with 'ollama pull nomic-embed-text' or similar, then pick it in the dashboard)")
		}

		// Discover the dimension by asking Ollama directly — one /api/embed
		// call with a single token returns a vector whose length IS the
		// model's embedding dimension. This works for ANY model the
		// server has pulled and that supports /api/embed, with no
		// hardcoded allow-list to keep in sync with Ollama's catalog.
		// Models that aren't embedding-capable (pure chat models without
		// embedding output) surface a clear Ollama error at this point
		// instead of failing mid-index.
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		dims, err := probeDimension(ctx, host, model)
		if err != nil {
			return nil, fmt.Errorf("ollama embedding: %w", err)
		}
		if dims <= 0 {
			return nil, fmt.Errorf("ollama embedding: model %q returned a zero-length embedding (not an embedding-capable model)", model)
		}

		return newProvider(host, model, dims), nil
	}, goembedding.ProviderMeta{
		Name:        "Ollama (Local)",
		Description: "Run open-source embedding models locally — free, air-gapped",
		ConfigFields: []goembedding.ConfigField{
			{Key: "host", Label: "Ollama Host", Type: "string", Default: defaultHost, Placeholder: defaultHost},
			{Key: "model", Label: "Model", Required: true, Type: "string", Default: "nomic-embed-text"},
		},
		// Models stays as a small suggestion catalog for the dashboard —
		// users can pick anything they've pulled, but these are the
		// known-good defaults the picker preselects.
		Models: []goembedding.ModelInfo{
			{ID: "nomic-embed-text", Name: "Nomic Embed Text", Dimensions: 768},
			{ID: "mxbai-embed-large", Name: "MxBai Embed Large", Dimensions: 1024},
			{ID: "snowflake-arctic-embed", Name: "Snowflake Arctic Embed", Dimensions: 1024},
			{ID: "bge-m3", Name: "BGE M3", Dimensions: 1024},
			{ID: "all-minilm", Name: "All-MiniLM", Dimensions: 384},
		},
	})
}

// probeDimension asks Ollama to embed a single token and returns the
// vector length. The HTTP client is a fresh one with probeTimeout so a
// hung server doesn't inherit the project's default timeout config.
func probeDimension(ctx context.Context, host, model string) (int, error) {
	body, err := json.Marshal(embedRequest{Model: model, Input: []string{"."}})
	if err != nil {
		return 0, fmt.Errorf("marshal probe: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build probe request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("probe %s (is Ollama running at %s?): %w", model, host, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return 0, fmt.Errorf("read probe response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// Ollama returns 404 with "model not found" when the user
		// picked a model the server hasn't pulled. Forward that
		// message verbatim so the dashboard surfaces an actionable
		// hint ('ollama pull mxbai-embed-large' or similar).
		return 0, fmt.Errorf("probe %s: HTTP %d: %s", model, resp.StatusCode, string(respBody))
	}

	var er embedResponse
	if err := json.Unmarshal(respBody, &er); err != nil {
		return 0, fmt.Errorf("parse probe response: %w", err)
	}
	if len(er.Embeddings) == 0 {
		return 0, nil
	}
	return len(er.Embeddings[0]), nil
}

// provider implements embedding.Provider using a local Ollama instance.
type provider struct {
	host   string
	model  string
	dims   int
	client *http.Client
}

func newProvider(host, model string, dims int) *provider {
	return &provider{
		host:  host,
		model: model,
		dims:  dims,
		client: &http.Client{
			Timeout: 120 * time.Second, // longer timeout for local models with cold start
		},
	}
}

// Embed generates vector embeddings for the given texts.
// Uses the Ollama /api/embed endpoint which supports batch inputs.
func (p *provider) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	reqBody := embedRequest{
		Model: p.model,
		Input: texts,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("ollama embedding: failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.host+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama embedding: failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embedding: request failed (is Ollama running at %s?): %w", p.host, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 40<<20)) // 40 MB limit
	if err != nil {
		return nil, fmt.Errorf("ollama embedding: failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama embedding: API error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var embResp embedResponse
	if err := json.Unmarshal(respBody, &embResp); err != nil {
		return nil, fmt.Errorf("ollama embedding: failed to unmarshal response: %w", err)
	}

	if len(embResp.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama embedding: expected %d embeddings, got %d", len(texts), len(embResp.Embeddings))
	}

	return embResp.Embeddings, nil
}

// Dimensions returns the vector dimensionality for this model.
func (p *provider) Dimensions() int {
	return p.dims
}

// ModelName returns the model identifier.
func (p *provider) ModelName() string {
	return p.model
}

// Validate checks that Ollama is reachable and the model is available.
func (p *provider) Validate(ctx context.Context) error {
	_, err := p.Embed(ctx, []string{"test"})
	return err
}

// embedRequest is the Ollama /api/embed request body.
type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embedResponse is the Ollama /api/embed response body.
type embedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float64 `json:"embeddings"`
}
