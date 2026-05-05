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

func TestFormatPromptSection_DocumentLabel(t *testing.T) {
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
		"[s1] [Document: handbook.pdf, p12] — score 0.87",
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

func TestFormatPromptSection_NoteLabel(t *testing.T) {
	chunks := []Chunk{
		{
			SourceID:   "note-1",
			SourceName: "Deprecated tables",
			SourceType: SourceTypeNote,
			Text:       "Use events_v2 instead of events_v1.",
			Score:      1.0,
		},
	}

	got := FormatPromptSection(chunks)

	if !strings.Contains(got, "[s1] [Note: Deprecated tables] — score 1.00") {
		t.Errorf("expected note label; got:\n%s", got)
	}
	if strings.Contains(got, "[Document:") {
		t.Errorf("note must not render as a document; got:\n%s", got)
	}
	if !strings.Contains(got, "Treat notes as operator guidance") {
		t.Errorf("expected note/document guidance in heading; got:\n%s", got)
	}
}

func TestFormatPromptSection_MixedNotesAndDocuments(t *testing.T) {
	chunks := []Chunk{
		{SourceName: "Pinned guidance", SourceType: SourceTypeNote, Text: "Pinned text", Score: 1.0},
		{SourceName: "Other note", SourceType: SourceTypeNote, Text: "More text", Score: 0.99},
		{SourceName: "doc.pdf", SourceType: "pdf", Text: "ref text", Score: 0.81, Metadata: map[string]string{"page": "3"}},
	}

	got := FormatPromptSection(chunks)

	idxNote1 := strings.Index(got, "[Note: Pinned guidance]")
	idxNote2 := strings.Index(got, "[Note: Other note]")
	idxDoc := strings.Index(got, "[Document: doc.pdf, p3]")
	if idxNote1 < 0 || idxNote2 < 0 || idxDoc < 0 {
		t.Fatalf("expected all three labels; got:\n%s", got)
	}
	if !(idxNote1 < idxNote2 && idxNote2 < idxDoc) {
		t.Fatalf("expected order Pinned → Other → doc.pdf; got positions %d, %d, %d", idxNote1, idxNote2, idxDoc)
	}
}

func TestFormatPromptSection_MultipleChunksDeterministicOrder(t *testing.T) {
	chunks := []Chunk{
		{SourceName: "a.md", SourceType: "md", Text: "alpha", Score: 0.9, Metadata: map[string]string{"section": "intro"}},
		{SourceName: "b.xlsx", SourceType: "xlsx", Text: "beta", Score: 0.8, Metadata: map[string]string{"sheet": "Q1"}},
		{SourceName: "c.txt", SourceType: "txt", Text: "gamma", Score: 0.7},
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

func TestFormatPromptSection_DocumentMetadataKeysSorted(t *testing.T) {
	chunks := []Chunk{
		{
			SourceName: "doc.pdf",
			SourceType: "pdf",
			Text:       "x",
			Metadata:   map[string]string{"sheet": "Q1", "page": "5", "author": "alice"},
		},
	}

	got := FormatPromptSection(chunks)

	// Keys must appear in alphabetical order; "page" renders as "p<value>".
	wantInline := "[Document: doc.pdf, author alice, p5, sheet Q1]"
	if !strings.Contains(got, wantInline) {
		t.Errorf("metadata not in sorted order; want %q in:\n%s", wantInline, got)
	}
}

func TestFormatPromptSection_DocumentNoMetadata(t *testing.T) {
	chunks := []Chunk{{SourceName: "plain.txt", SourceType: "txt", Text: "hello", Score: 0.5}}
	got := FormatPromptSection(chunks)

	if !strings.Contains(got, "[s1] [Document: plain.txt] — score 0.50") {
		t.Errorf("expected '[Document: plain.txt]' label without metadata; got:\n%s", got)
	}
	if strings.Contains(got, "plain.txt,") {
		t.Errorf("no metadata should render no comma; got:\n%s", got)
	}
}
