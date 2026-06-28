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
	"strconv"
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

		out = append(out, strings.TrimRight(reduceInline(line), " \t"))
	}

	result := strings.Join(out, "\n")
	// Collapse runs of 3+ newlines (created when block markers leave blank
	// lines) to a single blank line, then trim surrounding whitespace.
	result = reMultipleNewline.ReplaceAllString(result, "\n\n")
	return strings.TrimSpace(result)
}

// reduceInline strips inline Markdown markers from a single line. Inline-code
// span contents are protected first — replaced with NUL-delimited placeholders
// — so emphasis, link, and strikethrough rules cannot mutate an identifier or
// expression inside a code span (e.g. `__typename__` or `a * b * c`). The
// original code contents are restored verbatim afterward, with the surrounding
// backticks removed.
func reduceInline(line string) string {
	var codeSpans []string
	line = reInlineCode.ReplaceAllStringFunc(line, func(m string) string {
		// reInlineCode matches a single-backtick span, so the inner content is
		// m without its first and last byte (the delimiters).
		codeSpans = append(codeSpans, m[1:len(m)-1])
		return codePlaceholder(len(codeSpans) - 1)
	})

	line = reImage.ReplaceAllString(line, "$1")
	line = reLink.ReplaceAllString(line, "$1")
	// Asterisk emphasis is unwrapped only at CommonMark-valid flanking
	// positions, so literal asterisks in prose (`a * b * c`, `SELECT *`,
	// `3 * 4`) survive while genuine `*x*` / `**x**` / `***x***` are reduced —
	// matching exactly what react-markdown renders from `description_md`, so
	// the plain and Markdown fields cannot drift.
	line = stripAsteriskEmphasis(line, reBoldItalicAsterisk)
	line = stripAsteriskEmphasis(line, reBoldAsterisk)
	line = stripAsteriskEmphasis(line, reItalicAsterisk)
	line = reBoldUnderscore.ReplaceAllString(line, "$1")
	line = reItalicUnderscore.ReplaceAllString(line, "$1$2$3")
	line = reStrikethrough.ReplaceAllString(line, "$1")
	line = reMultipleSpaces.ReplaceAllString(line, " ")

	for i, c := range codeSpans {
		line = strings.Replace(line, codePlaceholder(i), c, 1)
	}
	return line
}

// stripAsteriskEmphasis unwraps `*…*`-style emphasis (re must capture the inner
// content in group 1) only when the delimiters are CommonMark left/right
// flanking — i.e. genuine emphasis, not literal asterisks. A non-flanking match
// is left verbatim so text like `a * b * c` or `COUNT(*)` (a lone asterisk) is
// preserved. This mirrors react-markdown's own emphasis parsing, so the plain
// reduction agrees with the rendered `description_md`.
func stripAsteriskEmphasis(s string, re *regexp.Regexp) string {
	var b strings.Builder
	last := 0
	for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
		full0, full1, in0, in1 := m[0], m[1], m[2], m[3]
		before := byte(' ')
		if full0 > 0 {
			before = s[full0-1]
		}
		after := byte(' ')
		if full1 < len(s) {
			after = s[full1]
		}
		// in0/in1 bracket the content, so s[in0] is the char just after the
		// opening run and s[in1-1] the char just before the closing run.
		if emphasisFlanks(before, s[in0], s[in1-1], after) {
			b.WriteString(s[last:full0])
			b.WriteString(s[in0:in1])
			last = full1
		}
	}
	b.WriteString(s[last:])
	return b.String()
}

// emphasisFlanks reports whether a delimiter run is a valid CommonMark
// emphasis opener+closer, given the bytes just outside and just inside each
// delimiter. Start/end of string are passed as a space. Byte-level classifies
// any non-ASCII byte as a word character, which is the correct flanking
// outcome for non-ASCII letters.
func emphasisFlanks(before, afterOpen, beforeClose, after byte) bool {
	leftFlank := !isMdSpace(afterOpen) &&
		(!isMdPunct(afterOpen) || isMdSpace(before) || isMdPunct(before))
	rightFlank := !isMdSpace(beforeClose) &&
		(!isMdPunct(beforeClose) || isMdSpace(after) || isMdPunct(after))
	return leftFlank && rightFlank
}

func isMdSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' || b == '\v'
}

func isMdPunct(b byte) bool {
	return strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", b) >= 0
}

// codePlaceholder returns a sentinel for the i-th protected code span. NUL
// delimiters cannot appear in a description, and the body has no Markdown
// markers, so neither the emphasis rules nor the space collapse touch it.
func codePlaceholder(i int) string {
	return "\x00c" + strconv.Itoa(i) + "\x00"
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
