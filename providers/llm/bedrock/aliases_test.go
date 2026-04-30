package bedrock

import (
	"strings"
	"testing"
)

func TestClaudeAliasesFor_CrossRegionMatrix(t *testing.T) {
	got := claudeAliasesFor("opus-4-7")
	canonical := "anthropic.claude-opus-4-7-v1:0"

	// Canonical must NOT be in the alias list — the catalog row's ID
	// already covers it.
	for _, a := range got {
		if a == canonical {
			t.Fatalf("alias list includes canonical ID %q", canonical)
		}
		if a == "" {
			t.Fatal("empty string in alias list")
		}
	}
	// Every cross-region prefix × every version suffix combination
	// (minus the canonical) plus the bare-prefix variants without
	// -v1:0 must be present.
	for _, prefix := range append([]string{""}, claudeRegionPrefixes...) {
		for _, suffix := range claudeVersionSuffixes {
			id := prefix + "anthropic.claude-opus-4-7" + suffix
			if id == canonical {
				continue
			}
			if !containsAlias(got, id) {
				t.Errorf("missing alias %q", id)
			}
		}
	}
	// Family-only short forms.
	for _, want := range []string{"claude-opus-4-7", "opus-4-7"} {
		if !containsAlias(got, want) {
			t.Errorf("missing family-only alias %q", want)
		}
	}

	// Total count: 7 prefixes (incl. bare "") × 3 suffixes = 21 IDs,
	// minus 1 canonical = 20, plus 2 family-only = 22 unique aliases.
	if len(got) != 22 {
		t.Errorf("got %d aliases, want 22", len(got))
	}

	// Sanity: no duplicates.
	seen := make(map[string]struct{}, len(got))
	for _, a := range got {
		if _, dup := seen[a]; dup {
			t.Errorf("duplicate alias %q", a)
		}
		seen[a] = struct{}{}
	}
}

func TestClaudeAliasesFor_AcrossFamilies(t *testing.T) {
	for _, family := range []string{
		"opus-4-7",
		"opus-4-6",
		"sonnet-4-6",
		"haiku-4-5-20251001",
		"opus-4-20250514",
	} {
		got := claudeAliasesFor(family)
		// Same shape per family.
		if len(got) != 22 {
			t.Errorf("%s: got %d aliases, want 22", family, len(got))
		}
		// Every alias contains the family stem.
		for _, a := range got {
			if !strings.Contains(a, family) && a != family && a != "claude-"+family {
				// "opus-4-7" / "claude-opus-4-7" don't contain
				// "opus-4-7" with the family-only-form rule —
				// allow those two specific forms.
				if a != "claude-"+family && a != family {
					t.Errorf("%s: alias %q has nothing to do with the family", family, a)
				}
			}
		}
	}
}

func TestOpenSourceAliasesFor(t *testing.T) {
	got := openSourceAliasesFor("deepseek.r1-v1:0")
	wants := []string{
		"deepseek.r1",                  // suffix-stripped short form
		"us.deepseek.r1-v1:0",          // cross-region canonical
		"us.deepseek.r1",               // cross-region short
		"eu.deepseek.r1-v1:0",
		"eu.deepseek.r1",
		"apac.deepseek.r1-v1:0",
		"apac.deepseek.r1",
		"global.deepseek.r1-v1:0",
		"global.deepseek.r1",
	}
	for _, w := range wants {
		if !containsAlias(got, w) {
			t.Errorf("missing %q in %v", w, got)
		}
	}
	// Canonical must not appear.
	for _, a := range got {
		if a == "deepseek.r1-v1:0" {
			t.Error("canonical ID leaked into alias list")
		}
	}
}

func TestOpenSourceAliasesFor_NoVersionSuffix(t *testing.T) {
	// "qwen.qwen3-next-80b-a3b" has no -v1:0 / -v1 suffix; the
	// suffix-stripped short form equals the canonical, so we should
	// only emit cross-region prefixes (no separate "short" variant).
	got := openSourceAliasesFor("qwen.qwen3-next-80b-a3b")
	for _, w := range []string{
		"us.qwen.qwen3-next-80b-a3b",
		"eu.qwen.qwen3-next-80b-a3b",
		"apac.qwen.qwen3-next-80b-a3b",
		"global.qwen.qwen3-next-80b-a3b",
	} {
		if !containsAlias(got, w) {
			t.Errorf("missing cross-region alias %q", w)
		}
	}
	// No empty entries / no duplicates.
	seen := map[string]struct{}{}
	for _, a := range got {
		if a == "" {
			t.Fatal("empty alias")
		}
		if _, dup := seen[a]; dup {
			t.Errorf("duplicate %q", a)
		}
		seen[a] = struct{}{}
	}
}

func TestOpenSourceAliasesFor_MalformedID(t *testing.T) {
	// No publisher dot → returns nil.
	if got := openSourceAliasesFor("invalid-no-dot"); got != nil {
		t.Errorf("malformed ID should yield nil aliases, got %v", got)
	}
}

func containsAlias(s []string, e string) bool {
	for _, x := range s {
		if x == e {
			return true
		}
	}
	return false
}
