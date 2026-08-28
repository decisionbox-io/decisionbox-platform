package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestResolveModelInfo_FromGatewayModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
		  {"id":"vllm-model","max_model_len":262144},
		  {"id":"gw-model","max_input_tokens":128000,"max_output_tokens":8192},
		  {"id":"plain-model"}
		]}`))
	}))
	defer srv.Close()
	p := NewOpenAIProvider("k", "vllm-model", srv.URL, 0)

	// max_model_len → context window.
	caps, err := p.ResolveModelInfo(context.Background(), "vllm-model")
	if err != nil {
		t.Fatalf("ResolveModelInfo err: %v", err)
	}
	if caps.MaxInputTokens != 262144 {
		t.Fatalf("vllm-model window = %d, want 262144", caps.MaxInputTokens)
	}

	// Explicit input/output fields.
	caps, _ = p.ResolveModelInfo(context.Background(), "gw-model")
	if caps.MaxInputTokens != 128000 || caps.MaxOutputTokens != 8192 {
		t.Fatalf("gw-model caps in=%d out=%d, want 128000/8192", caps.MaxInputTokens, caps.MaxOutputTokens)
	}

	// A model reporting nothing → zero (unknown), no error.
	caps, err = p.ResolveModelInfo(context.Background(), "plain-model")
	if err != nil || caps.MaxInputTokens != 0 || caps.MaxOutputTokens != 0 {
		t.Fatalf("plain-model caps=%+v err=%v, want zero/nil", caps, err)
	}

	// A model absent from the list → zero (unknown), no error.
	caps, err = p.ResolveModelInfo(context.Background(), "absent")
	if err != nil || caps.MaxInputTokens != 0 {
		t.Fatalf("absent caps=%+v err=%v, want zero/nil", caps, err)
	}
}

func TestResolveModelInfo_MatchesCatalogAlias(t *testing.T) {
	// Find a catalogued model that has at least one alias, so we can simulate a
	// gateway that lists the alias (with max_model_len) for a project saved as
	// the canonical id.
	meta, ok := gollm.GetProviderMeta("openai")
	if !ok {
		t.Skip("openai provider not registered")
	}
	var canonical, alias string
	for _, e := range meta.Models {
		if len(e.Aliases) > 0 {
			canonical, alias = e.ID, e.Aliases[0]
			break
		}
	}
	if canonical == "" {
		t.Skip("no catalogued model with an alias to exercise alias matching")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Gateway lists the ALIAS with a (reduced) window; canonical id absent.
		_, _ = w.Write([]byte(`{"data":[{"id":"` + alias + `","max_model_len":40000,"max_output_tokens":4096}]}`))
	}))
	defer srv.Close()
	p := NewOpenAIProvider("k", canonical, srv.URL, 0)

	caps, err := p.ResolveModelInfo(context.Background(), canonical)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if caps.MaxInputTokens != 40000 || caps.MaxOutputTokens != 4096 {
		t.Fatalf("alias match caps in=%d out=%d, want 40000/4096 (from alias %q for canonical %q)",
			caps.MaxInputTokens, caps.MaxOutputTokens, alias, canonical)
	}
}
