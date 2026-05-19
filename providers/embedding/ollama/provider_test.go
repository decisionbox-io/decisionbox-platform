package ollama

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
		if n == "ollama" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected ollama to be registered")
	}
}

func TestRegistrationMeta(t *testing.T) {
	meta, ok := goembedding.GetProviderMeta("ollama")
	if !ok {
		t.Fatal("expected ollama metadata to exist")
	}
	if meta.Name != "Ollama (Local)" {
		t.Errorf("expected Name='Ollama (Local)', got %s", meta.Name)
	}
	// Models is a non-empty suggestion catalog now (not an allow-list).
	// Any model the user has pulled works once the probe returns a
	// non-zero dimension.
	if len(meta.Models) == 0 {
		t.Errorf("expected suggestion catalog, got 0 models")
	}
}

// Picking a model the Ollama server hasn't pulled returns Ollama's
// "model not found" error verbatim — the factory forwards it so the
// dashboard surfaces an actionable hint ('ollama pull <model>').
func TestFactoryMissingModelOnServer(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"model 'nonexistent-model' not found"}`))
	})
	defer server.Close()

	_, err := goembedding.NewProvider("ollama", goembedding.ProviderConfig{
		"host":  server.URL,
		"model": "nonexistent-model",
	})
	if err == nil {
		t.Fatal("expected error when the server doesn't have the model")
	}
	if !strings.Contains(err.Error(), "model not found") && !strings.Contains(err.Error(), "404") {
		t.Errorf("expected upstream Ollama error to be forwarded; got: %v", err)
	}
}

// An empty model arg is a configuration bug — the dashboard should
// always send one, but the factory rejects with an actionable hint
// rather than silently picking a default that may not be pulled.
// TestFactoryEmptyModelReturnsListOnlyProvider verifies that
// constructing the provider without a `model` succeeds and yields a
// partial provider with dims=0. This is the path the dashboard's
// "Load models" flow takes — it constructs the provider before the
// user has picked a model so it can hit ListModels(). Without this,
// listing would either require guessing a default model (which is what
// caused the 404 against `nomic-embed-text` on servers that didn't
// have it pulled) or fail outright.
func TestFactoryEmptyModelReturnsListOnlyProvider(t *testing.T) {
	probeCalls := 0
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		probeCalls++
	})
	defer server.Close()

	prov, err := goembedding.NewProvider("ollama", goembedding.ProviderConfig{
		"host": server.URL,
		// model intentionally unset
	})
	if err != nil {
		t.Fatalf("expected list-only construction to succeed, got %v", err)
	}
	if prov == nil {
		t.Fatal("provider should not be nil in list-only mode")
	}
	if probeCalls != 0 {
		t.Errorf("probe should be skipped in list-only mode, got %d calls", probeCalls)
	}
	if prov.Dimensions() != 0 {
		t.Errorf("Dimensions() = %d, want 0 (no probe ran)", prov.Dimensions())
	}
	if prov.ModelName() != "" {
		t.Errorf("ModelName() = %q, want empty in list-only mode", prov.ModelName())
	}
}

// TestEmbedWithoutModelFailsCleanly verifies that calling Embed() on a
// list-only provider returns a clear error instead of forwarding an
// empty `model` to /api/embed (which would surface as a confusing
// upstream 400). Callers that picked a model should re-construct via
// NewProvider with cfg["model"] set.
func TestEmbedWithoutModelFailsCleanly(t *testing.T) {
	requests := 0
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
	})
	defer server.Close()

	prov, err := goembedding.NewProvider("ollama", goembedding.ProviderConfig{"host": server.URL})
	if err != nil {
		t.Fatalf("list-only construction failed: %v", err)
	}
	_, err = prov.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected Embed() to error in list-only mode")
	}
	if !strings.Contains(err.Error(), "list-only") {
		t.Errorf("error should mention list-only mode for a useful caller-side hint, got %v", err)
	}
	if requests != 0 {
		t.Errorf("Embed() should fail BEFORE hitting the server, got %d requests", requests)
	}
}

// The dimension is discovered from /api/embed at construction —
// whatever vector length the server returns is what we report. This
// test serves a 1024-dim probe response and asserts the provider
// surfaces 1024 without needing the model in any hardcoded map.
func TestFactoryProbeDimension(t *testing.T) {
	probeCalls := 0
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		probeCalls++
		json.NewEncoder(w).Encode(embedResponse{
			Embeddings: [][]float64{make([]float64, 1024)},
		})
	})
	defer server.Close()

	p, err := goembedding.NewProvider("ollama", goembedding.ProviderConfig{
		"host":  server.URL,
		"model": "mxbai-embed-large:latest",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ModelName() != "mxbai-embed-large:latest" {
		t.Errorf("expected mxbai-embed-large:latest preserved (with tag), got %s", p.ModelName())
	}
	if p.Dimensions() != 1024 {
		t.Errorf("expected probed dimension 1024, got %d", p.Dimensions())
	}
	if probeCalls != 1 {
		t.Errorf("expected exactly 1 probe call, got %d", probeCalls)
	}
}

// A model that returns an empty embedding (not embedding-capable —
// some pure chat models) is rejected at construction with a clear
// error, instead of silently producing zero-length vectors that would
// break the Qdrant collection at index time.
func TestFactoryZeroLengthEmbeddingRejected(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(embedResponse{Embeddings: [][]float64{}})
	})
	defer server.Close()

	_, err := goembedding.NewProvider("ollama", goembedding.ProviderConfig{
		"host":  server.URL,
		"model": "not-an-embedding-model",
	})
	if err == nil {
		t.Fatal("expected zero-length embedding to be rejected")
	}
	if !strings.Contains(err.Error(), "zero-length") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEmbedSingleText(t *testing.T) {
	expectedVec := []float64{0.1, 0.2, 0.3}

	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/embed" {
			t.Errorf("expected /api/embed path, got %s", r.URL.Path)
		}

		var req embedRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "nomic-embed-text" {
			t.Errorf("expected model nomic-embed-text, got %s", req.Model)
		}
		if len(req.Input) != 1 || req.Input[0] != "hello world" {
			t.Errorf("unexpected input: %v", req.Input)
		}

		json.NewEncoder(w).Encode(embedResponse{
			Model:      "nomic-embed-text",
			Embeddings: [][]float64{expectedVec},
		})
	})
	defer server.Close()

	p := newProvider(server.URL, "nomic-embed-text", 768)
	result, err := p.Embed(context.Background(), []string{"hello world"})
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if result[0][0] != 0.1 {
		t.Errorf("expected first value 0.1, got %f", result[0][0])
	}
}

func TestEmbedBatch(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		json.NewDecoder(r.Body).Decode(&req)

		embeddings := make([][]float64, len(req.Input))
		for i := range req.Input {
			embeddings[i] = make([]float64, 768)
		}
		json.NewEncoder(w).Encode(embedResponse{Embeddings: embeddings})
	})
	defer server.Close()

	p := newProvider(server.URL, "nomic-embed-text", 768)
	result, err := p.Embed(context.Background(), []string{"text1", "text2", "text3"})
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result))
	}
}

func TestEmbedEmpty(t *testing.T) {
	p := newProvider("http://unused", "nomic-embed-text", 768)
	result, err := p.Embed(context.Background(), []string{})
	if err != nil {
		t.Fatalf("Embed empty failed: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result for empty input, got %v", result)
	}
}

func TestEmbedServerError(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "model not found"}`))
	})
	defer server.Close()

	p := newProvider(server.URL, "nomic-embed-text", 768)
	_, err := p.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error for server error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected HTTP 500 in error, got: %v", err)
	}
}

func TestEmbedConnectionError(t *testing.T) {
	p := newProvider("http://localhost:1", "nomic-embed-text", 768)
	_, err := p.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error for connection failure")
	}
	if !strings.Contains(err.Error(), "is Ollama running") {
		t.Errorf("expected helpful error message, got: %v", err)
	}
}

func TestEmbedMismatchedCount(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(embedResponse{
			Embeddings: [][]float64{make([]float64, 768)},
		})
	})
	defer server.Close()

	p := newProvider(server.URL, "nomic-embed-text", 768)
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
		json.NewEncoder(w).Encode(embedResponse{
			Embeddings: [][]float64{make([]float64, 768)},
		})
	})
	defer server.Close()

	p := newProvider(server.URL, "nomic-embed-text", 768)
	err := p.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
}

func TestModelName(t *testing.T) {
	p := newProvider("http://unused", "mxbai-embed-large", 1024)
	if p.ModelName() != "mxbai-embed-large" {
		t.Errorf("expected mxbai-embed-large, got %s", p.ModelName())
	}
}

func TestDimensions(t *testing.T) {
	tests := []struct {
		model string
		dims  int
	}{
		{"nomic-embed-text", 768},
		{"mxbai-embed-large", 1024},
		{"all-minilm", 384},
	}
	for _, tt := range tests {
		p := newProvider("http://unused", tt.model, tt.dims)
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
