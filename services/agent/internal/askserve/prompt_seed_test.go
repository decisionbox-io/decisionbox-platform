package askserve

import (
	"strings"
	"testing"
)

func TestWriteSeedSection(t *testing.T) {
	t.Run("nil seed is a no-op", func(t *testing.T) {
		var b strings.Builder
		writeSeedSection(&b, nil, sourceShapes{})
		if b.Len() != 0 {
			t.Fatalf("expected empty output, got %q", b.String())
		}
	})

	t.Run("empty label and text is a no-op", func(t *testing.T) {
		var b strings.Builder
		writeSeedSection(&b, &SeedContext{Type: "insight", ID: "i1"}, sourceShapes{})
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
		}, sourceShapes{})
		out := b.String()
		for _, want := range []string{"FOCUS", "not instructions", `insight: "Churn spike in EU"`, `details: "Users in the EU`} {
			if !strings.Contains(out, want) {
				t.Fatalf("expected output to contain %q, got:\n%s", want, out)
			}
		}
	})

	t.Run("unknown type falls back to item", func(t *testing.T) {
		var b strings.Builder
		writeSeedSection(&b, &SeedContext{Type: "bogus", Label: "Something"}, sourceShapes{})
		if !strings.Contains(b.String(), `item: "Something"`) {
			t.Fatalf("expected 'item' fallback, got:\n%s", b.String())
		}
	})

	t.Run("recommendation type is honored", func(t *testing.T) {
		var b strings.Builder
		writeSeedSection(&b, &SeedContext{Type: "recommendation", Label: "Lower EU price"}, sourceShapes{})
		if !strings.Contains(b.String(), `recommendation: "Lower EU price"`) {
			t.Fatalf("expected recommendation label, got:\n%s", b.String())
		}
	})

	t.Run("long text is capped", func(t *testing.T) {
		var b strings.Builder
		long := strings.Repeat("x", seedPromptTextCap+500)
		writeSeedSection(&b, &SeedContext{Type: "insight", Label: "L", Text: long}, sourceShapes{})
		out := b.String()
		if !strings.Contains(out, "…") {
			t.Fatalf("expected truncation ellipsis, got len=%d", len(out))
		}
		// The rendered details must not carry the full oversized text: the run of
		// filler is capped at seedPromptTextCap. (Assert the run length directly
		// rather than counting 'x' across the whole prompt — the FOCUS prose can
		// legitimately contain the letter x, e.g. "explicitly".)
		if !strings.Contains(out, strings.Repeat("x", seedPromptTextCap)) {
			t.Fatalf("expected a capped run of %d chars", seedPromptTextCap)
		}
		if strings.Contains(out, strings.Repeat("x", seedPromptTextCap+1)) {
			t.Fatalf("capped run exceeds %d chars", seedPromptTextCap)
		}
	})
}

// TestWriteSeedSection_AnchorsInTheShapeTheTurnCanReach. The FOCUS block is
// written BEFORE the datasources section, so on a seeded turn it is the first
// thing the model reads about how to scope — and "scope your retrieval and SQL
// to ... its tables" is two false statements to a turn whose source has no
// tables and rejects SQL. Ordering mitigates it (the concrete per-source block
// lands later) but does not make it true.
func TestWriteSeedSection_AnchorsInTheShapeTheTurnCanReach(t *testing.T) {
	seed := &SeedContext{Type: "insight", ID: "i1", Label: "Sessions fell after the redesign"}

	t.Run("a turn that reaches only tables is unchanged", func(t *testing.T) {
		var b strings.Builder
		writeSeedSection(&b, seed, sourceShapes{})
		const want = "scope your retrieval and SQL to this insight — its tables, metric, and segment — instead of answering globally"
		if !strings.Contains(b.String(), want) {
			t.Fatalf("the SQL-only anchor drifted; want %q in:\n%s", want, b.String())
		}
	})

	t.Run("a turn that can reach a cube names both shapes", func(t *testing.T) {
		var b strings.Builder
		writeSeedSection(&b, seed, sourceShapes{anyCube: true})
		out := b.String()
		const want = "scope your retrieval and queries to this insight — its tables or cube items, its metric, and its segment — instead of answering globally"
		if !strings.Contains(out, want) {
			t.Fatalf("want %q in:\n%s", want, out)
		}
		// The two words that were false must be gone from the instruction.
		if strings.Contains(out, "retrieval and SQL") {
			t.Error("a cube turn must not be told to scope its SQL")
		}
		if strings.Contains(out, "its tables, metric, and segment") {
			t.Error("a cube turn must not be anchored to tables alone")
		}
	})

	t.Run("an all-cube turn is anchored the same way as a mixed one", func(t *testing.T) {
		// allCube implies anyCube; the anchor must not depend on reading only
		// the narrower flag.
		var b strings.Builder
		writeSeedSection(&b, seed, sourceShapes{anyCube: true, allCube: true})
		if !strings.Contains(b.String(), "its tables or cube items") {
			t.Fatalf("all-cube turn lost the shape-aware anchor:\n%s", b.String())
		}
	})
}
