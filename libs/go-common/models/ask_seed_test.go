package models

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAskSessionSeedPromptBlock(t *testing.T) {
	var nilSeed *AskSessionSeed
	if nilSeed.PromptBlock() != "" {
		t.Fatalf("nil seed should render empty block")
	}
	if (&AskSessionSeed{Type: "insight"}).PromptBlock() != "" {
		t.Fatalf("seed with no label/text should render empty block")
	}
	got := (&AskSessionSeed{Type: "insight", Label: "Churn spike", Text: "EU churn 2x"}).PromptBlock()
	for _, want := range []string{"anchored to it", "insight is titled: Churn spike", "Details: EU churn 2x"} {
		if !strings.Contains(got, want) {
			t.Fatalf("block missing %q:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, " ") {
		t.Fatalf("block should start with a leading space for clean appending: %q", got)
	}
	// Unknown type degrades to a generic noun rather than leaking the raw type.
	if strings.Contains((&AskSessionSeed{Type: "bogus", Label: "X"}).PromptBlock(), "bogus") {
		t.Fatalf("unknown type should not be echoed verbatim")
	}
}

// Legacy rows (no seed_context) must decode with a nil SeedContext, and a seeded
// session must round-trip its seed.
func TestAskSessionSeedJSONRoundTrip(t *testing.T) {
	var legacy AskSession
	if err := json.Unmarshal([]byte(`{"id":"s1","project_id":"p1"}`), &legacy); err != nil {
		t.Fatalf("legacy decode: %v", err)
	}
	if legacy.SeedContext != nil {
		t.Fatalf("legacy row should decode to nil seed, got %+v", legacy.SeedContext)
	}

	seeded := AskSession{ID: "s2", ProjectID: "p1", SeedContext: &AskSessionSeed{Type: "insight", ID: "i1", Label: "L", Text: "T"}}
	raw, err := json.Marshal(seeded)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back AskSession
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.SeedContext == nil || back.SeedContext.ID != "i1" {
		t.Fatalf("seed did not round-trip: %+v", back.SeedContext)
	}
}
