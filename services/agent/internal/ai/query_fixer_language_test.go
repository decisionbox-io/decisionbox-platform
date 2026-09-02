package ai

import (
	"context"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// reportRequest is what a source whose query language is a request format
// answers the repair prompt with: the corrected query IS a JSON object.
const reportRequest = `{"metrics":["activeUsers"],"dimensions":["country"],"date_ranges":[{"start_date":"28daysAgo","end_date":"yesterday"}]}`

// TestNewQueryFixerFor_TakesBothFromTheSource covers the wiring, not the text.
// The instruction and the extractor can each be correct and still leave a
// source without a repair path if the fixer is built with one of the two
// fields dropped — and a fixer built with the source's own repair template but
// the SQL instruction is worse than one built with neither, because the model
// then reads a template saying "this accepts no SQL" directly above an
// instruction demanding SQL.
func TestNewQueryFixerFor_TakesBothFromTheSource(t *testing.T) {
	warehouse := ai_testSQLProvider{}
	f := NewQueryFixerFor(warehouse, SQLFixerOptions{})
	if f.sqlFixPrompt != "sql fix template" {
		t.Errorf("a warehouse got repair template %q, want its own", f.sqlFixPrompt)
	}
	if f.language != "" {
		t.Errorf("a warehouse was given query language %q; its instruction must stay SQL", f.language)
	}

	native := ai_testNativeRunner{}
	n := NewQueryFixerFor(native, SQLFixerOptions{})
	if n.sqlFixPrompt != "native fix template" {
		t.Errorf("a non-SQL source got repair template %q, want its own", n.sqlFixPrompt)
	}
	if n.language != "Report Request (JSON)" {
		t.Errorf("a non-SQL source got query language %q, want its own", n.language)
	}

	// Options the caller set must survive being filled in around.
	c := NewQueryFixerFor(warehouse, SQLFixerOptions{Dataset: "ds", Filter: "app_id = 'x'"})
	if c.dataset != "ds" || c.filter != "app_id = 'x'" {
		t.Errorf("caller options were dropped: dataset=%q filter=%q", c.dataset, c.filter)
	}
}

// TestFixInstruction_SQLIsUnchanged pins the instruction every warehouse gets.
// It is the last thing the model reads before writing a replacement, so it is
// the sentence a language branch must not disturb.
func TestFixInstruction_SQLIsUnchanged(t *testing.T) {
	const want = "Fix this SQL query (attempt 3). Return ONLY the corrected SQL.\n\n" +
		"Query:\n```sql\nSELECT 1\n```\n\nError:\n```\nboom\n```"
	if got := fixInstruction("", "SELECT 1", "boom", 2); got != want {
		t.Errorf("the SQL fix instruction changed:\ngot:  %q\nwant: %q", got, want)
	}
}

// TestFixInstruction_NamesTheLanguageItIsAskingFor covers the contradiction
// that made this branch necessary: the source's repair template says "this
// source accepts no SQL", and the instruction immediately below it said "fix
// this SQL query" inside a ```sql fence. The instruction is later and more
// concrete, so it wins.
func TestFixInstruction_NamesTheLanguageItIsAskingFor(t *testing.T) {
	got := fixInstruction("Report Request (JSON)", reportRequest, "unknown dimension", 0)

	if strings.Contains(got, "```sql") {
		t.Errorf("a non-SQL query is fenced as SQL:\n%s", got)
	}
	if strings.Contains(got, "Fix this SQL query") || strings.Contains(got, "corrected SQL") {
		t.Errorf("a source that accepts no SQL is asked to fix SQL:\n%s", got)
	}
	for _, want := range []string{"Report Request (JSON)", "not SQL", reportRequest, "unknown dimension"} {
		if !strings.Contains(got, want) {
			t.Errorf("fix instruction is missing %q:\n%s", want, got)
		}
	}
}

// TestExtractFixedSQL_AcceptsAStructuredQuery is the fix that gives a non-SQL
// source a repair path at all. Before it, every reply to such a source's
// repair prompt was discarded as "not SQL": the attempt was recorded as a
// parse failure and the query failed on its first error.
func TestExtractFixedSQL_AcceptsAStructuredQuery(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"raw object, as the repair prompt asks for", reportRequest, reportRequest},
		{"fenced with a json tag", "```json\n" + reportRequest + "\n```", reportRequest},
		{"fenced with no tag", "```\n" + reportRequest + "\n```", reportRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractFixedSQL(&gollm.ChatResponse{Content: tc.body})
			if err != nil {
				t.Fatalf("a corrected request was discarded: %v", err)
			}
			if got != tc.want {
				t.Errorf("extracted %q, want %q", got, tc.want)
			}
		})
	}
}

// TestExtractFixedSQL_StructuredAcceptanceDoesNotSwallowAnEnvelope guards the
// one way the new path could do harm. A {"fixed_sql": …} wrapper is the model
// packaging its answer, so its CONTENTS are the query — returning the wrapper
// itself would submit the packaging to the source as if it were the query, and
// it parses as JSON, so nothing else would have stopped it.
func TestExtractFixedSQL_StructuredAcceptanceDoesNotSwallowAnEnvelope(t *testing.T) {
	if got, err := extractFixedSQL(&gollm.ChatResponse{Content: `{"fixed_sql":"SELECT 1"}`}); err != nil || got != "SELECT 1" {
		t.Errorf("a valid envelope must still yield its contents: got %q, err %v", got, err)
	}

	// The value is not a query, so extraction above rejected it. The reply
	// carried nothing usable and must be refused, not handed on as an object.
	got, err := extractFixedSQL(&gollm.ChatResponse{Content: `{"fixed_sql":"I cannot fix this"}`})
	if err == nil {
		t.Errorf("an envelope with no query in it was accepted as a structured query: %q", got)
	}

	if got, err := extractFixedSQL(&gollm.ChatResponse{Content: `{}`}); err == nil {
		t.Errorf("an empty object asks for nothing and must be refused: %q", got)
	}
}

// TestExtractFixedSQL_StillRefusesProse pins what the acceptance rule is FOR.
// Handing prose back as a query spends a retry and records an error about the
// wrong thing.
func TestExtractFixedSQL_StillRefusesProse(t *testing.T) {
	for _, body := range []string{
		"I'm sorry, I cannot correct this query.",
		"The dimension does not exist on this property.",
		"[1, 2, 3]",
	} {
		if got, err := extractFixedSQL(&gollm.ChatResponse{Content: body}); err == nil {
			t.Errorf("prose was accepted as a query: input %q gave %q", body, got)
		}
	}
}

// TestSQLFixer_RepairsANonSQLQueryEndToEnd runs the whole fixer, because the
// instruction and the extractor are separately correct and still leave the
// source without a repair path if they are not wired to the same language.
func TestSQLFixer_RepairsANonSQLQueryEndToEnd(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	provider.DefaultResponse = &gollm.ChatResponse{
		Content:    reportRequest,
		Model:      "mock-model",
		StopReason: "end_turn",
		Usage:      gollm.Usage{InputTokens: 10, OutputTokens: 5},
	}
	client, _ := New(provider, "mock-model")

	fixer := NewSQLFixer(SQLFixerOptions{
		Client:        client,
		SQLFixPrompt:  "Correct this request: {{ORIGINAL_SQL}}. Error: {{ERROR_MESSAGE}}",
		QueryLanguage: "Report Request (JSON)",
	})

	fix, err := fixer.FixSQL(context.Background(), `{"metrics":["nope"]}`, "unknown metric nope", 0, queryexec.FixOpts{})
	if err != nil {
		t.Fatalf("a non-SQL source still has no repair path: %v", err)
	}
	if fix.FixedSQL != reportRequest {
		t.Errorf("repaired query = %q, want %q", fix.FixedSQL, reportRequest)
	}
	if !strings.Contains(fix.Prompt, "Report Request (JSON)") {
		t.Errorf("the recorded prompt does not name the language the model was asked for:\n%s", fix.Prompt)
	}
	if strings.Contains(fix.Prompt, "```sql") {
		t.Errorf("the recorded prompt fenced a non-SQL query as SQL:\n%s", fix.Prompt)
	}
}

// ai_testSQLProvider is a warehouse: it does not implement QueryRunner, so the
// seam adapts it and its language resolves to SQL.
type ai_testSQLProvider struct{ gowarehouse.Provider }

func (ai_testSQLProvider) SQLFixPrompt() string { return "sql fix template" }

// ai_testNativeRunner supplies the seam itself, which is how a source declares
// that its queries are not SQL.
type ai_testNativeRunner struct{ gowarehouse.Provider }

func (ai_testNativeRunner) RunQuery(context.Context, gowarehouse.NativeQuery) (*gowarehouse.QueryResult, error) {
	return nil, nil
}
func (ai_testNativeRunner) QueryLanguage() string  { return "Report Request (JSON)" }
func (ai_testNativeRunner) QueryFixPrompt() string { return "native fix template" }
