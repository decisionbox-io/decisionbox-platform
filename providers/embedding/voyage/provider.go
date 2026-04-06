// Package voyage provides an embedding.Provider backed by the Voyage AI Embeddings API.
// Uses net/http directly (no SDK dependency) for minimal footprint.
//
// Register via init():
//
//	import _ "github.com/decisionbox-io/decisionbox/providers/embedding/voyage"
//
// Supported models:
//   - voyage-3-large (1024 dims, best quality)
//   - voyage-3 (1024 dims, balanced)
//   - voyage-3-lite (512 dims, fastest)
//   - voyage-code-3 (1024 dims, code-optimized)
package voyage

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

const defaultBaseURL = "https://api.voyageai.com/v1"

var modelDimensions = map[string]int{
	"voyage-3-large": 1024,
	"voyage-3":       1024,
	"voyage-3-lite":  512,
	"voyage-code-3":  1024,
}

func init() {
	goembedding.RegisterWithMeta("voyage", func(cfg goembedding.ProviderConfig) (goembedding.Provider, error) {
		apiKey := cfg["api_key"]
		if apiKey == "" {
			return nil, fmt.Errorf("voyage embedding: api_key is required")
		}

		model := cfg["model"]
		if model == "" {
			model = "voyage-3"
		}

		dims, ok := modelDimensions[model]
		if !ok {
			return nil, fmt.Errorf("voyage embedding: unsupported model %q (supported: voyage-3-large, voyage-3, voyage-3-lite, voyage-code-3)", model)
		}

		inputType := cfg["input_type"]
		if inputType != "" && inputType != "query" && inputType != "document" {
			return nil, fmt.Errorf("voyage embedding: invalid input_type %q (supported: query, document, or empty)", inputType)
		}

		baseURL := cfg["base_url"]
		if baseURL == "" {
			baseURL = defaultBaseURL
		}

		return newProvider(apiKey, model, inputType, baseURL, dims), nil
	}, goembedding.ProviderMeta{
		Name:        "Voyage AI",
		Description: "Voyage AI embeddings — top-tier retrieval quality, code-optimized models",
		ConfigFields: []goembedding.ConfigField{
			{Key: "api_key", Label: "API Key", Required: true, Type: "credential", Placeholder: "pa-..."},
			{Key: "model", Label: "Model", Required: true, Type: "string", Default: "voyage-3"},
			{Key: "input_type", Label: "Input Type", Type: "string", Description: "Optional: 'query' or 'document' for retrieval optimization"},
			{Key: "base_url", Label: "Base URL", Type: "string", Default: defaultBaseURL},
		},
		Models: []goembedding.ModelInfo{
			{ID: "voyage-3-large", Name: "Voyage 3 Large", Dimensions: 1024},
			{ID: "voyage-3", Name: "Voyage 3", Dimensions: 1024},
			{ID: "voyage-3-lite", Name: "Voyage 3 Lite", Dimensions: 512},
			{ID: "voyage-code-3", Name: "Voyage Code 3", Dimensions: 1024},
		},
	})
}

// provider implements embedding.Provider using the Voyage AI API.
type provider struct {
	apiKey    string
	model     string
	inputType string
	baseURL   string
	dims      int
	client    *http.Client
}

func newProvider(apiKey, model, inputType, baseURL string, dims int) *provider {
	return &provider{
		apiKey:    apiKey,
		model:     model,
		inputType: inputType,
		baseURL:   baseURL,
		dims:      dims,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// Embed generates vector embeddings for the given texts via Voyage AI.
// Supports up to 128 texts per request.
func (p *provider) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	reqBody := embeddingRequest{
		Model:      p.model,
		Input:      texts,
		Truncation: true,
	}
	if p.inputType != "" {
		reqBody.InputType = &p.inputType
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("voyage embedding: failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("voyage embedding: failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voyage embedding: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 40<<20)) // 40 MB limit
	if err != nil {
		return nil, fmt.Errorf("voyage embedding: failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr apiErrorResponse
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Detail != "" {
			return nil, fmt.Errorf("voyage embedding: API error (HTTP %d): %s", resp.StatusCode, apiErr.Detail)
		}
		return nil, fmt.Errorf("voyage embedding: API error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var embResp embeddingResponse
	if err := json.Unmarshal(respBody, &embResp); err != nil {
		return nil, fmt.Errorf("voyage embedding: failed to unmarshal response: %w", err)
	}

	if len(embResp.Data) != len(texts) {
		return nil, fmt.Errorf("voyage embedding: expected %d embeddings, got %d", len(texts), len(embResp.Data))
	}

	result := make([][]float64, len(texts))
	for _, d := range embResp.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("voyage embedding: invalid index %d in response", d.Index)
		}
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

// Validate checks that the Voyage AI API key is valid.
func (p *provider) Validate(ctx context.Context) error {
	_, err := p.Embed(ctx, []string{"test"})
	return err
}

// embeddingRequest is the Voyage AI embeddings API request body.
type embeddingRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	InputType  *string  `json:"input_type,omitempty"`
	Truncation bool     `json:"truncation"`
}

// embeddingResponse is the Voyage AI embeddings API response body.
type embeddingResponse struct {
	Data        []embeddingData `json:"data"`
	Model       string          `json:"model"`
	TotalTokens int             `json:"total_tokens"`
}

type embeddingData struct {
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}

type apiErrorResponse struct {
	Detail string `json:"detail"`
}
