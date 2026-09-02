package queryexec

import (
	"context"
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// nativeProvider is a source whose queries are its own request format rather
// than SQL — it implements QueryRunner, which is how a source declares that.
type nativeProvider struct {
	gowarehouse.Provider
	ran []gowarehouse.NativeQuery
}

func (p *nativeProvider) RunQuery(_ context.Context, q gowarehouse.NativeQuery) (*gowarehouse.QueryResult, error) {
	p.ran = append(p.ran, q)
	return &gowarehouse.QueryResult{Columns: []string{"sessions"}}, nil
}
func (p *nativeProvider) QueryLanguage() string  { return "Test Report Request (JSON)" }
func (p *nativeProvider) QueryFixPrompt() string { return "" }
func (p *nativeProvider) SQLDialect() string     { return "Test Report Request (JSON)" }

// A tenant filter is verified by looking for the field name in the query TEXT.
// That is weak evidence in SQL and none at all in a request format: a report
// naming `country` among its dimensions contains the string and filters by
// nothing, so the check passed and the query ran property-wide while reporting
// as scoped — an unscoped result presented as a scoped one, which is the exact
// failure the check exists to prevent.
func TestExecute_RefusesAFilterItCannotVerify(t *testing.T) {
	src := &nativeProvider{}
	e := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:   src,
		FilterField: "country",
	})

	// The query mentions the field, so the substring check would have passed.
	_, err := e.ExecuteNative(context.Background(),
		gowarehouse.NativeQuery{Text: `{"metrics":["sessions"],"dimensions":["country"]}`},
		"traffic", FixOpts{})

	if err == nil {
		t.Fatal("a filter that cannot be verified was treated as satisfied")
	}
	if len(src.ran) != 0 {
		t.Errorf("the query ran anyway: %+v", src.ran)
	}
	// The message has to name the way out, or an operator reads it as a bug.
	for _, want := range []string{"Test Report Request (JSON)", "country", "credential"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// pureRunner implements ONLY the query seam — no Provider surface at all,
// which is the ordinary shape of a source configured through the Runner
// option.
type pureRunner struct{ ran []gowarehouse.NativeQuery }

func (p *pureRunner) RunQuery(_ context.Context, q gowarehouse.NativeQuery) (*gowarehouse.QueryResult, error) {
	p.ran = append(p.ran, q)
	return &gowarehouse.QueryResult{}, nil
}
func (p *pureRunner) QueryLanguage() string  { return "Test Report Request (JSON)" }
func (p *pureRunner) QueryFixPrompt() string { return "" }

// The Runner option exists to supply the seam for a source that is not a SQL
// provider, so a caller reaching for it has already said what this is. Asking
// whether the runner ALSO implements Provider left a pure one — the ordinary
// case — looking like SQL, which reopened the bypass on the exact path built
// for the sources that have it.
func TestExecute_RefusesAnUnverifiableFilterOnTheRunnerPathToo(t *testing.T) {
	src := &pureRunner{}
	e := NewQueryExecutor(QueryExecutorOptions{
		Runner:      src,
		FilterField: "country",
	})

	_, err := e.ExecuteNative(context.Background(),
		gowarehouse.NativeQuery{Text: `{"metrics":["sessions"],"dimensions":["country"]}`},
		"traffic", FixOpts{})

	if err == nil {
		t.Fatal("a filter that cannot be verified was treated as satisfied on the Runner path")
	}
	if len(src.ran) != 0 {
		t.Errorf("the query ran anyway: %+v", src.ran)
	}
}

// With no filter configured there is nothing to verify and nothing to refuse.
// Scope for such a source rests on its credential, which is a property of the
// connection rather than of the query.
func TestExecute_RunsANativeQueryWhenNoFilterIsConfigured(t *testing.T) {
	src := &nativeProvider{}
	e := NewQueryExecutor(QueryExecutorOptions{Warehouse: src})

	if _, err := e.ExecuteNative(context.Background(),
		gowarehouse.NativeQuery{Text: `{"metrics":["sessions"]}`}, "traffic", FixOpts{}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(src.ran) != 1 {
		t.Fatalf("query ran %d times, want 1", len(src.ran))
	}
}

// And a SQL warehouse is untouched: the substring check is still what it was,
// because in SQL the field name appearing in the text at least means a
// predicate probably mentions it.
func TestVerifyFilter_SQLBehaviourIsUnchanged(t *testing.T) {
	e := &QueryExecutor{filterField: "app_id"}

	if err := e.verifyFilter("SELECT * FROM t WHERE app_id = 'a'"); err != nil {
		t.Errorf("a SQL query naming the filter field was refused: %v", err)
	}
	if err := e.verifyFilter("SELECT * FROM t"); err == nil {
		t.Error("a SQL query with no mention of the filter field was accepted")
	}
	// Case-insensitively, as before.
	if err := e.verifyFilter("SELECT * FROM t WHERE APP_ID = 'a'"); err != nil {
		t.Errorf("case sensitivity changed: %v", err)
	}
}
