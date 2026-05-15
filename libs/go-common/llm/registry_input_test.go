package llm

import (
	"sync"
	"testing"
)

// onceInputCatalog is dedicated to the new MaxInputTokens / Encoding
// tests so they coexist with the existing registry_test.go suite
// without sharing a sync.Once.
var onceInputCatalog sync.Once

func registerInputCatalogProvider(t *testing.T) ProviderMeta {
	t.Helper()
	name := "test-input-catalog"
	onceInputCatalog.Do(func() {
		RegisterWithMeta(name, mockFactory, ProviderMeta{
			Name: "Input catalog test",
			Models: []ModelEntry{
				{
					ID:              "claude-opus-4-7",
					Aliases:         []string{"opus-4-7"},
					Wire:            WireAnthropic,
					MaxOutputTokens: 128000,
					MaxInputTokens:  200000,
				},
				{
					ID:              "gpt-5",
					Wire:            WireOpenAICompat,
					MaxOutputTokens: 16384,
					MaxInputTokens:  400000,
					Encoding:        "o200k_base",
				},
				{
					// No MaxInputTokens / Encoding set — falls through to
					// provider DefaultMaxInputTokens / empty.
					ID:              "model-without-window",
					Wire:            WireAnthropic,
					MaxOutputTokens: 8192,
				},
			},
			DefaultMaxOutputTokens: 8192,
			DefaultMaxInputTokens:  64000,
		})
	})
	meta, _ := GetProviderMeta(name)
	return meta
}

// --- MaxInputTokensFor --------------------------------------------

func TestProviderMeta_MaxInputTokensFor_Canonical(t *testing.T) {
	meta := registerInputCatalogProvider(t)
	if got := meta.MaxInputTokensFor("claude-opus-4-7"); got != 200000 {
		t.Fatalf("got %d, want 200000", got)
	}
}

func TestProviderMeta_MaxInputTokensFor_Alias(t *testing.T) {
	meta := registerInputCatalogProvider(t)
	if got := meta.MaxInputTokensFor("opus-4-7"); got != 200000 {
		t.Fatalf("got %d via alias, want 200000", got)
	}
}

func TestProviderMeta_MaxInputTokensFor_FallsThroughEntryToProviderDefault(t *testing.T) {
	meta := registerInputCatalogProvider(t)
	// Entry exists but no MaxInputTokens declared → provider default.
	if got := meta.MaxInputTokensFor("model-without-window"); got != 64000 {
		t.Fatalf("got %d, want 64000 (provider DefaultMaxInputTokens)", got)
	}
}

func TestProviderMeta_MaxInputTokensFor_UnknownFallsToProviderDefault(t *testing.T) {
	meta := registerInputCatalogProvider(t)
	// Catalog miss → provider DefaultMaxInputTokens.
	if got := meta.MaxInputTokensFor("totally-unknown"); got != 64000 {
		t.Fatalf("got %d, want 64000 (provider default)", got)
	}
}

func TestProviderMeta_MaxInputTokensFor_GlobalFallback(t *testing.T) {
	// Provider with no DefaultMaxInputTokens — package fallback.
	name := "test-no-input-default"
	RegisterWithMeta(name, mockFactory, ProviderMeta{
		Name: "no input default",
		Models: []ModelEntry{
			{ID: "fixed", Wire: WireAnthropic, MaxOutputTokens: 1234, MaxInputTokens: 5678},
		},
	})
	meta, _ := GetProviderMeta(name)
	// Hit returns the per-model value.
	if got := meta.MaxInputTokensFor("fixed"); got != 5678 {
		t.Fatalf("hit got %d, want 5678", got)
	}
	// Miss falls through to package DefaultMaxInputTokens.
	if got := meta.MaxInputTokensFor("missing"); got != DefaultMaxInputTokens {
		t.Fatalf("miss got %d, want %d (package default)", got, DefaultMaxInputTokens)
	}
}

// --- GetMaxInputTokens ---------------------------------------------

func TestGetMaxInputTokens_KnownProvider(t *testing.T) {
	registerInputCatalogProvider(t)
	if got := GetMaxInputTokens("test-input-catalog", "claude-opus-4-7"); got != 200000 {
		t.Fatalf("got %d, want 200000", got)
	}
}

func TestGetMaxInputTokens_UnknownProvider(t *testing.T) {
	// Unknown provider should still return DefaultMaxInputTokens, not
	// 0 or a panic — callers expect a usable budget number.
	if got := GetMaxInputTokens("really-not-a-provider", "anything"); got != DefaultMaxInputTokens {
		t.Fatalf("unknown provider got %d, want %d", got, DefaultMaxInputTokens)
	}
}

// --- EncodingFor / GetEncoding -------------------------------------

func TestProviderMeta_EncodingFor_Hit(t *testing.T) {
	meta := registerInputCatalogProvider(t)
	if got := meta.EncodingFor("gpt-5"); got != "o200k_base" {
		t.Fatalf("got %q, want %q", got, "o200k_base")
	}
}

func TestProviderMeta_EncodingFor_NoneDeclared(t *testing.T) {
	meta := registerInputCatalogProvider(t)
	if got := meta.EncodingFor("claude-opus-4-7"); got != "" {
		t.Fatalf("got %q, want empty (Anthropic models have no encoding declared)", got)
	}
}

func TestProviderMeta_EncodingFor_Miss(t *testing.T) {
	meta := registerInputCatalogProvider(t)
	if got := meta.EncodingFor("unknown-model"); got != "" {
		t.Fatalf("got %q, want empty for unknown model", got)
	}
}

func TestGetEncoding_UnknownProvider(t *testing.T) {
	if got := GetEncoding("really-not-a-provider", "gpt-5"); got != "" {
		t.Fatalf("got %q, want empty for unknown provider", got)
	}
}

func TestGetEncoding_KnownProvider(t *testing.T) {
	registerInputCatalogProvider(t)
	if got := GetEncoding("test-input-catalog", "gpt-5"); got != "o200k_base" {
		t.Fatalf("got %q, want o200k_base", got)
	}
}

// --- CatalogModels serialises MaxInputTokens -----------------------

func TestProviderMeta_CatalogModels_IncludesMaxInputTokens(t *testing.T) {
	meta := registerInputCatalogProvider(t)
	infos := meta.CatalogModels()
	gotGPT5 := false
	for _, m := range infos {
		if m.ID == "gpt-5" {
			gotGPT5 = true
			if m.MaxInputTokens != 400000 {
				t.Errorf("gpt-5 MaxInputTokens = %d, want 400000", m.MaxInputTokens)
			}
		}
	}
	if !gotGPT5 {
		t.Fatal("gpt-5 missing from catalog output")
	}
}
