package litellm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const modelInfoBody = `{"data":[
  {"model_name":"gpt-4o","model_info":{"max_input_tokens":128000,"max_output_tokens":16384,"max_tokens":128000}},
  {"model_name":"only-max-tokens","model_info":{"max_input_tokens":32000,"max_output_tokens":null,"max_tokens":8192}},
  {"model_name":"unknown-model","model_info":{"max_input_tokens":null,"max_output_tokens":null,"max_tokens":null}}
]}`

func newModelInfoServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/model/info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(modelInfoBody))
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[
			  {"id":"gpt-4o","max_output_tokens":16384},
			  {"id":"unknown-model"}
			]}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestResolveModelInfo(t *testing.T) {
	srv := newModelInfoServer(t)
	defer srv.Close()
	p := NewLiteLLMProvider("k", "gpt-4o", srv.URL+"/v1", nil)

	caps, err := p.ResolveModelInfo(context.Background(), "gpt-4o")
	if err != nil {
		t.Fatalf("ResolveModelInfo err: %v", err)
	}
	if caps.MaxInputTokens != 128000 || caps.MaxOutputTokens != 16384 {
		t.Fatalf("gpt-4o caps in=%d out=%d, want 128000/16384", caps.MaxInputTokens, caps.MaxOutputTokens)
	}

	// max_output_tokens null → falls back to max_tokens for the output cap.
	caps, _ = p.ResolveModelInfo(context.Background(), "only-max-tokens")
	if caps.MaxInputTokens != 32000 || caps.MaxOutputTokens != 8192 {
		t.Fatalf("only-max-tokens caps in=%d out=%d, want 32000/8192", caps.MaxInputTokens, caps.MaxOutputTokens)
	}

	// All null → zero (unknown), no error.
	caps, err = p.ResolveModelInfo(context.Background(), "unknown-model")
	if err != nil || caps.MaxInputTokens != 0 || caps.MaxOutputTokens != 0 {
		t.Fatalf("unknown-model caps in=%d out=%d err=%v, want 0/0/nil", caps.MaxInputTokens, caps.MaxOutputTokens, err)
	}

	// A model not in /model/info → zero (unknown), no error.
	caps, err = p.ResolveModelInfo(context.Background(), "absent")
	if err != nil || caps.MaxInputTokens != 0 {
		t.Fatalf("absent model caps=%+v err=%v, want zero/nil", caps, err)
	}
}

func TestListModels_EnrichedWithWindow(t *testing.T) {
	srv := newModelInfoServer(t)
	defer srv.Close()
	p := NewLiteLLMProvider("k", "gpt-4o", srv.URL+"/v1", nil)

	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels err: %v", err)
	}
	byID := map[string]struct{ in, out int }{}
	for _, m := range models {
		byID[m.ID] = struct{ in, out int }{m.MaxInputTokens, m.MaxOutputTokens}
	}
	// gpt-4o: /v1/models gives out=16384, /model/info adds in=128000.
	if got := byID["gpt-4o"]; got.in != 128000 || got.out != 16384 {
		t.Fatalf("gpt-4o listed in=%d out=%d, want 128000/16384", got.in, got.out)
	}
	// unknown-model: no numbers anywhere → 0/0, still listed.
	if got, ok := byID["unknown-model"]; !ok || got.in != 0 || got.out != 0 {
		t.Fatalf("unknown-model listed=%v %+v, want present 0/0", ok, got)
	}
}

func TestListModels_ModelInfoFailureDoesNotBlock(t *testing.T) {
	// /model/info 500s, /v1/models is fine → listing still succeeds with the
	// /v1/models-supplied output cap and no window.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"m1","max_output_tokens":4096}]}`))
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	p := NewLiteLLMProvider("k", "m1", srv.URL+"/v1", nil)

	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels must not fail when /model/info errors: %v", err)
	}
	if len(models) != 1 || models[0].MaxOutputTokens != 4096 || models[0].MaxInputTokens != 0 {
		t.Fatalf("got %+v, want one row out=4096 in=0", models)
	}
}
