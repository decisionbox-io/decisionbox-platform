package queryexec

import (
	"context"
	"errors"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// qualityRunner is a QueryRunner that answers with a caveat-bearing result —
// the shape of a source that can silently degrade. Each call pops the next
// scripted response, so a repair sequence can hand back a different result per
// attempt.
type qualityRunner struct {
	responses []qualityResponse
	calls     int
}

type qualityResponse struct {
	result *gowarehouse.QueryResult
	err    error
}

func (r *qualityRunner) RunQuery(_ context.Context, _ gowarehouse.NativeQuery) (*gowarehouse.QueryResult, error) {
	i := r.calls
	r.calls++
	if i >= len(r.responses) {
		return nil, errors.New("qualityRunner: called more times than scripted")
	}
	resp := r.responses[i]
	return resp.result, resp.err
}

func (r *qualityRunner) QueryLanguage() string  { return "Report Request" }
func (r *qualityRunner) QueryFixPrompt() string { return "fix: {{ORIGINAL_SQL}} / {{ERROR_MESSAGE}}" }

func sampleRows() []map[string]interface{} {
	return []map[string]interface{}{{"sessions": 120}}
}

// TestExecute_CarriesQualityCaveatsToTheCaller is the point of the channel: a
// result the source declared degraded must arrive at the caller still saying so.
//
// Dropping the caveat here is the silent failure the field exists to prevent —
// the rows are well-formed, the query succeeded, and nothing downstream would
// have any reason to doubt a number computed from withheld data.
func TestExecute_CarriesQualityCaveatsToTheCaller(t *testing.T) {
	want := []gowarehouse.QualityCaveat{
		{Kind: gowarehouse.QualityWithheld, Detail: "rows below the reporting threshold omitted"},
		{Kind: gowarehouse.QualityTruncated, Detail: "tail collapsed into a catch-all row"},
	}
	runner := &qualityRunner{responses: []qualityResponse{
		{result: &gowarehouse.QueryResult{Columns: []string{"sessions"}, Rows: sampleRows(), Quality: want}},
	}}

	e := NewQueryExecutor(QueryExecutorOptions{Runner: runner})

	result, err := e.Execute(context.Background(), "sessions by channel", "test")
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil — a degraded result is still an answer", err)
	}
	if len(result.Quality) != len(want) {
		t.Fatalf("Quality has %d caveats, want %d", len(result.Quality), len(want))
	}
	for i := range want {
		if result.Quality[i] != want[i] {
			t.Errorf("Quality[%d] = %+v, want %+v", i, result.Quality[i], want[i])
		}
	}
	// The caveat must not cost the caller the rows it came with.
	if result.RowCount != 1 {
		t.Errorf("RowCount = %d, want 1 — a caveat annotates the result, it does not replace it", result.RowCount)
	}
}

// TestExecute_LeavesQualityNilForASoundResult pins the SQL path. Every existing
// warehouse returns a result with no caveats, and this asserts the executor
// invents none — a non-nil empty slice would read as "the source said something"
// to a consumer checking presence rather than length.
func TestExecute_LeavesQualityNilForASoundResult(t *testing.T) {
	runner := &qualityRunner{responses: []qualityResponse{
		{result: &gowarehouse.QueryResult{Columns: []string{"sessions"}, Rows: sampleRows()}},
	}}

	e := NewQueryExecutor(QueryExecutorOptions{Runner: runner})

	result, err := e.Execute(context.Background(), "SELECT 1", "test")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Quality != nil {
		t.Errorf("Quality = %+v, want nil for a source that declared no caveat", result.Quality)
	}
}

// TestExecute_ReportsTheSucceedingAttemptsQuality covers the repair path. The
// executor retries, and each attempt gets its own result — so the caveat that
// reaches the caller must be the one attached to the attempt whose rows they
// actually got, not a stale caveat from an earlier failure or an earlier
// success-shaped response.
func TestExecute_ReportsTheSucceedingAttemptsQuality(t *testing.T) {
	final := []gowarehouse.QualityCaveat{{Kind: gowarehouse.QualitySampled, Detail: "computed from a sample"}}
	runner := &qualityRunner{responses: []qualityResponse{
		{err: errors.New("unknown dimension")},
		{result: &gowarehouse.QueryResult{Columns: []string{"sessions"}, Rows: sampleRows(), Quality: final}},
	}}

	e := NewQueryExecutor(QueryExecutorOptions{
		Runner:   runner,
		SQLFixer: &mockFixer{FixedQuery: "sessions by date"},
	})

	result, err := e.Execute(context.Background(), "sessions by unknownDimension", "test")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Fixed {
		t.Fatal("expected the query to have been repaired; the scripted first attempt fails")
	}
	if len(result.Quality) != 1 || result.Quality[0] != final[0] {
		t.Errorf("Quality = %+v, want the caveat from the attempt that succeeded (%+v)", result.Quality, final)
	}
}

// TestExecute_NoQualityWhenEveryAttemptFails pins the failure path. There is no
// result, so there is nothing to declare a caveat about; reporting one would
// attribute a data-quality problem to a query that never returned data.
func TestExecute_NoQualityWhenEveryAttemptFails(t *testing.T) {
	runner := &qualityRunner{responses: []qualityResponse{
		{err: errors.New("boom")},
		{err: errors.New("boom")},
	}}

	e := NewQueryExecutor(QueryExecutorOptions{
		Runner:     runner,
		SQLFixer:   &mockFixer{FixedQuery: "still broken"},
		MaxRetries: 2,
	})

	result, err := e.Execute(context.Background(), "sessions", "test")
	if err == nil {
		t.Fatal("Execute() error = nil, want the underlying failure")
	}
	if result != nil && len(result.Quality) != 0 {
		t.Errorf("Quality = %+v, want empty — no result means no caveat", result.Quality)
	}
}
