package ai

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// maxFixSchemaTables bounds how many referenced tables get expanded into the
// fix prompt. A broken statement names a handful; a pathological one (a long
// UNION chain) must not undo the token discipline the lean catalog exists for.
const maxFixSchemaTables = 6

// maxFixSchemaColumns bounds how many columns of one table get expanded, for
// the same reason discovery's lookup path caps its own at
// defaultLookupColumnLimit: wide audit / event tables run to 200+ columns, and
// six of those would swamp the error context this prompt exists to act on.
// Deliberately the same number as that cap — a column the fixer can see and
// lookup_schema cannot (or the reverse) would be its own kind of confusing.
// Truncation is stated in the prompt rather than silent, because a model that
// reads a short list as exhaustive concludes a real column does not exist. The
// note names no remedy: FixSQL is one tool-less completion, so the model
// cannot look the rest up — it can only avoid asserting the column is absent.
const maxFixSchemaColumns = 50

// tableRefPattern finds the table names a statement reads from: the
// identifier following FROM or JOIN. The bare JOIN token also matches the
// tail of "LEFT JOIN" / "CROSS JOIN" / etc., so no flavour alternation is
// needed.
//
// Deliberately a regex rather than a SQL parser: this runs on a statement the
// warehouse has ALREADY rejected, so it may not parse. A parser would return
// nothing for exactly the inputs this has to serve. Over-matching is harmless
// (an unknown name resolves to no columns and is skipped) and the cost of
// under-matching is only the status quo. The repo vendors no SQL parser and
// takes the same approach in discovery.queryTableRefRe.
//
// It is NOT the same pattern as that one, and the difference is deliberate:
// queryTableSignature wants a stable dedup key, so it stops at the first
// quote character and treats `"shop"."events"` as `shop`. Resolving columns
// needs the whole multipart name, so this captures through the quoting and
// strips it afterwards. Unifying them would change that signature, and with
// it the step-dedup behaviour of a path this fix has no business touching.
var tableRefPattern = regexp.MustCompile(
	`(?i)\b(?:FROM|JOIN)\s+([` + "`" + `"\[\]\w.$-]+)`,
)

// referencedTables returns the distinct table names a statement reads from,
// in first-seen order, stripped of quoting. Subquery keywords that can follow
// FROM (a bare "(") never match the identifier class, so they drop out.
func referencedTables(query string) []string {
	matches := tableRefPattern.FindAllStringSubmatch(query, -1)
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		name := strings.Trim(m[1], "`\"[] \t")
		name = strings.ReplaceAll(name, "`", "")
		name = strings.ReplaceAll(name, `"`, "")
		name = strings.ReplaceAll(name, "[", "")
		name = strings.ReplaceAll(name, "]", "")
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, name)
	}
	return out
}

// SetTableColumnResolver supplies the per-table column lookup the fix prompt
// needs. Set by the orchestrator from the run's schema cache — the very map
// whose column COUNT the lean catalog renders as "6c".
//
// Without it the prompt's {{SCHEMA_INFO}} is the lean catalog alone, which
// names no columns at all, while the prompt instructs the model to "only
// reference columns that appear in the table schemas section". A
// `column X does not exist` error is then unfixable except by guessing, and
// every retry draws from the same guess distribution because every attempt
// sees identical context.
func (f *SQLFixer) SetTableColumnResolver(resolve func(table string) []models.ColumnInfo) {
	f.tableColumns = resolve
}

// schemaInfoFor renders the {{SCHEMA_INFO}} value for one failed statement:
// the columns of the tables it actually references, followed by the lean
// catalog so the model can still see what else exists. Both the table count
// and each table's column count are capped, and a truncated list says so.
//
// Falls back to the catalog alone when there is no resolver, nothing parsed
// out of the statement, or none of the names are known — the resolver is an
// improvement on the status quo, never a precondition for fixing a query.
func (f *SQLFixer) schemaInfoFor(query string) string {
	if f.tableColumns == nil {
		return f.schemaCtx
	}
	var b strings.Builder
	tables := 0
	for _, table := range referencedTables(query) {
		cols := f.tableColumns(table)
		if len(cols) == 0 {
			continue
		}
		rendered := make([]string, 0, min(len(cols), maxFixSchemaColumns))
		named := 0
		for _, c := range cols {
			if c.Name == "" {
				continue
			}
			named++
			if len(rendered) >= maxFixSchemaColumns {
				continue
			}
			if c.Type != "" {
				rendered = append(rendered, c.Name+" "+c.Type)
				continue
			}
			rendered = append(rendered, c.Name)
		}
		if len(rendered) == 0 {
			continue
		}
		more := ""
		if named > len(rendered) {
			more = fmt.Sprintf(" (+%d more columns not shown)", named-len(rendered))
		}
		fmt.Fprintf(&b, "%s: %s%s\n", table, strings.Join(rendered, ", "), more)
		tables++
		if tables >= maxFixSchemaTables {
			break
		}
	}
	if b.Len() == 0 {
		return f.schemaCtx
	}
	expanded := strings.TrimRight(b.String(), "\n")
	if f.schemaCtx == "" {
		return expanded
	}
	return expanded + "\n\n" + f.schemaCtx
}
