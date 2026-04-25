package openai

import (
	"context"
	"encoding/json"
	"fmt"
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

// TestFactoryUnknownModel documents the relaxed-validation contract:
// the factory accepts model IDs outside the shipped map so the
// list-only path and user-typed custom IDs work without a placeholder
// kludge. Unknown IDs come back with Dimensions()==0, and any real
// Embed() call will surface the "unknown model" error from OpenAI.
func TestFactoryUnknownModel(t *testing.T) {
	prov, err := goembedding.NewProvider("openai", goembedding.ProviderConfig{
		"api_key": "test-key",
		"model":   "text-embedding-future-ultra",
	})
	if err != nil {
		t.Fatalf("factory should accept unknown model for list-only use, got: %v", err)
	}
	if prov.Dimensions() != 0 {
		t.Errorf("unknown model should report Dimensions()==0, got %d", prov.Dimensions())
	}
	if prov.ModelName() != "text-embedding-future-ultra" {
		t.Errorf("ModelName should round-trip the unknown ID, got %q", prov.ModelName())
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

func TestEmbedDuplicateIndex(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(embeddingResponse{
			Data: []embeddingData{
				{Index: 0, Embedding: make([]float64, 3)},
				{Index: 0, Embedding: make([]float64, 3)},
			},
		})
	})
	defer server.Close()

	p := newProvider("test-key", "text-embedding-3-small", server.URL, 1536)
	_, err := p.Embed(context.Background(), []string{"text1", "text2"})
	if err == nil {
		t.Fatal("expected error for duplicate index")
	}
	if !strings.Contains(err.Error(), "duplicate index") {
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

// --- batching ---

// Embed batches internally when the input exceeds the per-request cap.
// These tests drive the Embed(N) → ceil(N/embedBatchSize) /v1/embeddings
// calls contract that protects us from the "too big → truncated JSON"
// failure mode we saw on a 1415-table MSSQL warehouse.

func TestEmbed_BatchingSplitsLargeInput(t *testing.T) {
	// 250 inputs with batch size 96 → 3 calls (96 + 96 + 58).
	var calls int
	var totalReceived int
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("bad batch: %v", err)
		}
		if len(req.Input) > embedBatchSize {
			t.Errorf("batch %d had %d inputs (cap %d)", calls, len(req.Input), embedBatchSize)
		}
		totalReceived += len(req.Input)

		data := make([]embeddingData, len(req.Input))
		for i := range req.Input {
			data[i] = embeddingData{Index: i, Embedding: make([]float64, 1536)}
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(embeddingResponse{Data: data})
	})
	defer server.Close()

	p := newProvider("test-key", "text-embedding-3-small", server.URL, 1536)
	texts := make([]string, 250)
	for i := range texts {
		texts[i] = "blurb"
	}
	out, err := p.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(out) != 250 {
		t.Errorf("got %d vectors, want 250", len(out))
	}
	if totalReceived != 250 {
		t.Errorf("server received %d texts across batches, want 250", totalReceived)
	}
	wantCalls := (250 + embedBatchSize - 1) / embedBatchSize // ceil
	if calls != wantCalls {
		t.Errorf("made %d HTTP calls, want %d", calls, wantCalls)
	}
}

func TestEmbed_ExactBatchBoundary(t *testing.T) {
	// Exactly embedBatchSize inputs → exactly 1 call. Guards the
	// off-by-one that would otherwise send a zero-length second batch.
	var calls int
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req embeddingRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		data := make([]embeddingData, len(req.Input))
		for i := range req.Input {
			data[i] = embeddingData{Index: i, Embedding: make([]float64, 1536)}
		}
		json.NewEncoder(w).Encode(embeddingResponse{Data: data})
	})
	defer server.Close()

	p := newProvider("test-key", "text-embedding-3-small", server.URL, 1536)
	texts := make([]string, embedBatchSize)
	for i := range texts {
		texts[i] = "t"
	}
	if _, err := p.Embed(context.Background(), texts); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if calls != 1 {
		t.Errorf("made %d calls at boundary, want 1", calls)
	}
}

func TestEmbed_BatchPlusOne(t *testing.T) {
	// embedBatchSize + 1 inputs → 2 calls; second call carries exactly 1 input.
	var calls int
	var lastBatchSize int
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req embeddingRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		lastBatchSize = len(req.Input)
		data := make([]embeddingData, len(req.Input))
		for i := range req.Input {
			data[i] = embeddingData{Index: i, Embedding: make([]float64, 1536)}
		}
		json.NewEncoder(w).Encode(embeddingResponse{Data: data})
	})
	defer server.Close()

	p := newProvider("test-key", "text-embedding-3-small", server.URL, 1536)
	texts := make([]string, embedBatchSize+1)
	for i := range texts {
		texts[i] = "t"
	}
	if _, err := p.Embed(context.Background(), texts); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if lastBatchSize != 1 {
		t.Errorf("last batch size = %d, want 1", lastBatchSize)
	}
}

func TestEmbed_VectorsPreserveCallerOrderAcrossBatches(t *testing.T) {
	// The indexer zips Embed output back against its own text slice by
	// index — any reordering would attach embeddings to the wrong
	// table. Tag each output vector with the input's ordinal so we can
	// detect a mis-merged batch.
	batchCount := 0
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		batchCount++
		var req embeddingRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		data := make([]embeddingData, len(req.Input))
		for i, text := range req.Input {
			// Encode the input text as the first element of the vector.
			// Safe — tests are the only consumer of this server.
			vec := make([]float64, 1536)
			// parse back "text-<n>" where n is the caller ordinal.
			var n int
			_, _ = fmt.Sscanf(text, "text-%d", &n)
			vec[0] = float64(n)
			data[i] = embeddingData{Index: i, Embedding: vec}
		}
		json.NewEncoder(w).Encode(embeddingResponse{Data: data})
	})
	defer server.Close()

	p := newProvider("test-key", "text-embedding-3-small", server.URL, 1536)
	total := embedBatchSize*2 + 7
	texts := make([]string, total)
	for i := range texts {
		texts[i] = fmt.Sprintf("text-%d", i)
	}
	out, err := p.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(out) != total {
		t.Fatalf("len = %d, want %d", len(out), total)
	}
	for i, v := range out {
		if int(v[0]) != i {
			t.Fatalf("vector %d carries ordinal %v — batch merge reordered outputs", i, v[0])
		}
	}
	if batchCount != 3 {
		t.Errorf("batchCount = %d, want 3 (%d+%d+7)", batchCount, embedBatchSize, embedBatchSize)
	}
}

func TestEmbed_EmptyBodyMidBatchSurfacesActionableError(t *testing.T) {
	// Flaky provider: first batch ok, second batch returns HTTP 200
	// with an empty body (the exact failure mode that triggered this
	// whole batching effort). The resulting error MUST name the inputs
	// count so operators don't have to guess.
	var call int
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		call++
		if call == 1 {
			var req embeddingRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			data := make([]embeddingData, len(req.Input))
			for i := range req.Input {
				data[i] = embeddingData{Index: i, Embedding: make([]float64, 1536)}
			}
			json.NewEncoder(w).Encode(embeddingResponse{Data: data})
			return
		}
		// Batch 2: truncated — 200 with an empty body.
		w.WriteHeader(http.StatusOK)
	})
	defer server.Close()

	p := newProvider("test-key", "text-embedding-3-small", server.URL, 1536)
	texts := make([]string, embedBatchSize+10)
	for i := range texts {
		texts[i] = "t"
	}
	_, err := p.Embed(context.Background(), texts)
	if err == nil {
		t.Fatal("expected error for empty-body second batch")
	}
	if !strings.Contains(err.Error(), "empty response body") {
		t.Errorf("error %q should name 'empty response body'", err.Error())
	}
	if !strings.Contains(err.Error(), "inputs=10") {
		t.Errorf("error %q should include the batch size that failed", err.Error())
	}
}

func TestEmbed_FirstBatchErrorAbortsRest(t *testing.T) {
	// Don't silently swallow a batch failure by continuing to the next
	// batch — the caller expects either all vectors or an error.
	var call int
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		call++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
	})
	defer server.Close()

	p := newProvider("test-key", "text-embedding-3-small", server.URL, 1536)
	texts := make([]string, embedBatchSize*3)
	_, err := p.Embed(context.Background(), texts)
	if err == nil {
		t.Fatal("expected error on 429")
	}
	if call != 1 {
		t.Errorf("made %d calls after first failure, want 1 (no retry)", call)
	}
}
