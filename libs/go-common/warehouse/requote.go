package warehouse

import "strings"

// RequoteIdentifiers rewrites backtick-quoted identifiers into the quoting a
// source actually accepts, and reports how many it rewrote.
//
// # Why this exists
//
// Models write `dataset.table` — BigQuery's quoting — against warehouses that
// reject a backtick outright. Telling them not to has been tried: the analysis
// prompts already name the dialect, render every table reference through a
// placeholder that emits the source's own quoting, and ask for SQL "the named
// warehouse accepts on the first try". A TPC-H run against Postgres still used
// backticks in 54 of 54 statements. Re-running those statements with nothing
// changed but the quoting made 44 of the 54 parse, so most of that traffic was
// one mechanical substitution away from working, and each failure instead bought
// an LLM repair call.
//
// A backtick is not ambiguous on a source that does not use it for quoting, so
// there is nothing to infer and the rewrite is deterministic.
//
// # What it will not touch
//
// A backtick inside a string literal, a quoted identifier, a dollar-quoted
// string or a comment is data, not quoting, and rewriting it would change what
// the query asks. The scanner walks the statement and skips all four. An
// unterminated backtick is left exactly as found rather than guessed at.
//
// openQuote and closeQuote are the source's identifier delimiters. When either is empty,
// or the source quotes with a backtick itself, the statement is returned
// unchanged — there is nothing to convert it to.
func RequoteIdentifiers(sql, openQuote, closeQuote string) (string, int) {
	if sql == "" || openQuote == "" || closeQuote == "" || openQuote == "`" || closeQuote == "`" {
		return sql, 0
	}
	if !strings.Contains(sql, "`") {
		return sql, 0
	}

	var b strings.Builder
	b.Grow(len(sql) + 16)
	n := 0

	for i := 0; i < len(sql); {
		switch {
		// Line comment: copy to end of line.
		case strings.HasPrefix(sql[i:], "--"):
			j := strings.IndexByte(sql[i:], '\n')
			if j < 0 {
				b.WriteString(sql[i:])
				return b.String(), n
			}
			b.WriteString(sql[i : i+j+1])
			i += j + 1

		// Block comment. Postgres nests them, so track depth.
		case strings.HasPrefix(sql[i:], "/*"):
			depth, j := 1, i+2
			for j < len(sql) && depth > 0 {
				switch {
				case strings.HasPrefix(sql[j:], "/*"):
					depth++
					j += 2
				case strings.HasPrefix(sql[j:], "*/"):
					depth--
					j += 2
				default:
					j++
				}
			}
			b.WriteString(sql[i:j])
			i = j

		// Single-quoted string. '' is an escaped quote, not a terminator.
		case sql[i] == '\'':
			j := i + 1
			for j < len(sql) {
				if sql[j] == '\'' {
					if j+1 < len(sql) && sql[j+1] == '\'' {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			b.WriteString(sql[i:j])
			i = j

		// Already-quoted identifier, in the source's own quoting. Doubling the
		// closeQuote character escapes it, same rule as a string literal.
		case strings.HasPrefix(sql[i:], openQuote):
			j := i + len(openQuote)
			for j < len(sql) {
				if strings.HasPrefix(sql[j:], closeQuote) {
					if strings.HasPrefix(sql[j+len(closeQuote):], closeQuote) {
						j += 2 * len(closeQuote)
						continue
					}
					j += len(closeQuote)
					break
				}
				j++
			}
			b.WriteString(sql[i:j])
			i = j

		// Dollar-quoted string: $$ ... $$ or $tag$ ... $tag$.
		case sql[i] == '$':
			if tag, ok := dollarTag(sql[i:]); ok {
				end := strings.Index(sql[i+len(tag):], tag)
				if end < 0 {
					b.WriteString(sql[i:])
					return b.String(), n
				}
				j := i + len(tag) + end + len(tag)
				b.WriteString(sql[i:j])
				i = j
				continue
			}
			b.WriteByte(sql[i])
			i++

		// The case this function exists for.
		case sql[i] == '`':
			j := strings.IndexByte(sql[i+1:], '`')
			if j < 0 {
				// Unterminated. Copy the rest verbatim; a half-rewritten
				// statement is worse than the one that came in.
				b.WriteString(sql[i:])
				return b.String(), n
			}
			ident := sql[i+1 : i+1+j]
			b.WriteString(quoteParts(ident, openQuote, closeQuote))
			n++
			i += j + 2

		default:
			b.WriteByte(sql[i])
			i++
		}
	}
	return b.String(), n
}

// quoteParts renders one backticked identifier in the target quoting.
//
// A dotted name is split into separately quoted parts, because that is what it
// means: `public.orders` is a qualified reference, and quoting it whole would
// ask for one identifier containing a dot. The split applies only when every
// segment is a bare identifier — a name holding a space or punctuation is taken
// as a single identifier, since there the dot is more likely part of the name
// than a qualifier.
func quoteParts(ident, openQuote, closeQuote string) string {
	if ident == "" {
		return openQuote + closeQuote
	}
	parts := strings.Split(ident, ".")
	if len(parts) > 1 && allBareIdentifiers(parts) {
		out := make([]string, len(parts))
		for i, p := range parts {
			out[i] = openQuote + escapeClose(p, closeQuote) + closeQuote
		}
		return strings.Join(out, ".")
	}
	return openQuote + escapeClose(ident, closeQuote) + closeQuote
}

// escapeClose doubles any closeQuote delimiter inside the identifier, which is how
// every supported dialect escapes it.
func escapeClose(s, closeQuote string) string {
	if closeQuote == "" || !strings.Contains(s, closeQuote) {
		return s
	}
	return strings.ReplaceAll(s, closeQuote, closeQuote+closeQuote)
}

func allBareIdentifiers(parts []string) bool {
	for _, p := range parts {
		if p == "" {
			return false
		}
		for i := 0; i < len(p); i++ {
			c := p[i]
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$' {
				continue
			}
			return false
		}
	}
	return true
}

// dollarTag returns the opening dollar-quote tag at the start of s ("$$" or
// "$tag$"), reporting false when s does not begin one.
func dollarTag(s string) (string, bool) {
	if len(s) < 2 || s[0] != '$' {
		return "", false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if c == '$' {
			return s[:i+1], true
		}
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' {
			continue
		}
		return "", false
	}
	return "", false
}

// IdentifierDelimiters reports the characters a source wraps identifiers in, by
// asking it to quote a name and reading the result. Reports false when the
// source adds no quoting, so a caller can decline to rewrite rather than guess.
//
// Derived rather than tabulated because a table of dialect to quote character is
// a second place to forget a provider, and the providers already answer this
// question to build their own SQL.
func IdentifierDelimiters(q interface{ QuoteRef(...string) string }) (openQuote, closeQuote string, ok bool) {
	if q == nil {
		return "", "", false
	}
	sample := q.QuoteRef("x")
	i := strings.Index(sample, "x")
	if i <= 0 || i+1 >= len(sample) {
		return "", "", false
	}
	return sample[:i], sample[i+1:], true
}
