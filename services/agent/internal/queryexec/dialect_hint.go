package queryexec

import "strings"

// A model writing SQL for one warehouse in another warehouse's dialect is the
// single most common way a statement fails here: across two TPC-H runs every
// statement the analysis model proposed quoted identifiers the way BigQuery does,
// with backticks, against a Postgres warehouse that rejects a backtick outright.
// That is not a missing instruction -- the prompt names the dialect, renders every
// table reference in the source's own quoting, and asks for SQL the warehouse
// accepts on the first try.
//
// The repair is left to the model, but it is told exactly what is wrong. Handing
// the generic fixer a bare "syntax error at or near" let it rewrite far more than
// the quoting: in one run it replaced APPROX_QUANTILES(x,4)[OFFSET(2)] with
// PERCENTILE_CONT(0.5) WITHIN GROUP -- an approximate median for an exact one --
// and turned a BigQuery UNNEST into a different grouping. Those rewrites change
// what the rows mean, the rows become evidence for a shipped claim, and nothing
// checks them. Naming the fault and forbidding everything else is the cheapest way
// to keep a repair to the thing that was broken.
//
// Deliberately NOT a rewrite. Substituting the quoting in Go would remove the LLM
// call altogether, and a scanner to do it safely has to know every place a
// backtick is data rather than quoting -- string literals, escape strings, quoted
// identifiers, dollar quoting, comments -- per dialect. Getting one of those wrong
// silently changes what the query asks, which is a worse failure than the one it
// saves, and it would sit in the path of every query. An 80%-of-statements
// substitution is not worth a component that can corrupt a statement.

// dialectQuotingHint returns a corrective instruction for the SQL fixer when the
// warehouse rejected a statement over another dialect's identifier quoting, and
// "" when the error says nothing about it.
//
// It reads the WAREHOUSE'S error and never our own statement. That is the whole
// point: the engine has already parsed the SQL and told us which token it choked
// on, so there is nothing for us to parse and no way for us to be wrong about what
// the statement contains. A backtick in the message is the engine quoting the
// offending token back at us.
//
// A false positive costs one extra paragraph in a prompt. A missed one costs
// nothing -- the generic fixer path is unchanged.
func dialectQuotingHint(err error) string {
	if err == nil || !strings.Contains(err.Error(), "`") {
		return ""
	}
	return "The statement quotes identifiers with backticks (`like_this`), which is BigQuery's " +
		"syntax and not this warehouse's. Re-emit the same statement using the identifier quoting " +
		"shown for every table in the schema block above, and change NOTHING else: keep the same " +
		"columns, the same filters, the same date bounds and the same functions. Only the quoting is wrong."
}
