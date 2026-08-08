package discovery

import (
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func TestNormDatasourceID(t *testing.T) {
	if got := normDatasourceID(""); got != models.DefaultWarehouseID {
		t.Errorf("empty → %q, want %q", got, models.DefaultWarehouseID)
	}
	if got := normDatasourceID("   "); got != models.DefaultWarehouseID {
		t.Errorf("blank → %q, want %q", got, models.DefaultWarehouseID)
	}
	if got := normDatasourceID("wh_oracle"); got != "wh_oracle" {
		t.Errorf("id → %q, want wh_oracle", got)
	}
}

func TestLabelSuffix(t *testing.T) {
	if labelSuffix("") != "" || labelSuffix("  ") != "" {
		t.Error("blank label should render no suffix")
	}
	if got := labelSuffix("Catalog"); got != " (Catalog)" {
		t.Errorf("labelSuffix = %q, want ' (Catalog)'", got)
	}
}

func TestFilterClause(t *testing.T) {
	if filterClause("", "acme") != "" || filterClause("tenant", "") != "" {
		t.Error("a partial filter should render no clause")
	}
	if got := filterClause("tenant_id", "acme"); got != "WHERE tenant_id = 'acme'" {
		t.Errorf("filterClause = %q", got)
	}
}

func TestOrderWarehousesPrimaryFirst(t *testing.T) {
	whs := []models.WarehouseConfig{{ID: "wh_b"}, {ID: "default"}, {ID: "wh_c"}}
	got := orderWarehousesPrimaryFirst(whs, "default")
	if len(got) != 3 || got[0].ID != "default" || got[1].ID != "wh_b" || got[2].ID != "wh_c" {
		t.Fatalf("primary-first order wrong: %+v", got)
	}

	// A legacy empty-id primary resolves to "default" and still leads.
	legacy := []models.WarehouseConfig{{ID: "wh_b"}, {ID: ""}}
	got2 := orderWarehousesPrimaryFirst(legacy, "default")
	if normDatasourceID(got2[0].ID) != "default" {
		t.Fatalf("legacy primary not first: %+v", got2)
	}
}

func TestDatasourceFocusAreas(t *testing.T) {
	if datasourceFocusAreas(nil) != "" {
		t.Error("nil prompts → empty")
	}
	p := &models.ProjectPrompts{AnalysisAreas: map[string]models.AnalysisAreaConfig{
		"a": {Name: "Catalog Performance", Enabled: true},
		"b": {Name: "Disabled Area", Enabled: false},
		"c": {Name: "", Enabled: true},
	}}
	got := datasourceFocusAreas(p)
	if !strings.Contains(got, "Catalog Performance") {
		t.Errorf("enabled area missing: %q", got)
	}
	if strings.Contains(got, "Disabled Area") {
		t.Errorf("disabled area leaked: %q", got)
	}
}

func TestBuildDatasourcesPromptSection(t *testing.T) {
	dc := &datasourceContext{descriptors: []datasourceDescriptor{
		{
			id: "default", label: "Sales PG", provider: "postgres", tableCount: 4,
			card: &models.WarehouseCard{SubjectAreas: []string{"sales"}, KeyMetrics: []string{"revenue"}},
		},
		{
			id: "wh_oracle", label: "Catalog", provider: "oracle", tableCount: 7,
			description: "music catalog",
			prompts: &models.ProjectPrompts{AnalysisAreas: map[string]models.AnalysisAreaConfig{
				"g": {Name: "Genre Performance", Enabled: true},
			}},
		},
	}}
	got := buildDatasourcesPromptSection(dc)
	for _, want := range []string{
		"datasource_id", "default", "wh_oracle", "postgres", "oracle",
		"revenue", "Genre Performance", "music catalog",
		"cross-datasource join", "bounded value-passing",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt section missing %q\n---\n%s", want, got)
		}
	}
}

func TestBuildGroupedCatalog(t *testing.T) {
	o := &Orchestrator{}
	dc := &datasourceContext{
		descriptors: []datasourceDescriptor{
			{id: "default", provider: "postgres", tableCount: 1},
			{id: "wh_oracle", provider: "oracle", tableCount: 1},
		},
		schemasByDS: map[string]map[string]models.TableSchema{
			"default":   {"public.invoice": {RowCount: 412}},
			"wh_oracle": {"CHINOOK.TRACK": {RowCount: 3503}},
		},
	}
	got := o.buildGroupedCatalog(dc, nil)
	for _, want := range []string{
		"Datasource `default`", "Datasource `wh_oracle`",
		"public.invoice", "CHINOOK.TRACK",
		`datasource_id: "default"`, `datasource_id: "wh_oracle"`,
	} {
		if !strings.Contains(got.Catalog, want) {
			t.Errorf("grouped catalog missing %q\n---\n%s", want, got.Catalog)
		}
	}
}
