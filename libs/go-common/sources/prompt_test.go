package sources

import (
	"strings"
	"testing"
)

func TestFormatPromptSection_Empty(t *testing.T) {
	if got := FormatPromptSection(nil); got != "" {
		t.Errorf("FormatPromptSection(nil) = %q, want empty", got)
	}
	if got := FormatPromptSection([]Chunk{}); got != "" {
		t.Errorf("FormatPromptSection([]) = %q, want empty", got)
	}
}

func TestFormatPromptSection_SingleChunk(t *testing.T) {
	chunks := []Chunk{
		{
			SourceID:   "uuid-1",
			SourceName: "handbook.pdf",
			SourceType: "pdf",
			Text:       "  Player retention is measured weekly.  ",
			Score:      0.873,
			Metadata:   map[string]string{"page": "12"},
		},
	}

	got := FormatPromptSection(chunks)

	wantSubstrings := []string{
		"## Project Knowledge",
		"[s1] handbook.pdf (page 12) — score 0.87",
		"Player retention is measured weekly.",
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(got, s) {
			t.Errorf("FormatPromptSection output missing %q\nfull output:\n%s", s, got)
		}
	}

	// Text should be trimmed (no leading/trailing whitespace from input).
	if strings.Contains(got, "  Player") {
		t.Error("chunk text was not trimmed")
	}
}

func TestFormatPromptSection_MultipleChunksDeterministicOrder(t *testing.T) {
	chunks := []Chunk{
		{SourceName: "a.md", Text: "alpha", Score: 0.9, Metadata: map[string]string{"section": "intro"}},
		{SourceName: "b.xlsx", Text: "beta", Score: 0.8, Metadata: map[string]string{"sheet": "Q1"}},
		{SourceName: "c.txt", Text: "gamma", Score: 0.7},
	}

	got := FormatPromptSection(chunks)

	idxA := strings.Index(got, "[s1]")
	idxB := strings.Index(got, "[s2]")
	idxC := strings.Index(got, "[s3]")
	if idxA < 0 || idxB < 0 || idxC < 0 {
		t.Fatalf("missing citation labels in output:\n%s", got)
	}
	if idxA >= idxB || idxB >= idxC {
		t.Error("chunk order in output does not match input order")
	}
}

func TestFormatPromptSection_MetadataKeysSorted(t *testing.T) {
	chunks := []Chunk{
		{
			SourceName: "doc.pdf",
			Text:       "x",
			Metadata:   map[string]string{"sheet": "Q1", "page": "5", "author": "alice"},
		},
	}

	got := FormatPromptSection(chunks)

	// Keys must appear in alphabetical order: author, page, sheet
	wantOrder := "(author alice, page 5, sheet Q1)"
	if !strings.Contains(got, wantOrder) {
		t.Errorf("metadata not in sorted order; want %q in:\n%s", wantOrder, got)
	}
}

func TestFormatPromptSection_NoMetadata(t *testing.T) {
	chunks := []Chunk{{SourceName: "plain.txt", Text: "hello", Score: 0.5}}
	got := FormatPromptSection(chunks)

	// No parenthesized metadata after the filename.
	if strings.Contains(got, "plain.txt (") {
		t.Errorf("expected no metadata parens for chunk without metadata; got:\n%s", got)
	}
	if !strings.Contains(got, "[s1] plain.txt — score 0.50") {
		t.Errorf("expected '[s1] plain.txt — score 0.50' in output:\n%s", got)
	}
}
