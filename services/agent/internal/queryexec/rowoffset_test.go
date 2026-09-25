package queryexec

import (
	"context"
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// capOffsetRunner recognises both halves of a paginated statement, as the five
// trailing-LIMIT dialects now do.
type capOffsetRunner struct {
	rows int
}

func (r capOffsetRunner) RunQuery(context.Context, gowarehouse.NativeQuery) (*gowarehouse.QueryResult, error) {
	out := make([]map[string]any, r.rows)
	for i := range out {
		out[i] = map[string]any{"i": i}
	}
	return &gowarehouse.QueryResult{Rows: out}, nil
}
func (capOffsetRunner) QueryLanguage() string  { return "" }
func (capOffsetRunner) QueryFixPrompt() string { return "" }
func (capOffsetRunner) RowCap(q string) (int, bool) {
	return gowarehouse.TrailingLimit(q)
}
func (capOffsetRunner) RowOffset(q string) (int, bool) {
	return gowarehouse.TrailingOffset(q)
}

func kinds(cav []gowarehouse.QualityCaveat) []string {
	out := make([]string, 0, len(cav))
	for _, c := range cav {
		out = append(out, string(c.Kind))
	}
	return out
}

// The case the cap check alone misses: a final page. 17 rows never equals the
// limit of 100, so nothing trips — yet a hundred rows were deliberately skipped
// and the page reads exactly like a complete small result.
func TestExecute_APaginatedFinalPageIsStillCaveated(t *testing.T) {
	e := NewQueryExecutor(QueryExecutorOptions{
		Runner: capOffsetRunner{rows: 17}, ProviderSlug: "queryexec-sql-probe",
	})
	res, err := e.Execute(context.Background(), "SELECT * FROM t ORDER BY x LIMIT 100 OFFSET 100", "page 2")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	got := kinds(res.Quality)
	if len(got) != 1 || got[0] != string(gowarehouse.QualityWithheld) {
		t.Fatalf("caveats = %v, want exactly the withheld/page caveat", got)
	}
	if !strings.Contains(res.Quality[0].Detail, "skipped the first 100") {
		t.Errorf("detail = %q", res.Quality[0].Detail)
	}
}

// A full page trips both: it is capped AND it is a page.
func TestExecute_AFullPageCarriesBothCaveats(t *testing.T) {
	e := NewQueryExecutor(QueryExecutorOptions{
		Runner: capOffsetRunner{rows: 100}, ProviderSlug: "queryexec-sql-probe",
	})
	res, _ := e.Execute(context.Background(), "SELECT * FROM t ORDER BY x LIMIT 100 OFFSET 100", "page 2")
	got := kinds(res.Quality)
	if len(got) != 2 {
		t.Fatalf("caveats = %v, want both truncated and withheld", got)
	}
}

// An unpaginated capped query is unchanged, and an unpaginated uncapped one still
// carries nothing — this must not start caveating sound results.
func TestExecute_UnpaginatedResultsAreUnchanged(t *testing.T) {
	capped := NewQueryExecutor(QueryExecutorOptions{
		Runner: capOffsetRunner{rows: 15}, ProviderSlug: "queryexec-sql-probe",
	})
	res, _ := capped.Execute(context.Background(), "SELECT * FROM t LIMIT 15", "capped")
	if got := kinds(res.Quality); len(got) != 1 || got[0] != string(gowarehouse.QualityTruncated) {
		t.Errorf("caveats = %v, want just the cap caveat", got)
	}

	clean := NewQueryExecutor(QueryExecutorOptions{
		Runner: capOffsetRunner{rows: 3}, ProviderSlug: "queryexec-sql-probe",
	})
	res2, _ := clean.Execute(context.Background(), "SELECT * FROM t", "all of it")
	if got := kinds(res2.Quality); len(got) != 0 {
		t.Errorf("caveats = %v, want none for a complete result", got)
	}
}

// A source that cannot read an offset yields no page caveat — unchanged behaviour.
func TestExecute_ASourceWithoutOffsetSupportIsUnaffected(t *testing.T) {
	e := NewQueryExecutor(QueryExecutorOptions{
		Runner: capOnlyRunner{rows: 17}, ProviderSlug: "queryexec-sql-probe",
	})
	res, _ := e.Execute(context.Background(), "SELECT * FROM t LIMIT 100 OFFSET 100", "page 2")
	if got := kinds(res.Quality); len(got) != 0 {
		t.Errorf("caveats = %v, want none from a source that cannot read an offset", got)
	}
}

type capOnlyRunner struct{ rows int }

func (r capOnlyRunner) RunQuery(context.Context, gowarehouse.NativeQuery) (*gowarehouse.QueryResult, error) {
	out := make([]map[string]any, r.rows)
	for i := range out {
		out[i] = map[string]any{"i": i}
	}
	return &gowarehouse.QueryResult{Rows: out}, nil
}
func (capOnlyRunner) QueryLanguage() string       { return "" }
func (capOnlyRunner) QueryFixPrompt() string      { return "" }
func (capOnlyRunner) RowCap(q string) (int, bool) { return gowarehouse.TrailingLimit(q) }
