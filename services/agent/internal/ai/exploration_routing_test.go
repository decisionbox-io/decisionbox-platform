package ai

import (
	"context"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// Multi-hop discovery routing: each query_data step must run against the
// datasource named by the action's datasource_id (empty → the primary),
// and an unknown id must be reported back rather than silently running
// somewhere.

func newRoutingExecutor(wh *testutil.MockWarehouseProvider) *queryexec.QueryExecutor {
	return queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{Warehouse: wh, MaxRetries: 1})
}

func TestExploration_executeQuery_RoutesByDatasource(t *testing.T) {
	primary := testutil.NewMockWarehouseProvider("public")
	oracle := testutil.NewMockWarehouseProvider("CHINOOK")
	engine := NewExplorationEngine(ExplorationEngineOptions{
		Executors: map[string]*queryexec.QueryExecutor{
			"default":   newRoutingExecutor(primary),
			"wh_oracle": newRoutingExecutor(oracle),
		},
		PrimaryDatasource: "default",
	})

	// datasource_id targets the secondary → only the oracle mock runs it.
	step := &models.ExplorationStep{Step: 1}
	engine.executeAction(context.Background(), &ExplorationAction{
		Action: "query_data", Query: "SELECT * FROM CHINOOK.TRACK", Datasource: "wh_oracle",
	}, step)
	if step.WarehouseID != "wh_oracle" {
		t.Fatalf("step.WarehouseID = %q, want wh_oracle", step.WarehouseID)
	}
	if len(oracle.Calls) != 1 || len(primary.Calls) != 0 {
		t.Fatalf("routed wrong: oracle calls=%d primary calls=%d", len(oracle.Calls), len(primary.Calls))
	}

	// Omitted datasource_id → the primary.
	step2 := &models.ExplorationStep{Step: 2}
	engine.executeAction(context.Background(), &ExplorationAction{
		Action: "query_data", Query: "SELECT * FROM public.invoice",
	}, step2)
	if step2.WarehouseID != "default" {
		t.Fatalf("omitted datasource_id → step.WarehouseID = %q, want default", step2.WarehouseID)
	}
	if len(primary.Calls) != 1 {
		t.Fatalf("primary should have run exactly one query, got %d", len(primary.Calls))
	}
}

func TestExploration_executeQuery_UnknownDatasourceRejected(t *testing.T) {
	primary := testutil.NewMockWarehouseProvider("public")
	engine := NewExplorationEngine(ExplorationEngineOptions{
		Executors:         map[string]*queryexec.QueryExecutor{"default": newRoutingExecutor(primary)},
		PrimaryDatasource: "default",
	})
	step := &models.ExplorationStep{Step: 1}
	msg := engine.executeAction(context.Background(), &ExplorationAction{
		Action: "query_data", Query: "SELECT 1", Datasource: "nope",
	}, step)

	if len(primary.Calls) != 0 {
		t.Fatalf("unknown datasource must not execute anywhere; primary calls=%d", len(primary.Calls))
	}
	if step.Error == "" {
		t.Error("unknown datasource should record step.Error")
	}
	if !strings.Contains(msg, "unknown datasource_id") || !strings.Contains(msg, "default") {
		t.Fatalf("rejection should name the bad id + list valid ids; msg=%q", msg)
	}
}

func TestExploration_executorFor(t *testing.T) {
	engine := NewExplorationEngine(ExplorationEngineOptions{
		Executors: map[string]*queryexec.QueryExecutor{
			"default":   newRoutingExecutor(testutil.NewMockWarehouseProvider("public")),
			"wh_oracle": newRoutingExecutor(testutil.NewMockWarehouseProvider("CHINOOK")),
		},
		PrimaryDatasource: "default",
	})
	cases := []struct {
		in     string
		wantID string
		wantOK bool
	}{
		{"", "default", true},
		{"default", "default", true},
		{"wh_oracle", "wh_oracle", true},
		{"missing", "missing", false},
	}
	for _, c := range cases {
		_, id, ok := engine.executorFor(c.in)
		if id != c.wantID || ok != c.wantOK {
			t.Errorf("executorFor(%q) = (%q,%v), want (%q,%v)", c.in, id, ok, c.wantID, c.wantOK)
		}
	}
}

// TestExploration_SingleExecutorBackCompat pins that a single Executor
// (no Executors map / PrimaryDatasource) still answers an omitted
// datasource_id — the single-warehouse and legacy wiring path.
func TestExploration_SingleExecutorBackCompat(t *testing.T) {
	primary := testutil.NewMockWarehouseProvider("public")
	engine := NewExplorationEngine(ExplorationEngineOptions{Executor: newRoutingExecutor(primary)})
	step := &models.ExplorationStep{Step: 1}
	engine.executeAction(context.Background(), &ExplorationAction{Action: "query_data", Query: "SELECT 1"}, step)
	if step.WarehouseID != "default" || len(primary.Calls) != 1 {
		t.Fatalf("single-executor back-compat broken: warehouse=%q calls=%d", step.WarehouseID, len(primary.Calls))
	}
}

// TestExploration_CrossDatasourceRefRejected pins the pre-flight guard:
// a statement that references a table owned by another datasource is
// rejected (with a routing hint) before it reaches any engine, so the
// target engine's SQL fixer can't silently rewrite an attempted
// cross-engine join into a valid-but-wrong query.
func TestExploration_CrossDatasourceRefRejected(t *testing.T) {
	primary := testutil.NewMockWarehouseProvider("public")
	oracle := testutil.NewMockWarehouseProvider("CHINOOK")
	engine := NewExplorationEngine(ExplorationEngineOptions{
		Executors: map[string]*queryexec.QueryExecutor{
			"default":   newRoutingExecutor(primary),
			"wh_oracle": newRoutingExecutor(oracle),
		},
		PrimaryDatasource: "default",
		TableDatasource: map[string]string{
			"public.invoice_line": "default",
			"CHINOOK.TRACK":       "wh_oracle",
			"CHINOOK.GENRE":       "wh_oracle",
		},
	})

	// Targets Oracle but references a Postgres table (quoted form) → the
	// guard rejects it pre-flight; nothing executes.
	step := &models.ExplorationStep{Step: 1}
	msg := engine.executeAction(context.Background(), &ExplorationAction{
		Action:     "query_data",
		Datasource: "wh_oracle",
		Query:      `SELECT g.name FROM "CHINOOK"."GENRE" g JOIN "public"."invoice_line" il ON il.track_id = g.id`,
	}, step)
	if len(oracle.Calls) != 0 || len(primary.Calls) != 0 {
		t.Fatalf("cross-datasource query must not execute anywhere; oracle=%d primary=%d", len(oracle.Calls), len(primary.Calls))
	}
	if step.Error == "" || !strings.Contains(msg, "public.invoice_line") || !strings.Contains(msg, "default") {
		t.Fatalf("expected cross-datasource rejection naming table+owner; err=%q msg=%q", step.Error, msg)
	}

	// A clean same-datasource (Oracle-only) query is allowed through.
	step2 := &models.ExplorationStep{Step: 2}
	engine.executeAction(context.Background(), &ExplorationAction{
		Action:     "query_data",
		Datasource: "wh_oracle",
		Query:      `SELECT g.name, COUNT(*) FROM "CHINOOK"."TRACK" t JOIN "CHINOOK"."GENRE" g ON t.genre_id = g.genre_id GROUP BY g.name`,
	}, step2)
	if step2.WarehouseID != "wh_oracle" || len(oracle.Calls) != 1 {
		t.Fatalf("clean oracle-only query should run on oracle; warehouse=%q oracleCalls=%d", step2.WarehouseID, len(oracle.Calls))
	}
}

func TestParseAction_DatasourceID(t *testing.T) {
	a, err := ParseAction(`{"query":"SELECT 1","datasource_id":"wh_oracle"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Action != "query_data" || a.Datasource != "wh_oracle" {
		t.Fatalf("action=%q datasource=%q, want query_data/wh_oracle", a.Action, a.Datasource)
	}
}

// TestFormatResults_DatasourceTagging pins that the datasource tag appears
// on search/lookup results only on a multi-warehouse run, so single-
// warehouse output is unchanged.
func TestFormatResults_DatasourceTagging(t *testing.T) {
	hits := []SearchHit{{Table: "CHINOOK.TRACK", Datasource: "wh_oracle", RowCount: 3, Score: 0.9}}
	if got := formatSearchResult("tracks", hits, 1, 30, true); !strings.Contains(got, "datasource: wh_oracle") {
		t.Errorf("multi-warehouse search should tag datasource; got %q", got)
	}
	if got := formatSearchResult("tracks", hits, 1, 30, false); strings.Contains(got, "datasource:") {
		t.Errorf("single-warehouse search must not tag datasource; got %q", got)
	}

	res := LookupResult{Tables: []LookupTable{{Table: "CHINOOK.TRACK", Datasource: "wh_oracle", RowCount: 3}}}
	if got := formatLookupResult(res, nil, false, 1, 30, true); !strings.Contains(got, "datasource: wh_oracle") {
		t.Errorf("multi-warehouse lookup should tag datasource; got %q", got)
	}
	if got := formatLookupResult(res, nil, false, 1, 30, false); strings.Contains(got, "datasource:") {
		t.Errorf("single-warehouse lookup must not tag datasource; got %q", got)
	}
}
