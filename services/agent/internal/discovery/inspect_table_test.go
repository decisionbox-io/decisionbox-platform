package discovery

import (
	"context"
	"errors"
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
)

// fakeWarehouse is a gowarehouse.Provider stub that returns scripted
// schemas + query results. Defined inline so the unit tests don't depend
// on any real provider package.
type fakeWarehouse struct {
	schemas        map[string]*gowarehouse.TableSchema  // key: "dataset.table"
	schemaErrors   map[string]error
	queryResults   map[string]*gowarehouse.QueryResult // key: substring match of query
	queryErr       error
}

func newFakeWarehouse() *fakeWarehouse {
	return &fakeWarehouse{
		schemas:      map[string]*gowarehouse.TableSchema{},
		schemaErrors: map[string]error{},
		queryResults: map[string]*gowarehouse.QueryResult{},
	}
}

func (f *fakeWarehouse) Query(_ context.Context, q string, _ map[string]interface{}) (*gowarehouse.QueryResult, error) {
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	for needle, res := range f.queryResults {
		if strings.Contains(q, needle) {
			return res, nil
		}
	}
	return &gowarehouse.QueryResult{}, nil
}
func (f *fakeWarehouse) ListTables(_ context.Context) ([]string, error) { return nil, nil }
func (f *fakeWarehouse) ListTablesInDataset(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (f *fakeWarehouse) GetTableSchema(_ context.Context, _ string) (*gowarehouse.TableSchema, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeWarehouse) GetTableSchemaInDataset(_ context.Context, dataset, table string) (*gowarehouse.TableSchema, error) {
	key := dataset + "." + table
	if err, ok := f.schemaErrors[key]; ok {
		return nil, err
	}
	s, ok := f.schemas[key]
	if !ok {
		return nil, errors.New("table not found: " + key)
	}
	return s, nil
}
func (f *fakeWarehouse) GetDataset() string                         { return "" }
func (f *fakeWarehouse) SQLDialect() string                         { return "test" }
func (f *fakeWarehouse) SQLFixPrompt() string                       { return "" }
func (f *fakeWarehouse) ValidateReadOnly(_ context.Context) error   { return nil }
func (f *fakeWarehouse) HealthCheck(_ context.Context) error        { return nil }
func (f *fakeWarehouse) Close() error                               { return nil }

// Helpers ----------------------------------------------------------------

func newInspectExecutor(t *testing.T, wh *fakeWarehouse) *InspectTableExecutor {
	t.Helper()
	exec := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{Warehouse: wh})
	return &InspectTableExecutor{Warehouse: wh, Executor: exec, SampleLimit: 3}
}

// Tests ------------------------------------------------------------------

func TestInspect_RequiresDeps(t *testing.T) {
	_, err := (&InspectTableExecutor{}).Inspect(context.Background(), []string{"x.y"})
	if err == nil {
		t.Error("missing warehouse should error")
	}
	_, err = (&InspectTableExecutor{Warehouse: newFakeWarehouse()}).Inspect(context.Background(), []string{"x.y"})
	if err == nil {
		t.Error("missing executor should error")
	}
}

func TestInspect_HappyPath(t *testing.T) {
	wh := newFakeWarehouse()
	wh.schemas["sales.orders"] = &gowarehouse.TableSchema{
		Name:     "orders",
		RowCount: 1_234_567,
		Columns: []gowarehouse.ColumnSchema{
			{Name: "order_id", Type: "INT64"},
			{Name: "total", Type: "FLOAT64", Nullable: true},
		},
	}
	wh.queryResults["sales.orders"] = &gowarehouse.QueryResult{
		Columns: []string{"order_id", "total"},
		Rows: []map[string]interface{}{
			{"order_id": 1, "total": 12.5},
			{"order_id": 2, "total": 7.0},
		},
	}
	ex := newInspectExecutor(t, wh)

	res, err := ex.Inspect(context.Background(), []string{"sales.orders"})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !strings.Contains(res.Content, "TABLE sales.orders (1234567 rows)") {
		t.Errorf("header wrong:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "order_id INT64 NOT NULL") {
		t.Errorf("column line wrong:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "total FLOAT64 NULL") {
		t.Errorf("nullable rendering wrong:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "order_id=1") {
		t.Errorf("sample row missing:\n%s", res.Content)
	}
	if len(res.Succeeded) != 1 {
		t.Errorf("succeeded = %d", len(res.Succeeded))
	}
	if res.Truncated {
		t.Error("should not be truncated")
	}
}

func TestInspect_DedupsAndSorts(t *testing.T) {
	wh := newFakeWarehouse()
	wh.schemas["s.a"] = &gowarehouse.TableSchema{Name: "a", Columns: []gowarehouse.ColumnSchema{{Name: "id"}}}
	wh.schemas["s.b"] = &gowarehouse.TableSchema{Name: "b", Columns: []gowarehouse.ColumnSchema{{Name: "id"}}}
	ex := newInspectExecutor(t, wh)

	res, _ := ex.Inspect(context.Background(), []string{"s.b", "s.a", "s.b", "s.a"})
	if len(res.Succeeded) != 2 {
		t.Errorf("dedup failed: %v", res.Succeeded)
	}
	// Order: alphabetical — so "s.a" before "s.b".
	idxA := strings.Index(res.Content, "TABLE s.a")
	idxB := strings.Index(res.Content, "TABLE s.b")
	if idxA == -1 || idxB == -1 || idxA > idxB {
		t.Errorf("unstable order:\n%s", res.Content)
	}
}

func TestInspect_TruncatesAt10(t *testing.T) {
	wh := newFakeWarehouse()
	refs := make([]string, 0, 15)
	for i := 0; i < 15; i++ {
		ref := "s.t" + string(rune('a'+i%26))
		if _, ok := wh.schemas[ref]; !ok {
			wh.schemas[ref] = &gowarehouse.TableSchema{Name: "t", Columns: []gowarehouse.ColumnSchema{{Name: "id"}}}
			refs = append(refs, ref)
		}
	}
	ex := newInspectExecutor(t, wh)
	res, _ := ex.Inspect(context.Background(), refs)
	if len(res.Succeeded) != MaxInspectTablesPerCall {
		t.Errorf("succeeded = %d, want %d", len(res.Succeeded), MaxInspectTablesPerCall)
	}
	if !res.Truncated {
		t.Error("Truncated flag not set")
	}
}

func TestInspect_MissingTableRendersError(t *testing.T) {
	wh := newFakeWarehouse()
	wh.schemaErrors["s.ghost"] = errors.New("table not found")
	ex := newInspectExecutor(t, wh)

	res, _ := ex.Inspect(context.Background(), []string{"s.ghost"})
	if !strings.Contains(res.Content, "inspect failed") {
		t.Errorf("should note failure:\n%s", res.Content)
	}
	if _, ok := res.Failed["s.ghost"]; !ok {
		t.Errorf("Failed map should list s.ghost: %v", res.Failed)
	}
	if len(res.Succeeded) != 0 {
		t.Errorf("succeeded should be empty")
	}
}

func TestInspect_MissingTableNameRejected(t *testing.T) {
	wh := newFakeWarehouse()
	ex := newInspectExecutor(t, wh)
	res, _ := ex.Inspect(context.Background(), []string{"dataset_only."})
	if _, ok := res.Failed["dataset_only."]; !ok {
		t.Errorf("empty-table ref should fail: %v", res.Failed)
	}
}

func TestInspect_UnqualifiedTable_UsesDefaultDataset(t *testing.T) {
	wh := newFakeWarehouse()
	wh.schemas[".products"] = &gowarehouse.TableSchema{
		Name: "products", Columns: []gowarehouse.ColumnSchema{{Name: "id"}},
	}
	ex := newInspectExecutor(t, wh)

	res, _ := ex.Inspect(context.Background(), []string{"products"})
	if len(res.Succeeded) != 1 {
		t.Errorf("unqualified table failed: %+v", res)
	}
}

func TestInspect_EmptyList(t *testing.T) {
	ex := newInspectExecutor(t, newFakeWarehouse())
	res, err := ex.Inspect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Succeeded) != 0 {
		t.Error("no tables requested → no successes")
	}
	if res.Content != "" {
		t.Errorf("empty content expected, got %q", res.Content)
	}
}

func TestInspect_EmptyStringsSkipped(t *testing.T) {
	wh := newFakeWarehouse()
	ex := newInspectExecutor(t, wh)
	res, _ := ex.Inspect(context.Background(), []string{"", "  ", ""})
	if len(res.Requested) != 0 {
		t.Errorf("empty refs should be skipped: %v", res.Requested)
	}
}

func TestSplitQualified(t *testing.T) {
	cases := map[string][2]string{
		"sales.orders":       {"sales", "orders"},
		"a.b.c":              {"a.b", "c"},
		"orders":             {"", "orders"},
		".orphan":            {"", "orphan"},
	}
	for in, want := range cases {
		d, tbl := splitQualified(in)
		if d != want[0] || tbl != want[1] {
			t.Errorf("splitQualified(%q) = (%q, %q), want (%q, %q)", in, d, tbl, want[0], want[1])
		}
	}
}
