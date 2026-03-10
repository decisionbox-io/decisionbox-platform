package discovery

import (
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/domainpack"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func TestBuildFilterClause(t *testing.T) {
	tests := []struct {
		name        string
		filterField string
		filterValue string
		want        string
	}{
		{"with filter", "app_id", "test-123", "WHERE app_id = 'test-123'"},
		{"empty field", "", "test-123", ""},
		{"empty value", "app_id", "", ""},
		{"no filter", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &Orchestrator{filterField: tt.filterField, filterValue: tt.filterValue}
			got := o.buildFilterClause()
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildFilterContext(t *testing.T) {
	o := &Orchestrator{filterField: "app_id", filterValue: "abc"}
	ctx := o.buildFilterContext()
	if ctx == "" {
		t.Error("should return context when filter is set")
	}

	o2 := &Orchestrator{}
	if o2.buildFilterContext() != "" {
		t.Error("should return empty when no filter")
	}
}

func TestBuildFilterRule(t *testing.T) {
	o := &Orchestrator{filterField: "app_id", filterValue: "abc"}
	rule := o.buildFilterRule()
	if rule == "" {
		t.Error("should return rule when filter is set")
	}

	o2 := &Orchestrator{}
	rule2 := o2.buildFilterRule()
	if rule2 == "" {
		t.Error("should return no-filter-required message")
	}
}

func TestBuildAnalysisAreasDescription(t *testing.T) {
	o := &Orchestrator{}
	areas := []domainpack.AnalysisArea{
		{ID: "churn", Name: "Churn Risks", Description: "Players leaving"},
		{ID: "levels", Name: "Level Difficulty", Description: "Hard levels"},
	}

	desc := o.buildAnalysisAreasDescription(areas)
	if desc == "" {
		t.Error("should produce description")
	}
	if !contains(desc, "Churn Risks") || !contains(desc, "Level Difficulty") {
		t.Error("should contain area names")
	}
}

func TestBuildPreviousContext(t *testing.T) {
	o := &Orchestrator{}

	// No context
	if o.buildPreviousContext(nil) != "" {
		t.Error("nil context should return empty")
	}

	// Empty context
	ctx := models.NewProjectContext("test")
	if o.buildPreviousContext(ctx) != "" {
		t.Error("empty context should return empty")
	}

	// Context with discoveries
	ctx.TotalDiscoveries = 5
	ctx.AddNote("schema", "sessions table has user_id", 0.9)
	result := o.buildPreviousContext(ctx)
	if result == "" {
		t.Error("should return context when discoveries exist")
	}
}

func TestSimplifySchemas(t *testing.T) {
	o := &Orchestrator{}
	schemas := map[string]models.TableSchema{
		"sessions": {
			TableName: "sessions",
			RowCount:  1000,
			Columns: []models.ColumnInfo{
				{Name: "user_id", Type: "STRING", Category: "primary_key"},
				{Name: "duration", Type: "INT64", Category: "metric"},
			},
			Metrics:    []string{"duration"},
			Dimensions: []string{},
		},
	}

	simplified := o.simplifySchemas(schemas)

	if _, ok := simplified["sessions"]; !ok {
		t.Fatal("should contain sessions table")
	}
	table := simplified["sessions"].(map[string]interface{})
	if table["row_count"].(int64) != 1000 {
		t.Error("row_count should be 1000")
	}
	cols := table["columns"].([]map[string]string)
	if len(cols) != 2 {
		t.Errorf("columns = %d, want 2", len(cols))
	}
}

func TestFilterQueriesByKeywords(t *testing.T) {
	o := &Orchestrator{}
	steps := []models.ExplorationStep{
		{Query: "SELECT * FROM sessions WHERE retention > 0", Thinking: "check retention", QueryPurpose: "retention analysis"},
		{Query: "SELECT * FROM levels WHERE quit_rate > 0.5", Thinking: "check level difficulty"},
		{Query: "", Thinking: "no query here"}, // should be filtered out
		{Query: "SELECT revenue FROM purchases", Thinking: "revenue check"},
	}

	// Filter for churn/retention keywords
	filtered := o.filterQueriesByKeywords(steps, []string{"retention", "churn", "cohort"})
	if len(filtered) != 1 {
		t.Errorf("filtered = %d, want 1", len(filtered))
	}

	// Filter for level keywords
	filtered = o.filterQueriesByKeywords(steps, []string{"level", "quit", "difficulty"})
	if len(filtered) != 1 {
		t.Errorf("filtered = %d, want 1", len(filtered))
	}

	// Filter for revenue keywords
	filtered = o.filterQueriesByKeywords(steps, []string{"revenue", "purchase"})
	if len(filtered) != 1 {
		t.Errorf("filtered = %d, want 1", len(filtered))
	}
}

func TestCleanJSONResponse(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "json code block",
			input: "Here:\n```json\n{\"key\": \"value\"}\n```",
			want:  `{"key": "value"}`,
		},
		{
			name:  "generic code block",
			input: "```\n{\"key\": \"value\"}\n```",
			want:  `{"key": "value"}`,
		},
		{
			name:  "raw json with prefix",
			input: "Result: {\"key\": \"value\"}",
			want:  `{"key": "value"}`,
		},
		{
			name:  "already clean json",
			input: `{"key": "value"}`,
			want:  `{"key": "value"}`,
		},
		{
			name:  "array json",
			input: "Result: [{\"a\":1}]",
			want:  `[{"a":1}]`,
		},
		{
			name:  "no json",
			input: "just plain text",
			want:  "just plain text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanJSONResponse(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
