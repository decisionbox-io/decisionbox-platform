package askserve

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// rowsOf builds n distinct result rows, so a cap can be told from a full read.
func rowsOf(n int) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, n)
	for i := range n {
		out = append(out, map[string]interface{}{"id": int64(i + 1), "name": fmt.Sprintf("row-%d", i+1)})
	}
	return out
}

// queryRunner is the bound re-read under test, over a real QueryExecutor on a
// mock warehouse — the tenant filter and the no-repair path are the executor's
// own, so nothing about them is stubbed here.
func queryRunner(t *testing.T, cfg Config, wh *testutil.MockWarehouseProvider, filterField string, fixer queryexec.SQLFixer) MutationQuery {
	t.Helper()
	exec := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
		Warehouse:   wh,
		SQLFixer:    fixer,
		MaxRetries:  5,
		FilterField: filterField,
		FilterValue: "TR",
	})
	rt := NewProjectRuntime(ProjectRuntimeOptions{
		PrimaryID: "default",
		Datasources: []DatasourceInfo{
			{ID: "default", Label: "Default"},
			{ID: "second", Label: "Second"},
		},
		Build: func(context.Context, string) (*WarehouseConn, error) {
			return &WarehouseConn{Executor: exec}, nil
		},
	})
	r := &runner{cfg: cfg, store: &fakeStore{}}
	st := &turnState{ctx: context.Background(), req: TurnRequest{ProjectID: "p1", SessionID: "s1", TurnID: "t1"}}
	return r.mutationQuery(rt, st)
}

func defaultQueryCfg() Config {
	return Config{MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
}

// The happy path: the named datasource is queried, the rows come back capped,
// and RowCount is the source's own count so a caller can say "showing N of M".
func TestMutationQuery_ReturnsCappedRowsAndTheTrueCount(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"id", "name"},
		Rows:    rowsOf(400),
	}
	run := queryRunner(t, defaultQueryCfg(), wh, "", nil)

	res, err := run(context.Background(), MutationQueryRequest{
		DatasourceID: "default", SQL: "SELECT id, name FROM orders", MaxRows: 250,
	})
	if err != nil {
		t.Fatalf("re-read error: %v", err)
	}
	if len(res.Rows) != 250 {
		t.Errorf("rows = %d, want the 250 asked for", len(res.Rows))
	}
	if res.RowCount != 400 {
		t.Errorf("RowCount = %d, want the source's 400 so 'showing 250 of 400' is true", res.RowCount)
	}
	// Columns are derived exactly as a first read's are.
	if strings.Join(res.Columns, ",") != "id,name" {
		t.Errorf("columns = %v", res.Columns)
	}
	if res.Rows[0]["id"] != int64(1) || res.Rows[249]["id"] != int64(250) {
		t.Error("the cap should keep the leading rows in order")
	}
}

// A result smaller than the cap comes back whole, and RowCount agrees with it.
func TestMutationQuery_SmallResultIsNotTruncated(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	wh.DefaultResult = &gowarehouse.QueryResult{Columns: []string{"id"}, Rows: rowsOf(7)}
	run := queryRunner(t, defaultQueryCfg(), wh, "", nil)

	res, err := run(context.Background(), MutationQueryRequest{
		DatasourceID: "default", SQL: "SELECT id FROM orders", MaxRows: 1000,
	})
	if err != nil {
		t.Fatalf("re-read error: %v", err)
	}
	if len(res.Rows) != 7 || res.RowCount != 7 {
		t.Errorf("rows=%d count=%d, want 7 and 7", len(res.Rows), res.RowCount)
	}
}

// MaxRows above the loop's fetch cap is clamped to it, and 0 means the cap —
// the process memory bound summarizeResult applies for the same reason.
func TestMutationQuery_ClampsToTheFetchCap(t *testing.T) {
	cfg := defaultQueryCfg()
	cfg.MaxFetchRows = 100
	for _, asked := range []int{0, 5000} {
		t.Run(fmt.Sprintf("max_rows=%d", asked), func(t *testing.T) {
			wh := testutil.NewMockWarehouseProvider("ds")
			wh.DefaultResult = &gowarehouse.QueryResult{Columns: []string{"id"}, Rows: rowsOf(400)}
			run := queryRunner(t, cfg, wh, "", nil)

			res, err := run(context.Background(), MutationQueryRequest{
				DatasourceID: "default", SQL: "SELECT id FROM orders", MaxRows: asked,
			})
			if err != nil {
				t.Fatalf("re-read error: %v", err)
			}
			if len(res.Rows) != 100 {
				t.Errorf("rows = %d, want the fetch cap of 100", len(res.Rows))
			}
			if res.RowCount != 400 {
				t.Errorf("RowCount = %d, want the uncapped 400", res.RowCount)
			}
		})
	}
}

// An unknown datasource is refused, and no connection is built for it:
// reading the wrong warehouse is the worst outcome available here.
func TestMutationQuery_RefusesAnUnknownOrMissingDatasource(t *testing.T) {
	cases := map[string]string{
		"unknown": "nope",
		"empty":   "",
		"blank":   "   ",
	}
	for name, dsID := range cases {
		t.Run(name, func(t *testing.T) {
			wh := testutil.NewMockWarehouseProvider("ds")
			built := 0
			exec := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{Warehouse: wh, MaxRetries: 1})
			rt := NewProjectRuntime(ProjectRuntimeOptions{
				PrimaryID:   "default",
				Datasources: []DatasourceInfo{{ID: "default"}},
				Build: func(context.Context, string) (*WarehouseConn, error) {
					built++
					return &WarehouseConn{Executor: exec}, nil
				},
			})
			r := &runner{cfg: defaultQueryCfg(), store: &fakeStore{}}
			run := r.mutationQuery(rt, &turnState{ctx: context.Background(), req: TurnRequest{ProjectID: "p1"}})

			if _, err := run(context.Background(), MutationQueryRequest{DatasourceID: dsID, SQL: "SELECT 1"}); err == nil {
				t.Fatal("expected a refusal")
			}
			if built != 0 {
				t.Errorf("no connection should be built for a datasource we refuse, got %d", built)
			}
			if len(wh.Calls) != 0 {
				t.Errorf("no query should run, got %d", len(wh.Calls))
			}
		})
	}
}

// An empty query is refused rather than sent to the warehouse.
func TestMutationQuery_RefusesAnEmptyQuery(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	run := queryRunner(t, defaultQueryCfg(), wh, "", nil)

	if _, err := run(context.Background(), MutationQueryRequest{DatasourceID: "default", SQL: "  "}); err == nil {
		t.Fatal("expected a refusal for an empty query")
	}
	if len(wh.Calls) != 0 {
		t.Errorf("nothing should reach the warehouse, got %d calls", len(wh.Calls))
	}
}

// The tenant filter is the executor's, and it still applies: a re-read is not
// a way around the scope check.
func TestMutationQuery_EnforcesTheTenantFilter(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	wh.DefaultResult = &gowarehouse.QueryResult{Columns: []string{"id"}, Rows: rowsOf(3)}
	run := queryRunner(t, defaultQueryCfg(), wh, "country", nil)

	_, err := run(context.Background(), MutationQueryRequest{
		DatasourceID: "default", SQL: "SELECT id FROM orders", MaxRows: 100,
	})
	if err == nil || !strings.Contains(err.Error(), "security violation") {
		t.Fatalf("an unscoped re-read must be refused, got %v", err)
	}

	// The same query WITH the filter field runs.
	res, err := run(context.Background(), MutationQueryRequest{
		DatasourceID: "default", SQL: "SELECT id FROM orders WHERE country = 'TR'", MaxRows: 100,
	})
	if err != nil {
		t.Fatalf("a scoped re-read should run: %v", err)
	}
	if len(res.Rows) != 3 {
		t.Errorf("rows = %d, want 3", len(res.Rows))
	}
}

// A failure is returned as a failure, and the SQL fixer is never asked to
// rewrite the query — the rows must be this query's rows or none.
func TestMutationQuery_DoesNotRepairAFailingQuery(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	wh.QueryError = fmt.Errorf("relation \"orders\" does not exist")
	fixer := &countingFixer{}
	run := queryRunner(t, defaultQueryCfg(), wh, "", fixer)

	_, err := run(context.Background(), MutationQueryRequest{
		DatasourceID: "default", SQL: "SELECT id FROM orders", MaxRows: 100,
	})
	if err == nil {
		t.Fatal("a failing re-read must be an error")
	}
	if fixer.calls != 0 {
		t.Errorf("the fixer must not be called on a re-read, got %d calls", fixer.calls)
	}
}

// A connection that cannot be built fails the re-read rather than the turn.
func TestMutationQuery_SurfacesAConnectFailure(t *testing.T) {
	rt := NewProjectRuntime(ProjectRuntimeOptions{
		PrimaryID:   "default",
		Datasources: []DatasourceInfo{{ID: "default"}},
		Build: func(context.Context, string) (*WarehouseConn, error) {
			return nil, fmt.Errorf("dial tcp: connection refused")
		},
	})
	r := &runner{cfg: defaultQueryCfg(), store: &fakeStore{}}
	run := r.mutationQuery(rt, &turnState{ctx: context.Background(), req: TurnRequest{ProjectID: "p1"}})

	_, err := run(context.Background(), MutationQueryRequest{DatasourceID: "default", SQL: "SELECT 1"})
	if err == nil || !strings.Contains(err.Error(), "could not connect") {
		t.Fatalf("expected a connect failure, got %v", err)
	}
}

// A cancelled context returns promptly rather than hanging the generate.
func TestMutationQuery_HonoursACancelledContext(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	// The builder runs under context.WithoutCancel, so its Done channel is nil
	// and blocking on it would leave the goroutine alive for the rest of the
	// test process. Block on a channel the test owns and closes instead.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	rt := NewProjectRuntime(ProjectRuntimeOptions{
		PrimaryID:   "default",
		Datasources: []DatasourceInfo{{ID: "default"}},
		Build: func(context.Context, string) (*WarehouseConn, error) {
			<-release
			return nil, fmt.Errorf("released")
		},
	})
	r := &runner{cfg: defaultQueryCfg(), store: &fakeStore{}}
	run := r.mutationQuery(rt, &turnState{ctx: context.Background(), req: TurnRequest{ProjectID: "p1"}})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := run(ctx, MutationQueryRequest{DatasourceID: "default", SQL: "SELECT 1"})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error once the context is done")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the re-read did not return after its context was cancelled")
	}
	_ = wh
}

// countingFixer records whether the repair path was entered at all.
type countingFixer struct{ calls int }

func (c *countingFixer) FixSQL(context.Context, string, string, int, queryexec.FixOpts) (queryexec.FixResult, error) {
	c.calls++
	return queryexec.FixResult{FixedSQL: "SELECT 1"}, nil
}

// --- what the platform review surfaced -----------------------------------

// Re-reads do not spend the model's query budget, so they need a bound of
// their own: otherwise "not charged" would mean "not limited".
func TestMutationQuery_IsBoundedPerTurn(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	wh.DefaultResult = &gowarehouse.QueryResult{Columns: []string{"id"}, Rows: rowsOf(5)}
	cfg := defaultQueryCfg()
	cfg.MaxReReadsPerTurn = 3
	run := queryRunner(t, cfg, wh, "", nil)

	for i := 1; i <= 3; i++ {
		if _, err := run(context.Background(), MutationQueryRequest{DatasourceID: "default", SQL: "SELECT id FROM t"}); err != nil {
			t.Fatalf("re-read %d should be allowed: %v", i, err)
		}
	}
	_, err := run(context.Background(), MutationQueryRequest{DatasourceID: "default", SQL: "SELECT id FROM t"})
	if err == nil {
		t.Fatal("the fourth re-read must be refused")
	}
	if !strings.Contains(err.Error(), "already made its 3") {
		t.Errorf("the refusal should name the bound: %v", err)
	}
	// And the model's own query budget is untouched by any of it.
	if len(wh.Calls) != 3 {
		t.Errorf("warehouse calls = %d, want 3", len(wh.Calls))
	}
}

// The read runs under the TURN's context, so a plugin handing in a detached
// one cannot lose the project id a warehouse middleware scopes by.
func TestMutationQuery_RunsUnderTheTurnContext(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	wh.DefaultResult = &gowarehouse.QueryResult{Columns: []string{"id"}, Rows: rowsOf(3)}
	exec := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{Warehouse: wh, MaxRetries: 1})

	var sawWarehouse string
	rt := NewProjectRuntime(ProjectRuntimeOptions{
		PrimaryID:   "default",
		Datasources: []DatasourceInfo{{ID: "default"}},
		Build: func(ctx context.Context, _ string) (*WarehouseConn, error) {
			sawWarehouse = gowarehouse.WarehouseIDFromContext(ctx)
			return &WarehouseConn{Executor: exec}, nil
		},
	})
	r := &runner{cfg: defaultQueryCfg(), store: &fakeStore{}}
	turnCtx := gowarehouse.WithWarehouseID(context.Background(), "turn-scope")
	st := &turnState{ctx: turnCtx, req: TurnRequest{ProjectID: "p1"}}
	run := r.mutationQuery(rt, st)

	// A caller that passes a brand-new context must not detach the read.
	if _, err := run(context.Background(), MutationQueryRequest{DatasourceID: "default", SQL: "SELECT id FROM t"}); err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if sawWarehouse != "default" {
		t.Errorf("the datasource should be stamped on the context, got %q", sawWarehouse)
	}
}

// The caller's context may only SHORTEN the read: a cancelled caller stops it
// even though the turn is still live.
func TestMutationQuery_CallerContextCanOnlyShorten(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	wh.DefaultResult = &gowarehouse.QueryResult{Columns: []string{"id"}, Rows: rowsOf(3)}
	exec := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{Warehouse: wh, MaxRetries: 1})
	rt := NewProjectRuntime(ProjectRuntimeOptions{
		PrimaryID:   "default",
		Datasources: []DatasourceInfo{{ID: "default"}},
		Build: func(context.Context, string) (*WarehouseConn, error) {
			return &WarehouseConn{Executor: exec}, nil
		},
	})
	r := &runner{cfg: defaultQueryCfg(), store: &fakeStore{}}
	run := r.mutationQuery(rt, &turnState{ctx: context.Background(), req: TurnRequest{ProjectID: "p1"}})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := run(ctx, MutationQueryRequest{DatasourceID: "default", SQL: "SELECT id FROM t"}); err == nil {
		t.Fatal("a cancelled caller must stop the read even while the turn lives")
	}
}

// A source-declared caveat survives the re-read. Dropping it would hand a
// caller len(Rows) == RowCount for a sampled result, which reads as the whole
// population.
func TestMutationQuery_CarriesQualityCaveats(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"id"}, Rows: rowsOf(10),
		Quality: []gowarehouse.QualityCaveat{{Kind: "sampled", Detail: "1% sample"}},
	}
	run := queryRunner(t, defaultQueryCfg(), wh, "", nil)

	res, err := run(context.Background(), MutationQueryRequest{DatasourceID: "default", SQL: "SELECT id FROM t", MaxRows: 100})
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if len(res.Quality) != 1 || res.Quality[0].Kind != "sampled" {
		t.Errorf("the source's caveat must survive, got %+v", res.Quality)
	}
}

// The capped rows are copied, not resliced: a reslice keeps the whole
// warehouse response alive for as long as the caller holds the result.
func TestMutationQuery_CappedRowsDoNotRetainTheWholeResult(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	wh.DefaultResult = &gowarehouse.QueryResult{Columns: []string{"id"}, Rows: rowsOf(500)}
	run := queryRunner(t, defaultQueryCfg(), wh, "", nil)

	res, err := run(context.Background(), MutationQueryRequest{DatasourceID: "default", SQL: "SELECT id FROM t", MaxRows: 10})
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if len(res.Rows) != 10 {
		t.Fatalf("rows = %d, want 10", len(res.Rows))
	}
	if cap(res.Rows) != 10 {
		t.Errorf("cap = %d, want 10 — a reslice would keep all 500 rows alive", cap(res.Rows))
	}
}

// The query executes verbatim: trimming is only how a blank request is
// rejected, because a trailing character can belong to a quoted identifier.
func TestMutationQuery_ExecutesTheQueryVerbatim(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	wh.DefaultResult = &gowarehouse.QueryResult{Columns: []string{"id"}, Rows: rowsOf(1)}
	run := queryRunner(t, defaultQueryCfg(), wh, "", nil)

	sql := "SELECT id FROM t  "
	if _, err := run(context.Background(), MutationQueryRequest{DatasourceID: "default", SQL: sql}); err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if len(wh.Calls) != 1 || wh.Calls[0].Query != sql {
		t.Errorf("the warehouse should see the query unchanged, got %q", wh.Calls[0].Query)
	}

	// Blank is still refused.
	if _, err := run(context.Background(), MutationQueryRequest{DatasourceID: "default", SQL: "   \n"}); err == nil {
		t.Error("a blank query must be refused")
	}
}
