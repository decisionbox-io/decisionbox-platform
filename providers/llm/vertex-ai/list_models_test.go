package vertexai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListModels_MultiPublisher(t *testing.T) {
	// Simulate the publisher list endpoint per publisher. Respond with
	// different data for each publisher so we can confirm the ID prefixing.
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.Contains(r.URL.Path, "/publishers/google/models"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"publisherModels": []map[string]string{
					{"name": "publishers/google/models/gemini-2.5-pro", "displayName": "Gemini 2.5 Pro"},
					{"name": "publishers/google/models/gemini-2.5-flash", "displayName": "Gemini 2.5 Flash"},
				},
			})
		case strings.Contains(r.URL.Path, "/publishers/meta/models"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"publisherModels": []map[string]string{
					{"name": "publishers/meta/models/llama-3.3-70b-instruct-maas", "displayName": "Llama 3.3 70B"},
				},
			})
		case strings.Contains(r.URL.Path, "/publishers/anthropic/models"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"publisherModels": []map[string]string{
					{"name": "publishers/anthropic/models/claude-opus-4-6@20251101", "displayName": "Claude Opus 4.6"},
				},
			})
		default:
			_, _ = w.Write([]byte(`{"publisherModels":[]}`))
		}
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "gemini-2.5-pro")
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) < 4 {
		t.Fatalf("len = %d, want at least 4", len(models))
	}

	ids := make(map[string]bool)
	for _, m := range models {
		ids[m.ID] = true
	}
	for _, want := range []string{
		"gemini-2.5-pro",
		"gemini-2.5-flash",
		"meta/llama-3.3-70b-instruct-maas",
		"claude-opus-4-6@20251101",
	} {
		if !ids[want] {
			t.Errorf("expected id %q in result, got %v", want, ids)
		}
	}
}

func TestListModels_PerPublisherFailureNonFatal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/publishers/anthropic/models") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"publisherModels": []map[string]string{
				{"name": "publishers/google/models/gemini-2.5-flash", "displayName": "Gemini 2.5 Flash"},
			},
		})
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "gemini-2.5-flash")
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("one publisher failure should be non-fatal, got: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("expected models from the surviving publishers")
	}
}

func TestListModels_AuthError(t *testing.T) {
	p := &VertexAIProvider{
		projectID: "test-project",
		location:  "us-central1",
		model:     "gemini-2.5-pro",
		auth:      &gcpAuth{tokenSource: &mockTokenSource{err: context.DeadlineExceeded}},
		httpClient: &http.Client{},
	}
	_, err := p.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error when auth fails")
	}
	if !strings.Contains(err.Error(), "auth") {
		t.Errorf("error = %q, should mention auth", err.Error())
	}
}

func TestLastSegment(t *testing.T) {
	tests := []struct {
		in   string
		sep  string
		want string
	}{
		{"publishers/google/models/gemini-2.5-pro", "/models/", "gemini-2.5-pro"},
		{"publishers/anthropic/models/claude-opus-4-6@20251101", "/models/", "claude-opus-4-6@20251101"},
		{"nothing-here", "/models/", ""},
	}
	for _, tt := range tests {
		if got := lastSegment(tt.in, tt.sep); got != tt.want {
			t.Errorf("lastSegment(%q, %q) = %q, want %q", tt.in, tt.sep, got, tt.want)
		}
	}
}
