package askserve

import (
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// sqlDatasource is the shape every datasource had before the capability
// descriptor existed: no declaration at all.
func sqlDatasource(id string) DatasourceInfo {
	return DatasourceInfo{
		ID:       id,
		Label:    "Warehouse " + id,
		Dialect:  "postgres",
		Datasets: []string{"public"},
	}
}

func cubeDatasource(id string) DatasourceInfo {
	return DatasourceInfo{
		ID:    id,
		Label: "Analytics " + id,
		// A cube source still carries a Dialect hint (the provider slug); the
		// point of the shape flag is that the renderer must not treat it as a
		// SQL dialect.
		Dialect: "ga4",
		Capability: gowarehouse.Capability{
			QueryLanguage: "GA4 Report Request",
			Shape:         gowarehouse.ShapeCube,
			CanAnchor:     gowarehouse.Anchoring(false),
		},
	}
}

// TestWarehouseSection_SQLRenderingIsUnchanged pins the exact block an
// undeclared (SQL) datasource produces. This is the regression guard for the
// descriptor work: threading capability through must not alter one character
// of what a SQL project's model sees.
func TestWarehouseSection_SQLRenderingIsUnchanged(t *testing.T) {
	var b strings.Builder
	writeWarehouseSection(&b, sqlDatasource("wh_1"))

	want := "WAREHOUSE\n" +
		"- SQL dialect: postgres\n" +
		"- Datasets available: public\n" +
		"- The warehouse is READ-ONLY. Emit only SELECT/CTE queries. Never attempt INSERT, UPDATE, DELETE, MERGE, or DDL.\n"

	if got := b.String(); got != want {
		t.Errorf("SQL warehouse block changed.\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestWarehouseSection_CubeDoesNotClaimSQL pins that a cube-shaped source is
// never described as SQL. Describing it as SQL is the failure the shape flag
// exists to prevent: the model writes joins and a SELECT the source cannot
// honour, and the rejection reads like a bug rather than an honest capability
// limit.
func TestWarehouseSection_CubeDoesNotClaimSQL(t *testing.T) {
	var b strings.Builder
	writeWarehouseSection(&b, cubeDatasource("ga_1"))
	got := b.String()

	if strings.Contains(got, "SQL dialect") {
		t.Errorf("a cube source must not be given a SQL dialect line:\n%s", got)
	}
	if strings.Contains(got, "SELECT/CTE") {
		t.Errorf("a cube source must not be told to emit SELECT/CTE queries:\n%s", got)
	}
	if !strings.Contains(got, "Query language: GA4 Report Request") {
		t.Errorf("expected the declared query language in the block:\n%s", got)
	}
	if !strings.Contains(got, "metric/dimension cube") {
		t.Errorf("expected the cube shape to be stated:\n%s", got)
	}
	if !strings.Contains(got, "READ-ONLY") {
		t.Errorf("the read-only rule must survive the shape branch:\n%s", got)
	}
}

// TestDatasourcesSection_SQLOnlyPreambleIsUnchanged pins the multi-datasource
// preamble for a SQL-only project.
func TestDatasourcesSection_SQLOnlyPreambleIsUnchanged(t *testing.T) {
	var b strings.Builder
	writeDatasourcesSection(&b, turnRouting{
		datasources: []DatasourceInfo{sqlDatasource("wh_1"), sqlDatasource("wh_2")},
		primary:     "wh_1",
	})
	got := b.String()

	want := "This project has multiple datasources. Each SQL query runs against exactly ONE datasource — pass its id as datasource_id. A single query cannot join across datasources.\n"
	if !strings.Contains(got, want) {
		t.Errorf("SQL-only preamble changed.\ngot:\n%s\nwant to contain:\n%s", got, want)
	}
	if strings.Contains(got, "not all the same shape") {
		t.Errorf("a SQL-only project must not get the mixed-shape preamble:\n%s", got)
	}
	if c := strings.Count(got, "SQL dialect: postgres"); c != 2 {
		t.Errorf("expected a dialect line per datasource, got %d:\n%s", c, got)
	}
}

// TestDatasourcesSection_MixedShapesAreDescribedHonestly pins that a project
// mixing shapes stops claiming every query is SQL, and that each datasource is
// described in its own terms.
func TestDatasourcesSection_MixedShapesAreDescribedHonestly(t *testing.T) {
	var b strings.Builder
	writeDatasourcesSection(&b, turnRouting{
		datasources: []DatasourceInfo{sqlDatasource("wh_1"), cubeDatasource("ga_1")},
		primary:     "wh_1",
	})
	got := b.String()

	if !strings.Contains(got, "not all the same shape") {
		t.Errorf("a mixed-shape project needs the generalised preamble:\n%s", got)
	}
	if strings.Contains(got, "Each SQL query runs against exactly ONE datasource") {
		t.Errorf("the SQL-only preamble must not appear on a mixed project:\n%s", got)
	}
	if !strings.Contains(got, "query language: GA4 Report Request") {
		t.Errorf("the cube datasource must carry its own language:\n%s", got)
	}
	if !strings.Contains(got, "SQL dialect: postgres") {
		t.Errorf("the SQL datasource must keep its dialect line:\n%s", got)
	}
}

// TestDatasourceInfo_DescriptorDefaults pins that a datasource which declares
// nothing behaves exactly as it did before the descriptor existed. A zero
// value reaching the wrong default here would be silent — the datasource would
// simply stop being eligible to anchor.
func TestDatasourceInfo_DescriptorDefaults(t *testing.T) {
	d := DatasourceInfo{ID: "wh_1"}

	if got := d.EffectiveShape(); got != gowarehouse.ShapeEntities {
		t.Errorf("EffectiveShape() = %q, want %q", got, gowarehouse.ShapeEntities)
	}
	if !d.Anchors() {
		t.Error("an undeclared datasource must be able to anchor a project")
	}
	if got := d.Language(); got != "SQL" {
		t.Errorf("Language() = %q, want %q", got, "SQL")
	}
	if isCube(d) {
		t.Error("an undeclared datasource must not be treated as a cube")
	}
}

// TestAnyCube pins the mixed-project predicate the preamble branches on.
func TestAnyCube(t *testing.T) {
	if anyCube(nil) {
		t.Error("no datasources means no cube")
	}
	if anyCube([]DatasourceInfo{sqlDatasource("a"), sqlDatasource("b")}) {
		t.Error("SQL-only routing must not report a cube")
	}
	if !anyCube([]DatasourceInfo{sqlDatasource("a"), cubeDatasource("b")}) {
		t.Error("a single cube datasource must be detected")
	}
}
