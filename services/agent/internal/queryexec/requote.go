package queryexec

import (
	"strings"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
)

// requoteForRunner converts backtick-quoted identifiers in a SQL statement into
// the quoting this source accepts, before the statement is sent.
//
// # Why
//
// Models write `dataset.table` -- BigQuery's quoting -- against warehouses that
// reject a backtick outright. Telling them not to has been tried: the prompts
// already name the dialect, render every table reference through a placeholder
// that emits the source's own quoting, and ask for SQL the warehouse "accepts on
// the first try". A TPC-H run against Postgres still used backticks in 54 of 54
// statements. Re-running those with nothing changed but the quoting made 44 of
// the 54 parse, so most of that traffic was one mechanical substitution short of
// working, and each failure instead bought an LLM repair call.
//
// The delimiters are resolved once at construction, from the registry-first
// precedence NewQueryExecutor uses, so a source that is not a SQL warehouse --
// or could not say how it quotes -- leaves them empty and this is a no-op. A
// structured query is never touched: its meaning lives in a payload, not in text.
func (e *QueryExecutor) requoteForRunner(q gowarehouse.NativeQuery) (gowarehouse.NativeQuery, int) {
	if q.IsStructured() || e.identifierOpen == "" || e.identifierClose == "" {
		return q, 0
	}
	// Nothing to do, and the common case. Checked first so a clean statement
	// costs one substring scan.
	if !strings.Contains(q.String(), "`") {
		return q, 0
	}
	rewritten, n := gowarehouse.RequoteIdentifiers(q.String(), e.identifierOpen, e.identifierClose)
	if n == 0 {
		return q, 0
	}
	applog.WithFields(applog.Fields{
		"step":        e.currentStep,
		"phase":       e.currentPhase,
		"identifiers": n,
		"quoting":     e.identifierOpen + e.identifierClose,
	}).Info("Rewrote backtick-quoted identifiers into the warehouse's own quoting before sending")
	return gowarehouse.SQLQuery(rewritten), n
}
