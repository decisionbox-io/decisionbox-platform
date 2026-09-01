package askserve

import (
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// cubeDatasource is a source with no tables: a metric/dimension cube whose
// queries are not SQL.
func cubeDatasource(id string) DatasourceInfo {
	d := DatasourceInfo{ID: id, Label: "Cube " + id, Description: "observations about traffic"}
	d.Capability = gowarehouse.Capability{
		QueryLanguage: "Report Request (JSON)",
		Shape:         gowarehouse.ShapeCube,
		CanAnchor:     gowarehouse.Anchoring(false),
	}
	return d
}

// sqlSentences are the SQL assertions this prompt made about every datasource
// before shape was read. Each is true only while no datasource the turn can
// reach is cube-shaped, and each must survive verbatim while that holds — a
// project of only SQL datasources is every project that exists today.
var sqlSentences = []string{
	"running read-only SQL against their data warehouse",
	"to run read-only SQL against their data warehouse",
	`"query":"SELECT ...","purpose":"what this answers"`,
	"query_data: run one read-only SQL query",
	"write aggregate SQL (COUNT, SUM, AVG, GROUP BY) — do NOT page through raw rows",
	"your FIRST action must be a discovery query",
	"start with search_tables or a discovery query",
}

// cubePhrases are the statements a cube turn adds. None may appear on a turn
// that can only reach SQL datasources: warning a model about a source it cannot
// query is noise at best, and at worst invites it to hedge correct SQL.
var cubePhrases = []string{
	"NO TABLES",
	"Not every source is SQL",
	"query language",
	"metric/dimension cube",
}

// promptsFor renders every prompt surface a turn produces: both model paths
// (JSON-text and native tool-calling) and the query_data tool definition, which
// is the description that actually rides alongside the argument it governs.
func promptsFor(routing turnRouting) map[string]string {
	cfg := Config{PreviewRows: 20, MaxQueriesPerTurn: 8, MaxRounds: 12}
	rt := &ProjectRuntime{}
	return map[string]string{
		"text prompt":  buildSystemPrompt(rt, routing, cfg, false),
		"tools prompt": buildSystemPromptForTools(rt, routing, cfg, false),
		"query_data":   toolQueryData(routing.multi, routing.hasCube()).Description,
	}
}

func sqlRouting(multi bool) turnRouting {
	a := sqlDatasource("wh_1")
	if !multi {
		return turnRouting{datasources: []DatasourceInfo{a}, pinned: "wh_1", primary: "wh_1"}
	}
	return turnRouting{datasources: []DatasourceInfo{a, sqlDatasource("wh_2")}, primary: "wh_1", multi: true}
}

// TestSQLOnlyTurn_SaysExactlyWhatItAlwaysSaid is the regression guard for every
// existing deployment. Shape-awareness is a branch, not a rewrite: when no
// reachable datasource is a cube, each SQL sentence must still be there and no
// cube phrasing may leak in.
func TestSQLOnlyTurn_SaysExactlyWhatItAlwaysSaid(t *testing.T) {
	for _, multi := range []bool{false, true} {
		for surface, got := range promptsFor(sqlRouting(multi)) {
			for _, phrase := range cubePhrases {
				if strings.Contains(got, phrase) {
					t.Errorf("multi=%v %s leaked cube phrasing %q:\n%s", multi, surface, phrase, got)
				}
			}
		}
	}

	// The SQL sentences are split across surfaces (single vs multi, text vs
	// tools), so each is asserted against the union of everything the turn
	// renders rather than one surface.
	var all strings.Builder
	for _, multi := range []bool{false, true} {
		for _, got := range promptsFor(sqlRouting(multi)) {
			all.WriteString(got)
		}
	}
	for _, want := range sqlSentences {
		if !strings.Contains(all.String(), want) {
			t.Errorf("a SQL-only turn no longer says %q", want)
		}
	}
}

// TestCubeTurn_DropsEverySQLAssertion is the other half: on a turn pinned to a
// source with no tables, not one of those SQL sentences may survive. A single
// leftover is enough — the model resolves a contradiction in favour of the
// concrete instruction, writes SQL, and spends the turn on a query the source
// was always going to reject.
func TestCubeTurn_DropsEverySQLAssertion(t *testing.T) {
	routing := turnRouting{datasources: []DatasourceInfo{cubeDatasource("ga_1")}, pinned: "ga_1", primary: "ga_1"}

	surfaces := promptsFor(routing)
	for surface, got := range surfaces {
		for _, sentence := range sqlSentences {
			if strings.Contains(got, sentence) {
				t.Errorf("%s still asserts SQL to a source with no tables (%q):\n%s", surface, sentence, got)
			}
		}
		if !strings.Contains(got, "NO TABLES") {
			t.Errorf("%s never tells the model this source has no tables:\n%s", surface, got)
		}
	}

	// The language is named by the datasource block, not by the tool
	// description — the tool serves every datasource in a turn and cannot name
	// one language for all of them.
	for _, surface := range []string{"text prompt", "tools prompt"} {
		if !strings.Contains(surfaces[surface], "Report Request (JSON)") {
			t.Errorf("%s never names the source's query language:\n%s", surface, surfaces[surface])
		}
	}
}

// TestCubeSection_StatesTheAbsenceBeforeTheLanguage pins the ordering, which is
// the whole design of the block. A model that reads "no tables" first cannot
// then write a FROM clause by reflex; one that reads a language name first
// fills the gaps with SQL habits.
func TestCubeSection_StatesTheAbsenceBeforeTheLanguage(t *testing.T) {
	var b strings.Builder
	writeWarehouseSection(&b, cubeDatasource("ga_1"))
	got := b.String()

	noTables := strings.Index(got, "NO TABLES")
	language := strings.Index(got, "Report Request (JSON)")
	if noTables < 0 || language < 0 {
		t.Fatalf("cube block is missing the absence or the language:\n%s", got)
	}
	if noTables > language {
		t.Errorf("the query language is stated before the absence of tables:\n%s", got)
	}
	if !strings.Contains(got, "Do not call lookup_schema against this source") {
		t.Errorf("cube block does not rule out lookup_schema, whose every call would fail:\n%s", got)
	}
	if strings.Contains(got, "WAREHOUSE\n") {
		t.Errorf("cube block still renders the SQL warehouse header:\n%s", got)
	}
}

// TestDatasourcesSection_MixedShapes covers the case the per-datasource branch
// exists for: one SQL datasource and one cube in the same project. Each must be
// described in its own terms, and the blanket rules must be qualified rather
// than dropped — the SQL datasource still needs its SELECT-only rule.
func TestDatasourcesSection_MixedShapes(t *testing.T) {
	var b strings.Builder
	writeDatasourcesSection(&b, turnRouting{
		datasources: []DatasourceInfo{sqlDatasource("wh_1"), cubeDatasource("ga_1")},
		primary:     "wh_1",
		multi:       true,
	})
	got := b.String()

	for _, want := range []string{
		"SQL dialect: postgres",
		"NO TABLES — a metric/dimension cube. Query language: Report Request (JSON) (not SQL).",
		"Datasources do not all speak the same query language.",
		"Against a SQL datasource emit only SELECT/CTE queries",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("mixed-shape catalog is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Each SQL query runs against exactly ONE datasource") {
		t.Errorf("preamble still calls every query SQL:\n%s", got)
	}
}

// TestTurnRouting_HasCube_ReadsOnlyWhatTheTurnCanReach pins the set the
// branches are computed over. A turn pinned to a SQL warehouse must be told
// SQL even when a cube sits beside it in the same project, because the pin is
// what the model can actually query — the non-multi routing carries the whole
// project's datasource list, so reading the field instead of the reachable set
// would silently de-SQL a pinned SQL turn.
func TestTurnRouting_HasCube_ReadsOnlyWhatTheTurnCanReach(t *testing.T) {
	sql, cube := sqlDatasource("wh_1"), cubeDatasource("ga_1")

	cases := []struct {
		name    string
		routing turnRouting
		want    bool
	}{
		{"pinned to the SQL source, cube unreachable beside it",
			turnRouting{datasources: []DatasourceInfo{sql, cube}, pinned: "wh_1", primary: "wh_1"}, false},
		{"pinned to the cube",
			turnRouting{datasources: []DatasourceInfo{cube, sql}, pinned: "ga_1", primary: "ga_1"}, true},
		{"model picks per query, one of them a cube",
			turnRouting{datasources: []DatasourceInfo{sql, cube}, primary: "wh_1", multi: true}, true},
		{"model picks per query, all SQL",
			turnRouting{datasources: []DatasourceInfo{sql, sqlDatasource("wh_2")}, primary: "wh_1", multi: true}, false},
		{"no datasources at all",
			turnRouting{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.routing.hasCube(); got != tc.want {
				t.Errorf("hasCube() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestToolQueryData_DescribesWhatTheSourceAccepts pins the tool description,
// which is the most concrete instruction the model receives and the one prose
// elsewhere cannot soften.
func TestToolQueryData_DescribesWhatTheSourceAccepts(t *testing.T) {
	for _, multi := range []bool{false, true} {
		sqlTool := toolQueryData(multi, false)
		if !strings.HasPrefix(sqlTool.Description, "Run one read-only SQL query (SELECT / CTE only) against the data warehouse") {
			t.Errorf("multi=%v: the SQL tool description changed: %q", multi, sqlTool.Description)
		}
		if got := queryPropDescription(t, sqlTool); got != "The read-only SQL to execute." {
			t.Errorf("multi=%v: SQL query argument described as %q", multi, got)
		}

		cubeTool := toolQueryData(multi, true)
		if strings.Contains(cubeTool.Description, "Run one read-only SQL query (SELECT / CTE only)") {
			t.Errorf("multi=%v: the tool still demands SQL of a source with no tables: %q", multi, cubeTool.Description)
		}
		for _, want := range []string{"NO TABLES accepts no SQL at all", "not every datasource here is SQL"} {
			if !strings.Contains(cubeTool.Description, want) {
				t.Errorf("multi=%v: cube tool description is missing %q: %q", multi, want, cubeTool.Description)
			}
		}
		if got := queryPropDescription(t, cubeTool); !strings.Contains(got, "query language") {
			t.Errorf("multi=%v: cube query argument described as %q", multi, got)
		}
		// The datasource_id argument is orthogonal to shape and must still
		// appear exactly when the model chooses the datasource.
		if _, ok := toolProps(t, cubeTool)["datasource_id"]; ok != multi {
			t.Errorf("multi=%v: datasource_id present = %v", multi, ok)
		}
	}
}

// toolProps reads a tool's declared arguments out of its JSON-schema input.
func toolProps(t *testing.T, td gollm.ToolDefinition) map[string]interface{} {
	t.Helper()
	props, ok := td.InputSchema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool %s declares no properties: %#v", td.Name, td.InputSchema)
	}
	return props
}

// queryPropDescription reads the description of the `query` argument — the
// text attached to the field the model fills in, and so the last thing it
// reads before writing a query.
func queryPropDescription(t *testing.T, td gollm.ToolDefinition) string {
	t.Helper()
	q, ok := toolProps(t, td)["query"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool %s declares no query argument", td.Name)
	}
	desc, _ := q["description"].(string)
	return desc
}
