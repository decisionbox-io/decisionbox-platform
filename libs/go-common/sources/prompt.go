package sources

import (
	"fmt"
	"sort"
	"strings"
)

// SourceTypeNote is the canonical Source.Type value for inline notes —
// short user-authored guidance that travels through the same chunker /
// embedder / vector index as documents but renders with a different
// prompt label so the LLM can distinguish operator guidance from
// reference material.
const SourceTypeNote = "note"

// FormatPromptSection renders chunks as a markdown section suitable for
// injection into LLM prompts. Returns an empty string when no chunks are
// supplied so callers can safely concatenate the result.
//
// Each chunk's source line is labelled [Note: …] for notes and
// [Document: …, p<page>] for documents so the LLM treats notes as
// operator instructions and documents as reference material. Chunk order
// is preserved — the retriever already orders pinned notes first, then
// small-corpus notes, then RAG hits.
//
// Output format:
//
//	## Project Knowledge
//	The following excerpts are from notes and documents the user attached to this project.
//	Treat notes as operator guidance and documents as reference material;
//	cite them when relevant as [s1], [s2], etc.
//
//	[s1] [Note: Deprecated tables] — score 1.00
//	<note text>
//
//	[s2] [Document: handbook.pdf, p12] — score 0.87
//	<document chunk text>
func FormatPromptSection(chunks []Chunk) string {
	if len(chunks) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Project Knowledge\n")
	b.WriteString("The following excerpts are from notes and documents the user attached to this project.\n")
	b.WriteString("Treat notes as operator guidance and documents as reference material; cite them when relevant as [s1], [s2], etc.\n\n")

	for i, c := range chunks {
		fmt.Fprintf(&b, "[s%d] %s — score %.2f\n", i+1, formatSourceLabel(c), c.Score)
		b.WriteString(strings.TrimSpace(c.Text))
		b.WriteString("\n\n")
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// formatSourceLabel renders the per-chunk source identifier inline with
// the citation marker. Notes get [Note: <name>]; documents get
// [Document: <name>, <metadata>]. Documents without metadata fall back
// to [Document: <name>].
func formatSourceLabel(c Chunk) string {
	if c.SourceType == SourceTypeNote {
		return "[Note: " + c.SourceName + "]"
	}
	if meta := formatMetadataInline(c.Metadata); meta != "" {
		return "[Document: " + c.SourceName + ", " + meta + "]"
	}
	return "[Document: " + c.SourceName + "]"
}

// formatMetadataInline renders metadata as "key value, key value" with
// keys in deterministic order. Used inside the [Document: ...] label.
func formatMetadataInline(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		// "page 12" reads naturally; the renderer doesn't try to be
		// clever about pluralization or units.
		if k == "page" {
			parts = append(parts, "p"+m[k])
			continue
		}
		parts = append(parts, k+" "+m[k])
	}
	return strings.Join(parts, ", ")
}

// formatMetadata renders metadata key/value pairs as a parenthesized suffix.
// Returns empty string when there is no metadata. Keys are rendered in
// deterministic order so the output is stable across runs (helps prompt caching).
func formatMetadata(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %s", k, m[k]))
	}
	return " (" + strings.Join(parts, ", ") + ")"
}
