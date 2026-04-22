package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// withMockedBase is a test-only helper that rewrites the base URL by
// swapping anthropic.com with a test server via a custom transport.
func newTestClaudeWithServer(t *testing.T, serverURL string) *ClaudeProvider {
	t.Helper()
	p, err := NewClaudeProvider(ClaudeConfig{
		APIKey:     "test-key",
		Model:      "claude-sonnet-4-6",
		MaxRetries: 1,
		Timeout:    2 * time.Second,
	})
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	// Replace the HTTP client with a rewrite transport so the request
	// hits the test server regardless of the hardcoded list URL.
	target := strings.TrimPrefix(serverURL, "http://")
	p.httpClient = &http.Client{
		Timeout: 2 * time.Second,
		Transport: &rewriteTransport{target: target},
	}
	return p
}

type rewriteTransport struct{ target string }

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = t.target
	return http.DefaultTransport.RoundTrip(req)
}

func TestListModels_Success(t *testing.T) {
	var receivedKey, receivedVersion, receivedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedKey = r.Header.Get("x-api-key")
		receivedVersion = r.Header.Get("anthropic-version")
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]string{
				{"id": "claude-opus-4-6", "display_name": "Claude Opus 4.6", "type": "model"},
				{"id": "claude-sonnet-4-6", "display_name": "Claude Sonnet 4.6", "type": "model"},
			},
			"has_more": false,
			"last_id":  "",
		})
	}))
	defer server.Close()

	p := newTestClaudeWithServer(t, server.URL)
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedKey != "test-key" {
		t.Errorf("x-api-key = %q", receivedKey)
	}
	if receivedVersion == "" {
		t.Errorf("anthropic-version header missing")
	}
	if receivedPath != "/v1/models" {
		t.Errorf("path = %q, want /v1/models", receivedPath)
	}
	if len(models) != 2 {
		t.Fatalf("len = %d, want 2", len(models))
	}
	if models[0].ID != "claude-opus-4-6" {
		t.Errorf("id = %q", models[0].ID)
	}
	if models[0].DisplayName != "Claude Opus 4.6" {
		t.Errorf("display_name = %q", models[0].DisplayName)
	}
}

func TestListModels_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","message":"invalid"}}`))
	}))
	defer server.Close()

	p := newTestClaudeWithServer(t, server.URL)
	_, err := p.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention status: %q", err.Error())
	}
}

func TestListModels_Pagination(t *testing.T) {
	call := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data":     []map[string]string{{"id": "a", "display_name": "A"}},
				"has_more": true,
				"last_id":  "a",
			})
			return
		}
		// second page
		after := r.URL.Query().Get("after_id")
		if after != "a" {
			t.Errorf("expected after_id=a, got %q", after)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data":     []map[string]string{{"id": "b", "display_name": "B"}},
			"has_more": false,
		})
	}))
	defer server.Close()

	p := newTestClaudeWithServer(t, server.URL)
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len = %d, want 2 (paginated)", len(models))
	}
	if models[0].ID != "a" || models[1].ID != "b" {
		t.Errorf("ids = %v", models)
	}
}

func TestListModels_DisplayNameFallsBackToID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data":     []map[string]string{{"id": "mystery-model", "display_name": ""}},
			"has_more": false,
		})
	}))
	defer server.Close()

	p := newTestClaudeWithServer(t, server.URL)
	models, _ := p.ListModels(context.Background())
	if len(models) != 1 || models[0].DisplayName != "mystery-model" {
		t.Errorf("expected DisplayName fallback to ID, got %+v", models)
	}
}
