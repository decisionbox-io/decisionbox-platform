package ollama

import (
	"context"
	"errors"
	"testing"

	ollamaapi "github.com/ollama/ollama/api"
)

func TestResolveModelInfo_NumCtxUnset_ReportsUnknown(t *testing.T) {
	// With num_ctx unset, Chat uses Ollama's server default (not the GGUF
	// architectural max), so the effective window is unknown here — even though
	// /api/show reports a large context_length. Show must not even be consulted.
	mock := &mockOllamaClient{
		showResp: &ollamaapi.ShowResponse{ModelInfo: map[string]any{"qwen3.context_length": float64(262144)}},
	}
	p := newMockOllamaProvider(mock, "qwen3:32b") // numCtx == 0
	caps, err := p.ResolveModelInfo(context.Background(), "qwen3:32b")
	if err != nil {
		t.Fatalf("ResolveModelInfo err: %v", err)
	}
	if caps.MaxInputTokens != 0 {
		t.Fatalf("num_ctx unset must report unknown (0), got %d", caps.MaxInputTokens)
	}
	if mock.lastShowReq != nil {
		t.Fatalf("Show must not be called when num_ctx is unset")
	}
}

func TestResolveModelInfo_ClampsToNumCtx(t *testing.T) {
	mock := &mockOllamaClient{
		showResp: &ollamaapi.ShowResponse{ModelInfo: map[string]any{"qwen3.context_length": float64(262144)}},
	}
	// Operator set num_ctx below the architectural context → the effective
	// request window is num_ctx (Chat sends at most that).
	p := &OllamaProvider{client: mock, model: "qwen3:32b", numCtx: 8192}
	caps, err := p.ResolveModelInfo(context.Background(), "qwen3:32b")
	if err != nil {
		t.Fatalf("ResolveModelInfo err: %v", err)
	}
	if caps.MaxInputTokens != 8192 {
		t.Fatalf("num_ctx below architectural: window = %d, want 8192", caps.MaxInputTokens)
	}
	if caps.MaxOutputTokens != 0 {
		t.Fatalf("Ollama reports no output cap; got %d", caps.MaxOutputTokens)
	}

	// num_ctx above architectural → the smaller architectural value wins.
	p = &OllamaProvider{client: mock, model: "qwen3:32b", numCtx: 999999}
	caps, _ = p.ResolveModelInfo(context.Background(), "qwen3:32b")
	if caps.MaxInputTokens != 262144 {
		t.Fatalf("num_ctx above architectural: window = %d, want 262144", caps.MaxInputTokens)
	}

	// num_ctx set but architectural unknown → report num_ctx.
	mock2 := &mockOllamaClient{showResp: &ollamaapi.ShowResponse{ModelInfo: map[string]any{}}}
	p = &OllamaProvider{client: mock2, model: "m", numCtx: 4096}
	caps, _ = p.ResolveModelInfo(context.Background(), "m")
	if caps.MaxInputTokens != 4096 {
		t.Fatalf("architectural unknown: window = %d, want num_ctx 4096", caps.MaxInputTokens)
	}
}

func TestResolveModelInfo_ShowError(t *testing.T) {
	mock := &mockOllamaClient{showErr: errors.New("connection refused")}
	p := &OllamaProvider{client: mock, model: "m", numCtx: 8192} // num_ctx set → Show is consulted
	if _, err := p.ResolveModelInfo(context.Background(), "m"); err == nil {
		t.Fatal("expected the Show error to surface")
	}
}

func TestListModels_EnrichedWithWindow(t *testing.T) {
	mock := &mockOllamaClient{
		listResp: &ollamaapi.ListResponse{Models: []ollamaapi.ListModelResponse{{Name: "qwen3:32b"}}},
		showResp: &ollamaapi.ShowResponse{ModelInfo: map[string]any{"qwen3.context_length": float64(262144)}},
	}
	// num_ctx set → the enriched window is min(architectural, num_ctx).
	p := &OllamaProvider{client: mock, model: "qwen3:32b", numCtx: 131072}
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels err: %v", err)
	}
	if len(models) != 1 || models[0].MaxInputTokens != 131072 {
		t.Fatalf("got %+v, want one row window=131072", models)
	}
}

func TestContextLengthFromModelInfo_Types(t *testing.T) {
	if got := contextLengthFromModelInfo(map[string]any{"llama.context_length": int(8192)}); got != 8192 {
		t.Fatalf("int value: got %d", got)
	}
	if got := contextLengthFromModelInfo(map[string]any{"llama.context_length": int64(4096)}); got != 4096 {
		t.Fatalf("int64 value: got %d", got)
	}
	if got := contextLengthFromModelInfo(nil); got != 0 {
		t.Fatalf("nil map: got %d", got)
	}
}
