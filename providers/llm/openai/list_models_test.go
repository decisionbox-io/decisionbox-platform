package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListModels_Success(t *testing.T) {
	var receivedAuth string
	var receivedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]string{
				{"id": "gpt-4o", "owned_by": "openai"},
				{"id": "gpt-4o-mini", "owned_by": "openai"},
				{"id": "text-embedding-3-large", "owned_by": "openai"},
			},
		})
	}))
	defer server.Close()

	p := NewOpenAIProvider("test-key", "gpt-4o", server.URL, 0)
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedAuth != "Bearer test-key" {
		t.Errorf("auth = %q", receivedAuth)
	}
	if receivedPath != "/models" {
		t.Errorf("path = %q, want /models", receivedPath)
	}
	if len(models) != 3 {
		t.Fatalf("len = %d, want 3", len(models))
	}
	if models[0].ID != "gpt-4o" {
		t.Errorf("id = %q", models[0].ID)
	}
}

func TestListModels_WindowFromGatewayFields(t *testing.T) {
	// vLLM exposes max_model_len; some gateways add max_input_tokens /
	// max_output_tokens. Verify each is surfaced onto the RemoteModel.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
		  {"id":"vllm-model","max_model_len":262144},
		  {"id":"gw-model","max_input_tokens":128000,"max_output_tokens":8192},
		  {"id":"plain-model"}
		]}`))
	}))
	defer server.Close()

	p := NewOpenAIProvider("k", "m", server.URL, 0)
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byID := map[string]struct{ in, out int }{}
	for _, m := range models {
		byID[m.ID] = struct{ in, out int }{m.MaxInputTokens, m.MaxOutputTokens}
	}
	if got := byID["vllm-model"]; got.in != 262144 {
		t.Errorf("vllm-model window = %d, want 262144 (from max_model_len)", got.in)
	}
	if got := byID["gw-model"]; got.in != 128000 || got.out != 8192 {
		t.Errorf("gw-model in=%d out=%d, want 128000/8192", got.in, got.out)
	}
	if got := byID["plain-model"]; got.in != 0 || got.out != 0 {
		t.Errorf("plain-model in=%d out=%d, want 0/0", got.in, got.out)
	}
}

func TestListModels_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer server.Close()

	p := NewOpenAIProvider("bad", "gpt-4o", server.URL, 0)
	_, err := p.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error %q should mention status", err.Error())
	}
}

func TestListModels_MalformedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()

	p := NewOpenAIProvider("k", "m", server.URL, 0)
	_, err := p.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestListModels_ServerDown(t *testing.T) {
	p := NewOpenAIProvider("k", "m", "http://127.0.0.1:1", 0)
	_, err := p.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestListModels_EmptyData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	p := NewOpenAIProvider("k", "m", server.URL, 0)
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 0 {
		t.Errorf("len = %d, want 0", len(models))
	}
}
