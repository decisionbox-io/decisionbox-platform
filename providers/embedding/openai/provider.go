// Package openai provides an embedding.Provider backed by the OpenAI Embeddings API.
// Uses net/http directly (no SDK dependency) for minimal footprint.
//
// Register via init():
//
//	import _ "github.com/decisionbox-io/decisionbox/providers/embedding/openai"
//
// Supported models:
//   - text-embedding-3-small (1536 dims, $0.02/1M tokens)
//   - text-embedding-3-large (3072 dims, $0.13/1M tokens)
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
)

const defaultBaseURL = "https://api.openai.com/v1"

var modelDimensions = map[string]int{
	"text-embedding-3-small": 1536,
	"text-embedding-3-large": 3072,
}

func init() {
	goembedding.RegisterWithMeta("openai", func(cfg goembedding.ProviderConfig) (goembedding.Provider, error) {
		apiKey := cfg["api_key"]
		if apiKey == "" {
			return nil, fmt.Errorf("openai embedding: api_key is required")
		}

		model := cfg["model"]
		if model == "" {
			model = "text-embedding-3-small"
		}

		// Accept unknown models with dims=0 so the list-only path and
		// user-typed custom model IDs both work. Actual Embed() calls
		// against an unknown model will still fail upstream at the
		// OpenAI API; we don't pretend to know the vector size.
		dims := modelDimensions[model]

		baseURL := cfg["base_url"]
		if baseURL == "" {
			baseURL = defaultBaseURL
		}

		return newProvider(apiKey, model, baseURL, dims), nil
	}, goembedding.ProviderMeta{
		Name:        "OpenAI",
		Description: "OpenAI text embeddings - best cost/quality ratio",
		ConfigFields: []goembedding.ConfigField{
			{Key: "api_key", Label: "API Key", Required: true, Type: "credential", Placeholder: "sk-..."},
			{Key: "model", Label: "Model", Required: true, Type: "string", Default: "text-embedding-3-small"},
			{Key: "base_url", Label: "Base URL", Type: "string", Default: defaultBaseURL, Description: "For OpenAI-compatible APIs"},
		},
		Models: []goembedding.ModelInfo{
			{ID: "text-embedding-3-small", Name: "Embedding 3 Small", Dimensions: 1536},
			{ID: "text-embedding-3-large", Name: "Embedding 3 Large", Dimensions: 3072},
		},
	})
}

// provider implements embedding.Provider using the OpenAI embeddings API.
type provider struct {
	apiKey  string
	model   string
	baseURL string
	dims    int
	client  *http.Client
}

func newProvider(apiKey, model, baseURL string, dims int) *provider {
	return &provider{
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL,
		dims:    dims,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// embedBatchSize bounds the number of inputs per /v1/embeddings POST.
//
// OpenAI documents two hard limits on the request: (a) at most 2048
// items in `input`, (b) at most 300K tokens summed across those items.
// The batch is small enough to stay comfortably under (b) even when
// every blurb hits the 4000-char MaxBlurbLen — 96 × ~250 tokens ≈ 24K
// tokens, 8% of the per-request budget. Over-large batches silently
// truncate the response on OpenAI's edge, which then surfaces here as
// "unexpected end of JSON input" — we've been bitten, small batch is
// the boring correct default.
const embedBatchSize = 96

// Embed generates vector embeddings for the given texts. Batches
// internally so callers can pass thousands of inputs at once without
// blowing past OpenAI's 300K-token-per-request cap.
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
		chunk := texts[start:end]
		vecs, err := p.embedChunk(ctx, chunk)
		if err != nil {
			return nil, err
		}
		result = append(result, vecs...)
	}
	return result, nil
}

// embedChunk sends a single batch to /v1/embeddings. Size MUST be
// within OpenAI's per-request limits (see embedBatchSize).
func (p *provider) embedChunk(ctx context.Context, texts []string) ([][]float64, error) {
	reqBody := embeddingRequest{
		Model: p.model,
		Input: texts,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("openai embedding: failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.baseURL, "/")+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai embedding: failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai embedding: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 40<<20)) // 40 MB limit
	if err != nil {
		return nil, fmt.Errorf("openai embedding: failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr apiErrorResponse
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			return nil, fmt.Errorf("openai embedding: API error (HTTP %d): %s", resp.StatusCode, apiErr.Error.Message)
		}
		return nil, fmt.Errorf("openai embedding: API error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	// Guard against the exact failure mode that motivated the batch:
	// an empty / truncated 200 body deserves an actionable error
	// rather than the generic "unexpected end of JSON input".
	if len(respBody) == 0 {
		return nil, fmt.Errorf("openai embedding: empty response body on HTTP 200 (likely a truncated batch — inputs=%d)", len(texts))
	}

	var embResp embeddingResponse
	if err := json.Unmarshal(respBody, &embResp); err != nil {
		return nil, fmt.Errorf("openai embedding: failed to unmarshal response (inputs=%d, body_bytes=%d): %w", len(texts), len(respBody), err)
	}

	if len(embResp.Data) != len(texts) {
		return nil, fmt.Errorf("openai embedding: expected %d embeddings, got %d", len(texts), len(embResp.Data))
	}

	result := make([][]float64, len(texts))
	seen := make([]bool, len(texts))
	for _, d := range embResp.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("openai embedding: invalid index %d in response", d.Index)
		}
		if seen[d.Index] {
			return nil, fmt.Errorf("openai embedding: duplicate index %d in response", d.Index)
		}
		seen[d.Index] = true
		result[d.Index] = d.Embedding
	}

	return result, nil
}

// Dimensions returns the vector dimensionality for this model.
func (p *provider) Dimensions() int {
	return p.dims
}

// ModelName returns the model identifier.
func (p *provider) ModelName() string {
	return p.model
}

// Validate checks that the provider credentials are valid.
func (p *provider) Validate(ctx context.Context) error {
	_, err := p.Embed(ctx, []string{"test"})
	return err
}

// embeddingRequest is the OpenAI embeddings API request body.
type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embeddingResponse is the OpenAI embeddings API response body.
type embeddingResponse struct {
	Data  []embeddingData `json:"data"`
	Model string          `json:"model"`
	Usage embeddingUsage  `json:"usage"`
}

type embeddingData struct {
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}

type embeddingUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type apiErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}
