package queryexec

import (
	"context"
	"errors"
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// The hint is decided from the WAREHOUSE'S error, never from our statement.
func TestDialectQuotingHint(t *testing.T) {
	fires := []string{
		"postgres: query failed: pq: syntax error at or near \"`\"",
		"mssql: incorrect syntax near '`'",
	}
	for _, msg := range fires {
		if dialectQuotingHint(errors.New(msg)) == "" {
			t.Errorf("no hint for %q, want one", msg)
		}
	}
	quiet := []string{
		`pq: column "revenue" does not exist`,
		"pq: function date_diff(date, date) does not exist",
		"pq: WITHIN GROUP is required for ordered-set aggregate percentile_cont",
		"",
	}
	for _, msg := range quiet {
		if got := dialectQuotingHint(errors.New(msg)); got != "" {
			t.Errorf("hint for %q, want none: %q", msg, got)
		}
	}
	if dialectQuotingHint(nil) != "" {
		t.Error("a nil error must not produce a hint")
	}
}

// The hint must forbid collateral edits, because the failure it replaces was the
// generic fixer rewriting more than the quoting — an approximate median became an
// exact one in an observed run.
func TestDialectQuotingHint_ForbidsCollateralEdits(t *testing.T) {
	h := dialectQuotingHint(errors.New("syntax error at or near \"`\""))
	for _, want := range []string{"NOTHING else", "same filters", "same date bounds", "same functions"} {
		if !strings.Contains(h, want) {
			t.Errorf("hint does not say %q:\n%s", want, h)
		}
	}
}

type errMsgFixer struct{ errsSeen []string }

func (f *errMsgFixer) FixSQL(_ context.Context, _ string, errMsg string, _ int, _ FixOpts) (FixResult, error) {
	f.errsSeen = append(f.errsSeen, errMsg)
	return FixResult{}, errors.New("fixer declined, which ends the loop")
}

type failingRunner struct{ msg string }

func (r failingRunner) RunQuery(context.Context, gowarehouse.NativeQuery) (*gowarehouse.QueryResult, error) {
	return nil, errors.New(r.msg)
}
func (failingRunner) QueryLanguage() string  { return "" }
func (failingRunner) QueryFixPrompt() string { return "" }

// End to end: the hint reaches the fixer alongside the engine's own message, and
// the statement itself is handed over untouched.
func TestExecute_HandsTheFixerTheHintAndTheUnalteredStatement(t *testing.T) {
	const stmt = "SELECT * FROM `public.orders`"
	fixer := &errMsgFixer{}
	e := NewQueryExecutor(QueryExecutorOptions{
		Runner:   failingRunner{msg: "pq: syntax error at or near \"`\""},
		SQLFixer: fixer, MaxRetries: 1,
	})
	_, err := e.Execute(context.Background(), stmt, "counts")
	if err == nil {
		t.Fatal("want the declined fix to surface as an error")
	}
	if len(fixer.errsSeen) == 0 {
		t.Fatal("the fixer was never called")
	}
	seen := fixer.errsSeen[0]
	if !strings.Contains(seen, "syntax error at or near") {
		t.Errorf("the engine's own message was lost:\n%s", seen)
	}
	if !strings.Contains(seen, "backticks") {
		t.Errorf("the hint did not reach the fixer:\n%s", seen)
	}
}

// An error that says nothing about quoting must reach the fixer verbatim, or
// every repair prompt grows a paragraph about a fault it does not have.
func TestExecute_LeavesAnUnrelatedErrorAlone(t *testing.T) {
	fixer := &errMsgFixer{}
	e := NewQueryExecutor(QueryExecutorOptions{
		Runner:   failingRunner{msg: `pq: column "revenue" does not exist`},
		SQLFixer: fixer, MaxRetries: 1,
	})
	_, _ = e.Execute(context.Background(), "SELECT revenue FROM t", "counts")
	if len(fixer.errsSeen) == 0 {
		t.Fatal("the fixer was never called")
	}
	if fixer.errsSeen[0] != `pq: column "revenue" does not exist` {
		t.Errorf("the error was altered: %q", fixer.errsSeen[0])
	}
}

// Nothing rewrites the statement any more. This is the property the design turns
// on, so it is asserted rather than assumed.
func TestExecute_SendsTheStatementExactlyAsWritten(t *testing.T) {
	const stmt = "SELECT * FROM `public.orders` WHERE c LIKE '%`%'"
	var seen []string
	e := NewQueryExecutor(QueryExecutorOptions{
		Runner: capturingRunner{seen: &seen}, MaxRetries: 0,
	})
	if _, err := e.Execute(context.Background(), stmt, "counts"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(seen) != 1 || seen[0] != stmt {
		t.Errorf("statement sent = %q, want it byte-identical to the input", seen)
	}
}

type capturingRunner struct{ seen *[]string }

func (r capturingRunner) RunQuery(_ context.Context, q gowarehouse.NativeQuery) (*gowarehouse.QueryResult, error) {
	*r.seen = append(*r.seen, q.String())
	return &gowarehouse.QueryResult{Rows: []map[string]any{{"n": 1}}}, nil
}
func (capturingRunner) QueryLanguage() string  { return "" }
func (capturingRunner) QueryFixPrompt() string { return "" }
