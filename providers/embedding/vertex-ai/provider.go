// Package vertexai provides an embedding.Provider backed by Google Cloud Vertex AI.
// Uses Application Default Credentials (ADC) for authentication — same as the LLM vertex-ai provider.
//
// Register via init():
//
//	import _ "github.com/decisionbox-io/decisionbox/providers/embedding/vertex-ai"
//
// Supported models:
//   - text-embedding-005 (768 dims, recommended)
//   - text-multilingual-embedding-002 (768 dims, 100+ languages)
//   - gemini-embedding-001 (3072 dims, highest quality)
package vertexai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

var modelDimensions = map[string]int{
	"text-embedding-005":              768,
	"text-multilingual-embedding-002": 768,
	"gemini-embedding-001":            3072,
}

func init() {
	goembedding.RegisterWithMeta("vertex-ai", func(cfg goembedding.ProviderConfig) (goembedding.Provider, error) {
		projectID := cfg["project_id"]
		if projectID == "" {
			return nil, fmt.Errorf("vertex-ai embedding: project_id is required")
		}

		location := cfg["location"]
		if location == "" {
			location = "us-central1"
		}

		model := cfg["model"]
		if model == "" {
			model = "text-embedding-005"
		}

		dims, ok := modelDimensions[model]
		if !ok {
			return nil, fmt.Errorf("vertex-ai embedding: unsupported model %q (supported: text-embedding-005, text-multilingual-embedding-002, gemini-embedding-001)", model)
		}

		ctx := context.Background()
		auth, err := newGCPAuth(ctx)
		if err != nil {
			return nil, err
		}

		endpoint := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:predict",
			location, projectID, location, model)

		return newProvider(model, dims, endpoint, auth, location, projectID), nil
	}, goembedding.ProviderMeta{
		Name:        "Vertex AI (GCP)",
		Description: "Google Cloud embedding models — ADC auth, no API key needed",
		ConfigFields: []goembedding.ConfigField{
			{Key: "project_id", Label: "GCP Project ID", Required: true, Type: "string", Placeholder: "my-project"},
			{Key: "location", Label: "Region", Type: "string", Default: "us-central1", Placeholder: "us-central1"},
			{Key: "model", Label: "Model", Required: true, Type: "string", Default: "text-embedding-005"},
		},
		Models: []goembedding.ModelInfo{
			{ID: "text-embedding-005", Name: "Text Embedding 005", Dimensions: 768},
			{ID: "text-multilingual-embedding-002", Name: "Multilingual Embedding 002", Dimensions: 768},
			{ID: "gemini-embedding-001", Name: "Gemini Embedding 001", Dimensions: 3072},
		},
	})
}

// provider implements embedding.Provider using Vertex AI.
type provider struct {
	model     string
	dims      int
	endpoint  string
	location  string
	projectID string
	auth      tokenProvider
	client    *http.Client
}

// tokenProvider abstracts GCP token retrieval for testing.
type tokenProvider interface {
	token(ctx context.Context) (string, error)
}

func newProvider(model string, dims int, endpoint string, auth tokenProvider, location, projectID string) *provider {
	return &provider{
		model:     model,
		dims:      dims,
		endpoint:  endpoint,
		location:  location,
		projectID: projectID,
		auth:      auth,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// maxBatchSize is the Vertex AI predict API limit per request.
const maxBatchSize = 250

// Embed generates vector embeddings for the given texts via Vertex AI predict API.
// Automatically chunks inputs exceeding the 250-text API limit.
func (p *provider) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	if len(texts) <= maxBatchSize {
		return p.embedBatch(ctx, texts)
	}

	result := make([][]float64, len(texts))
	for start := 0; start < len(texts); start += maxBatchSize {
		end := start + maxBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		chunk, err := p.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		copy(result[start:end], chunk)
	}
	return result, nil
}

func (p *provider) embedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	instances := make([]instance, len(texts))
	for i, t := range texts {
		instances[i] = instance{Content: t}
	}

	reqBody := predictRequest{
		Instances:  instances,
		Parameters: predictParameters{AutoTruncate: true},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai embedding: failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("vertex-ai embedding: failed to create request: %w", err)
	}

	tok, err := p.auth.token(ctx)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai embedding: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai embedding: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 40<<20)) // 40 MB limit
	if err != nil {
		return nil, fmt.Errorf("vertex-ai embedding: failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr vertexErrorResponse
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			return nil, fmt.Errorf("vertex-ai embedding: API error (HTTP %d): %s", resp.StatusCode, apiErr.Error.Message)
		}
		return nil, fmt.Errorf("vertex-ai embedding: API error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var predResp predictResponse
	if err := json.Unmarshal(respBody, &predResp); err != nil {
		return nil, fmt.Errorf("vertex-ai embedding: failed to unmarshal response: %w", err)
	}

	if len(predResp.Predictions) != len(texts) {
		return nil, fmt.Errorf("vertex-ai embedding: expected %d predictions, got %d", len(texts), len(predResp.Predictions))
	}

	result := make([][]float64, len(texts))
	for i, pred := range predResp.Predictions {
		result[i] = pred.Embeddings.Values
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

// Validate checks that GCP credentials and project access are valid.
func (p *provider) Validate(ctx context.Context) error {
	_, err := p.Embed(ctx, []string{"test"})
	return err
}

// gcpAuth manages GCP access tokens from Application Default Credentials.
type gcpAuth struct {
	tokenSource oauth2.TokenSource
}

func newGCPAuth(ctx context.Context) (*gcpAuth, error) {
	creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, fmt.Errorf("vertex-ai embedding: failed to find GCP credentials (run 'gcloud auth application-default login'): %w", err)
	}
	return &gcpAuth{tokenSource: creds.TokenSource}, nil
}

func (a *gcpAuth) token(ctx context.Context) (string, error) {
	tok, err := a.tokenSource.Token()
	if err != nil {
		return "", fmt.Errorf("failed to get access token: %w", err)
	}
	return tok.AccessToken, nil
}

// Request/response types for Vertex AI predict API.

type instance struct {
	Content string `json:"content"`
}

type predictParameters struct {
	AutoTruncate bool `json:"autoTruncate"`
}

type predictRequest struct {
	Instances  []instance        `json:"instances"`
	Parameters predictParameters `json:"parameters"`
}

type predictResponse struct {
	Predictions []prediction `json:"predictions"`
}

type prediction struct {
	Embeddings embeddingResult `json:"embeddings"`
}

type embeddingResult struct {
	Values     []float64      `json:"values"`
	Statistics embeddingStats `json:"statistics"`
}

type embeddingStats struct {
	Truncated  bool `json:"truncated"`
	TokenCount int  `json:"token_count"`
}

type vertexErrorResponse struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}
