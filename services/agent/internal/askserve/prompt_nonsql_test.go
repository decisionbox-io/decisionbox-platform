package askserve

import (
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// nonSQLTablesDatasource is a source that HAS tables — they can be listed,
// looked up and sampled — but whose queries are written in its own language.
func nonSQLTablesDatasource(id string) DatasourceInfo {
	d := DatasourceInfo{ID: id, Label: "App " + id, Description: "sales and stock", Datasets: []string{"app-1"}}
	d.Capability = gowarehouse.Capability{QueryLanguage: "Hypercube Request (JSON)", Shape: gowarehouse.ShapeEntities}
	d.NonSQLLanguage = "Hypercube Request (JSON)"
	return d
}

// sqlOnlyTriggers are the phrases that send a model to SQL or to a SQL
// catalogue. None may reach a turn whose only source is not SQL. ("find
// relevant tables" is not one: this source has tables.)
var sqlOnlyTriggers = func() []string {
	out := []string{"INFORMATION_SCHEMA", "Emit only SELECT/CTE", "SQL dialect"}
	for _, s := range sqlSentences {
		if s != "— find relevant tables semantically" {
			out = append(out, s)
		}
	}
	return out
}()

func TestNonSQLTablesTurn_DropsSQLButKeepsTheTables(t *testing.T) {
	src := []DatasourceInfo{nonSQLTablesDatasource("qa_1")}
	routing := turnRouting{datasources: src, all: src, pinned: "qa_1", primary: "qa_1"}

	surfaces := promptsFor(routing)
	for surface, got := range surfaces {
		for _, phrase := range sqlOnlyTriggers {
			if strings.Contains(got, phrase) {
				t.Errorf("%s tells a non-SQL source %q:\n%s", surface, phrase, got)
			}
		}
		if strings.Contains(got, "NO TABLES") {
			t.Errorf("%s calls a source with tables a cube:\n%s", surface, got)
		}
	}
	for _, surface := range []string{"text prompt", "tools prompt"} {
		got := surfaces[surface]
		for _, want := range []string{"Queries are written as: Hypercube Request (JSON) — not SQL", "Datasets available: app-1", "lookup_schema"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s never says %q:\n%s", surface, want, got)
			}
		}
	}
	if !strings.Contains(surfaces["text prompt"], `"lookup_schema":["dataset.table_a","dataset.table_b"]`) {
		t.Error("the text path must still offer lookup_schema to a source with tables")
	}

	tools := map[string]bool{}
	for _, td := range toolsForPhase(false, true, false, false, false, routing.shapes(), false, false, nil) {
		tools[td.Name] = true
	}
	if !tools[string(actLookup)] {
		t.Error("lookup_schema was withheld from a source that has tables")
	}
	if got := groundingNudge(routing.shapes()); strings.Contains(got, "INFORMATION_SCHEMA") {
		t.Errorf("the grounding nudge sends a non-SQL source to INFORMATION_SCHEMA: %s", got)
	}
}

func TestTurnRouting_Shapes_NonSQLTables(t *testing.T) {
	sql, tables, cube := sqlDatasource("wh"), nonSQLTablesDatasource("qa"), cubeDatasource("ga")
	for _, tc := range []struct {
		name                  string
		reach                 []DatasourceInfo
		nonSQL, cubes, allCub bool
	}{
		{"SQL only", []DatasourceInfo{sql}, false, false, false},
		{"tables, not SQL", []DatasourceInfo{tables}, true, false, false},
		{"SQL beside tables-not-SQL", []DatasourceInfo{sql, tables}, true, false, false},
		{"tables-not-SQL beside a cube", []DatasourceInfo{tables, cube}, true, true, false},
		{"nothing reachable", nil, false, false, false},
	} {
		s := turnRouting{all: tc.reach, datasources: tc.reach}.shapes()
		if s.nonSQL() != tc.nonSQL || s.anyCube != tc.cubes || s.allCube != tc.allCub {
			t.Errorf("%s: nonSQL=%v anyCube=%v allCube=%v", tc.name, s.nonSQL(), s.anyCube, s.allCube)
		}
	}
}

// A SQL provider resolves a QueryLanguage too (its dialect), so the language
// field alone must not make a source read as non-SQL.
func TestIsNonSQL_ReadsTheRegistryAnswerNotTheLanguageLabel(t *testing.T) {
	d := sqlDatasource("wh")
	d.QueryLanguage = "BigQuery Standard SQL"
	if isNonSQL(d) {
		t.Error("a SQL dialect label made a warehouse read as non-SQL")
	}
}

func TestDatasourcesSection_NonSQLTables(t *testing.T) {
	all := []DatasourceInfo{sqlDatasource("wh_1"), nonSQLTablesDatasource("qa_1")}
	var b strings.Builder
	writeDatasourcesSection(&b, turnRouting{datasources: all, all: all, primary: "wh_1", multi: true})
	got := b.String()
	for _, want := range []string{
		"Datasources do not all speak the same query language",
		"Query language: Hypercube Request (JSON) (not SQL). It has tables; search_tables and lookup_schema apply.",
		"Against a SQL datasource emit only SELECT/CTE",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("catalog never says %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "NO TABLES") {
		t.Errorf("a source with tables was described as a cube:\n%s", got)
	}

	// A non-SQL source the router passed over is still reachable, and its
	// language was never shown, so it is named.
	var h strings.Builder
	writeDatasourcesSection(&h, turnRouting{datasources: all[:1], all: all, primary: "wh_1", multi: true})
	if !strings.Contains(h.String(), "datasource_id: qa_1 (not selected") {
		t.Errorf("an unlisted non-SQL source is not named:\n%s", h.String())
	}
}

func TestSeedSection_NonSQLTables(t *testing.T) {
	var b strings.Builder
	writeSeedSection(&b, &SeedContext{Type: "insight", Label: "x"}, sourceShapes{nonSQLTables: true})
	got := b.String()
	if !strings.Contains(got, "scope your retrieval and queries to this insight — its tables, metric, and segment") {
		t.Errorf("seed block = %s", got)
	}
}

// The source's own query guide is shown under its language line, so the first
// query is written in its format rather than guessed; nothing is added for a
// source that declared none.
func TestQueryGuide_IsShownUnderTheLanguage(t *testing.T) {
	d := nonSQLTablesDatasource("qa_1")
	d.QueryGuide = "Write one JSON object.\nNo joins."
	var single strings.Builder
	writeWarehouseSection(&single, d)
	if !strings.Contains(single.String(), "  How to write a query in Hypercube Request (JSON):\n  Write one JSON object.\n  No joins.\n") {
		t.Errorf("single-datasource block lacks the guide:\n%s", single.String())
	}
	all := []DatasourceInfo{sqlDatasource("wh_1"), d}
	var multi strings.Builder
	writeDatasourcesSection(&multi, turnRouting{datasources: all, all: all, primary: "wh_1", multi: true})
	if !strings.Contains(multi.String(), "    Write one JSON object.\n") {
		t.Errorf("catalog lacks the guide:\n%s", multi.String())
	}
	var none strings.Builder
	writeWarehouseSection(&none, nonSQLTablesDatasource("qa_2"))
	if strings.Contains(none.String(), "How to write a query") {
		t.Errorf("a guide heading was rendered for a source with no guide:\n%s", none.String())
	}
}
