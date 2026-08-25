package litellm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListModels_ParsesProxyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.Error(w, "wrong path "+r.URL.Path, http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o"},{"id":"claude-sonnet"},{"id":""}]}`))
	}))
	defer srv.Close()

	prov := NewLiteLLMProvider("key", "", srv.URL, srv.Client())
	models, err := prov.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	// Empty IDs are dropped.
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2: %+v", len(models), models)
	}
	if models[0].ID != "gpt-4o" || models[1].ID != "claude-sonnet" {
		t.Errorf("unexpected models: %+v", models)
	}
}

func TestListModels_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	prov := NewLiteLLMProvider("", "", srv.URL, srv.Client())
	if _, err := prov.ListModels(context.Background()); err == nil {
		t.Fatal("expected an error on 500")
	}
}
