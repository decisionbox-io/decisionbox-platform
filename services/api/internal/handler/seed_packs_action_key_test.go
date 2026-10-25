package handler

import (
	"strings"
	"testing"
)

// TestSeedPacks_NeverAskForThinkingKey: the exploration action's free-text
// key is "rationale". Claude Opus 5.5 refuses (stop_reason "refusal", no
// content) a conversation whose prompt asks for a JSON field named
// "thinking", so no built-in pack may teach that key.
func TestSeedPacks_NeverAskForThinkingKey(t *testing.T) {
	entries, err := seedFS.ReadDir("seed")
	if err != nil {
		t.Fatalf("listing seed packs: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no seed packs embedded")
	}
	for _, e := range entries {
		data, err := seedFS.ReadFile("seed/" + e.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		// Inside the JSON file the prompt's quotes are escaped.
		if strings.Contains(string(data), `\"thinking\":`) {
			t.Errorf("%s teaches the \"thinking\" action key; use \"rationale\"", e.Name())
		}
	}
}
