package askserve

import (
	"strings"
	"testing"
)

func TestWriteSeedSection(t *testing.T) {
	t.Run("nil seed is a no-op", func(t *testing.T) {
		var b strings.Builder
		writeSeedSection(&b, nil)
		if b.Len() != 0 {
			t.Fatalf("expected empty output, got %q", b.String())
		}
	})

	t.Run("empty label and text is a no-op", func(t *testing.T) {
		var b strings.Builder
		writeSeedSection(&b, &SeedContext{Type: "insight", ID: "i1"})
		if b.Len() != 0 {
			t.Fatalf("expected empty output, got %q", b.String())
		}
	})

	t.Run("insight renders FOCUS with label and details", func(t *testing.T) {
		var b strings.Builder
		writeSeedSection(&b, &SeedContext{
			Type: "insight", ID: "i1",
			Label: "Churn spike in EU",
			Text:  "Users in the EU region churned 2x after the price change.",
		})
		out := b.String()
		for _, want := range []string{"FOCUS", "not instructions", `insight: "Churn spike in EU"`, `details: "Users in the EU`} {
			if !strings.Contains(out, want) {
				t.Fatalf("expected output to contain %q, got:\n%s", want, out)
			}
		}
	})

	t.Run("unknown type falls back to item", func(t *testing.T) {
		var b strings.Builder
		writeSeedSection(&b, &SeedContext{Type: "bogus", Label: "Something"})
		if !strings.Contains(b.String(), `item: "Something"`) {
			t.Fatalf("expected 'item' fallback, got:\n%s", b.String())
		}
	})

	t.Run("recommendation type is honored", func(t *testing.T) {
		var b strings.Builder
		writeSeedSection(&b, &SeedContext{Type: "recommendation", Label: "Lower EU price"})
		if !strings.Contains(b.String(), `recommendation: "Lower EU price"`) {
			t.Fatalf("expected recommendation label, got:\n%s", b.String())
		}
	})

	t.Run("long text is capped", func(t *testing.T) {
		var b strings.Builder
		long := strings.Repeat("x", seedPromptTextCap+500)
		writeSeedSection(&b, &SeedContext{Type: "insight", Label: "L", Text: long})
		out := b.String()
		if !strings.Contains(out, "…") {
			t.Fatalf("expected truncation ellipsis, got len=%d", len(out))
		}
		// The rendered details must not carry the full oversized text.
		if strings.Count(out, "x") > seedPromptTextCap {
			t.Fatalf("expected text capped at %d chars, got %d", seedPromptTextCap, strings.Count(out, "x"))
		}
	})
}
