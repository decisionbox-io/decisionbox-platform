// Package azureopenai provides an embedding.Provider backed by Azure OpenAI Service.
// Uses the Azure-specific endpoint format with api-key authentication.
//
// Register via init():
//
//	import _ "github.com/decisionbox-io/decisionbox/providers/embedding/azure-openai"
//
// Endpoint format:
//
//	POST {endpoint}/openai/deployments/{deployment}/embeddings?api-version=2024-10-21
//
// Supported models (deployed as Azure deployments):
//   - text-embedding-3-small (1536 dims)
//   - text-embedding-3-large (3072 dims)
//   - text-embedding-ada-002 (1536 dims)
package azureopenai

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

const defaultAPIVersion = "2024-10-21"

var modelDimensions = map[string]int{
	"text-embedding-3-small": 1536,
	"text-embedding-3-large": 3072,
	"text-embedding-ada-002": 1536,
}

func init() {
	goembedding.RegisterWithMeta("azure-openai", func(cfg goembedding.ProviderConfig) (goembedding.Provider, error) {
		endpoint := cfg["endpoint"]
		if endpoint == "" {
			return nil, fmt.Errorf("azure-openai embedding: endpoint is required")
		}

		apiKey := cfg["api_key"]
		if apiKey == "" {
			return nil, fmt.Errorf("azure-openai embedding: api_key is required")
		}

		deployment := cfg["deployment"]
		if deployment == "" {
			return nil, fmt.Errorf("azure-openai embedding: deployment is required")
		}

		model := cfg["model"]
		if model == "" {
			model = "text-embedding-3-small"
		}

		dims, ok := modelDimensions[model]
		if !ok {
			return nil, fmt.Errorf("azure-openai embedding: unsupported model %q (supported: text-embedding-3-small, text-embedding-3-large, text-embedding-ada-002)", model)
		}

		apiVersion := cfg["api_version"]
		if apiVersion == "" {
			apiVersion = defaultAPIVersion
		}

		return newProvider(endpoint, apiKey, deployment, model, apiVersion, dims), nil
	}, goembedding.ProviderMeta{
		Name:        "Azure OpenAI",
		Description: "Azure-hosted OpenAI embeddings — enterprise compliance, regional deployment",
		ConfigFields: []goembedding.ConfigField{
			{Key: "endpoint", Label: "Azure Endpoint", Required: true, Type: "string", Placeholder: "https://your-resource.openai.azure.com"},
			{Key: "api_key", Label: "API Key", Required: true, Type: "credential", Placeholder: "your-api-key"},
			{Key: "deployment", Label: "Deployment Name", Required: true, Type: "string", Placeholder: "my-embedding-deployment"},
			{Key: "model", Label: "Model", Required: true, Type: "string", Default: "text-embedding-3-small"},
			{Key: "api_version", Label: "API Version", Type: "string", Default: defaultAPIVersion},
		},
		Models: []goembedding.ModelInfo{
			{ID: "text-embedding-3-small", Name: "Embedding 3 Small", Dimensions: 1536},
			{ID: "text-embedding-3-large", Name: "Embedding 3 Large", Dimensions: 3072},
			{ID: "text-embedding-ada-002", Name: "Embedding Ada 002", Dimensions: 1536},
		},
	})
}

// provider implements embedding.Provider using Azure OpenAI.
type provider struct {
	endpoint   string
	apiKey     string
	deployment string
	model      string
	apiVersion string
	dims       int
	client     *http.Client
}

func newProvider(endpoint, apiKey, deployment, model, apiVersion string, dims int) *provider {
	return &provider{
		endpoint:   endpoint,
		apiKey:     apiKey,
		deployment: deployment,
		model:      model,
		apiVersion: apiVersion,
		dims:       dims,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// Embed generates vector embeddings for the given texts via Azure OpenAI.
func (p *provider) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	reqBody := embeddingRequest{
		Input: texts,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("azure-openai embedding: failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/openai/deployments/%s/embeddings?api-version=%s",
		p.endpoint, p.deployment, p.apiVersion)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("azure-openai embedding: failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api-key", p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("azure-openai embedding: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 40<<20)) // 40 MB limit
	if err != nil {
		return nil, fmt.Errorf("azure-openai embedding: failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr apiErrorResponse
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			return nil, fmt.Errorf("azure-openai embedding: API error (HTTP %d): %s", resp.StatusCode, apiErr.Error.Message)
		}
		return nil, fmt.Errorf("azure-openai embedding: API error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var embResp embeddingResponse
	if err := json.Unmarshal(respBody, &embResp); err != nil {
		return nil, fmt.Errorf("azure-openai embedding: failed to unmarshal response: %w", err)
	}

	if len(embResp.Data) != len(texts) {
		return nil, fmt.Errorf("azure-openai embedding: expected %d embeddings, got %d", len(texts), len(embResp.Data))
	}

	result := make([][]float64, len(texts))
	for _, d := range embResp.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("azure-openai embedding: invalid index %d in response", d.Index)
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

// Validate checks that Azure OpenAI credentials and deployment are valid.
func (p *provider) Validate(ctx context.Context) error {
	_, err := p.Embed(ctx, []string{"test"})
	return err
}

// embeddingRequest is the Azure OpenAI embeddings API request body.
type embeddingRequest struct {
	Input []string `json:"input"`
}

// embeddingResponse is the Azure OpenAI embeddings API response body.
// Identical to OpenAI's response format.
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
		Code    string `json:"code"`
	} `json:"error"`
}
