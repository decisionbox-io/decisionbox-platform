package ollama

import (
	"context"
	"errors"
	"testing"

	ollamaapi "github.com/ollama/ollama/api"
)

func TestResolveModelInfo_ContextLength(t *testing.T) {
	mock := &mockOllamaClient{
		showResp: &ollamaapi.ShowResponse{
			ModelInfo: map[string]any{
				"general.architecture": "qwen3",
				"qwen3.context_length": float64(262144),
				"qwen3.block_count":    float64(64),
			},
		},
	}
	p := newMockOllamaProvider(mock, "qwen3:32b")

	caps, err := p.ResolveModelInfo(context.Background(), "qwen3:32b")
	if err != nil {
		t.Fatalf("ResolveModelInfo err: %v", err)
	}
	if caps.MaxInputTokens != 262144 {
		t.Fatalf("got window=%d, want 262144", caps.MaxInputTokens)
	}
	if caps.MaxOutputTokens != 0 {
		t.Fatalf("Ollama reports no output cap; got %d", caps.MaxOutputTokens)
	}
	if mock.lastShowReq == nil || mock.lastShowReq.Model != "qwen3:32b" {
		t.Fatalf("Show called with wrong model: %+v", mock.lastShowReq)
	}
}

func TestResolveModelInfo_NoContextLength(t *testing.T) {
	mock := &mockOllamaClient{showResp: &ollamaapi.ShowResponse{ModelInfo: map[string]any{"general.architecture": "x"}}}
	p := newMockOllamaProvider(mock, "m")
	caps, err := p.ResolveModelInfo(context.Background(), "m")
	if err != nil || caps.MaxInputTokens != 0 {
		t.Fatalf("missing context_length must yield 0/nil, got %d err=%v", caps.MaxInputTokens, err)
	}
}

func TestResolveModelInfo_ShowError(t *testing.T) {
	mock := &mockOllamaClient{showErr: errors.New("connection refused")}
	p := newMockOllamaProvider(mock, "m")
	if _, err := p.ResolveModelInfo(context.Background(), "m"); err == nil {
		t.Fatal("expected the Show error to surface")
	}
}

func TestListModels_EnrichedWithWindow(t *testing.T) {
	mock := &mockOllamaClient{
		listResp: &ollamaapi.ListResponse{Models: []ollamaapi.ListModelResponse{{Name: "qwen3:32b"}}},
		showResp: &ollamaapi.ShowResponse{ModelInfo: map[string]any{"qwen3.context_length": float64(131072)}},
	}
	p := newMockOllamaProvider(mock, "qwen3:32b")
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
