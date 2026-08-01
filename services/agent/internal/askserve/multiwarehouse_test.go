package askserve

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// twoDatasourceRuntime builds a runtime over two datasources ("wh_a" primary,
// "wh_b"), each backed by its own mock warehouse so a test can assert WHICH
// datasource a query hit. builds (optional) counts lazy connection builds per
// datasource; span (optional) is the cross-datasource search stub.
func twoDatasourceRuntime(
	provider gollm.Provider,
	whA, whB *testutil.MockWarehouseProvider,
	builds map[string]*int32,
	span func(context.Context, string, int) ([]TaggedHit, error),
) *ProjectRuntime {
	client, _ := ai.New(provider, "test-model")
	mocks := map[string]*testutil.MockWarehouseProvider{"wh_a": whA, "wh_b": whB}
	build := func(_ context.Context, id string) (*WarehouseConn, error) {
		wh, ok := mocks[id]
		if !ok {
			return nil, fmt.Errorf("no such datasource %q", id)
		}
		if builds != nil && builds[id] != nil {
			atomic.AddInt32(builds[id], 1)
		}
		return &WarehouseConn{Executor: queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
			Warehouse: wh, MaxRetries: 1,
		})}, nil
	}
	router := NewSchemaRouter(SchemaRouterOptions{
		Lookups: map[string]ai.SchemaProvider{"wh_a": &fakeSchema{}, "wh_b": &fakeSchema{}},
		Labels:  map[string]string{"wh_a": "Sales", "wh_b": "CRM"},
		Primary: "wh_a",
		Span:    span,
	})
	return NewProjectRuntime(ProjectRuntimeOptions{
		AIClient:  client,
		Model:     "test-model",
		Schema:    router,
		PrimaryID: "wh_a",
		Datasources: []DatasourceInfo{
			{ID: "wh_a", Label: "Sales", Dialect: "postgres", Datasets: []string{"sales"}},
			{ID: "wh_b", Label: "CRM", Dialect: "postgres", Datasets: []string{"crm"}},
		},
		Build: build,
	})
}

func mwConfig() Config {
	return Config{MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
}

// runMW drives a turn on the JSON-text path against a two-datasource runtime.
func runMW(t *testing.T, rt *ProjectRuntime, req TurnRequest, responses []string) *fakeStore {
	t.Helper()
	if req.TurnID == "" {
		req = TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "q", DatasourceID: req.DatasourceID}
	}
	store := &fakeStore{}
	(&runner{cfg: mwConfig(), store: store}).run(context.Background(), rt, req)
	if store.final == nil {
		t.Fatal("turn did not finalize")
	}
	return store
}

func eventDatasource(ev commonmodels.ToolEvent) string {
	if ev.Args == nil {
		return ""
	}
	if s, ok := ev.Args["datasource_id"].(string); ok {
		return s
	}
	return ""
}

func TestMW_RoutesQueryToNamedDatasource(t *testing.T) {
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		`{"query":"SELECT count(*) FROM crm.users","datasource_id":"wh_b"}`,
		`{"answer":"done"}`,
	}}, whA, whB, nil, nil)

	store := runMW(t, rt, TurnRequest{}, nil)
	if len(whB.Calls) != 1 {
		t.Fatalf("wh_b should have been queried once, got %d", len(whB.Calls))
	}
	if len(whA.Calls) != 0 {
		t.Fatalf("wh_a must NOT be queried (the model targeted wh_b), got %d", len(whA.Calls))
	}
	if len(store.events) != 1 || eventDatasource(store.events[0]) != "wh_b" {
		t.Fatalf("query event should stamp datasource_id=wh_b, got %+v", store.events)
	}
	if got := store.final.RoutedDatasourceIDs; len(got) != 1 || got[0] != "wh_b" {
		t.Fatalf("RoutedDatasourceIDs = %v, want [wh_b]", got)
	}
}

func TestMW_DefaultsToPrimaryWhenUnspecified(t *testing.T) {
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		`{"query":"SELECT 1 FROM sales.orders"}`, // no datasource_id → primary (wh_a)
		`{"answer":"done"}`,
	}}, whA, whB, nil, nil)

	store := runMW(t, rt, TurnRequest{}, nil)
	if len(whA.Calls) != 1 || len(whB.Calls) != 0 {
		t.Fatalf("unspecified datasource should default to the primary wh_a; a=%d b=%d", len(whA.Calls), len(whB.Calls))
	}
	if eventDatasource(store.events[0]) != "wh_a" {
		t.Fatalf("event datasource_id = %q, want wh_a", eventDatasource(store.events[0]))
	}
}

func TestMW_RejectsUnknownDatasource(t *testing.T) {
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		`{"query":"SELECT 1","datasource_id":"wh_ghost"}`, // not a real datasource
		`{"decline":"bad datasource"}`,
	}}, whA, whB, nil, nil)

	store := runMW(t, rt, TurnRequest{}, nil)
	if len(whA.Calls) != 0 || len(whB.Calls) != 0 {
		t.Fatalf("no warehouse should be queried for an unknown datasource; a=%d b=%d", len(whA.Calls), len(whB.Calls))
	}
	if len(store.events) != 1 || store.events[0].Error == "" {
		t.Fatalf("unknown datasource must record an error event (no silent fallback), got %+v", store.events)
	}
	if eventDatasource(store.events[0]) != "wh_ghost" {
		t.Fatalf("rejected event should record the bad id wh_ghost, got %q", eventDatasource(store.events[0]))
	}
	// An error event is not grounding — the turn must not answer off it.
	if store.final.Status != commonmodels.AskTurnStatusDeclined {
		t.Fatalf("status = %q, want declined", store.final.Status)
	}
}

func TestMW_MultiHopChainsAndRecordsBoth(t *testing.T) {
	// The canonical multi-hop: top buyers from sales, flagged in CRM. Two
	// statements, one datasource each, recorded in first-touched order.
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		`{"query":"SELECT userid FROM sales.orders ORDER BY total DESC LIMIT 10","datasource_id":"wh_a"}`,
		`{"query":"SELECT userid, flagged FROM crm.users WHERE userid IN (1,2,3)","datasource_id":"wh_b"}`,
		`{"answer":"top buyers and their flags"}`,
	}}, whA, whB, nil, nil)

	store := runMW(t, rt, TurnRequest{}, nil)
	if len(whA.Calls) != 1 || len(whB.Calls) != 1 {
		t.Fatalf("each datasource should be queried once; a=%d b=%d", len(whA.Calls), len(whB.Calls))
	}
	got := store.final.RoutedDatasourceIDs
	if len(got) != 2 || got[0] != "wh_a" || got[1] != "wh_b" {
		t.Fatalf("RoutedDatasourceIDs = %v, want [wh_a wh_b] in first-touched order", got)
	}
}

func TestMW_PinnedTurnForcesDatasource(t *testing.T) {
	// An explicit datasource_id on the request pins the turn: even if the model
	// names another datasource, the query runs against the pinned one, and no
	// routing telemetry is recorded (no routing decision was made).
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		`{"query":"SELECT 1 FROM sales.orders","datasource_id":"wh_a"}`, // model asks wh_a...
		`{"answer":"done"}`,
	}}, whA, whB, nil, nil)

	store := runMW(t, rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "q", DatasourceID: "wh_b"}, nil)
	if len(whB.Calls) != 1 || len(whA.Calls) != 0 {
		t.Fatalf("pinned turn must run against wh_b regardless of the model's choice; a=%d b=%d", len(whA.Calls), len(whB.Calls))
	}
	if eventDatasource(store.events[0]) != "wh_b" {
		t.Fatalf("pinned event datasource_id = %q, want wh_b", eventDatasource(store.events[0]))
	}
	if store.final.RoutedDatasourceIDs != nil {
		t.Fatalf("a pinned turn records no routing telemetry, got %v", store.final.RoutedDatasourceIDs)
	}
}

func TestMW_InvalidExplicitDatasourceFailsTurn(t *testing.T) {
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{`{"answer":"never reached"}`}}, whA, whB, nil, nil)

	store := runMW(t, rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "q", DatasourceID: "nope"}, nil)
	if store.final.Status != commonmodels.AskTurnStatusFailed {
		t.Fatalf("status = %q, want failed (caller sent a bad datasource override)", store.final.Status)
	}
	if len(whA.Calls) != 0 || len(whB.Calls) != 0 {
		t.Fatal("no warehouse should be touched when the override is invalid")
	}
}

func TestMW_LazyBuildSkipsUntouchedDatasource(t *testing.T) {
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	builds := map[string]*int32{"wh_a": new(int32), "wh_b": new(int32)}
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		`{"query":"SELECT 1 FROM crm.users","datasource_id":"wh_b"}`,
		`{"answer":"done"}`,
	}}, whA, whB, builds, nil)

	runMW(t, rt, TurnRequest{}, nil)
	if atomic.LoadInt32(builds["wh_b"]) != 1 {
		t.Fatalf("touched datasource wh_b should build once, got %d", atomic.LoadInt32(builds["wh_b"]))
	}
	if atomic.LoadInt32(builds["wh_a"]) != 0 {
		t.Fatalf("untouched datasource wh_a must NOT open a connection, got %d", atomic.LoadInt32(builds["wh_a"]))
	}
}

func TestMW_SearchSpansAllDatasources(t *testing.T) {
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	span := func(context.Context, string, int) ([]TaggedHit, error) {
		return []TaggedHit{
			{DatasourceID: "wh_a", DatasourceLabel: "Sales", Table: "sales.orders", Blurb: "orders", RowCount: 100, Score: 0.9},
			{DatasourceID: "wh_b", DatasourceLabel: "CRM", Table: "crm.users", Blurb: "users", RowCount: 50, Score: 0.8},
		}, nil
	}
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		`{"search_tables":"orders and users"}`,
		`{"answer":"found tables in both datasources"}`,
	}}, whA, whB, nil, span)

	store := runMW(t, rt, TurnRequest{}, nil)
	if len(store.events) != 1 || store.events[0].Name != "search_tables" {
		t.Fatalf("events = %+v, want one search_tables event", store.events)
	}
	rows, ok := store.events[0].Output.([]map[string]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("search output should carry both datasources' hits: %#v", store.events[0].Output)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if ds, ok := row["datasource_id"].(string); ok {
			seen[ds] = true
		}
	}
	if !seen["wh_a"] || !seen["wh_b"] {
		t.Fatalf("spanning search must tag hits from both datasources, saw %v", seen)
	}
}
