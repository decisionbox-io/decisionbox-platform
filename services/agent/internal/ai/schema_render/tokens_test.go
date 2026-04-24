package schema_render

import (
	"strings"
	"testing"
)

func TestCharCounter_Empty(t *testing.T) {
	c := CharCounter{}
	if c.CountTokens("") != 0 {
		t.Error("empty string should yield 0 tokens")
	}
}

func TestCharCounter_DefaultsSane(t *testing.T) {
	// 100 chars with defaults: 100 / 4 * 1.25 = 31.25 → 31.
	c := CharCounter{}
	got := c.CountTokens(strings.Repeat("a", 100))
	if got < 28 || got > 35 {
		t.Errorf("100-char default count = %d, expected ~31", got)
	}
}

func TestCharCounter_CustomRatios(t *testing.T) {
	c := CharCounter{CharsPerToken: 2, SafetyFactor: 1}
	got := c.CountTokens(strings.Repeat("a", 100)) // 100/2*1 = 50
	if got != 50 {
		t.Errorf("got %d, want 50", got)
	}
	c = CharCounter{CharsPerToken: 4, SafetyFactor: 2}
	got = c.CountTokens(strings.Repeat("a", 100)) // 100/4*2 = 50
	if got != 50 {
		t.Errorf("got %d, want 50", got)
	}
}

func TestCharCounter_RoundHalfUp(t *testing.T) {
	c := CharCounter{CharsPerToken: 2, SafetyFactor: 1}
	got := c.CountTokens("abc") // 3/2*1 = 1.5 → 2
	if got != 2 {
		t.Errorf("round-half-up: got %d, want 2", got)
	}
}

func TestCharCounter_NegativeFieldsFallback(t *testing.T) {
	c := CharCounter{CharsPerToken: -5, SafetyFactor: -1}
	got := c.CountTokens("hello")
	if got <= 0 {
		t.Errorf("got %d, want positive (fallback to defaults)", got)
	}
}

func TestTiktokenCounter_CL100KBase_BasicShape(t *testing.T) {
	tc, err := NewTiktokenCounter("cl100k_base")
	if err != nil {
		t.Skipf("tiktoken vocab unavailable in this env: %v", err)
	}
	if tc.CountTokens("") != 0 {
		t.Error("empty should be 0")
	}

	// Known-ish: "hello world" is 2 tokens in cl100k_base.
	n := tc.CountTokens("hello world")
	if n != 2 {
		t.Errorf("hello world → %d tokens, expected 2", n)
	}

	// Larger input should produce proportionally larger count.
	text := strings.Repeat("a simple sentence of text. ", 100)
	if tc.CountTokens(text) < 100 {
		t.Errorf("long text token count too small: %d", tc.CountTokens(text))
	}
}

func TestTiktokenCounter_InvalidEncoding(t *testing.T) {
	_, err := NewTiktokenCounter("nonexistent_encoding_xyz")
	if err == nil {
		t.Fatal("invalid encoding should error")
	}
}

func TestTiktokenCounter_EmptyStringEncodingFallsBackToDefault(t *testing.T) {
	tc, err := NewTiktokenCounter("")
	if err != nil {
		t.Skipf("tiktoken vocab unavailable: %v", err)
	}
	if tc.CountTokens("x") <= 0 {
		t.Error("empty encoding arg should fall back to cl100k_base")
	}
}

func TestTiktokenCounter_CacheReusesInstance(t *testing.T) {
	// NewTiktokenCounter called twice should hit the cache — this is
	// visible through the sync.Map so we assert via direct lookup.
	if _, err := NewTiktokenCounter("cl100k_base"); err != nil {
		t.Skipf("tiktoken vocab unavailable: %v", err)
	}
	if _, err := NewTiktokenCounter("cl100k_base"); err != nil {
		t.Skipf("tiktoken vocab unavailable: %v", err)
	}
	tiktokenCacheMu.Lock()
	defer tiktokenCacheMu.Unlock()
	if _, ok := tiktokenCache["cl100k_base"]; !ok {
		t.Error("cache should contain cl100k_base after two loads")
	}
}
