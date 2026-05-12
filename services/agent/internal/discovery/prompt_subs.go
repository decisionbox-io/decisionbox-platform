package discovery

import (
	"regexp"
	"strings"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// refPlaceholderPattern matches {{REF:identifier}} placeholders in prompt
// templates. The identifier is captured as group 1 and must be a plain
// SQL-style name (letter or underscore followed by alphanumerics or
// underscores). Domain-pack prompts use this placeholder to reference
// warehouse tables in example SQL snippets so the orchestrator can
// render them with the connected provider's dialect-correct quoting.
var refPlaceholderPattern = regexp.MustCompile(`\{\{REF:([a-zA-Z_][a-zA-Z0-9_]*)\}\}`)

// substituteDialectTokens applies every warehouse-dialect-aware
// substitution that prompts share, so each call site (exploration,
// analysis, recommendations, etc.) does not redundantly fan out the
// same ReplaceAll + regex logic.
//
// Substitutions performed:
//
//   - {{DIALECT}}        → provider.SQLDialect()
//   - {{REF:identifier}} → provider.QuoteRef(refDataset, identifier)
//
// refDataset names the dataset / schema used to qualify {{REF:...}}
// placeholders — callers pass the first / canonical dataset of the
// project (orchestrator: o.datasets[0]). When refDataset is empty the
// placeholder still renders, but as an unqualified single-part
// identifier (e.g. `table` on BigQuery, [table] on MSSQL) — that path
// is exercised only by tests since the orchestrator always wires a
// non-empty dataset.
//
// Templates that do not contain either placeholder pass through
// unchanged, so prompts authored before the new placeholders shipped
// keep working — exactly the migration story we tell other
// deployments with custom domain packs.
func substituteDialectTokens(template string, provider gowarehouse.Provider, refDataset string) string {
	if provider == nil {
		return template
	}
	template = strings.ReplaceAll(template, "{{DIALECT}}", provider.SQLDialect())
	template = refPlaceholderPattern.ReplaceAllStringFunc(template, func(match string) string {
		// FindStringSubmatch is guaranteed to return [full match, group 1]
		// because ReplaceAllStringFunc only invokes us on a successful
		// match against refPlaceholderPattern (which always has exactly
		// one capture group). No defensive length check needed.
		ident := refPlaceholderPattern.FindStringSubmatch(match)[1]
		if refDataset == "" {
			return provider.QuoteRef(ident)
		}
		return provider.QuoteRef(refDataset, ident)
	})
	return template
}
