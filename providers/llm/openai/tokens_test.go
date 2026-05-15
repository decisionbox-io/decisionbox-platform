package openai

import (
	"context"
	"errors"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// --- TokenCounter construction ------------------------------------

func TestOpenAIProvider_TokenCounter_BuildsForKnownModel(t *testing.T) {
	p := NewOpenAIProvider("test-key", "gpt-4o", "", 10*time.Second)
	c, err := p.TokenCounter(context.Background(), "gpt-4o")
	if err != nil {
		t.Fatalf("TokenCounter errored: %v", err)
	}
	if c == nil {
		t.Fatal("TokenCounter returned nil counter without error")
	}
	if !gollm.IsExact(c) {
		t.Fatal("openai TokenCounter must register as exact")
	}
}

func TestOpenAIProvider_TokenCounter_UsesFallbackEncodingForUnknownModel(t *testing.T) {
	// An unknown model (e.g. a brand-new snapshot) still gets a counter
	// using the o200k_base fallback — we never want to return nil
	// counter for the active provider's "best guess".
	p := NewOpenAIProvider("test-key", "gpt-5", "", 10*time.Second)
	c, err := p.TokenCounter(context.Background(), "no-such-model-xyz")
	if err != nil {
		t.Fatalf("TokenCounter errored: %v", err)
	}
	if c == nil {
		t.Fatal("nil counter for unknown model; expected o200k_base fallback")
	}
}

func TestOpenAIProvider_TokenCounter_EmptyModelFallsToProviderModel(t *testing.T) {
	// When the caller passes empty, the counter should use the
	// provider's configured model. We verify by counting twice and
	// asserting the same result; the actual values come from
	// tiktoken so the assertion stays implementation-agnostic.
	p := NewOpenAIProvider("test-key", "gpt-4o", "", 10*time.Second)
	c1, err := p.TokenCounter(context.Background(), "")
	if err != nil {
		t.Fatalf("TokenCounter(\"\") errored: %v", err)
	}
	c2, err := p.TokenCounter(context.Background(), "gpt-4o")
	if err != nil {
		t.Fatalf("TokenCounter(\"gpt-4o\") errored: %v", err)
	}
	a, _ := c1.Count(context.Background(), "hello world")
	b, _ := c2.Count(context.Background(), "hello world")
	if a != b {
		t.Fatalf("empty-model counter %d != explicit-model counter %d", a, b)
	}
}

// --- Count behaviour ----------------------------------------------

func TestOpenAITokenCounter_Empty(t *testing.T) {
	p := NewOpenAIProvider("test-key", "gpt-4o", "", 10*time.Second)
	c, _ := p.TokenCounter(context.Background(), "gpt-4o")
	got, err := c.Count(context.Background(), "")
	if err != nil {
		t.Fatalf("Count(\"\") errored: %v", err)
	}
	if got != 0 {
		t.Fatalf("Count(\"\") = %d, want 0", got)
	}
}

func TestOpenAITokenCounter_SingleWord(t *testing.T) {
	p := NewOpenAIProvider("test-key", "gpt-4o", "", 10*time.Second)
	c, _ := p.TokenCounter(context.Background(), "gpt-4o")
	got, err := c.Count(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Count errored: %v", err)
	}
	if got <= 0 {
		t.Fatalf("Count(\"hello\") = %d, want > 0", got)
	}
}

func TestOpenAITokenCounter_GrowsWithText(t *testing.T) {
	p := NewOpenAIProvider("test-key", "gpt-4o", "", 10*time.Second)
	c, _ := p.TokenCounter(context.Background(), "gpt-4o")
	short, _ := c.Count(context.Background(), "hello")
	longer, _ := c.Count(context.Background(), "hello world how are you today")
	if longer <= short {
		t.Fatalf("longer text counted as %d, expected > short text %d", longer, short)
	}
}

func TestOpenAITokenCounter_MultiByteContent(t *testing.T) {
	// CJK / emoji tokenize denser per rune than English. The counter
	// must handle them without error and return a positive count.
	p := NewOpenAIProvider("test-key", "gpt-4o", "", 10*time.Second)
	c, _ := p.TokenCounter(context.Background(), "gpt-4o")
	got, err := c.Count(context.Background(), "你好世界 🌍")
	if err != nil {
		t.Fatalf("Count multibyte errored: %v", err)
	}
	if got <= 0 {
		t.Fatalf("Count CJK+emoji = %d, want > 0", got)
	}
}

func TestOpenAITokenCounter_HonoursCancelledContext(t *testing.T) {
	p := NewOpenAIProvider("test-key", "gpt-4o", "", 10*time.Second)
	c, _ := p.TokenCounter(context.Background(), "gpt-4o")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Count(ctx, "hello")
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Count with cancelled ctx returned err=%v, want context.Canceled", err)
	}
}

func TestOpenAITokenCounter_DeterministicSameInput(t *testing.T) {
	// Same input twice must produce the same count. Guards against a
	// stateful tiktoken instance accidentally caching the prior call's
	// state into the next.
	p := NewOpenAIProvider("test-key", "gpt-4o", "", 10*time.Second)
	c, _ := p.TokenCounter(context.Background(), "gpt-4o")
	a, _ := c.Count(context.Background(), "DecisionBox token budget walk")
	b, _ := c.Count(context.Background(), "DecisionBox token budget walk")
	if a != b {
		t.Fatalf("non-deterministic counter: a=%d b=%d", a, b)
	}
}

// --- Canonical-host routing ----------------------------------------

func TestOpenAIProvider_TokenCounter_UsesApproximateForCustomBaseURL(t *testing.T) {
	// Self-hosted OpenAI-compatible proxy: tokenizer is unknown,
	// tiktoken would over- or under-count. Counter must drop to
	// ApproximateCounter so the budget walk picks the wider 15%
	// safety margin instead of falsely claiming 5% exactness.
	p := NewOpenAIProvider("test-key", "gpt-4o", "https://my-llm-proxy.example.com/v1", 10*time.Second)
	c, err := p.TokenCounter(context.Background(), "gpt-4o")
	if err != nil {
		t.Fatalf("TokenCounter errored: %v", err)
	}
	if _, ok := c.(gollm.ApproximateCounter); !ok {
		t.Fatalf("got %T, want ApproximateCounter for custom base_url", c)
	}
	if gollm.IsExact(c) {
		t.Fatal("custom-base_url counter must NOT register as exact")
	}
}

func TestOpenAIProvider_TokenCounter_RealOpenAIBaseURLStaysExact(t *testing.T) {
	// Default base URL ("") → canonical OpenAI host → tiktoken.
	p := NewOpenAIProvider("test-key", "gpt-4o", "", 10*time.Second)
	c, err := p.TokenCounter(context.Background(), "gpt-4o")
	if err != nil {
		t.Fatalf("TokenCounter errored: %v", err)
	}
	if !gollm.IsExact(c) {
		t.Fatal("default-base_url counter must register as exact (tiktoken)")
	}
}

func TestOpenAIProvider_TokenCounter_ExplicitCanonicalOpenAIBaseURL(t *testing.T) {
	// Explicit api.openai.com URL → still canonical → tiktoken.
	p := NewOpenAIProvider("test-key", "gpt-4o", "https://api.openai.com/v1", 10*time.Second)
	c, err := p.TokenCounter(context.Background(), "gpt-4o")
	if err != nil {
		t.Fatalf("TokenCounter errored: %v", err)
	}
	if !gollm.IsExact(c) {
		t.Fatal("api.openai.com base_url must register as exact")
	}
}

func TestIsCanonicalOpenAIHost(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"", true},                                   // default → api.openai.com
		{"https://api.openai.com/v1", true},          // explicit canonical
		{"https://api.openai.com", true},             // no path
		{"http://api.openai.com/v1", true},           // http scheme
		{"api.openai.com", true},                     // schemeless canonical
		{"api.openai.com/v1", true},                  // schemeless canonical with path
		{"my-proxy.example.com", false},              // schemeless custom
		{"https://my-proxy.example.com/v1", false},   // self-hosted
		{"https://oai-proxy.internal.net/v1", false}, // private
		{"https://api.openai.com.attacker.tld/v1", false}, // suffix attack
		{"not-a-url", false},                         // malformed (single token)
		{"http://[::1]:%ZZ/x", false},                // truly unparseable URL (bad escape in port)
	}
	for _, c := range cases {
		got := isCanonicalOpenAIHost(c.url)
		if got != c.want {
			t.Errorf("isCanonicalOpenAIHost(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}
