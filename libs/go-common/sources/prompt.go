package sources

import (
	"fmt"
	"sort"
	"strings"
)

// FormatPromptSection renders chunks as a markdown section suitable for
// injection into LLM prompts. Returns an empty string when no chunks are
// supplied so callers can safely concatenate the result.
//
// Output format:
//
//	## Project Knowledge
//	The following excerpts are from documents the user attached to this project.
//	Treat them as authoritative context; cite them when relevant as [s1], [s2], etc.
//
//	[s1] handbook.pdf (page 12) — score 0.87
//	<chunk text>
//
//	[s2] glossary.md — score 0.74
//	<chunk text>
func FormatPromptSection(chunks []Chunk) string {
	if len(chunks) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Project Knowledge\n")
	b.WriteString("The following excerpts are from documents the user attached to this project.\n")
	b.WriteString("Treat them as authoritative context; cite them when relevant as [s1], [s2], etc.\n\n")

	for i, c := range chunks {
		fmt.Fprintf(&b, "[s%d] %s%s — score %.2f\n", i+1, c.SourceName, formatMetadata(c.Metadata), c.Score)
		b.WriteString(strings.TrimSpace(c.Text))
		b.WriteString("\n\n")
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
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
