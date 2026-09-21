package agentserver

import (
	"context"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// askFixerSQLProvider is a warehouse: it does not implement QueryRunner, so
// the seam adapts it and its queries stay SQL.
type askFixerSQLProvider struct{ gowarehouse.Provider }

func (askFixerSQLProvider) SQLFixPrompt() string { return "SQL REPAIR TEMPLATE" }

// askFixerNativeProvider supplies the query seam itself, which is how a source
// declares that its queries are not SQL.
type askFixerNativeProvider struct{ gowarehouse.Provider }

func (askFixerNativeProvider) RunQuery(context.Context, gowarehouse.NativeQuery) (*gowarehouse.QueryResult, error) {
	return nil, nil
}
func (askFixerNativeProvider) QueryLanguage() string  { return "Report Request (JSON)" }
func (askFixerNativeProvider) QueryFixPrompt() string { return "NATIVE REPAIR TEMPLATE" }

// The repair template and the instruction must arrive as a matched pair. The
// assertion is made against the RENDERED dialog because that is what the model
// sees and what the fix history records — a fixer configured correctly and
// rendering SQL anyway is the failure that matters.
func TestAskQueryFixer_ReadsTheSourceNotTheSQLSurface(t *testing.T) {
	render := func(t *testing.T, wp gowarehouse.Provider, query string) string {
		t.Helper()
		llm := testutil.NewMockLLMProvider()
		llm.DefaultResponse = &gollm.ChatResponse{Content: "SELECT 1", StopReason: "end_turn"}
		client, err := ai.New(llm, "mock-model")
		if err != nil {
			t.Fatalf("ai.New: %v", err)
		}
		wh := models.WarehouseConfig{ID: "wh_1", FilterField: "app_id", FilterValue: "x"}

		fix, err := askQueryFixer(client, wp, []string{"ds"}, wh).
			FixSQL(context.Background(), query, "boom", 0, queryexec.FixOpts{})
		if err != nil {
			t.Fatalf("FixSQL: %v", err)
		}
		return fix.Prompt
	}

	t.Run("a warehouse is repaired exactly as before", func(t *testing.T) {
		got := render(t, askFixerSQLProvider{}, "SELECT BAD FROM t")
		if !strings.Contains(got, "SQL REPAIR TEMPLATE") {
			t.Errorf("a warehouse did not get its own repair template:\n%s", got)
		}
		if !strings.Contains(got, "Fix this SQL query") {
			t.Errorf("a warehouse's instruction is no longer SQL:\n%s", got)
		}
	})

	t.Run("a non-SQL source gets its own template AND its own instruction", func(t *testing.T) {
		got := render(t, askFixerNativeProvider{}, `{"metrics":["nope"]}`)
		if !strings.Contains(got, "NATIVE REPAIR TEMPLATE") {
			t.Errorf("a non-SQL source did not get its own repair template:\n%s", got)
		}
		if !strings.Contains(got, "Report Request (JSON)") {
			t.Errorf("the instruction does not name the source's language:\n%s", got)
		}
		if strings.Contains(got, "Fix this SQL query") || strings.Contains(got, "```sql") {
			t.Errorf("a source that accepts no SQL was still told to fix SQL:\n%s", got)
		}
	})
}
