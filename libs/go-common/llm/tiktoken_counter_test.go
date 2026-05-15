package llm

import (
	"context"
	"strings"
	"testing"
)

// --- NewTiktokenCounter construction ------------------------------

func TestNewTiktokenCounter_KnownEncoding(t *testing.T) {
	c, err := NewTiktokenCounter("o200k_base")
	if err != nil {
		t.Fatalf("NewTiktokenCounter(o200k_base) errored: %v", err)
	}
	if c == nil {
		t.Fatal("nil counter for valid encoding")
	}
	if !IsExact(c) {
		t.Fatal("tiktoken counter must register as exact (not ApproximateCounter)")
	}
}

func TestNewTiktokenCounter_LegacyEncodingStillWorks(t *testing.T) {
	// cl100k_base is the GPT-3.5 / GPT-4 era encoding. Even if our
	// shipped catalog has moved everything to o200k_base, the
	// constructor must still accept legacy names so historical
	// snapshots / custom catalog overrides keep working.
	c, err := NewTiktokenCounter("cl100k_base")
	if err != nil {
		t.Fatalf("NewTiktokenCounter(cl100k_base) errored: %v", err)
	}
	if c == nil {
		t.Fatal("nil counter for cl100k_base")
	}
}

func TestNewTiktokenCounter_UnknownEncodingErrors(t *testing.T) {
	_, err := NewTiktokenCounter("definitely-not-a-real-encoding")
	if err == nil {
		t.Fatal("expected error for unknown encoding name")
	}
}

func TestNewTiktokenCounter_EmptyNameErrors(t *testing.T) {
	_, err := NewTiktokenCounter("")
	if err == nil {
		t.Fatal("expected error for empty encoding name")
	}
}

// --- Count behavior -----------------------------------------------

func TestTiktokenCounter_Empty(t *testing.T) {
	c, err := NewTiktokenCounter("o200k_base")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	got, err := c.Count(context.Background(), "")
	if err != nil {
		t.Fatalf("Count(\"\") errored: %v", err)
	}
	if got != 0 {
		t.Fatalf("Count(\"\") = %d, want 0", got)
	}
}

func TestTiktokenCounter_NonEmpty(t *testing.T) {
	c, _ := NewTiktokenCounter("o200k_base")
	got, err := c.Count(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Count errored: %v", err)
	}
	if got <= 0 {
		t.Fatalf("Count(\"hello world\") = %d, want > 0", got)
	}
}

func TestTiktokenCounter_GrowsWithText(t *testing.T) {
	c, _ := NewTiktokenCounter("o200k_base")
	short, _ := c.Count(context.Background(), "hi")
	long, _ := c.Count(context.Background(), "the quick brown fox jumps over the lazy dog repeatedly")
	if long <= short {
		t.Fatalf("long=%d short=%d — long must be strictly greater", long, short)
	}
}

func TestTiktokenCounter_MultiByteContent(t *testing.T) {
	// CJK + emoji are denser than 4 chars/token in BPE; the counter
	// must handle them without error and return a positive count.
	c, _ := NewTiktokenCounter("o200k_base")
	got, err := c.Count(context.Background(), "你好世界 🌍 こんにちは")
	if err != nil {
		t.Fatalf("Count multibyte errored: %v", err)
	}
	if got <= 0 {
		t.Fatalf("Count multibyte = %d, want > 0", got)
	}
}

func TestTiktokenCounter_HonoursCancelledContext(t *testing.T) {
	c, _ := NewTiktokenCounter("o200k_base")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Count(ctx, "anything")
	if err == nil {
		t.Fatal("Count with cancelled ctx returned nil error")
	}
}

func TestTiktokenCounter_Deterministic(t *testing.T) {
	// Same input twice must produce the same count — guards against
	// an accidentally stateful tiktoken instance bleeding state
	// across calls.
	c, _ := NewTiktokenCounter("o200k_base")
	a, _ := c.Count(context.Background(), "DecisionBox token budget walk")
	b, _ := c.Count(context.Background(), "DecisionBox token budget walk")
	if a != b {
		t.Fatalf("non-deterministic counter: a=%d b=%d", a, b)
	}
}

// --- Encoding cache -----------------------------------------------

func TestLoadTiktokenEncoding_CachesSameInstance(t *testing.T) {
	// loadTiktokenEncoding is the cache path; two calls with the
	// same name must return the same *tiktoken.Tiktoken pointer,
	// otherwise each /ask request would re-parse the BPE table.
	a, err := loadTiktokenEncoding("o200k_base")
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	b, err := loadTiktokenEncoding("o200k_base")
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if a != b {
		t.Fatal("cache returned different instances for the same encoding name")
	}
}

func TestLoadTiktokenEncoding_UnknownNameErrors(t *testing.T) {
	_, err := loadTiktokenEncoding("not-a-real-encoding-12345")
	if err == nil {
		t.Fatal("expected error for unknown encoding")
	}
	if !strings.Contains(err.Error(), "encoding") &&
		!strings.Contains(err.Error(), "not found") &&
		!strings.Contains(err.Error(), "unknown") {
		// Don't pin the exact phrasing — tiktoken-go owns it.
		t.Logf("unknown-encoding err: %v (informational)", err)
	}
}

// --- IsExact integration ------------------------------------------

func TestTiktokenCounter_ReportsAsExactViaIsExact(t *testing.T) {
	c, _ := NewTiktokenCounter("o200k_base")
	if !IsExact(c) {
		t.Fatal("tiktoken-backed counter should be exact (not ApproximateCounter)")
	}
}
