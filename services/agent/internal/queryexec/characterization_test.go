package queryexec

// Characterization tests for the query-execution repair loop.
//
// These pin the CURRENT observable behaviour of QueryExecutor — how many times
// the warehouse is called, how many times the fixer is called, the exact error
// strings callers match on, and the shape of the recorded repair trail — so
// that extracting a query-runner seam underneath it is provably behaviour-
// preserving. They are deliberately exact where the surrounding unit tests are
// loose (several assert only `err != nil`), because "the loop still fails" is
// not the property that matters here: "the loop fails after the same number of
// warehouse round-trips, with the same message, having recorded the same
// history" is.
//
// If a change makes one of these fail, that is a behaviour change. Either it
// was intended — in which case update the test in the same commit and say so —
// or the change is wrong. Do not relax an assertion to make it pass.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// queryCalls counts the Query calls the executor made against the mock.
func queryCalls(wh *testutil.MockWarehouseProvider) int {
	n := 0
	for _, c := range wh.Calls {
		if c.Method == "Query" {
			n++
		}
	}
	return n
}

// TestCharacterization_RetryBudget pins the loop's arithmetic: MaxRetries=N
// costs N+1 warehouse round-trips and N fixer calls, and every failed attempt
// leaves one entry in Errors. The off-by-one here is load-bearing — the final
// attempt is executed but never repaired, because the budget check precedes
// the fix call.
func TestCharacterization_RetryBudget(t *testing.T) {
	for _, maxRetries := range []int{0, 1, 2, 5} {
		t.Run(fmt.Sprintf("max_retries_%d", maxRetries), func(t *testing.T) {
			wh := testutil.NewMockWarehouseProvider("test_dataset")
			wh.QueryError = errors.New("persistent error")
			fixer := &mockFixer{}

			e := NewQueryExecutor(QueryExecutorOptions{
				Warehouse:  wh,
				SQLFixer:   fixer,
				MaxRetries: maxRetries,
			})

			// MaxRetries=0 is not "no retries": NewQueryExecutor treats the zero
			// value as "unset" and substitutes the default of 5.
			effective := maxRetries
			if maxRetries == 0 {
				effective = 5
			}

			result, err := e.Execute(context.Background(), "SELECT 1", "test")
			if err == nil {
				t.Fatal("expected the run to fail once the retry budget is exhausted")
			}
			if got, want := queryCalls(wh), effective+1; got != want {
				t.Errorf("warehouse Query calls = %d, want %d (one per attempt, budget+1)", got, want)
			}
			if got, want := fixer.Calls, effective; got != want {
				t.Errorf("fixer calls = %d, want %d (the last attempt is not repaired)", got, want)
			}
			if result == nil {
				t.Fatal("a budget-exhausted Execute must still return its partial result")
			}
			if got, want := result.FixAttempts, effective; got != want {
				t.Errorf("FixAttempts = %d, want %d", got, want)
			}
			if got, want := len(result.Errors), effective+1; got != want {
				t.Errorf("len(Errors) = %d, want %d (one per failed attempt)", got, want)
			}
			if got, want := len(result.FixHistory), effective; got != want {
				t.Errorf("len(FixHistory) = %d, want %d", got, want)
			}
		})
	}
}

// TestCharacterization_ErrorShapes pins the exact wording of every terminal
// error the executor produces. Callers upstream match on these strings, and
// the enterprise ask path surfaces some of them to end users.
func TestCharacterization_ErrorShapes(t *testing.T) {
	tests := []struct {
		name       string
		setup      func() *QueryExecutor
		query      string
		wantErr    string
		wantResult bool // whether a non-nil partial result accompanies the error
	}{
		{
			name: "budget exhausted names the attempt count and wraps the warehouse error",
			setup: func() *QueryExecutor {
				wh := testutil.NewMockWarehouseProvider("d")
				wh.QueryError = errors.New("boom")
				return NewQueryExecutor(QueryExecutorOptions{
					Warehouse: wh, SQLFixer: &mockFixer{}, MaxRetries: 2,
				})
			},
			query:      "SELECT 1",
			wantErr:    "query failed after 3 attempts: boom",
			wantResult: true,
		},
		{
			name: "no fixer configured is reported as such, not as an exhausted budget",
			setup: func() *QueryExecutor {
				wh := testutil.NewMockWarehouseProvider("d")
				wh.QueryError = errors.New("boom")
				return NewQueryExecutor(QueryExecutorOptions{
					Warehouse: wh, MaxRetries: 3,
				})
			},
			query:      "SELECT 1",
			wantErr:    "query failed and no SQL fixer available: boom",
			wantResult: true,
		},
		{
			name: "a failing fixer surfaces as a fix failure, not a query failure",
			setup: func() *QueryExecutor {
				wh := testutil.NewMockWarehouseProvider("d")
				wh.QueryError = errors.New("boom")
				return NewQueryExecutor(QueryExecutorOptions{
					Warehouse: wh, SQLFixer: &mockFixer{Error: errors.New("llm down")}, MaxRetries: 3,
				})
			},
			query:      "SELECT 1",
			wantErr:    "failed to fix SQL query: llm down",
			wantResult: true,
		},
		{
			name: "the incoming query is filter-checked before the warehouse is touched",
			setup: func() *QueryExecutor {
				wh := testutil.NewMockWarehouseProvider("d")
				return NewQueryExecutor(QueryExecutorOptions{
					Warehouse: wh, SQLFixer: &mockFixer{},
					FilterField: "app_id", FilterValue: "acme",
				})
			},
			query:      "SELECT * FROM t",
			wantErr:    "security violation: query must filter by app_id for security",
			wantResult: false,
		},
		{
			name: "a repaired query that drops the filter is rejected with its own message",
			setup: func() *QueryExecutor {
				wh := testutil.NewMockWarehouseProvider("d")
				wh.QueryError = errors.New("boom")
				return NewQueryExecutor(QueryExecutorOptions{
					Warehouse: wh,
					// The repaired SQL no longer mentions app_id.
					SQLFixer:    &mockFixer{FixedQuery: "SELECT * FROM t"},
					MaxRetries:  3,
					FilterField: "app_id", FilterValue: "acme",
				})
			},
			query:      "SELECT * FROM t WHERE app_id = 'acme'",
			wantErr:    "fixed query security violation: query must filter by app_id for security",
			wantResult: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.setup().Execute(context.Background(), tc.query, "test")
			if err == nil {
				t.Fatal("expected an error")
			}
			if err.Error() != tc.wantErr {
				t.Errorf("error = %q, want %q", err.Error(), tc.wantErr)
			}
			if got := result != nil; got != tc.wantResult {
				t.Errorf("non-nil result = %v, want %v", got, tc.wantResult)
			}
		})
	}
}

// TestCharacterization_FilterCheckPrecedesExecution pins that a scope
// violation costs zero warehouse round-trips — the check runs before the loop,
// not inside it.
func TestCharacterization_FilterCheckPrecedesExecution(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("d")
	e := NewQueryExecutor(QueryExecutorOptions{
		Warehouse: wh, FilterField: "app_id", FilterValue: "acme",
	})

	if _, err := e.Execute(context.Background(), "SELECT * FROM t", "test"); err == nil {
		t.Fatal("expected a security violation")
	}
	if n := queryCalls(wh); n != 0 {
		t.Errorf("warehouse Query calls = %d, want 0 — the filter check must precede execution", n)
	}
}

// TestCharacterization_HappyPath pins the untouched-query result shape: no
// repair recorded, FinalQuery identical to OriginalQuery, and exactly one
// warehouse round-trip.
func TestCharacterization_HappyPath(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("d")
	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"n"},
		Rows:    []map[string]interface{}{{"n": int64(1)}, {"n": int64(2)}},
	}
	fixer := &mockFixer{}
	e := NewQueryExecutor(QueryExecutorOptions{Warehouse: wh, SQLFixer: fixer})

	result, err := e.Execute(context.Background(), "SELECT n FROM t", "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if queryCalls(wh) != 1 {
		t.Errorf("warehouse Query calls = %d, want 1", queryCalls(wh))
	}
	if fixer.Calls != 0 {
		t.Errorf("fixer calls = %d, want 0 on the happy path", fixer.Calls)
	}
	if result.Fixed {
		t.Error("Fixed must be false when the first attempt succeeds")
	}
	if result.FixAttempts != 0 || len(result.FixHistory) != 0 || len(result.Errors) != 0 {
		t.Errorf("expected an empty repair trail, got attempts=%d history=%d errors=%d",
			result.FixAttempts, len(result.FixHistory), len(result.Errors))
	}
	if result.OriginalQuery != "SELECT n FROM t" || result.FinalQuery != result.OriginalQuery {
		t.Errorf("query fields = %q / %q, want both %q",
			result.OriginalQuery, result.FinalQuery, "SELECT n FROM t")
	}
	if result.RowCount != 2 || len(result.Data) != 2 {
		t.Errorf("RowCount/Data = %d/%d, want 2/2", result.RowCount, len(result.Data))
	}
}

// TestCharacterization_RepairTrailChains pins that a successful repair records
// the before/after SQL as a chain — each attempt's SQLBefore is the previous
// attempt's SQLAfter — and that the succeeding query becomes FinalQuery. The
// chain is what makes the history readable as a trajectory rather than a set.
func TestCharacterization_RepairTrailChains(t *testing.T) {
	const fixedSQL = "SELECT 1 -- repaired"
	wh := &failThenSucceedWarehouse{
		MockWarehouseProvider: testutil.NewMockWarehouseProvider("d"),
		failures:              2,
		err:                   errors.New("syntax error"),
	}
	fixer := &mockFixer{FixedQuery: fixedSQL}
	e := NewQueryExecutor(QueryExecutorOptions{Warehouse: wh, SQLFixer: fixer, MaxRetries: 5})
	e.SetStep(7)

	result, err := e.Execute(context.Background(), "SELECT 1", "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Fixed {
		t.Error("Fixed must be true when a later attempt succeeded")
	}
	if result.FinalQuery != fixedSQL {
		t.Errorf("FinalQuery = %q, want the repaired SQL %q", result.FinalQuery, fixedSQL)
	}
	if result.OriginalQuery != "SELECT 1" {
		t.Errorf("OriginalQuery = %q, want the query as submitted", result.OriginalQuery)
	}
	if got, want := len(result.FixHistory), 2; got != want {
		t.Fatalf("len(FixHistory) = %d, want %d", got, want)
	}
	if result.FixHistory[0].SQLBefore != "SELECT 1" {
		t.Errorf("first SQLBefore = %q, want the original query", result.FixHistory[0].SQLBefore)
	}
	for i, a := range result.FixHistory {
		if a.SQLAfter != fixedSQL {
			t.Errorf("attempt %d SQLAfter = %q, want %q", i, a.SQLAfter, fixedSQL)
		}
		if a.Attempt != i {
			t.Errorf("attempt %d records Attempt = %d, want %d", i, a.Attempt, i)
		}
		if a.Step != 7 {
			t.Errorf("attempt %d records Step = %d, want the executor's current step 7", i, a.Step)
		}
		if a.FixerError != "" {
			t.Errorf("attempt %d records FixerError = %q, want empty on a clean fix", i, a.FixerError)
		}
		if a.ErrorIn == "" {
			t.Errorf("attempt %d records no ErrorIn; the triggering error must be kept", i)
		}
		if a.InputTokens == 0 || a.OutputTokens == 0 {
			t.Errorf("attempt %d dropped token accounting (in=%d out=%d)", i, a.InputTokens, a.OutputTokens)
		}
		if a.Timestamp.IsZero() {
			t.Errorf("attempt %d has a zero Timestamp", i)
		}
	}
	if result.FixHistory[1].SQLBefore != result.FixHistory[0].SQLAfter {
		t.Errorf("history is not chained: attempt 1 SQLBefore = %q, attempt 0 SQLAfter = %q",
			result.FixHistory[1].SQLBefore, result.FixHistory[0].SQLAfter)
	}
}

// TestCharacterization_FixOptsForwardedOnEveryRetry pins that per-call
// grounding context reaches the fixer on every attempt, not just the first —
// the verifier relies on this to stop the model re-emitting a hallucinated
// column.
func TestCharacterization_FixOptsForwardedOnEveryRetry(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("d")
	wh.QueryError = errors.New("boom")
	fixer := &recordingFixer{}
	e := NewQueryExecutor(QueryExecutorOptions{Warehouse: wh, SQLFixer: fixer, MaxRetries: 3})

	opts := FixOpts{VerificationContext: "step 4 SQL: SELECT id FROM orders"}
	if _, err := e.ExecuteWithFixOpts(context.Background(), "SELECT 1", "test", opts); err == nil {
		t.Fatal("expected the run to fail")
	}
	if len(fixer.seen) != 3 {
		t.Fatalf("fixer called %d times, want 3", len(fixer.seen))
	}
	for i, got := range fixer.seen {
		if got != opts {
			t.Errorf("attempt %d received FixOpts %+v, want %+v unchanged", i, got, opts)
		}
	}
}

// TestCharacterization_ExecuteWithHistoryFields pins the QueryHistory record
// on both outcomes: it always carries the query as submitted (never the
// repaired one) and the fix count, and only the success path fills the
// result-derived fields.
func TestCharacterization_ExecuteWithHistoryFields(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		wh := testutil.NewMockWarehouseProvider("d")
		e := NewQueryExecutor(QueryExecutorOptions{Warehouse: wh, SQLFixer: &mockFixer{}})

		result, history := e.ExecuteWithHistory(context.Background(), "SELECT 1", "counting")
		if !history.Success {
			t.Error("Success must be true")
		}
		if history.Error != "" {
			t.Errorf("Error = %q, want empty on success", history.Error)
		}
		if history.Query != "SELECT 1" || history.Purpose != "counting" {
			t.Errorf("Query/Purpose = %q/%q, want %q/%q", history.Query, history.Purpose, "SELECT 1", "counting")
		}
		if history.RowsReturned != result.RowCount {
			t.Errorf("RowsReturned = %d, want the result's RowCount %d", history.RowsReturned, result.RowCount)
		}
		if history.FixAttempts != result.FixAttempts {
			t.Errorf("FixAttempts = %d, want %d", history.FixAttempts, result.FixAttempts)
		}
		if history.ExecutedAt.IsZero() {
			t.Error("ExecutedAt must be stamped")
		}
	})

	t.Run("failure", func(t *testing.T) {
		wh := testutil.NewMockWarehouseProvider("d")
		wh.QueryError = errors.New("boom")
		e := NewQueryExecutor(QueryExecutorOptions{Warehouse: wh, SQLFixer: &mockFixer{}, MaxRetries: 1})

		result, history := e.ExecuteWithHistory(context.Background(), "SELECT 1", "counting")
		if history.Success {
			t.Error("Success must be false")
		}
		if !strings.Contains(history.Error, "query failed after 2 attempts") {
			t.Errorf("Error = %q, want it to carry the executor's error", history.Error)
		}
		if history.RowsReturned != 0 {
			t.Errorf("RowsReturned = %d, want 0 on failure", history.RowsReturned)
		}
		if history.FixAttempts != result.FixAttempts {
			t.Errorf("FixAttempts = %d, want the partial result's %d", history.FixAttempts, result.FixAttempts)
		}
	})
}

// --- helpers -------------------------------------------------------------

// failThenSucceedWarehouse fails its first `failures` Query calls, then
// delegates to the mock. It exists because the shared mock is all-or-nothing:
// a repair trajectory needs a warehouse that recovers.
type failThenSucceedWarehouse struct {
	*testutil.MockWarehouseProvider
	failures int
	err      error
	calls    int
}

func (w *failThenSucceedWarehouse) Query(ctx context.Context, query string, params map[string]interface{}) (*gowarehouse.QueryResult, error) {
	w.calls++
	if w.calls <= w.failures {
		w.MockWarehouseProvider.Calls = append(w.MockWarehouseProvider.Calls,
			testutil.MockWarehouseCall{Method: "Query", Query: query})
		return nil, w.err
	}
	return w.MockWarehouseProvider.Query(ctx, query, params)
}

// recordingFixer captures the FixOpts of every call so a test can assert they
// are forwarded unchanged across retries.
type recordingFixer struct {
	seen []FixOpts
}

func (f *recordingFixer) FixSQL(_ context.Context, query string, _ string, _ int, opts FixOpts) (FixResult, error) {
	f.seen = append(f.seen, opts)
	return FixResult{FixedSQL: query, InputTokens: 1, OutputTokens: 1, DurationMs: 1}, nil
}
