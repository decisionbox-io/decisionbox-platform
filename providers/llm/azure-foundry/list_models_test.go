package azurefoundry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListModels_Success(t *testing.T) {
	var receivedKey, receivedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedKey = r.Header.Get("api-key")
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]string{
				{"id": "gpt-5"},
				{"id": "gpt-4o"},
				{"id": "claude-sonnet-4-6"},
			},
		})
	}))
	defer server.Close()

	p := newTestProvider(server.URL, "gpt-4o")
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedKey != "test-api-key-123" {
		t.Errorf("api-key header = %q", receivedKey)
	}
	if !strings.HasSuffix(receivedPath, "/openai/v1/models") {
		t.Errorf("path = %q, want .../openai/v1/models", receivedPath)
	}
	if len(models) != 3 {
		t.Fatalf("len = %d, want 3", len(models))
	}
}

func TestListModels_Forbidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"no access"}`))
	}))
	defer server.Close()

	p := newTestProvider(server.URL, "gpt-4o")
	_, err := p.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %q, should mention status", err.Error())
	}
}

func TestListModels_ServerDown(t *testing.T) {
	p := newTestProvider("http://127.0.0.1:1", "gpt-4o")
	_, err := p.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error for unreachable endpoint")
	}
}
