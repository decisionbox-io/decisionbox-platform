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
	st := &turnState{req: TurnRequest{ProjectID: "p1", SessionID: "s1", TurnID: "t1"}}
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
			run := r.mutationQuery(rt, &turnState{req: TurnRequest{ProjectID: "p1"}})

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
	run := r.mutationQuery(rt, &turnState{req: TurnRequest{ProjectID: "p1"}})

	_, err := run(context.Background(), MutationQueryRequest{DatasourceID: "default", SQL: "SELECT 1"})
	if err == nil || !strings.Contains(err.Error(), "could not connect") {
		t.Fatalf("expected a connect failure, got %v", err)
	}
}

// A cancelled context returns promptly rather than hanging the generate.
func TestMutationQuery_HonoursACancelledContext(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	rt := NewProjectRuntime(ProjectRuntimeOptions{
		PrimaryID:   "default",
		Datasources: []DatasourceInfo{{ID: "default"}},
		Build: func(ctx context.Context, _ string) (*WarehouseConn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	r := &runner{cfg: defaultQueryCfg(), store: &fakeStore{}}
	run := r.mutationQuery(rt, &turnState{req: TurnRequest{ProjectID: "p1"}})

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
