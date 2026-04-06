package vertexai

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

// mockAuth implements tokenProvider for testing.
type mockAuth struct {
	tok string
	err error
}

func (m *mockAuth) token(ctx context.Context) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.tok, nil
}

func TestRegistration(t *testing.T) {
	names := goembedding.RegisteredProviders()
	found := false
	for _, n := range names {
		if n == "vertex-ai" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected vertex-ai to be registered")
	}
}

func TestRegistrationMeta(t *testing.T) {
	meta, ok := goembedding.GetProviderMeta("vertex-ai")
	if !ok {
		t.Fatal("expected vertex-ai metadata to exist")
	}
	if meta.Name != "Vertex AI (GCP)" {
		t.Errorf("expected Name='Vertex AI (GCP)', got %s", meta.Name)
	}
	if len(meta.Models) != 3 {
		t.Errorf("expected 3 models, got %d", len(meta.Models))
	}
	if meta.Models[0].Dimensions != 768 {
		t.Errorf("expected first model dims=768, got %d", meta.Models[0].Dimensions)
	}

	hasProjectID := false
	for _, f := range meta.ConfigFields {
		if f.Key == "project_id" && f.Required {
			hasProjectID = true
		}
	}
	if !hasProjectID {
		t.Error("expected required project_id config field")
	}
}

func TestFactoryMissingProjectID(t *testing.T) {
	_, err := goembedding.NewProvider("vertex-ai", goembedding.ProviderConfig{})
	if err == nil {
		t.Fatal("expected error for missing project_id")
	}
	if !strings.Contains(err.Error(), "project_id is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFactoryUnsupportedModel(t *testing.T) {
	_, err := goembedding.NewProvider("vertex-ai", goembedding.ProviderConfig{
		"project_id": "test-project",
		"model":      "nonexistent-model",
	})
	if err == nil {
		t.Fatal("expected error for unsupported model")
	}
	if !strings.Contains(err.Error(), "unsupported model") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEmbedSingleText(t *testing.T) {
	expectedVec := []float64{0.1, 0.2, 0.3}

	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("expected Bearer test-token, got %s", r.Header.Get("Authorization"))
		}

		var req predictRequest
		json.NewDecoder(r.Body).Decode(&req)
		if len(req.Instances) != 1 {
			t.Errorf("expected 1 instance, got %d", len(req.Instances))
		}
		if req.Instances[0].Content != "hello world" {
			t.Errorf("expected content 'hello world', got %s", req.Instances[0].Content)
		}
		if !req.Parameters.AutoTruncate {
			t.Error("expected autoTruncate=true")
		}

		json.NewEncoder(w).Encode(predictResponse{
			Predictions: []prediction{
				{Embeddings: embeddingResult{
					Values:     expectedVec,
					Statistics: embeddingStats{TokenCount: 2},
				}},
			},
		})
	})
	defer server.Close()

	p := newProvider("text-embedding-005", 768, server.URL, &mockAuth{tok: "test-token"})
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
		var req predictRequest
		json.NewDecoder(r.Body).Decode(&req)

		preds := make([]prediction, len(req.Instances))
		for i := range req.Instances {
			preds[i] = prediction{Embeddings: embeddingResult{
				Values: make([]float64, 3),
			}}
		}
		json.NewEncoder(w).Encode(predictResponse{Predictions: preds})
	})
	defer server.Close()

	p := newProvider("text-embedding-005", 768, server.URL, &mockAuth{tok: "test-token"})
	result, err := p.Embed(context.Background(), []string{"text1", "text2", "text3"})
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result))
	}
}

func TestEmbedEmpty(t *testing.T) {
	p := newProvider("text-embedding-005", 768, "http://unused", &mockAuth{tok: "test-token"})
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
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(vertexErrorResponse{
			Error: struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
				Status  string `json:"status"`
			}{
				Code:    403,
				Message: "Permission denied on resource project test-project",
				Status:  "PERMISSION_DENIED",
			},
		})
	})
	defer server.Close()

	p := newProvider("text-embedding-005", 768, server.URL, &mockAuth{tok: "test-token"})
	_, err := p.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error for API error")
	}
	if !strings.Contains(err.Error(), "Permission denied") {
		t.Errorf("expected permission denied message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("expected HTTP 403 in error, got: %v", err)
	}
}

func TestEmbedServerError(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	})
	defer server.Close()

	p := newProvider("text-embedding-005", 768, server.URL, &mockAuth{tok: "test-token"})
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
		json.NewEncoder(w).Encode(predictResponse{
			Predictions: []prediction{
				{Embeddings: embeddingResult{Values: make([]float64, 3)}},
			},
		})
	})
	defer server.Close()

	p := newProvider("text-embedding-005", 768, server.URL, &mockAuth{tok: "test-token"})
	_, err := p.Embed(context.Background(), []string{"text1", "text2"})
	if err == nil {
		t.Fatal("expected error for mismatched count")
	}
	if !strings.Contains(err.Error(), "expected 2 predictions") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEmbedAuthError(t *testing.T) {
	p := newProvider("text-embedding-005", 768, "http://unused", &mockAuth{
		err: fmt.Errorf("failed to get access token"),
	})
	_, err := p.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error for auth failure")
	}
	if !strings.Contains(err.Error(), "failed to get access token") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidate(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(predictResponse{
			Predictions: []prediction{
				{Embeddings: embeddingResult{Values: make([]float64, 768)}},
			},
		})
	})
	defer server.Close()

	p := newProvider("text-embedding-005", 768, server.URL, &mockAuth{tok: "test-token"})
	err := p.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
}

func TestValidateError(t *testing.T) {
	server := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(vertexErrorResponse{
			Error: struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
				Status  string `json:"status"`
			}{Message: "invalid credentials"},
		})
	})
	defer server.Close()

	p := newProvider("text-embedding-005", 768, server.URL, &mockAuth{tok: "bad-token"})
	err := p.Validate(context.Background())
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestModelName(t *testing.T) {
	p := newProvider("gemini-embedding-001", 3072, "http://unused", &mockAuth{tok: "t"})
	if p.ModelName() != "gemini-embedding-001" {
		t.Errorf("expected gemini-embedding-001, got %s", p.ModelName())
	}
}

func TestDimensions(t *testing.T) {
	tests := []struct {
		model string
		dims  int
	}{
		{"text-embedding-005", 768},
		{"text-multilingual-embedding-002", 768},
		{"gemini-embedding-001", 3072},
	}
	for _, tt := range tests {
		p := newProvider(tt.model, tt.dims, "http://unused", &mockAuth{tok: "t"})
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
