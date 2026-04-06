package voyage

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
		if n == "voyage" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected voyage to be registered")
	}
}

func TestRegistrationMeta(t *testing.T) {
	meta, ok := goembedding.GetProviderMeta("voyage")
	if !ok {
		t.Fatal("expected voyage metadata to exist")
	}
	if meta.Name != "Voyage AI" {
		t.Errorf("expected Name='Voyage AI', got %s", meta.Name)
	}
	if len(meta.Models) != 4 {
		t.Errorf("expected 4 models, got %d", len(meta.Models))
	}
}

func TestFactoryMissingAPIKey(t *testing.T) {
	_, err := goembedding.NewProvider("voyage", goembedding.ProviderConfig{})
	if err == nil {
		t.Fatal("expected error for missing api_key")
	}
	if !strings.Contains(err.Error(), "api_key is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFactoryUnsupportedModel(t *testing.T) {
	_, err := goembedding.NewProvider("voyage", goembedding.ProviderConfig{
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

func TestFactoryInvalidInputType(t *testing.T) {
	_, err := goembedding.NewProvider("voyage", goembedding.ProviderConfig{
		"api_key":    "test-key",
		"input_type": "invalid",
	})
	if err == nil {
		t.Fatal("expected error for invalid input_type")
	}
	if !strings.Contains(err.Error(), "invalid input_type") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFactoryDefaultModel(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(embeddingResponse{
			Data: []embeddingData{{Index: 0, Embedding: make([]float64, 1024)}},
		})
	})
	defer server.Close()

	p, err := goembedding.NewProvider("voyage", goembedding.ProviderConfig{
		"api_key":  "test-key",
		"base_url": server.URL,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ModelName() != "voyage-3" {
		t.Errorf("expected default model voyage-3, got %s", p.ModelName())
	}
	if p.Dimensions() != 1024 {
		t.Errorf("expected 1024 dims, got %d", p.Dimensions())
	}
}

func TestFactoryValidInputTypes(t *testing.T) {
	for _, inputType := range []string{"query", "document", ""} {
		_, err := goembedding.NewProvider("voyage", goembedding.ProviderConfig{
			"api_key":    "test-key",
			"base_url":   "http://unused",
			"input_type": inputType,
		})
		if err != nil {
			t.Errorf("input_type %q should be valid, got error: %v", inputType, err)
		}
	}
}

func TestEmbedSingleText(t *testing.T) {
	expectedVec := []float64{0.1, 0.2, 0.3}

	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
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
		if req.Model != "voyage-3" {
			t.Errorf("expected model voyage-3, got %s", req.Model)
		}
		if len(req.Input) != 1 {
			t.Errorf("expected 1 input, got %d", len(req.Input))
		}
		if !req.Truncation {
			t.Error("expected truncation=true")
		}
		if req.InputType != nil {
			t.Errorf("expected nil input_type for default, got %v", *req.InputType)
		}

		json.NewEncoder(w).Encode(embeddingResponse{
			Data: []embeddingData{
				{Index: 0, Embedding: expectedVec},
			},
			Model:       "voyage-3",
			TotalTokens: 5,
		})
	})
	defer server.Close()

	p := newProvider("test-key", "voyage-3", "", server.URL, 1024)
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

func TestEmbedWithInputType(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req embeddingRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.InputType == nil || *req.InputType != "query" {
			t.Error("expected input_type=query")
		}

		json.NewEncoder(w).Encode(embeddingResponse{
			Data: []embeddingData{{Index: 0, Embedding: make([]float64, 3)}},
		})
	})
	defer server.Close()

	p := newProvider("test-key", "voyage-3", "query", server.URL, 1024)
	_, err := p.Embed(context.Background(), []string{"search query"})
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
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

	p := newProvider("test-key", "voyage-3", "", server.URL, 1024)
	result, err := p.Embed(context.Background(), []string{"text1", "text2", "text3"})
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result))
	}
}

func TestEmbedEmpty(t *testing.T) {
	p := newProvider("test-key", "voyage-3", "", "http://unused", 1024)
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
			Detail: "Invalid API key. Please check your API key and try again.",
		})
	})
	defer server.Close()

	p := newProvider("bad-key", "voyage-3", "", server.URL, 1024)
	_, err := p.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error for API error")
	}
	if !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("expected API key error message, got: %v", err)
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

	p := newProvider("test-key", "voyage-3", "", server.URL, 1024)
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

	p := newProvider("test-key", "voyage-3", "", server.URL, 1024)
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

	p := newProvider("test-key", "voyage-3", "", server.URL, 1024)
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
			Data: []embeddingData{{Index: 0, Embedding: make([]float64, 1024)}},
		})
	})
	defer server.Close()

	p := newProvider("test-key", "voyage-3", "", server.URL, 1024)
	err := p.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
}

func TestValidateError(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(apiErrorResponse{Detail: "invalid key"})
	})
	defer server.Close()

	p := newProvider("bad-key", "voyage-3", "", server.URL, 1024)
	err := p.Validate(context.Background())
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestModelName(t *testing.T) {
	p := newProvider("key", "voyage-3-large", "", "http://unused", 1024)
	if p.ModelName() != "voyage-3-large" {
		t.Errorf("expected voyage-3-large, got %s", p.ModelName())
	}
}

func TestDimensions(t *testing.T) {
	tests := []struct {
		model string
		dims  int
	}{
		{"voyage-3-large", 1024},
		{"voyage-3", 1024},
		{"voyage-3-lite", 512},
		{"voyage-code-3", 1024},
	}
	for _, tt := range tests {
		p := newProvider("key", tt.model, "", "http://unused", tt.dims)
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
