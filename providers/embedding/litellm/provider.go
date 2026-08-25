// Package litellm provides an embedding.Provider backed by a LiteLLM proxy.
//
// LiteLLM exposes an OpenAI-compatible /v1/embeddings endpoint, so this
// mirrors the openai embedding provider but with a required base_url, an
// optional Bearer key (open proxies), and per-project custom TLS (CA
// upload / skip-verify) for a private-CA HTTPS proxy — the on-prem case
// where the same proxy fronts both chat and embeddings.
//
// Register via init():
//
//	import _ "github.com/decisionbox-io/decisionbox/providers/embedding/litellm"
package litellm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
)

// litellmEmbeddingTimeout mirrors the other embedding providers' fixed
// 60s HTTP timeout (embedding calls are short).
const litellmEmbeddingTimeout = 60 * time.Second

func init() {
	goembedding.RegisterWithMeta("litellm", factory, goembedding.ProviderMeta{
		Name:        "LiteLLM",
		Description: "LiteLLM proxy — OpenAI-compatible embeddings gateway (supports a private-CA HTTPS endpoint)",
		ConfigFields: append([]goembedding.ConfigField{
			{Key: "base_url", Label: "LiteLLM proxy URL", Required: true, Type: "string", Placeholder: "https://litellm.internal:4000", Description: "The LiteLLM proxy root URL. The OpenAI-compatible /v1 routes are derived from it."},
			{Key: "model", Label: "Model", Required: true, Type: "string", Placeholder: "text-embedding-3-small", Description: "Any embedding model the LiteLLM proxy is configured to route."},
			{Key: "dimensions", Label: "Dimensions", Type: "string", Description: "Vector size for the model. Leave blank to auto-detect by probing the proxy with a live embedding."},
		}, tlsConfigFields()...),
		AuthMethods: []goembedding.AuthMethod{
			{
				ID: "api_key", Name: "API Key",
				Description: "LiteLLM master or virtual key, sent as a Bearer token.",
				Fields: []goembedding.ConfigField{
					{Key: "credentials_json", Label: "LiteLLM key", Required: true, Type: "credential", Placeholder: "sk-..."},
				},
			},
			{
				ID: "none", Name: "No authentication",
				Description: "For a LiteLLM proxy that requires no key.",
			},
		},
	})
}

// tlsConfigFields returns the shared custom-TLS fields as embedding
// ConfigFields.
func tlsConfigFields() []goembedding.ConfigField {
	return goembedding.TLSConfigFields()
}

func factory(cfg goembedding.ProviderConfig) (goembedding.Provider, error) {
	baseURL := strings.TrimSpace(cfg["base_url"])
	if baseURL == "" {
		return nil, fmt.Errorf("litellm embedding: base_url is required (the LiteLLM proxy URL)")
	}

	model := cfg["model"]
	if model == "" {
		return nil, fmt.Errorf("litellm embedding: model is required")
	}

	// Optional key. Dropped when the "none" auth method is chosen so an
	// open proxy never receives a stale/global key (mirrors the LLM side).
	apiKey := cfg["credentials_json"]
	if cfg["auth_method"] == "none" {
		apiKey = ""
	}

	dims := 0
	if s := strings.TrimSpace(cfg["dimensions"]); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("litellm embedding: invalid dimensions %q: must be a positive integer", s)
		}
		dims = n
	}

	client, err := goembedding.HTTPClientFor(cfg, litellmEmbeddingTimeout)
	if err != nil {
		return nil, fmt.Errorf("litellm embedding: %w", err)
	}

	return &provider{apiKey: apiKey, model: model, baseURL: normalizeBaseURL(baseURL), dims: dims, client: client}, nil
}

// normalizeBaseURL trims a trailing slash and appends /v1 unless already
// present, matching the LLM litellm provider.
func normalizeBaseURL(raw string) string {
	b := strings.TrimRight(strings.TrimSpace(raw), "/")
	if b == "" || strings.HasSuffix(b, "/v1") {
		return b
	}
	return b + "/v1"
}

type provider struct {
	apiKey  string
	model   string
	baseURL string // normalised, ends with /v1
	dims    int
	client  *http.Client
}

// embedBatchSize bounds inputs per request (same rationale as openai).
const embedBatchSize = 96

func (p *provider) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	result := make([][]float64, 0, len(texts))
	for start := 0; start < len(texts); start += embedBatchSize {
		end := start + embedBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := p.embedChunk(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		result = append(result, vecs...)
	}
	return result, nil
}

func (p *provider) embedChunk(ctx context.Context, texts []string) ([][]float64, error) {
	body, err := json.Marshal(embeddingRequest{Model: p.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("litellm embedding: failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("litellm embedding: failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("litellm embedding: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 40<<20))
	if err != nil {
		return nil, fmt.Errorf("litellm embedding: failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr apiErrorResponse
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			return nil, fmt.Errorf("litellm embedding: API error (HTTP %d): %s", resp.StatusCode, apiErr.Error.Message)
		}
		return nil, fmt.Errorf("litellm embedding: API error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}
	if len(respBody) == 0 {
		return nil, fmt.Errorf("litellm embedding: empty response body on HTTP 200 (inputs=%d)", len(texts))
	}

	var embResp embeddingResponse
	if err := json.Unmarshal(respBody, &embResp); err != nil {
		return nil, fmt.Errorf("litellm embedding: failed to unmarshal response (inputs=%d): %w", len(texts), err)
	}
	if len(embResp.Data) != len(texts) {
		return nil, fmt.Errorf("litellm embedding: expected %d embeddings, got %d", len(texts), len(embResp.Data))
	}

	result := make([][]float64, len(texts))
	seen := make([]bool, len(texts))
	for _, d := range embResp.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("litellm embedding: invalid index %d in response", d.Index)
		}
		if seen[d.Index] {
			return nil, fmt.Errorf("litellm embedding: duplicate index %d in response", d.Index)
		}
		seen[d.Index] = true
		result[d.Index] = d.Embedding
	}
	return result, nil
}

// Dimensions returns the configured vector size, or 0 ("unknown") when
// it must be probed with a live embedding.
func (p *provider) Dimensions() int { return p.dims }

func (p *provider) ModelName() string { return p.model }

func (p *provider) Validate(ctx context.Context) error {
	_, err := p.Embed(ctx, []string{"test"})
	return err
}

type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingResponse struct {
	Data []embeddingData `json:"data"`
}

type embeddingData struct {
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}

type apiErrorResponse struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}
