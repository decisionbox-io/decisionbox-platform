package ollama

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// TestOllamaCatalog_Resolution exercises the full registration → registry
// resolution path: the ollama provider's init() registers the catalog, so
// gollm.GetMax*Tokens("ollama", …) reads it. Covers the newly added
// families — including the exact 31B quant tag a user pulls, which must
// resolve to the 256k tier — and confirms that tags the catalog does not
// list fall back to the provider defaults (exact match only, by design).
func TestOllamaCatalog_Resolution(t *testing.T) {
	cases := []struct {
		model     string
		wantOut   int
		wantInput int
	}{
		// Gemma 4 small tier (128k) — reasoning-capable, cap raised
		// to the uncatalogued default so thinking + answer fit.
		{"gemma4", ollamaDefaultMaxOutputTokens, ctx128K},
		{"gemma4:latest", ollamaDefaultMaxOutputTokens, ctx128K},
		{"gemma4:e4b", ollamaDefaultMaxOutputTokens, ctx128K},
		// Gemma 4 26B/31B tier (256k), including a quant tag users pull.
		// Reasoning-capable; same cap.
		{"gemma4:31b", ollamaDefaultMaxOutputTokens, ctx256K},
		{"gemma4:31b-it-bf16", ollamaDefaultMaxOutputTokens, ctx256K},
		{"gemma4:26b-it-q4_K_M", ollamaDefaultMaxOutputTokens, ctx256K},
		// Gemma 3 — reasoning-capable, raised cap.
		{"gemma3", ollamaDefaultMaxOutputTokens, ctx128K},
		{"gemma3:27b", ollamaDefaultMaxOutputTokens, ctx128K},
		// Qwen 3 + DeepSeek R1 — reasoning models, raised caps.
		{"qwen3", ollamaDefaultMaxOutputTokens, ctx128K},
		{"qwen3:32b", ollamaDefaultMaxOutputTokens, ctx128K},
		{"deepseek-r1", ollamaDefaultMaxOutputTokens, ctx128K},
		{"deepseek-r1:32b", ollamaDefaultMaxOutputTokens, ctx128K},
		// Non-reasoning rows keep their existing caps.
		{"qwen3-coder:30b", 16384, ctx256K},
		{"qwen2:7b", 16384, ctx128K},
		{"gpt-oss:20b", 16384, ctx128K},
		{"phi4", 16384, ctx16K},
		{"mistral:7b", 16384, ctx32K},
		{"mistral-small:24b", 16384, ctx128K},
		{"codellama:13b", 16384, ctx16K},
		{"llama2:7b", 4096, ctx4K},
		{"tinyllama", 2048, ctx2K},
		// Uncatalogued tags (an unlisted quant/format, or an unknown
		// model) fall back to the provider defaults.
		{"gemma4:31b-mlx", ollamaDefaultMaxOutputTokens, ctx128K},
		{"totally-unknown:1b", ollamaDefaultMaxOutputTokens, ctx128K},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			if got := gollm.GetMaxOutputTokens("ollama", tc.model); got != tc.wantOut {
				t.Errorf("GetMaxOutputTokens(%q) = %d, want %d", tc.model, got, tc.wantOut)
			}
			if got := gollm.GetMaxInputTokens("ollama", tc.model); got != tc.wantInput {
				t.Errorf("GetMaxInputTokens(%q) = %d, want %d", tc.model, got, tc.wantInput)
			}
		})
	}
}

// TestOllamaCatalog_Defaults pins the provider-level fallbacks used for
// models the catalog does not list to 128k for both output and context.
func TestOllamaCatalog_Defaults(t *testing.T) {
	meta, ok := gollm.GetProviderMeta("ollama")
	if !ok {
		t.Fatal("ollama provider not registered")
	}
	if meta.DefaultMaxOutputTokens != ollamaDefaultMaxOutputTokens {
		t.Errorf("DefaultMaxOutputTokens = %d, want %d", meta.DefaultMaxOutputTokens, ollamaDefaultMaxOutputTokens)
	}
	if meta.DefaultMaxInputTokens != ctx128K {
		t.Errorf("DefaultMaxInputTokens = %d, want %d", meta.DefaultMaxInputTokens, ctx128K)
	}
	// Both defaults must be 128k (131072) per the catalog requirement.
	if ollamaDefaultMaxOutputTokens != 131072 || ctx128K != 131072 {
		t.Errorf("both 128k defaults must equal 131072; got out=%d in=%d", ollamaDefaultMaxOutputTokens, ctx128K)
	}
}

// TestOllamaCatalog_Invariants guards every catalog entry: a positive
// output cap (also enforced by registry seed validation at init), a
// positive context window, and output never exceeding context — a model
// cannot generate beyond its own window. Catches a typo in any future edit.
func TestOllamaCatalog_Invariants(t *testing.T) {
	for _, e := range buildOllamaCatalog() {
		if e.MaxOutputTokens <= 0 {
			t.Errorf("%s: MaxOutputTokens = %d, want > 0", e.ID, e.MaxOutputTokens)
		}
		if e.MaxInputTokens <= 0 {
			t.Errorf("%s: MaxInputTokens = %d, want > 0", e.ID, e.MaxInputTokens)
		}
		if e.MaxOutputTokens > e.MaxInputTokens {
			t.Errorf("%s: MaxOutputTokens (%d) exceeds MaxInputTokens (%d)", e.ID, e.MaxOutputTokens, e.MaxInputTokens)
		}
	}
}
