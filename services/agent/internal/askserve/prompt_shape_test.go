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

// TestWarehouseSection_SQLRenderingIsUnchanged pins the exact block a
// datasource produces. This is the regression guard for the descriptor work:
// threading capability through the runtime must not alter one character of
// what a project's model sees.
func TestWarehouseSection_SQLRenderingIsUnchanged(t *testing.T) {
	var b strings.Builder
	writeWarehouseSection(&b, sqlDatasource("wh_1"))

	want := "WAREHOUSE\n" +
		"- SQL dialect: postgres\n" +
		"- Datasets available: public\n" +
		"- The warehouse is READ-ONLY. Emit only SELECT/CTE queries. Never attempt INSERT, UPDATE, DELETE, MERGE, or DDL.\n"

	if got := b.String(); got != want {
		t.Errorf("warehouse block changed.\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestWarehouseSection_DescriptorDoesNotLeakIntoThePrompt pins that carrying a
// capability descriptor on a datasource changes nothing about what is
// rendered. The prompt and tool surface are SQL-shaped throughout and
// generalise together with the first non-SQL adapter; until then a declared
// shape must not produce a partly-generalised prompt.
func TestWarehouseSection_DescriptorDoesNotLeakIntoThePrompt(t *testing.T) {
	plain := sqlDatasource("wh_1")
	declared := sqlDatasource("wh_1")
	declared.Capability = gowarehouse.Capability{
		QueryLanguage: "PostgreSQL",
		Shape:         gowarehouse.ShapeEntities,
		CanAnchor:     gowarehouse.Anchoring(true),
	}

	var a, b strings.Builder
	writeWarehouseSection(&a, plain)
	writeWarehouseSection(&b, declared)

	if a.String() != b.String() {
		t.Errorf("declaring a descriptor changed the prompt.\nplain:\n%s\ndeclared:\n%s", a.String(), b.String())
	}
}

// TestDatasourcesSection_PreambleIsUnchanged pins the multi-datasource
// preamble and per-datasource dialect lines.
func TestDatasourcesSection_PreambleIsUnchanged(t *testing.T) {
	var b strings.Builder
	writeDatasourcesSection(&b, turnRouting{
		datasources: []DatasourceInfo{sqlDatasource("wh_1"), sqlDatasource("wh_2")},
		primary:     "wh_1",
	})
	got := b.String()

	want := "This project has multiple datasources. Each SQL query runs against exactly ONE datasource — pass its id as datasource_id. A single query cannot join across datasources.\n"
	if !strings.Contains(got, want) {
		t.Errorf("preamble changed.\ngot:\n%s\nwant to contain:\n%s", got, want)
	}
	if c := strings.Count(got, "SQL dialect: postgres"); c != 2 {
		t.Errorf("expected a dialect line per datasource, got %d:\n%s", c, got)
	}
}

// TestDatasourceInfo_DescriptorDefaults pins that a datasource which declares
// nothing behaves exactly as it did before the descriptor existed. A zero
// value resolving the wrong way here would be silent — the datasource would
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
}
