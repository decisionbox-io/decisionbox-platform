package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
)

// ListModels filters /v1/models to IDs prefixed `text-embedding-`
// (OpenAI doesn't flag embedding models on the endpoint; prefix is
// the canonical signal). These tests stub the upstream to cover the
// happy path + filter correctness + error propagation.

func buildOpenAIListHandler(data []map[string]any, status int, body string) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/models" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if body != "" {
			_, _ = w.Write([]byte(body))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
}

func newTestProvider(server *httptest.Server) *provider {
	return newProvider("test-key", "text-embedding-3-small", server.URL, 1536)
}

func TestListModels_Filters_EmbeddingOnly(t *testing.T) {
	server := httptest.NewServer(buildOpenAIListHandler([]map[string]any{
		{"id": "text-embedding-3-small", "object": "model", "owned_by": "openai"},
		{"id": "text-embedding-3-large", "object": "model", "owned_by": "openai"},
		{"id": "gpt-4o", "object": "model", "owned_by": "openai"},
		{"id": "whisper-1", "object": "model", "owned_by": "openai"},
		{"id": "dall-e-3", "object": "model", "owned_by": "openai"},
	}, 0, ""))
	defer server.Close()

	p := newTestProvider(server)
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 embedding models after filter, got %d (%+v)", len(models), models)
	}
	// Dimensions come from the catalog map, so known IDs pick them up.
	for _, m := range models {
		if m.ID == "text-embedding-3-small" && m.Dimensions != 1536 {
			t.Errorf("small dims = %d, want 1536", m.Dimensions)
		}
		if m.ID == "text-embedding-3-large" && m.Dimensions != 3072 {
			t.Errorf("large dims = %d, want 3072", m.Dimensions)
		}
	}
}

func TestListModels_UnknownEmbeddingID_DimensionsZero(t *testing.T) {
	// Future OpenAI embedding model we haven't catalogued — should be
	// returned but with Dimensions=0 so the dashboard's combobox shows
	// it as "dimensions unknown" rather than crashing.
	server := httptest.NewServer(buildOpenAIListHandler([]map[string]any{
		{"id": "text-embedding-4-ultra", "object": "model", "owned_by": "openai"},
	}, 0, ""))
	defer server.Close()

	p := newTestProvider(server)
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models", len(models))
	}
	if models[0].ID != "text-embedding-4-ultra" {
		t.Errorf("ID = %q", models[0].ID)
	}
	if models[0].Dimensions != 0 {
		t.Errorf("unknown model should report Dimensions=0, got %d", models[0].Dimensions)
	}
}

func TestListModels_UpstreamErrorSurfacesMessage(t *testing.T) {
	// Structured 401 body — the handler should extract the
	// provider-shaped error so the UI shows a specific message instead
	// of a generic "status 401".
	server := httptest.NewServer(buildOpenAIListHandler(nil, http.StatusUnauthorized, `{
		"error": {
			"message": "Incorrect API key provided: sk-****",
			"type":    "invalid_request_error"
		}
	}`))
	defer server.Close()

	p := newTestProvider(server)
	_, err := p.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error from 401")
	}
	// The specific message from the upstream should be in the wrapped
	// error; the dashboard's live_error surfacing depends on it.
	if !containsAny(err.Error(), "Incorrect API key", "invalid_request_error") {
		t.Errorf("error did not include upstream message: %v", err)
	}
}

func TestListModels_NetworkErrorPropagates(t *testing.T) {
	// No server — connection refused should bubble up as a wrapped
	// error rather than panic.
	p := newProvider("test-key", "text-embedding-3-small", "http://127.0.0.1:0", 1536)
	_, err := p.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected network error")
	}
}

// ProviderRegisteredAsListModelLister ensures the concrete provider
// satisfies the embedding.ModelLister capability interface — otherwise
// the API-side handler's type assertion would silently return nil.
func TestProviderSatisfiesModelListerCapability(t *testing.T) {
	prov, err := goembedding.NewProvider("openai", goembedding.ProviderConfig{
		"api_key": "test-key",
		"model":   "text-embedding-3-small",
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if _, ok := prov.(goembedding.ModelLister); !ok {
		t.Fatal("openai embedding provider must implement embedding.ModelLister")
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}
