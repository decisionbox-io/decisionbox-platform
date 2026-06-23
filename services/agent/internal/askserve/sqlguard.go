package askserve

import (
	"fmt"
	"regexp"
	"strings"
)

// validateAskSQL is the ask-serve-specific guard run on the model's SQL before
// execution. It is defense-in-depth on top of the warehouse's read-only
// credentials (ValidateReadOnly at connect) and the executor's tenant-filter
// check, hardened for this path because the SQL is influenced by a user's
// natural-language question:
//
//   - read-only: the statement must be a single SELECT / WITH (CTE) query, so
//     a misled model can't emit INSERT/UPDATE/DELETE/MERGE/DDL or smuggle a
//     second statement after a ';'.
//   - tenant scope: when the project is multi-tenant (filterField set), the
//     exact `field = value` predicate must appear, which rejects the evasions
//     a name-presence check misses — `field <> 'x'`, `field IN (...)`, a bare
//     `SELECT field`, or the wrong value.
//
// Known limitation (tracked as a follow-up): a substring predicate check
// cannot catch OR-based broadening such as `... AND (field = 'x' OR 1=1)`;
// fully robust tenant isolation needs SQL-AST parsing. The read-only +
// governance + read-only-credential layers remain in force regardless.
func validateAskSQL(sql, filterField, filterValue string) error {
	if !isReadOnlyQuery(sql) {
		return fmt.Errorf("only a single read-only SELECT/WITH query is allowed")
	}
	if strings.TrimSpace(filterField) != "" && strings.TrimSpace(filterValue) != "" {
		if !hasTenantPredicate(sql, filterField, filterValue) {
			return fmt.Errorf("query must be scoped to this tenant with the exact predicate %s = '%s'", filterField, filterValue)
		}
	}
	return nil
}

// isReadOnlyQuery reports whether sql is a single read-only statement that
// begins with SELECT or WITH (after stripping comments and an optional leading
// open-paren). Multiple statements (a ';' with anything but trailing
// comments/whitespace after it) are rejected.
func isReadOnlyQuery(sql string) bool {
	s := strings.TrimSpace(stripSQLNoise(sql))
	if s == "" {
		return false
	}
	// Single-statement: a ';' may only be trailing (followed by nothing once
	// comments/whitespace are stripped).
	if idx := indexOutsideString(s, ';'); idx >= 0 {
		if strings.TrimSpace(stripSQLNoise(s[idx+1:])) != "" {
			return false
		}
		s = strings.TrimSpace(s[:idx])
	}
	low := strings.ToLower(strings.TrimLeft(s, "("))
	low = strings.TrimSpace(low)
	return strings.HasPrefix(low, "select") || strings.HasPrefix(low, "with")
}

// hasTenantPredicate reports whether sql contains the exact equality predicate
// `<field> = <value>` (case-insensitive on the field; the value matches with
// or without surrounding single quotes so numeric and string tenants both
// work).
func hasTenantPredicate(sql, field, value string) bool {
	re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(field) + `\s*=\s*'?` + regexp.QuoteMeta(value) + `'?`)
	return re.MatchString(sql)
}

// stripSQLNoise removes line (--) and block (/* */) comments that fall outside
// single-quoted string literals, so the read-only/statement checks see the
// real SQL. It is intentionally conservative: anything inside a '...' literal
// is preserved verbatim.
func stripSQLNoise(sql string) string {
	var b strings.Builder
	inString := false
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if inString {
			b.WriteByte(c)
			if c == '\'' {
				// Doubled '' is an escaped quote inside the string.
				if i+1 < len(sql) && sql[i+1] == '\'' {
					b.WriteByte('\'')
					i++
					continue
				}
				inString = false
			}
			continue
		}
		switch {
		case c == '\'':
			inString = true
			b.WriteByte(c)
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			// Line comment to end of line.
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			b.WriteByte(' ')
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			// Block comment to */.
			i += 2
			for i+1 < len(sql) && (sql[i] != '*' || sql[i+1] != '/') {
				i++
			}
			i++ // skip the closing '/'
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// indexOutsideString returns the index of the first occurrence of ch that is
// not inside a single-quoted string literal, or -1.
func indexOutsideString(s string, ch byte) int {
	inString := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				inString = false
			}
			continue
		}
		if c == '\'' {
			inString = true
			continue
		}
		if c == ch {
			return i
		}
	}
	return -1
}
