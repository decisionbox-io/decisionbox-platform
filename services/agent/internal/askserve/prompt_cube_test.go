package askserve

import (
	"context"
	"slices"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
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
	"— find relevant tables semantically",
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
		"text prompt":   buildSystemPrompt(rt, routing, cfg, false),
		"tools prompt":  buildSystemPromptForTools(rt, routing, cfg, false),
		"query_data":    toolQueryData(routing.multi, routing.shapes().anyCube).Description,
		"search_tables": toolSearchTables(routing.shapes().anyCube).Description,
	}
}

func sqlRouting(multi bool) turnRouting {
	a := sqlDatasource("wh_1")
	if !multi {
		return turnRouting{datasources: []DatasourceInfo{a}, all: []DatasourceInfo{a}, pinned: "wh_1", primary: "wh_1"}
	}
	all := []DatasourceInfo{a, sqlDatasource("wh_2")}
	return turnRouting{datasources: all, all: all, primary: "wh_1", multi: true}
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
	cube := []DatasourceInfo{cubeDatasource("ga_1")}
	routing := turnRouting{datasources: cube, all: cube, pinned: "ga_1", primary: "ga_1"}

	surfaces := promptsFor(routing)
	for surface, got := range surfaces {
		for _, sentence := range sqlSentences {
			if strings.Contains(got, sentence) {
				t.Errorf("%s still asserts SQL to a source with no tables (%q):\n%s", surface, sentence, got)
			}
		}
	}

	// The absence and the language are stated by the datasource block, not by a
	// tool description — a tool serves every datasource in a turn and cannot
	// name one language for all of them. What each tool says instead is pinned
	// by its own test.
	for _, surface := range []string{"text prompt", "tools prompt"} {
		for _, want := range []string{"NO TABLES", "Report Request (JSON)"} {
			if !strings.Contains(surfaces[surface], want) {
				t.Errorf("%s never says %q:\n%s", surface, want, surfaces[surface])
			}
		}
	}
}

// TestTurnRouting_Shapes_DistinguishAnyFromAll pins the two facts apart. They
// are not interchangeable: anyCube decides whether a blanket SQL statement is
// still true, while allCube decides whether a table-shaped tool can work at
// all. Collapsing them would either describe a mixed turn as SQL or strip the
// SQL datasource beside a cube of the tool it needs.
func TestTurnRouting_Shapes_DistinguishAnyFromAll(t *testing.T) {
	sql, cube := sqlDatasource("wh_1"), cubeDatasource("ga_1")

	cases := []struct {
		name             string
		reach            []DatasourceInfo
		anyCube, allCube bool
	}{
		{"all SQL", []DatasourceInfo{sql, sqlDatasource("wh_2")}, false, false},
		{"mixed", []DatasourceInfo{sql, cube}, true, false},
		{"all cube", []DatasourceInfo{cube, cubeDatasource("ga_2")}, true, true},
		{"nothing reachable", nil, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := turnRouting{datasources: tc.reach, all: tc.reach, primary: "wh_1", multi: true}.shapes()
			if got.anyCube != tc.anyCube || got.allCube != tc.allCube {
				t.Errorf("shapes() = %+v, want {anyCube:%v allCube:%v}", got, tc.anyCube, tc.allCube)
			}
		})
	}
}

// TestToolsForPhase_WithholdsLookupSchemaWhenNothingHasTables covers the one
// place shape removes a tool rather than rewording it. lookup_schema returns
// columns, so against a turn where nothing has any it can only fail — and
// while the turn is ungrounded the model is FORCED to call some tool, so an
// advertised-but-impossible tool can consume the very step meant to gather
// evidence. On a mixed turn it must survive: it is still right for the SQL
// datasource.
func TestToolsForPhase_WithholdsLookupSchemaWhenNothingHasTables(t *testing.T) {
	offered := func(shapes sourceShapes) bool {
		for _, td := range toolsForPhase(true, true, false, true, shapes, false, false) {
			if td.Name == string(actLookup) {
				return true
			}
		}
		return false
	}

	if !offered(sourceShapes{}) {
		t.Error("lookup_schema was withheld from an all-SQL turn")
	}
	if !offered(sourceShapes{anyCube: true}) {
		t.Error("lookup_schema was withheld from a mixed turn, where the SQL datasource still needs it")
	}
	if offered(sourceShapes{anyCube: true, allCube: true}) {
		t.Error("lookup_schema was offered to a turn where nothing has columns to look up")
	}

	// Withholding it must not take search_tables with it — that is the only
	// discovery tool such a turn has.
	var names []string
	for _, td := range toolsForPhase(true, true, false, true, sourceShapes{anyCube: true, allCube: true}, false, false) {
		names = append(names, td.Name)
	}
	if !slices.Contains(names, string(actSearch)) {
		t.Errorf("search_tables was not offered to a cube-only turn: %v", names)
	}
}

// TestTextPrompt_AdvertisesNoLookupWhereNothingHasTables covers the JSON-text
// fallback, which has no tool gate: the parser accepts any action this list
// advertises and execLookup will run it. Advertising a lookup no reachable
// datasource can answer spends a forced grounding step on a guaranteed
// failure, so the action list is withheld on the same condition the tool is.
func TestTextPrompt_AdvertisesNoLookupWhereNothingHasTables(t *testing.T) {
	cfg := Config{PreviewRows: 20, MaxQueriesPerTurn: 8, MaxRounds: 12}
	rt := &ProjectRuntime{}
	render := func(ds []DatasourceInfo) string {
		return buildSystemPrompt(rt, turnRouting{datasources: ds, all: ds, primary: ds[0].ID, multi: true}, cfg, false)
	}

	sql, cube := sqlDatasource("wh_1"), cubeDatasource("ga_1")
	const action = `"lookup_schema":["dataset.table_a"]`

	if got := render([]DatasourceInfo{cube, cubeDatasource("ga_2")}); strings.Contains(got, action) {
		t.Errorf("a project of only cubes still advertises lookup_schema:\n%s", got)
	}
	if got := render([]DatasourceInfo{sql, cube}); !strings.Contains(got, action) {
		t.Errorf("a mixed project lost lookup_schema, which its SQL datasource still needs:\n%s", got)
	}
	if got := render([]DatasourceInfo{sql, sqlDatasource("wh_2")}); !strings.Contains(got, action) {
		t.Errorf("an all-SQL project lost lookup_schema:\n%s", got)
	}
}

// TestToolsPrompt_OffersOnlyToolsTheTurnWasGiven pins the prompt against the
// tool set it describes. toolsForPhase withholds lookup_schema when nothing
// reachable has columns, so a prompt that still OFFERS it — in the TOOLS
// listing or in the grounding rule naming what may be called — sends the model
// after a tool it was never handed. That is the mirror of the bug this file
// exists to fix.
//
// Only those two places count. The per-datasource block also names
// lookup_schema, to say it does not apply, and that stays: it cannot misdirect
// a model into calling a tool, and on the JSON-text path — which advertises
// actions, not tools, and whose parser accepts any action the model invents —
// the prohibition is what stops the call.
func TestToolsPrompt_OffersOnlyToolsTheTurnWasGiven(t *testing.T) {
	cfg := Config{PreviewRows: 20, MaxQueriesPerTurn: 8, MaxRounds: 12}
	sql, cube := sqlDatasource("wh_1"), cubeDatasource("ga_1")

	render := func(rt *ProjectRuntime, routing turnRouting) (string, bool) {
		offered := false
		for _, td := range toolsForPhase(true, true, rt.InsightsProvider != nil, routing.multi, routing.shapes(), false, false) {
			if td.Name == string(actLookup) {
				offered = true
			}
		}
		return buildSystemPromptForTools(rt, routing, cfg, false), offered
	}
	multi := func(ds ...DatasourceInfo) turnRouting {
		return turnRouting{datasources: ds, all: ds, primary: ds[0].ID, multi: true}
	}
	pinned := func(d DatasourceInfo) turnRouting {
		ds := []DatasourceInfo{d}
		return turnRouting{datasources: ds, all: ds, pinned: d.ID, primary: d.ID}
	}

	// The two places a tool is offered rather than merely mentioned.
	offerSites := func(prompt string) string {
		tools := section(t, prompt, "\nTOOLS\n", "\nRESULT HANDLING\n")
		grounding := section(t, prompt, "\nGROUNDING (required):", "\n")
		return tools + grounding
	}

	cases := []struct {
		name    string
		rt      *ProjectRuntime
		routing turnRouting
	}{
		{"cubes only", &ProjectRuntime{}, multi(cube, cubeDatasource("ga_2"))},
		{"cubes only, with insights", &ProjectRuntime{InsightsProvider: &fakeInsights{}}, multi(cube, cubeDatasource("ga_2"))},
		{"pinned to a cube", &ProjectRuntime{}, pinned(cube)},
		{"mixed", &ProjectRuntime{}, multi(sql, cube)},
		{"pinned to the SQL source", &ProjectRuntime{}, pinned(sql)},
		{"all SQL", &ProjectRuntime{}, multi(sql, sqlDatasource("wh_2"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, offered := render(tc.rt, tc.routing)
			sites := offerSites(got)
			if o := strings.Contains(sites, "lookup_schema"); o != offered {
				t.Errorf("prompt offers lookup_schema = %v but the turn was given it = %v:\n%s", o, offered, sites)
			}
		})
	}
}

// section returns the prompt text from the start marker up to the next end
// marker, so an assertion can be aimed at one block instead of the whole
// prompt.
func section(t *testing.T, prompt, start, end string) string {
	t.Helper()
	i := strings.Index(prompt, start)
	if i < 0 {
		t.Fatalf("prompt has no %q section:\n%s", strings.TrimSpace(start), prompt)
	}
	rest := prompt[i+len(start):]
	if j := strings.Index(rest, end); j >= 0 {
		return rest[:j]
	}
	return rest
}

// TestGroundingNudge_CorrectsInTheShapeOfTheSource covers the message that
// arrives after the model has already gone wrong. It is the most recent thing
// in the conversation at that point — on the tool path it arrives as an error
// on the call it is correcting — so a nudge pointing at INFORMATION_SCHEMA
// outranks everything the system prompt said about a source with no tables.
func TestGroundingNudge_CorrectsInTheShapeOfTheSource(t *testing.T) {
	const historic = "Do NOT answer yet — you have run no query, so you have no data to ground an answer in. " +
		"Run a query_data action first to gather evidence; never state a table, count, total, or value you have not seen in a query result this turn. " +
		"If you don't know the tables or columns, discover them with a query against INFORMATION_SCHEMA (e.g. `SELECT table_name FROM <dataset>.INFORMATION_SCHEMA.TABLES`) or use search_tables / lookup_schema. " +
		"Only use clarify or decline if the question genuinely cannot be turned into any query."

	if got := groundingNudge(sourceShapes{}); got != historic {
		t.Errorf("the all-SQL nudge changed:\ngot:  %q\nwant: %q", got, historic)
	}

	mixed := groundingNudge(sourceShapes{anyCube: true})
	if !strings.Contains(mixed, "apply only to a SQL datasource") {
		t.Errorf("a mixed turn's nudge does not qualify its SQL discovery advice: %q", mixed)
	}
	if !strings.Contains(mixed, "INFORMATION_SCHEMA") {
		t.Errorf("a mixed turn's nudge dropped SQL discovery, which its SQL datasource still needs: %q", mixed)
	}

	cube := groundingNudge(sourceShapes{anyCube: true, allCube: true})
	for _, banned := range []string{"INFORMATION_SCHEMA", "lookup_schema", "tables or columns"} {
		if strings.Contains(cube, banned) {
			t.Errorf("a cube-only turn is nudged back toward %q: %q", banned, cube)
		}
	}
	if !strings.Contains(cube, "search_tables") {
		t.Errorf("a cube-only turn's nudge names no way to discover anything: %q", cube)
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
	mixed := []DatasourceInfo{sqlDatasource("wh_1"), cubeDatasource("ga_1")}
	writeDatasourcesSection(&b, turnRouting{
		datasources: mixed,
		all:         mixed,
		primary:     "wh_1",
		multi:       true,
	})
	got := b.String()

	for _, want := range []string{
		"SQL dialect: postgres",
		"NO TABLES — a metric/dimension cube. Query language: Report Request (JSON) (not SQL).",
		"Datasources do not all speak the same query language — write each query in the language of the datasource you are targeting.",
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

// TestTurnRouting_Shapes_ReadWhatTheTurnCanReach pins the set the branches
// are computed over, which is neither the visible list nor always the
// project's.
//
// The two directions fail differently and both matter. A pinned turn reaches
// only its pin, so a SQL pin must still be told SQL — warning it about a cube
// it cannot query is noise. An unpinned turn reaches the whole project even
// when the router narrowed what the prompt lists, because a model-chosen
// datasource_id is validated against every datasource and search_tables spans
// all of them: reading the narrowed list would promise SELECT-only to a turn
// that can still target a source accepting no SQL.
func TestTurnRouting_Shapes_ReadWhatTheTurnCanReach(t *testing.T) {
	sql, sql2, cube := sqlDatasource("wh_1"), sqlDatasource("wh_2"), cubeDatasource("ga_1")
	project := []DatasourceInfo{sql, sql2, cube}

	cases := []struct {
		name    string
		routing turnRouting
		want    bool
	}{
		{"pinned to the SQL source, cube unreachable beside it",
			turnRouting{datasources: []DatasourceInfo{sql}, all: project, pinned: "wh_1", primary: "wh_1"}, false},
		{"pinned to the cube",
			turnRouting{datasources: []DatasourceInfo{cube}, all: project, pinned: "ga_1", primary: "ga_1"}, true},
		{"model picks per query across the whole project",
			turnRouting{datasources: project, all: project, primary: "wh_1", multi: true}, true},
		{"router narrowed the visible set to SQL, but the cube is still targetable",
			turnRouting{datasources: []DatasourceInfo{sql, sql2}, all: project, primary: "wh_1", multi: true}, true},
		{"model picks per query, the project is all SQL",
			turnRouting{datasources: []DatasourceInfo{sql, sql2}, all: []DatasourceInfo{sql, sql2}, primary: "wh_1", multi: true}, false},
		{"a pin naming no datasource in the project reaches nothing",
			turnRouting{datasources: []DatasourceInfo{cube}, all: project, pinned: "gone", primary: "gone"}, false},
		{"project list absent falls back to the visible set",
			turnRouting{datasources: []DatasourceInfo{cube}, pinned: "ga_1", primary: "ga_1"}, true},
		{"no datasources at all",
			turnRouting{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.routing.shapes().anyCube; got != tc.want {
				t.Errorf("hasCube() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestToolSearchTables_StopsSayingTablesWhenThereAreNone covers the one
// discovery tool a cube turn is told to use. On a native tool-calling provider
// the description drives tool selection, so a model told this searches tables
// — and told by the prompt that its source has none — has been given a reason
// not to call the only tool that would have worked.
func TestToolSearchTables_StopsSayingTablesWhenThereAreNone(t *testing.T) {
	sqlTool := toolSearchTables(false)
	if sqlTool.Description != "Semantically search the indexed schema for tables relevant to a description. Use this first when you don't know which tables hold what you need." {
		t.Errorf("the SQL search_tables description changed: %q", sqlTool.Description)
	}
	if got := topKDescription(t, sqlTool); got != "Max number of tables to return (optional)." {
		t.Errorf("SQL top_k described as %q", got)
	}

	cubeTool := toolSearchTables(true)
	for _, want := range []string{"metrics and dimensions", "each result says what it is"} {
		if !strings.Contains(cubeTool.Description, want) {
			t.Errorf("cube search_tables description is missing %q: %q", want, cubeTool.Description)
		}
	}
	if got := topKDescription(t, cubeTool); strings.Contains(got, "tables") {
		t.Errorf("cube top_k still counts tables: %q", got)
	}
}

// TestLoop_OffersCubeShapedToolsToTheModel closes the wiring at its outermost
// point: the shape flag is computed in the loop, and everything below it can be
// correct while the loop hands the model SQL-only tools. It asserts what the
// provider actually received, not what a builder would have produced.
func TestLoop_OffersCubeShapedToolsToTheModel(t *testing.T) {
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		toolCall(string(actDecline), map[string]any{"reason": "done"}),
	}}
	wh := testutil.NewMockWarehouseProvider("ds")
	rt := toolRuntime(p, wh, &fakeSchema{}, "")
	// One cube datasource, under the id the runtime is primed with.
	cube := cubeDatasource("default")
	cube.Label, cube.Dialect, cube.Datasets = "Default", "", nil
	rt.Datasources = []DatasourceInfo{cube}

	r := &runner{cfg: Config{MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}, store: &fakeStore{}}
	r.run(context.Background(), rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "how many?"})

	if len(p.reqs) == 0 {
		t.Fatal("the model was never called")
	}
	req := p.reqs[0]
	if !strings.Contains(req.SystemPrompt, "NO TABLES") {
		t.Errorf("the system prompt sent to the model never says the source has no tables:\n%s", req.SystemPrompt)
	}
	for _, name := range []string{string(actQuery), string(actSearch)} {
		var got string
		for _, td := range req.Tools {
			if td.Name == name {
				got = td.Description
			}
		}
		if got == "" {
			t.Fatalf("%s was not offered to the model", name)
		}
		if want := toolFor(t, name, false, true).Description; got != want {
			t.Errorf("the model was offered a SQL-shaped %s on a cube turn:\ngot:  %q\nwant: %q", name, got, want)
		}
	}
	for _, td := range req.Tools {
		if td.Name == string(actLookup) {
			t.Errorf("lookup_schema was offered on a turn whose only source has no columns: %q", td.Description)
		}
	}
}

// TestToolsForPhase_HandsShapeToEveryToolThatNeedsIt covers the wiring rather
// than the text. Both query_data and search_tables branch on shape, and both
// are constructed here — a tool built with the flag dropped is described
// correctly by its own unit test and still ships SQL-only text to a cube turn.
func TestToolsForPhase_HandsShapeToEveryToolThatNeedsIt(t *testing.T) {
	byName := func(hasCube bool) map[string]gollm.ToolDefinition {
		out := map[string]gollm.ToolDefinition{}
		shapes := sourceShapes{anyCube: hasCube, allCube: false}
		for _, td := range toolsForPhase(true, true, false, true, shapes, false, false) {
			out[td.Name] = td
		}
		return out
	}

	sqlTools, cubeTools := byName(false), byName(true)
	for _, name := range []string{string(actQuery), string(actSearch)} {
		sqlTool, ok := sqlTools[name]
		if !ok {
			t.Fatalf("%s was not offered", name)
		}
		cubeTool, ok := cubeTools[name]
		if !ok {
			t.Fatalf("%s was not offered on a cube turn", name)
		}
		if sqlTool.Description == cubeTool.Description {
			t.Errorf("%s is built without the shape flag — it describes a cube turn as SQL: %q", name, cubeTool.Description)
		}
		if want := toolFor(t, name, true, false).Description; sqlTool.Description != want {
			t.Errorf("%s on a SQL turn: got %q, want %q", name, sqlTool.Description, want)
		}
		if want := toolFor(t, name, true, true).Description; cubeTool.Description != want {
			t.Errorf("%s on a cube turn: got %q, want %q", name, cubeTool.Description, want)
		}
	}
}

// toolFor builds one tool directly, as the definition of what the caller
// should have produced for the same routing.
func toolFor(t *testing.T, name string, multi, hasCube bool) gollm.ToolDefinition {
	t.Helper()
	switch name {
	case string(actQuery):
		return toolQueryData(multi, hasCube)
	case string(actSearch):
		return toolSearchTables(hasCube)
	}
	t.Fatalf("no builder for tool %s", name)
	return gollm.ToolDefinition{}
}

func topKDescription(t *testing.T, td gollm.ToolDefinition) string {
	t.Helper()
	k, ok := toolProps(t, td)["top_k"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool %s declares no top_k argument", td.Name)
	}
	desc, _ := k["description"].(string)
	return desc
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
