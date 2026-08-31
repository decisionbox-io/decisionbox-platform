package queryexec

import (
	"context"
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// stubRunner is a QueryRunner that records what it was asked to execute — the
// shape a non-SQL adapter takes.
type stubRunner struct {
	got   []gowarehouse.NativeQuery
	err   error
	calls int
}

func (r *stubRunner) RunQuery(_ context.Context, q gowarehouse.NativeQuery) (*gowarehouse.QueryResult, error) {
	r.calls++
	r.got = append(r.got, q)
	if r.err != nil {
		return nil, r.err
	}
	return &gowarehouse.QueryResult{Columns: []string{"c"}, Rows: []map[string]interface{}{{"c": 1}}}, nil
}

func (r *stubRunner) QueryLanguage() string  { return "Report Request" }
func (r *stubRunner) QueryFixPrompt() string { return "" }

// TestExecuteNative_RefusesAStructuredQueryUnderTenantScope is the security
// property behind the refusal: the scope check reads query TEXT, while a
// structured query's meaning is in its PAYLOAD, and the two are set
// independently. Checking the text would pass a query whose payload asks for
// everything — a security check that reports a guarantee it cannot keep.
//
// The refusal must happen before the source is touched, so a rejected query
// costs zero round-trips and leaks nothing.
func TestExecuteNative_RefusesAStructuredQueryUnderTenantScope(t *testing.T) {
	runner := &stubRunner{}
	e := NewQueryExecutor(QueryExecutorOptions{
		Runner:      runner,
		FilterField: "app_id",
		FilterValue: "acme",
	})

	// Text that would satisfy the substring scope check, paired with a payload
	// that carries no scope at all — exactly the bypass being refused.
	q := gowarehouse.NativeQuery{
		Text:    "report filtered by app_id = 'acme'",
		Payload: map[string]any{"metrics": []string{"sessions"}},
	}

	result, err := e.ExecuteNative(context.Background(), q, "test", FixOpts{})
	if err == nil {
		t.Fatal("a structured query under tenant scope must be refused")
	}
	if !strings.HasPrefix(err.Error(), "security violation: a structured query cannot be scope-checked against app_id") {
		t.Errorf("error = %q, want it to name the unenforceable scope field", err.Error())
	}
	if result != nil {
		t.Error("a refused query must not return a result")
	}
	if runner.calls != 0 {
		t.Errorf("runner called %d times, want 0 — the refusal must precede execution", runner.calls)
	}
}

// TestExecuteNative_RunsAStructuredQueryWithoutTenantScope pins that the
// refusal is scoped to the case it is about: with no tenant filter configured
// there is nothing to enforce, and the payload must reach the runner intact.
func TestExecuteNative_RunsAStructuredQueryWithoutTenantScope(t *testing.T) {
	type reportRequest struct{ Metrics []string }

	runner := &stubRunner{}
	e := NewQueryExecutor(QueryExecutorOptions{Runner: runner})

	q := gowarehouse.NativeQuery{
		Text:    "sessions by date",
		Payload: reportRequest{Metrics: []string{"sessions"}},
	}

	result, err := e.ExecuteNative(context.Background(), q, "test", FixOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("runner called %d times, want 1", runner.calls)
	}
	if _, ok := runner.got[0].Payload.(reportRequest); !ok {
		t.Errorf("payload reached the runner as %T, want it preserved", runner.got[0].Payload)
	}
	if result.OriginalQuery != "sessions by date" || result.FinalQuery != "sessions by date" {
		t.Errorf("query fields = %q / %q, want the readable rendering in both",
			result.OriginalQuery, result.FinalQuery)
	}
	if result.RowCount != 1 {
		t.Errorf("RowCount = %d, want 1", result.RowCount)
	}
}

// TestExecuteNative_StructuredQueryIsNotRepaired pins that a failing
// structured query is reported rather than rewritten. The fixer rewrites text;
// applying that to a payload-carrying query would either drop the payload or
// pair it with contradicting text — a query that runs cleanly and answers a
// different question than the one asked.
func TestExecuteNative_StructuredQueryIsNotRepaired(t *testing.T) {
	runner := &stubRunner{err: context.DeadlineExceeded}
	fixer := &mockFixer{}
	e := NewQueryExecutor(QueryExecutorOptions{Runner: runner, SQLFixer: fixer, MaxRetries: 3})

	q := gowarehouse.NativeQuery{Text: "sessions by date", Payload: map[string]any{"m": 1}}

	_, err := e.ExecuteNative(context.Background(), q, "test", FixOpts{})
	if err == nil {
		t.Fatal("expected the failure to surface")
	}
	if !strings.Contains(err.Error(), "structured queries have no repair path") {
		t.Errorf("error = %q, want it to say why no repair was attempted", err.Error())
	}
	if fixer.Calls != 0 {
		t.Errorf("fixer called %d times, want 0", fixer.Calls)
	}
	if runner.calls != 1 {
		t.Errorf("runner called %d times, want 1 — no retry without a repair", runner.calls)
	}
}

// TestExecute_StringPathIsUnaffectedByTheRefusal pins that a plain SQL query
// under tenant scope still behaves exactly as before: the text check runs and
// a scoped query passes it.
func TestExecute_StringPathIsUnaffectedByTheRefusal(t *testing.T) {
	runner := &stubRunner{}
	e := NewQueryExecutor(QueryExecutorOptions{
		Runner:      runner,
		FilterField: "app_id",
		FilterValue: "acme",
	})

	if _, err := e.Execute(context.Background(), "SELECT 1 WHERE app_id = 'acme'", "test"); err != nil {
		t.Fatalf("a scoped SQL query must still run: %v", err)
	}
	if runner.calls != 1 {
		t.Errorf("runner called %d times, want 1", runner.calls)
	}
	if runner.got[0].IsStructured() {
		t.Error("the string path must produce an unstructured NativeQuery")
	}
}
