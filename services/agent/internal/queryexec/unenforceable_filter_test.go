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

// wrappedNative is a provider that HOLDS a native source but exposes only the
// Provider surface — what a middleware wrapper looks like when it does not
// re-expose the optional interfaces. It is not hypothetical: the governance
// wrapper did exactly this until it was fixed, and the middleware contract
// only requires returning a Provider, so the next one may do it again.
type wrappedNative struct {
	gowarehouse.Provider
	ran int
}

func (w *wrappedNative) Query(context.Context, string, map[string]interface{}) (*gowarehouse.QueryResult, error) {
	w.ran++
	return &gowarehouse.QueryResult{}, nil
}

// The registry is the only statement of a source's query language that a
// wrapper cannot erase. Reading it by slug is what keeps a SECURITY guard from
// depending on every present and future middleware preserving an optional
// interface — a dependency that has already failed once.
func TestExecute_RefusesThroughAWrapperThatHidesTheSeam(t *testing.T) {
	wrapped := &wrappedNative{}
	e := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:    wrapped, // the seam is gone from the type
		ProviderSlug: "queryexec-native-probe",
		FilterField:  "country",
	})

	_, err := e.ExecuteNative(context.Background(),
		gowarehouse.NativeQuery{Text: `{"dimensions":["country"]}`}, "traffic", FixOpts{})

	if err == nil {
		t.Fatal("a wrapper hiding the query seam let an unverifiable filter through")
	}
	if wrapped.ran != 0 {
		t.Errorf("the query ran anyway (%d times)", wrapped.ran)
	}
	if !strings.Contains(err.Error(), "Probe Request (JSON)") {
		t.Errorf("error = %v, want the language the REGISTRY declares", err)
	}
}

// A cube has no tables to select from, so it has no SQL to write — whatever
// its metadata happens to fill in. A descriptor offers two ways to name a
// query language and a provider need only use one, so reading QueryLanguage
// alone would call a cube registered with a Dialect a SQL warehouse and skip
// the guard. This answer gates a security check, so it fails closed.
func TestNonSQLLanguageOf_ACubeIsNeverSQL(t *testing.T) {
	for slug, want := range map[string]string{
		"queryexec-cube-dialect-only": "Cube Request",
		"queryexec-cube-bare":         "this source's own query format",
	} {
		if got := gowarehouse.NonSQLLanguageOf(slug); got != want {
			t.Errorf("NonSQLLanguageOf(%q) = %q, want %q", slug, got, want)
		}
	}
}

// And the guard follows: a cube whose language is only a Dialect still refuses
// a filter it cannot verify.
func TestExecute_RefusesForACubeThatNamesNoQueryLanguage(t *testing.T) {
	wrapped := &wrappedNative{}
	e := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:    wrapped,
		ProviderSlug: "queryexec-cube-dialect-only",
		FilterField:  "country",
	})

	if _, err := e.ExecuteNative(context.Background(),
		gowarehouse.NativeQuery{Text: `{"dimensions":["country"]}`}, "traffic", FixOpts{}); err == nil {
		t.Fatal("a cube declaring only a Dialect was treated as a SQL warehouse")
	}
	if wrapped.ran != 0 {
		t.Errorf("the query ran anyway (%d times)", wrapped.ran)
	}
}

// A SQL warehouse is not made native by supplying its slug: the registry
// answers "" for a provider that declares no query language, which is every
// one of them, and an unregistered slug answers "" too — what every provider
// was before the descriptor existed.
func TestNonSQLLanguageOf_IsEmptyForSQLAndForTheUnknown(t *testing.T) {
	if got := gowarehouse.NonSQLLanguageOf("not-registered-anywhere"); got != "" {
		t.Errorf("unregistered slug = %q, want empty", got)
	}
	if got := gowarehouse.NonSQLLanguageOf("queryexec-sql-probe"); got != "" {
		t.Errorf("a SQL warehouse = %q, want empty — a dialect is not another language", got)
	}
}
