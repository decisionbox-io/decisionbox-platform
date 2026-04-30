package ollama

import (
	"context"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestOllamaProvider_Registered(t *testing.T) {
	meta, ok := gollm.GetProviderMeta("ollama")
	if !ok {
		t.Fatal("ollama not registered")
	}
	if meta.Name == "" {
		t.Error("missing provider name")
	}
	if meta.Description == "" {
		t.Error("missing description")
	}

	if len(meta.Models) == 0 {
		t.Fatal("catalog is empty")
	}
	if meta.DefaultMaxOutputTokens != 16384 {
		t.Errorf("DefaultMaxOutputTokens = %d, want 16384", meta.DefaultMaxOutputTokens)
	}

	// Per-model caps for the biggest Qwen / Gemma / DeepSeek / Meta models.
	cases := []struct {
		model string
		want  int
	}{
		// Qwen 3.6 / 3.5 — hosted-Plus-tier 64K generation.
		{"qwen3.6", 65536},
		{"qwen3.6:latest", 65536},
		{"qwen3.6:35b-a3b", 65536},
		{"qwen3.5", 65536},
		{"qwen3.5:122b", 65536},

		// Qwen 3 — recommended 32K output.
		{"qwen3", 32768},
		{"qwen3:32b", 32768},
		{"qwen3:235b", 32768},

		// DeepSeek R1 reasoning — 32K default.
		{"deepseek-r1", 32768},
		{"deepseek-r1:70b", 32768},
		{"deepseek-r1:671b", 32768},

		// Qwen 2.5 — 16K.
		{"qwen2.5:72b", 16384},
		{"qwen2.5-coder:32b", 16384},

		// DeepSeek V3 — 16K.
		{"deepseek-v3", 16384},

		// Gemma 3 — 16K on the big 27B.
		{"gemma3:27b", 16384},

		// Llama 4 / Llama 3.x — 8K practical generation cap.
		{"llama4:maverick", 8192},
		{"llama3.3:70b", 8192},
		{"llama3.1:8b", 8192}, // documented in docs/guides/configuring-llm.md
		{"llama3.1:405b", 8192},
		{"llama3.2:3b", 8192},
		{"llama3:8b", 8192},

		// Gemma 2 — 8K context.
		{"gemma2:9b", 8192},
		{"gemma2:27b", 8192},

		// Fallback to _default for unrecognized model tags.
		{"some-unknown-model:42b", 16384},
		{"qwen2.5:0.5b", 16384}, // small Qwen not in the focused list — falls to default
	}
	for _, tc := range cases {
		if got := gollm.GetMaxOutputTokens("ollama", tc.model); got != tc.want {
			t.Errorf("GetMaxOutputTokens(ollama, %q) = %d, want %d", tc.model, got, tc.want)
		}
	}
}

func TestOllamaProvider_ConfigFields(t *testing.T) {
	meta, _ := gollm.GetProviderMeta("ollama")

	keys := make(map[string]bool)
	for _, f := range meta.ConfigFields {
		keys[f.Key] = true
	}
	if !keys["host"] {
		t.Error("missing host config field")
	}
	if !keys["model"] {
		t.Error("missing model config field")
	}
	// Should NOT have api_key — local models
	if keys["api_key"] {
		t.Error("ollama should not have api_key field")
	}
}

// TestOllamaProvider_CatalogModels_Dispatchable confirms that the
// JSON shape exposed via /api/v1/providers/llm marks every Ollama
// catalog row as a real model — even though entries leave Wire blank
// (single-wire provider, no dispatch switch). The handler's
// `Dispatchable` derivation must not assume "no wire == not
// dispatchable".
func TestOllamaProvider_CatalogModels_HaveBlankWire(t *testing.T) {
	meta, _ := gollm.GetProviderMeta("ollama")
	for _, m := range meta.CatalogModels() {
		// Ollama models intentionally leave Wire as "" (WireUnknown)
		// because Chat() does not dispatch on wire — the provider
		// has only one path through ollamaapi.
		if m.Wire != "" {
			t.Errorf("%s: Wire = %q, expected empty for Ollama", m.ID, m.Wire)
		}
	}
}

func TestOllamaProvider_ZeroPricing(t *testing.T) {
	meta, _ := gollm.GetProviderMeta("ollama")
	// Ollama runs locally — every catalog entry must carry zero
	// pricing so the dashboard's cost estimate row stays at $0.
	for _, e := range meta.Models {
		if e.Pricing.InputPerMillion != 0 || e.Pricing.OutputPerMillion != 0 {
			t.Errorf("%s: pricing should be zero, got in=%f out=%f",
				e.ID, e.Pricing.InputPerMillion, e.Pricing.OutputPerMillion)
		}
	}
}

func TestOllamaProvider_FactoryMissingModel(t *testing.T) {
	_, err := gollm.NewProvider("ollama", gollm.ProviderConfig{
		"host": "http://localhost:11434",
	})
	if err == nil {
		t.Error("should error without model")
	}
}

func TestOllamaProvider_FactorySuccess(t *testing.T) {
	p, err := gollm.NewProvider("ollama", gollm.ProviderConfig{
		"host":  "http://localhost:11434",
		"model": "qwen2.5:0.5b",
	})
	if err != nil {
		t.Fatalf("factory error: %v", err)
	}
	if p == nil {
		t.Error("provider should not be nil")
	}
}

func TestOllamaProvider_DefaultHost(t *testing.T) {
	p, err := gollm.NewProvider("ollama", gollm.ProviderConfig{
		"model": "qwen2.5:0.5b",
	})
	if err != nil {
		t.Fatalf("factory error: %v", err)
	}
	if p == nil {
		t.Error("provider should not be nil")
	}
}

func TestOllamaProvider_Validate_ServerDown(t *testing.T) {
	p, err := NewOllamaProvider("http://localhost:1", "qwen2.5:0.5b")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(context.Background()); err == nil {
		t.Error("Validate should fail when server is unreachable")
	}
}

func TestNewOllamaProvider_InvalidURL(t *testing.T) {
	_, err := NewOllamaProvider("://invalid", "model")
	if err == nil {
		t.Error("should error on invalid URL")
	}
}
