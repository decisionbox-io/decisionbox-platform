package openai

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
		if n == "openai" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected openai to be registered")
	}
}

func TestRegistrationMeta(t *testing.T) {
	meta, ok := goembedding.GetProviderMeta("openai")
	if !ok {
		t.Fatal("expected openai metadata to exist")
	}
	if meta.Name != "OpenAI" {
		t.Errorf("expected Name=OpenAI, got %s", meta.Name)
	}
	if len(meta.Models) != 2 {
		t.Errorf("expected 2 models, got %d", len(meta.Models))
	}
	if meta.Models[0].Dimensions != 1536 {
		t.Errorf("expected first model dims=1536, got %d", meta.Models[0].Dimensions)
	}
}

func TestFactoryMissingAPIKey(t *testing.T) {
	_, err := goembedding.NewProvider("openai", goembedding.ProviderConfig{})
	if err == nil {
		t.Fatal("expected error for missing api_key")
	}
	if !strings.Contains(err.Error(), "api_key is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFactoryUnsupportedModel(t *testing.T) {
	_, err := goembedding.NewProvider("openai", goembedding.ProviderConfig{
		"api_key": "test-key",
		"model":   "nonexistent-model",
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
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(embeddingResponse{
			Data: []embeddingData{{Index: 0, Embedding: make([]float64, 1536)}},
		})
	})
	defer server.Close()

	p, err := goembedding.NewProvider("openai", goembedding.ProviderConfig{
		"api_key":  "test-key",
		"base_url": server.URL,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ModelName() != "text-embedding-3-small" {
		t.Errorf("expected default model text-embedding-3-small, got %s", p.ModelName())
	}
	if p.Dimensions() != 1536 {
		t.Errorf("expected 1536 dims, got %d", p.Dimensions())
	}
}

func TestFactoryLargeModel(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(embeddingResponse{
			Data: []embeddingData{{Index: 0, Embedding: make([]float64, 3072)}},
		})
	})
	defer server.Close()

	p, err := goembedding.NewProvider("openai", goembedding.ProviderConfig{
		"api_key":  "test-key",
		"model":    "text-embedding-3-large",
		"base_url": server.URL,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Dimensions() != 3072 {
		t.Errorf("expected 3072 dims, got %d", p.Dimensions())
	}
}

func TestEmbedSingleText(t *testing.T) {
	expectedVec := []float64{0.1, 0.2, 0.3}

	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/embeddings" {
			t.Errorf("expected /embeddings path, got %s", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("expected Bearer auth header")
		}

		var req embeddingRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "text-embedding-3-small" {
			t.Errorf("expected model text-embedding-3-small, got %s", req.Model)
		}
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

	p := newProvider("test-key", "text-embedding-3-small", server.URL, 1536)
	result, err := p.Embed(context.Background(), []string{"hello world"})
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if len(result[0]) != 3 {
		t.Fatalf("expected 3 dims in result, got %d", len(result[0]))
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
			data[i] = embeddingData{
				Index:     i,
				Embedding: make([]float64, 3),
			}
		}
		json.NewEncoder(w).Encode(embeddingResponse{Data: data})
	})
	defer server.Close()

	p := newProvider("test-key", "text-embedding-3-small", server.URL, 1536)
	result, err := p.Embed(context.Background(), []string{"text1", "text2", "text3"})
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result))
	}
}

func TestEmbedEmpty(t *testing.T) {
	p := newProvider("test-key", "text-embedding-3-small", "http://unused", 1536)
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
			}{
				Message: "Incorrect API key provided",
				Type:    "invalid_request_error",
			},
		})
	})
	defer server.Close()

	p := newProvider("bad-key", "text-embedding-3-small", server.URL, 1536)
	_, err := p.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error for API error")
	}
	if !strings.Contains(err.Error(), "Incorrect API key") {
		t.Errorf("expected API error message, got: %v", err)
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

	p := newProvider("test-key", "text-embedding-3-small", server.URL, 1536)
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
		// Return 1 embedding for 2 inputs
		json.NewEncoder(w).Encode(embeddingResponse{
			Data: []embeddingData{
				{Index: 0, Embedding: make([]float64, 3)},
			},
		})
	})
	defer server.Close()

	p := newProvider("test-key", "text-embedding-3-small", server.URL, 1536)
	_, err := p.Embed(context.Background(), []string{"text1", "text2"})
	if err == nil {
		t.Fatal("expected error for mismatched count")
	}
	if !strings.Contains(err.Error(), "expected 2 embeddings") {
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

	p := newProvider("test-key", "text-embedding-3-small", server.URL, 1536)
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
			}{Message: "invalid key"},
		})
	})
	defer server.Close()

	p := newProvider("bad-key", "text-embedding-3-small", server.URL, 1536)
	err := p.Validate(context.Background())
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestModelName(t *testing.T) {
	p := newProvider("key", "text-embedding-3-large", "http://unused", 3072)
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
	}
	for _, tt := range tests {
		p := newProvider("key", tt.model, "http://unused", tt.dims)
		if p.Dimensions() != tt.dims {
			t.Errorf("model %s: expected %d dims, got %d", tt.model, tt.dims, p.Dimensions())
		}
	}
}

// Verify provider implements the interface at compile time.
var _ goembedding.Provider = (*provider)(nil)

func newMockServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(handler)
}
