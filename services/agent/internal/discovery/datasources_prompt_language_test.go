package discovery

import (
	"os"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// sqlOnlyContext is the all-SQL multi-datasource run: the shape every
// multi-warehouse project has today. It exercises every field the routing
// contract renders — label, description, card, per-datasource pack areas — so
// the golden below covers the whole section rather than its opening.
func sqlOnlyContext() *datasourceContext {
	return &datasourceContext{descriptors: []datasourceDescriptor{
		{
			id: "default", label: "Sales PG", provider: "postgres", tableCount: 4,
			description: "orders and invoices",
			card: &models.WarehouseCard{
				SubjectAreas: []string{"sales", "billing"},
				KeyEntities:  []string{"invoice", "customer"},
				KeyMetrics:   []string{"revenue", "arpu"},
			},
		},
		{
			id: "wh_oracle", label: "Catalog", provider: "oracle", tableCount: 7,
			description: "music catalog",
			prompts: &models.ProjectPrompts{AnalysisAreas: map[string]models.AnalysisAreaConfig{
				"g": {Name: "Genre Performance", Enabled: true},
			}},
		},
	}}
}

// TestBuildDatasourcesPromptSection_AllSQLIsUnchanged pins the text every
// existing multi-warehouse project receives, byte for byte.
//
// The golden was captured from the tree before this file existed, so a diff
// against it is a diff against what shipped. It is a fixture, not an output:
// regenerating it to make a failure go away deletes the only evidence that a
// run with no non-SQL datasource still reads exactly as it did.
func TestBuildDatasourcesPromptSection_AllSQLIsUnchanged(t *testing.T) {
	want, err := os.ReadFile("testdata/datasources_prompt_all_sql.golden")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got := buildDatasourcesPromptSection(sqlOnlyContext()); got != string(want) {
		t.Errorf("all-SQL routing contract changed.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
