package azureopenai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
)

func TestRegistration(t *testing.T) {
	names := goembedding.RegisteredProviders()
	found := false
	for _, n := range names {
		if n == "azure-openai" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected azure-openai to be registered")
	}
}

func TestRegistrationMeta(t *testing.T) {
	meta, ok := goembedding.GetProviderMeta("azure-openai")
	if !ok {
		t.Fatal("expected azure-openai metadata to exist")
	}
	if meta.Name != "Azure OpenAI" {
		t.Errorf("expected Name='Azure OpenAI', got %s", meta.Name)
	}
	if len(meta.Models) != 3 {
		t.Errorf("expected 3 models, got %d", len(meta.Models))
	}

	hasEndpoint := false
	hasAPIKey := false
	hasDeployment := false
	for _, f := range meta.ConfigFields {
		switch f.Key {
		case "endpoint":
			hasEndpoint = f.Required
		case "api_key":
			hasAPIKey = f.Required && f.Type == "credential"
		case "deployment":
			hasDeployment = f.Required
		}
	}
	if !hasEndpoint {
		t.Error("expected required endpoint config field")
	}
	if !hasAPIKey {
		t.Error("expected required api_key credential field")
	}
	if !hasDeployment {
		t.Error("expected required deployment config field")
	}
}

func TestFactoryMissingEndpoint(t *testing.T) {
	_, err := goembedding.NewProvider("azure-openai", goembedding.ProviderConfig{
		"api_key":    "test-key",
		"deployment": "my-deployment",
	})
	if err == nil {
		t.Fatal("expected error for missing endpoint")
	}
	if !strings.Contains(err.Error(), "endpoint is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFactoryMissingAPIKey(t *testing.T) {
	_, err := goembedding.NewProvider("azure-openai", goembedding.ProviderConfig{
		"endpoint":   "https://test.openai.azure.com",
		"deployment": "my-deployment",
	})
	if err == nil {
		t.Fatal("expected error for missing api_key")
	}
	if !strings.Contains(err.Error(), "api_key is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFactoryMissingDeployment(t *testing.T) {
	_, err := goembedding.NewProvider("azure-openai", goembedding.ProviderConfig{
		"endpoint": "https://test.openai.azure.com",
		"api_key":  "test-key",
	})
	if err == nil {
		t.Fatal("expected error for missing deployment")
	}
	if !strings.Contains(err.Error(), "deployment is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFactoryUnsupportedModel(t *testing.T) {
	_, err := goembedding.NewProvider("azure-openai", goembedding.ProviderConfig{
		"endpoint":   "https://test.openai.azure.com",
		"api_key":    "test-key",
		"deployment": "my-deployment",
		"model":      "nonexistent-model",
	})
	if err == nil {
		t.Fatal("expected error for unsupported model")
	}
	if !strings.Contains(err.Error(), "unsupported model") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFactoryDefaultModel(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(embeddingResponse{
			Data: []embeddingData{{Index: 0, Embedding: make([]float64, 1536)}},
		})
	})
	defer server.Close()

	p := newProvider(server.URL, "test-key", "my-deployment", "text-embedding-3-small", defaultAPIVersion, 1536)
	if p.ModelName() != "text-embedding-3-small" {
		t.Errorf("expected default model text-embedding-3-small, got %s", p.ModelName())
	}
	if p.Dimensions() != 1536 {
		t.Errorf("expected 1536 dims, got %d", p.Dimensions())
	}
}

func TestEmbedSingleText(t *testing.T) {
	expectedVec := []float64{0.1, 0.2, 0.3}

	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		// Azure uses api-key header, NOT Authorization: Bearer
		if r.Header.Get("api-key") != "test-key" {
			t.Errorf("expected api-key header 'test-key', got %s", r.Header.Get("api-key"))
		}
		// Verify URL structure: /openai/deployments/{deployment}/embeddings?api-version=...
		if !strings.Contains(r.URL.Path, "/openai/deployments/my-deployment/embeddings") {
			t.Errorf("unexpected URL path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("api-version") != defaultAPIVersion {
			t.Errorf("expected api-version=%s, got %s", defaultAPIVersion, r.URL.Query().Get("api-version"))
		}

		var req embeddingRequest
		json.NewDecoder(r.Body).Decode(&req)
		if len(req.Input) != 1 {
			t.Errorf("expected 1 input, got %d", len(req.Input))
		}

		json.NewEncoder(w).Encode(embeddingResponse{
			Data: []embeddingData{
				{Index: 0, Embedding: expectedVec},
			},
			Model: "text-embedding-3-small",
			Usage: embeddingUsage{PromptTokens: 5, TotalTokens: 5},
		})
	})
	defer server.Close()

	p := newProvider(server.URL, "test-key", "my-deployment", "text-embedding-3-small", defaultAPIVersion, 1536)
	result, err := p.Embed(context.Background(), []string{"hello world"})
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if len(result[0]) != 3 {
		t.Fatalf("expected 3 dims, got %d", len(result[0]))
	}
	if result[0][0] != 0.1 {
		t.Errorf("expected first value 0.1, got %f", result[0][0])
	}
}

func TestEmbedBatch(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req embeddingRequest
		json.NewDecoder(r.Body).Decode(&req)

		data := make([]embeddingData, len(req.Input))
		for i := range req.Input {
			data[i] = embeddingData{Index: i, Embedding: make([]float64, 3)}
		}
		json.NewEncoder(w).Encode(embeddingResponse{Data: data})
	})
	defer server.Close()

	p := newProvider(server.URL, "test-key", "my-deployment", "text-embedding-3-small", defaultAPIVersion, 1536)
	result, err := p.Embed(context.Background(), []string{"text1", "text2", "text3"})
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result))
	}
}

func TestEmbedEmpty(t *testing.T) {
	p := newProvider("http://unused", "key", "dep", "text-embedding-3-small", defaultAPIVersion, 1536)
	result, err := p.Embed(context.Background(), []string{})
	if err != nil {
		t.Fatalf("Embed empty failed: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result for empty input, got %v", result)
	}
}

func TestEmbedAPIError(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(apiErrorResponse{
			Error: struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    string `json:"code"`
			}{
				Message: "Access denied due to invalid subscription key",
				Code:    "401",
			},
		})
	})
	defer server.Close()

	p := newProvider(server.URL, "bad-key", "my-deployment", "text-embedding-3-small", defaultAPIVersion, 1536)
	_, err := p.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error for API error")
	}
	if !strings.Contains(err.Error(), "Access denied") {
		t.Errorf("expected access denied message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected HTTP 401 in error, got: %v", err)
	}
}

func TestEmbedServerError(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	})
	defer server.Close()

	p := newProvider(server.URL, "test-key", "my-deployment", "text-embedding-3-small", defaultAPIVersion, 1536)
	_, err := p.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error for server error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected HTTP 500 in error, got: %v", err)
	}
}

func TestEmbedMismatchedCount(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(embeddingResponse{
			Data: []embeddingData{
				{Index: 0, Embedding: make([]float64, 3)},
			},
		})
	})
	defer server.Close()

	p := newProvider(server.URL, "test-key", "my-deployment", "text-embedding-3-small", defaultAPIVersion, 1536)
	_, err := p.Embed(context.Background(), []string{"text1", "text2"})
	if err == nil {
		t.Fatal("expected error for mismatched count")
	}
	if !strings.Contains(err.Error(), "expected 2 embeddings") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEmbedInvalidIndex(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(embeddingResponse{
			Data: []embeddingData{
				{Index: 5, Embedding: make([]float64, 3)},
			},
		})
	})
	defer server.Close()

	p := newProvider(server.URL, "test-key", "my-deployment", "text-embedding-3-small", defaultAPIVersion, 1536)
	_, err := p.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error for invalid index")
	}
	if !strings.Contains(err.Error(), "invalid index") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidate(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(embeddingResponse{
			Data: []embeddingData{{Index: 0, Embedding: make([]float64, 1536)}},
		})
	})
	defer server.Close()

	p := newProvider(server.URL, "test-key", "my-deployment", "text-embedding-3-small", defaultAPIVersion, 1536)
	err := p.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
}

func TestValidateError(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(apiErrorResponse{
			Error: struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    string `json:"code"`
			}{Message: "invalid key"},
		})
	})
	defer server.Close()

	p := newProvider(server.URL, "bad-key", "my-deployment", "text-embedding-3-small", defaultAPIVersion, 1536)
	err := p.Validate(context.Background())
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestModelName(t *testing.T) {
	p := newProvider("http://unused", "key", "dep", "text-embedding-3-large", defaultAPIVersion, 3072)
	if p.ModelName() != "text-embedding-3-large" {
		t.Errorf("expected text-embedding-3-large, got %s", p.ModelName())
	}
}

func TestDimensions(t *testing.T) {
	tests := []struct {
		model string
		dims  int
	}{
		{"text-embedding-3-small", 1536},
		{"text-embedding-3-large", 3072},
		{"text-embedding-ada-002", 1536},
	}
	for _, tt := range tests {
		p := newProvider("http://unused", "key", "dep", tt.model, defaultAPIVersion, tt.dims)
		if p.Dimensions() != tt.dims {
			t.Errorf("model %s: expected %d dims, got %d", tt.model, tt.dims, p.Dimensions())
		}
	}
}

func TestCustomAPIVersion(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api-version") != "2025-01-01" {
			t.Errorf("expected custom api-version, got %s", r.URL.Query().Get("api-version"))
		}
		json.NewEncoder(w).Encode(embeddingResponse{
			Data: []embeddingData{{Index: 0, Embedding: make([]float64, 1536)}},
		})
	})
	defer server.Close()

	p := newProvider(server.URL, "test-key", "my-deployment", "text-embedding-3-small", "2025-01-01", 1536)
	_, err := p.Embed(context.Background(), []string{"test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Verify provider implements the interface at compile time.
var _ goembedding.Provider = (*provider)(nil)

func newMockServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(handler)
}
