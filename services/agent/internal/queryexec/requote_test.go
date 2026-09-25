package queryexec

import (
	"context"
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// recordingRunner captures the statement it was handed, so a test can assert on
// what the warehouse would actually have received.
type recordingRunner struct {
	seen       []string
	openQ      string
	closeQ     string
	quotes     bool // false = does not implement QuoteRef at all
	failOnTick bool // reject any statement containing a backtick, as Postgres does
}

func (r *recordingRunner) RunQuery(_ context.Context, q gowarehouse.NativeQuery) (*gowarehouse.QueryResult, error) {
	r.seen = append(r.seen, q.String())
	if r.failOnTick && strings.Contains(q.String(), "`") {
		return nil, errBacktick{}
	}
	return &gowarehouse.QueryResult{Rows: []map[string]any{{"n": 1}}}, nil
}
func (r *recordingRunner) QueryLanguage() string  { return "SQL" }
func (r *recordingRunner) QueryFixPrompt() string { return "" }

type errBacktick struct{}

func (errBacktick) Error() string { return `pq: syntax error at or near "` + "`" + `"` }

// quotingRunner adds the optional QuoteRef capability.
type quotingRunner struct {
	*recordingRunner
}

func (q quotingRunner) QuoteRef(parts ...string) string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = q.openQ + p + q.closeQ
	}
	return strings.Join(out, ".")
}

// failingFixer records whether the LLM repair path was reached at all. The point
// of the pre-flight substitution is that it is not.
type failingFixer struct{ called int }

func (f *failingFixer) FixSQL(_ context.Context, _ string, _ string, _ int, _ FixOpts) (FixResult, error) {
	f.called++
	return FixResult{}, errBacktick{}
}

// newExec wires a SQL warehouse: the slug names one the registry knows (see
// capability_stubs_test.go), which is the declaration the executor trusts when
// deciding whether SQL identifier re-quoting applies.
func newExec(r gowarehouse.QueryRunner, f SQLFixer) *QueryExecutor {
	return NewQueryExecutor(QueryExecutorOptions{
		Runner: r, SQLFixer: f, MaxRetries: 2, ProviderSlug: "queryexec-sql-probe",
	})
}

// The behaviour this exists for: a statement in another dialect's quoting
// reaches the warehouse in the warehouse's own quoting, on the FIRST attempt,
// without an LLM call.
func TestExecute_RequotesBeforeSendingAndSkipsTheFixer(t *testing.T) {
	base := &recordingRunner{openQ: `"`, closeQ: `"`, failOnTick: true}
	fixer := &failingFixer{}
	res, err := newExec(quotingRunner{base}, fixer).Execute(
		context.Background(), "SELECT * FROM `public.orders`", "counts")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(base.seen) != 1 {
		t.Fatalf("warehouse saw %d statements, want 1: %q", len(base.seen), base.seen)
	}
	if base.seen[0] != `SELECT * FROM "public"."orders"` {
		t.Errorf("warehouse received %q", base.seen[0])
	}
	if fixer.called != 0 {
		t.Errorf("the LLM fixer was called %d times; the substitution should have made that unnecessary", fixer.called)
	}
	if res.RequotedIdentifiers != 1 {
		t.Errorf("RequotedIdentifiers = %d, want 1", res.RequotedIdentifiers)
	}
	if res.Fixed {
		t.Error("Fixed should stay false: a deterministic pre-flight rewrite is not an LLM repair")
	}
	if res.FinalQuery != `SELECT * FROM "public"."orders"` {
		t.Errorf("FinalQuery = %q, want the statement that ran", res.FinalQuery)
	}
	if res.OriginalQuery != "SELECT * FROM `public.orders`" {
		t.Errorf("OriginalQuery = %q, want the model's proposal preserved", res.OriginalQuery)
	}
}

// A source that cannot say how it quotes must be left alone rather than guessed
// at — the statement goes out exactly as written.
func TestExecute_LeavesTheStatementAloneWhenTheRunnerCannotSayHowItQuotes(t *testing.T) {
	base := &recordingRunner{}
	res, err := newExec(base, &failingFixer{}).Execute(
		context.Background(), "SELECT * FROM `public.orders`", "counts")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if base.seen[0] != "SELECT * FROM `public.orders`" {
		t.Errorf("statement was rewritten for a source that never declared its quoting: %q", base.seen[0])
	}
	if res.RequotedIdentifiers != 0 {
		t.Errorf("RequotedIdentifiers = %d, want 0", res.RequotedIdentifiers)
	}
}

// A source that quotes with backticks has nothing to convert to.
func TestExecute_DoesNotRewriteForABacktickQuotingSource(t *testing.T) {
	base := &recordingRunner{openQ: "`", closeQ: "`"}
	_, err := newExec(quotingRunner{base}, &failingFixer{}).Execute(
		context.Background(), "SELECT * FROM `ds.tbl`", "counts")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if base.seen[0] != "SELECT * FROM `ds.tbl`" {
		t.Errorf("a backtick-quoting source had its own quoting rewritten: %q", base.seen[0])
	}
}

// A statement already in the source's quoting must be byte-identical on the
// wire, or this change would touch every query on every clean run.
func TestExecute_CleanStatementIsUnchanged(t *testing.T) {
	base := &recordingRunner{openQ: `"`, closeQ: `"`}
	in := `SELECT * FROM "public"."orders" WHERE c LIKE '%x%'`
	res, err := newExec(quotingRunner{base}, &failingFixer{}).Execute(context.Background(), in, "counts")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if base.seen[0] != in {
		t.Errorf("clean statement was altered:\n got %q\n want %q", base.seen[0], in)
	}
	if res.RequotedIdentifiers != 0 {
		t.Errorf("RequotedIdentifiers = %d, want 0", res.RequotedIdentifiers)
	}
}

// Regression: a source whose queries are its own request format must not be
// asked how it quotes SQL identifiers. Such a source can satisfy QuoteRef only
// by embedding Provider without implementing it, and calling it panics — an
// existing test in this package caught exactly that.
func TestExecute_DoesNotAskANonSQLSourceHowItQuotes(t *testing.T) {
	src := &nativeProvider{}
	e := NewQueryExecutor(QueryExecutorOptions{Warehouse: src})
	// A backtick in a request format is data. Reaching the quoting path at all
	// would panic, so surviving this call is the assertion.
	_, err := e.ExecuteNative(context.Background(),
		gowarehouse.NativeQuery{Text: "{\"dimensions\":[\"`country`\"]}"},
		"traffic", FixOpts{})
	if err != nil {
		t.Fatalf("ExecuteNative() error = %v", err)
	}
	if len(src.ran) != 1 {
		t.Fatalf("source ran %d queries, want 1", len(src.ran))
	}
	if got := src.ran[0].String(); got != "{\"dimensions\":[\"`country`\"]}" {
		t.Errorf("a request format was rewritten as SQL: %q", got)
	}
}
