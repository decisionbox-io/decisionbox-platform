package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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
