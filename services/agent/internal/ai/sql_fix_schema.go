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

// tableRefPattern finds the table names a statement reads from: the
// identifier following FROM or any flavour of JOIN. It accepts every quoting
// style the six shipped providers emit (backticks, double quotes, brackets)
// plus unquoted refs, and allows a dotted schema/dataset prefix.
//
// Deliberately a regex rather than a SQL parser: this runs on a statement the
// warehouse has ALREADY rejected, so it may not parse. A parser would return
// nothing for exactly the inputs this has to serve. Over-matching is harmless
// (an unknown name resolves to no columns and is skipped) and the cost of
// under-matching is only the status quo.
var tableRefPattern = regexp.MustCompile(
	`(?i)\b(?:FROM|(?:INNER|LEFT|RIGHT|FULL|CROSS|OUTER)\s+JOIN|JOIN)\s+([` + "`" + `"\[\]\w.]+)`,
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
// the full column list of the tables it actually references, followed by the
// lean catalog so the model can still see what else exists.
//
// Falls back to the catalog alone when there is no resolver, nothing parsed
// out of the statement, or none of the names are known — the resolver is an
// improvement on the status quo, never a precondition for fixing a query.
func (f *SQLFixer) schemaInfoFor(query string) string {
	if f.tableColumns == nil {
		return f.schemaCtx
	}
	var b strings.Builder
	for _, table := range referencedTables(query) {
		cols := f.tableColumns(table)
		if len(cols) == 0 {
			continue
		}
		rendered := make([]string, 0, len(cols))
		for _, c := range cols {
			if c.Name == "" {
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
		fmt.Fprintf(&b, "%s: %s\n", table, strings.Join(rendered, ", "))
		if strings.Count(b.String(), "\n") >= maxFixSchemaTables {
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
