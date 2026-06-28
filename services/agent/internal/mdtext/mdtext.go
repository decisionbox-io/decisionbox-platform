// Package mdtext reduces the small GitHub-Flavored Markdown subset that the
// analysis LLM authors in an insight/recommendation description down to clean
// plain text.
//
// The agent stores two fields per description: `description_md` keeps the
// authored Markdown for the dashboard to render, and `description` holds the
// plain reduction produced here — the raw, formatting-free text that API
// consumers, list/preview UIs, and the embedding text all read. Keeping the
// plain field a faithful reduction of the Markdown guarantees the two never
// drift.
//
// The reducer targets exactly the supported subset (emphasis, small
// sub-headings, bulleted/numbered lists, simple tables, inline code,
// blockquotes, links, strikethrough). It is intentionally a line-oriented
// transform rather than a full Markdown parser: the input space is small and
// platform-controlled, so a dependency-free reducer is enough and avoids
// pulling a new library through the license allow-list. Plain text with no
// Markdown passes through unchanged.
package mdtext

import (
	"regexp"
	"strings"
)

var (
	// Inline emphasis, in order: bold+italic, bold, italic, then strikethrough
	// and inline code. Bold is unwrapped before italic so `**x**` is not
	// mangled by the single-marker italic pass.
	reBoldItalicAsterisk = regexp.MustCompile(`\*\*\*([^*]+)\*\*\*`)
	reBoldAsterisk       = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reBoldUnderscore     = regexp.MustCompile(`__([^_]+)__`)
	reItalicAsterisk     = regexp.MustCompile(`\*([^*]+)\*`)
	// Underscore italics only at word boundaries (GFM disables intra-word `_`
	// emphasis) so prose containing an identifier like `a_b_c` is left intact.
	reItalicUnderscore = regexp.MustCompile(`(^|[^\w])_([^_]+)_([^\w]|$)`)
	reStrikethrough    = regexp.MustCompile(`~~([^~]+)~~`)
	reInlineCode         = regexp.MustCompile("`([^`]+)`")

	// Links and images. Images reduce to their alt text, links to their label;
	// the URL is dropped (descriptions render links as plain text — no
	// navigation — so the URL has no place in the plain reduction either). The
	// URL group tolerates one level of balanced parens (e.g. `alert(1)`) so a
	// stray `)` is not left behind.
	reImage = regexp.MustCompile(`!\[([^\]]*)\]\((?:[^()]|\([^()]*\))*\)`)
	reLink  = regexp.MustCompile(`\[([^\]]*)\]\((?:[^()]|\([^()]*\))*\)`)

	// Block-level markers stripped from the start of a line.
	reHeading    = regexp.MustCompile(`^\s{0,3}#{1,6}\s+`)
	reBlockquote = regexp.MustCompile(`^\s{0,3}>\s?`)
	reBullet     = regexp.MustCompile(`^(\s*)[-*+]\s+`)
	reOrdered    = regexp.MustCompile(`^(\s*)\d+[.)]\s+`)

	// A horizontal rule (`---`, `***`, `___`) or a GFM table separator row
	// (`|---|:--:|`) on its own line — both carry no text and are dropped.
	// RE2 has no backreferences, so each rule char is its own alternation.
	reHorizontalRule  = regexp.MustCompile(`^\s{0,3}(?:-\s*){3,}$|^\s{0,3}(?:\*\s*){3,}$|^\s{0,3}(?:_\s*){3,}$`)
	reTableSeparator  = regexp.MustCompile(`^\s*\|?[\s:|-]*-[\s:|-]*\|?\s*$`)
	reMultipleSpaces  = regexp.MustCompile(`[ \t]{2,}`)
	reMultipleNewline = regexp.MustCompile(`\n{3,}`)
)

// ToPlainText converts the supported Markdown subset in md to plain text.
// Plain input (no Markdown syntax) is returned with only whitespace
// normalization. The result never contains stray Markdown markers (`*`, `#`,
// backticks, table pipes/separators, list bullets).
func ToPlainText(md string) string {
	if md == "" {
		return ""
	}

	// Normalize line endings so the line-oriented pass is uniform.
	md = strings.ReplaceAll(md, "\r\n", "\n")
	md = strings.ReplaceAll(md, "\r", "\n")

	lines := strings.Split(md, "\n")
	out := make([]string, 0, len(lines))
	inFence := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Code fences: drop the ``` marker lines, keep the inner content as-is.
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}

		// Drop separator-only lines (horizontal rules, table separator rows).
		if reHorizontalRule.MatchString(line) || reTableSeparator.MatchString(trimmed) {
			continue
		}

		// Strip leading block markers (heading hashes, blockquote, list bullet,
		// ordered-list number). List markers are replaced with nothing — the
		// item text alone reads cleanly in plain form.
		line = reHeading.ReplaceAllString(line, "")
		line = reBlockquote.ReplaceAllString(line, "")
		line = reBullet.ReplaceAllString(line, "$1")
		line = reOrdered.ReplaceAllString(line, "$1")

		// Table content rows: split on the pipe, trim each cell, rejoin with a
		// readable separator. A leading/trailing pipe yields empty edge cells
		// that are dropped.
		if isTableRow(line) {
			line = flattenTableRow(line)
		}

		// Inline replacements.
		line = reImage.ReplaceAllString(line, "$1")
		line = reLink.ReplaceAllString(line, "$1")
		line = reBoldItalicAsterisk.ReplaceAllString(line, "$1")
		line = reBoldAsterisk.ReplaceAllString(line, "$1")
		line = reBoldUnderscore.ReplaceAllString(line, "$1")
		line = reItalicAsterisk.ReplaceAllString(line, "$1")
		line = reItalicUnderscore.ReplaceAllString(line, "$1$2$3")
		line = reStrikethrough.ReplaceAllString(line, "$1")
		line = reInlineCode.ReplaceAllString(line, "$1")

		line = reMultipleSpaces.ReplaceAllString(line, " ")
		out = append(out, strings.TrimRight(line, " \t"))
	}

	result := strings.Join(out, "\n")
	// Collapse runs of 3+ newlines (created when block markers leave blank
	// lines) to a single blank line, then trim surrounding whitespace.
	result = reMultipleNewline.ReplaceAllString(result, "\n\n")
	return strings.TrimSpace(result)
}

// isTableRow reports whether a line looks like a GFM table content row: it
// must contain at least one interior pipe (a bare leading/trailing pipe alone
// is not enough).
func isTableRow(line string) bool {
	t := strings.TrimSpace(line)
	if !strings.Contains(t, "|") {
		return false
	}
	inner := strings.Trim(t, "|")
	return strings.Contains(inner, "|") || (strings.HasPrefix(t, "|") && strings.HasSuffix(t, "|"))
}

// flattenTableRow turns `| a | b |` into `a | b`, dropping empty edge cells
// produced by the surrounding pipes.
func flattenTableRow(line string) string {
	cells := strings.Split(line, "|")
	kept := make([]string, 0, len(cells))
	for _, c := range cells {
		c = strings.TrimSpace(c)
		if c != "" {
			kept = append(kept, c)
		}
	}
	return strings.Join(kept, " | ")
}
