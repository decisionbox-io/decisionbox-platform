package discovery

import (
	"context"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// fixerSQLProvider is a warehouse: it does not implement QueryRunner, so the
// seam adapts it and its queries stay SQL.
type fixerSQLProvider struct{ gowarehouse.Provider }

func (fixerSQLProvider) SQLFixPrompt() string { return "SQL REPAIR TEMPLATE" }

// fixerNativeProvider supplies the query seam itself, which is how a source
// declares that its queries are not SQL.
type fixerNativeProvider struct{ gowarehouse.Provider }

func (fixerNativeProvider) RunQuery(context.Context, gowarehouse.NativeQuery) (*gowarehouse.QueryResult, error) {
	return nil, nil
}
func (fixerNativeProvider) QueryLanguage() string  { return "Report Request (JSON)" }
func (fixerNativeProvider) QueryFixPrompt() string { return "NATIVE REPAIR TEMPLATE" }

// TestOrchestrator_NewQueryFixer_ReadsTheSourceNotTheSQLSurface covers the
// construction both orchestration paths share.
//
// The repair template and the instruction have to arrive as a matched pair. A
// fixer holding a source's own template while still instructing the model in
// SQL contradicts itself, and the model resolves that in favour of the
// instruction — it is later and more concrete. So the assertion is made
// against the RENDERED dialog, which is what the model actually sees and what
// the fix history records.
func TestOrchestrator_NewQueryFixer_ReadsTheSourceNotTheSQLSurface(t *testing.T) {
	render := func(t *testing.T, p gowarehouse.Provider, query string) string {
		t.Helper()
		llm := testutil.NewMockLLMProvider()
		llm.DefaultResponse = &gollm.ChatResponse{Content: "SELECT 1", StopReason: "end_turn"}
		client, err := ai.New(llm, "mock-model")
		if err != nil {
			t.Fatalf("ai.New: %v", err)
		}
		o := &Orchestrator{aiClient: client}

		fix, err := o.newQueryFixer(p, "ds", "app_id = 'x'").
			FixSQL(context.Background(), query, "boom", 0, queryexec.FixOpts{})
		if err != nil {
			t.Fatalf("FixSQL: %v", err)
		}
		if fix.Prompt == "" {
			t.Fatal("no dialog was recorded")
		}
		return fix.Prompt
	}

	t.Run("a warehouse is repaired exactly as before", func(t *testing.T) {
		got := render(t, fixerSQLProvider{}, "SELECT BAD FROM t")
		if !strings.Contains(got, "SQL REPAIR TEMPLATE") {
			t.Errorf("a warehouse did not get its own repair template:\n%s", got)
		}
		if !strings.Contains(got, "Fix this SQL query") {
			t.Errorf("a warehouse's instruction is no longer SQL:\n%s", got)
		}
	})

	t.Run("a non-SQL source gets its own template AND its own instruction", func(t *testing.T) {
		got := render(t, fixerNativeProvider{}, `{"metrics":["nope"]}`)
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
