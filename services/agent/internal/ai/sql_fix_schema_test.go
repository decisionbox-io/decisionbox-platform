package ai

import (
	"context"
	"strconv"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

func cols(names ...string) []models.ColumnInfo {
	out := make([]models.ColumnInfo, 0, len(names))
	for _, n := range names {
		out = append(out, models.ColumnInfo{Name: n, Type: "text"})
	}
	return out
}

// The defect this fix exists for: the fix prompt's "Table Schemas" section
// was the lean catalog, which renders column COUNTS ("shop.events | 6c"),
// while the same prompt tells the model to use only columns listed there. A
// `column X does not exist` error was therefore unfixable except by guessing.
//
// This asserts the real column name reaches the prompt. Revert the
// {{SCHEMA_INFO}} substitution in FixSQL to f.schemaCtx and it fails.
func TestSchemaInfoFor_NamesTheColumnsOfReferencedTables(t *testing.T) {
	f := &SQLFixer{schemaCtx: "shop.events | 6c | 92.4K rows"}
	f.SetTableColumnResolver(func(table string) []models.ColumnInfo {
		if table == "shop.events" {
			return cols("event_id", "customer_id", "event_type", "event_ts")
		}
		return nil
	})

	got := f.schemaInfoFor("SELECT count(*) FROM shop.events e WHERE e.created_at > now()")

	if !strings.Contains(got, "event_ts") {
		t.Fatalf("the real column name never reached the prompt; got:\n%s", got)
	}
	if !strings.Contains(got, "shop.events | 6c | 92.4K rows") {
		t.Errorf("the lean catalog should remain as the backdrop; got:\n%s", got)
	}
}

func TestSchemaInfoFor_FallsBackToCatalog(t *testing.T) {
	const catalog = "shop.events | 6c | 92.4K rows"
	tests := []struct {
		name    string
		resolve func(string) []models.ColumnInfo
		query   string
	}{
		{"no resolver", nil, "SELECT 1 FROM shop.events"},
		{"table unknown to the cache", func(string) []models.ColumnInfo { return nil }, "SELECT 1 FROM other.thing"},
		{"nothing parses out of the statement", func(string) []models.ColumnInfo { return cols("a") }, "this is not sql at all"},
		{"resolver returns unnamed columns", func(string) []models.ColumnInfo {
			return []models.ColumnInfo{{Name: "", Type: "text"}}
		}, "SELECT 1 FROM shop.events"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &SQLFixer{schemaCtx: catalog}
			if tc.resolve != nil {
				f.SetTableColumnResolver(tc.resolve)
			}
			if got := f.schemaInfoFor(tc.query); got != catalog {
				t.Errorf("want the catalog unchanged, got:\n%s", got)
			}
		})
	}
}

func TestSchemaInfoFor_CapsExpandedTables(t *testing.T) {
	f := &SQLFixer{}
	f.SetTableColumnResolver(func(table string) []models.ColumnInfo { return cols("id") })
	var q strings.Builder
	q.WriteString("SELECT 1 FROM t0")
	for i := 1; i < maxFixSchemaTables+4; i++ {
		q.WriteString(" JOIN t")
		q.WriteString(string(rune('0' + i)))
		q.WriteString(" ON 1=1")
	}
	lines := strings.Count(f.schemaInfoFor(q.String()), "\n") + 1
	if lines > maxFixSchemaTables {
		t.Errorf("expanded %d tables, cap is %d", lines, maxFixSchemaTables)
	}
}

func TestReferencedTables(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"bare", "SELECT 1 FROM shop.events", []string{"shop.events"}},
		{"bigquery backticks", "SELECT 1 FROM `shop.events`", []string{"shop.events"}},
		{"ansi quotes", `SELECT 1 FROM "shop"."events"`, []string{"shop.events"}},
		{"sqlserver brackets", "SELECT 1 FROM [shop].[events]", []string{"shop.events"}},
		{"join flavours", "SELECT 1 FROM a LEFT JOIN b ON 1=1 CROSS JOIN c", []string{"a", "b", "c"}},
		{"deduped case-insensitively", "SELECT 1 FROM shop.events JOIN SHOP.EVENTS ON 1=1", []string{"shop.events"}},
		{"subquery open paren is not a table", "SELECT 1 FROM ( SELECT 1 FROM shop.orders ) x", []string{"shop.orders"}},
		{"no tables", "SELECT 1", nil},
		// BigQuery project ids carry hyphens; some warehouses allow $ in
		// identifiers. Both are in the char class the existing
		// discovery.queryTableRefRe accepts, so this one accepts them too.
		{"hyphenated project ref", "SELECT 1 FROM `my-proj.ds.events`", []string{"my-proj.ds.events"}},
		{"dollar in identifier", "SELECT 1 FROM shop.events$raw", []string{"shop.events$raw"}},
		// A non-SQL source's query has no FROM/JOIN, so nothing is extracted
		// and schemaInfoFor falls back to the catalog. The fixer serves
		// non-SQL datasources too (warehouse.QueryRunner), so this path is
		// real, not hypothetical.
		{"non-SQL request format", "{\"metric\":\"sessions\",\"breakdown\":\"country\"}", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := referencedTables(tc.query)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if !strings.EqualFold(got[i], tc.want[i]) {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// The wiring test, not the helper test. TestSchemaInfoFor_* above calls
// schemaInfoFor directly, so they pass even if FixSQL never consults it —
// which is precisely the mutation that must not survive. This goes through
// FixSQL and asserts the rendered prompt the caller records.
func TestFixSQL_RenderedPromptNamesTheRealColumns(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	provider.DefaultResponse = &gollm.ChatResponse{
		Content:    "```sql\nSELECT count(*) FROM shop.events\n```",
		Model:      "mock-model",
		StopReason: "end_turn",
	}
	client, _ := New(provider, "mock-model")

	fixer := NewSQLFixer(SQLFixerOptions{
		Client:       client,
		SQLFixPrompt: "Table Schemas: {{SCHEMA_INFO}}\nFix {{ORIGINAL_SQL}}\nError: {{ERROR_MESSAGE}}",
		Dataset:      "shop",
	})
	fixer.SetSchemaContext("shop.events | 6c | 92.4K rows")
	fixer.SetTableColumnResolver(func(table string) []models.ColumnInfo {
		if strings.EqualFold(table, "shop.events") {
			return cols("event_id", "customer_id", "event_type", "event_ts")
		}
		return nil
	})

	fix, err := fixer.FixSQL(context.Background(),
		"SELECT count(*) FROM shop.events e WHERE e.created_at > now()",
		"pq: column e.created_at does not exist", 1, queryexec.FixOpts{})
	if err != nil {
		t.Fatalf("FixSQL: %v", err)
	}
	if !strings.Contains(fix.Prompt, "event_ts") {
		t.Fatalf("the prompt sent to the model never named the real column:\n%s", fix.Prompt)
	}
}

// A wide audit / event table runs to 200+ columns, and six of them would
// swamp the error context the fix prompt exists to act on. Cap the columns
// per table the way discovery's lookup path caps its own — but say so, since
// a model that reads a truncated list as exhaustive draws the opposite
// conclusion to the one this change was made to enable.
func TestSchemaInfoFor_CapsColumnsPerTable(t *testing.T) {
	wide := make([]string, 0, maxFixSchemaColumns+37)
	for i := 0; i < maxFixSchemaColumns+37; i++ {
		wide = append(wide, "c"+strconv.Itoa(i))
	}
	f := &SQLFixer{}
	f.SetTableColumnResolver(func(string) []models.ColumnInfo { return cols(wide...) })

	got := f.schemaInfoFor("SELECT 1 FROM shop.events")

	if n := strings.Count(got, "text"); n != maxFixSchemaColumns {
		t.Errorf("rendered %d columns, cap is %d", n, maxFixSchemaColumns)
	}
	if !strings.Contains(got, "(+37 more columns not shown)") {
		t.Errorf("truncation not stated, so the list reads as exhaustive:\n%s", got)
	}
	// The last column inside the cap is present and the first one past it is
	// not — the cap takes a prefix rather than an arbitrary subset.
	if !strings.Contains(got, "c"+strconv.Itoa(maxFixSchemaColumns-1)+" text") {
		t.Error("the last column inside the cap is missing")
	}
	if strings.Contains(got, "c"+strconv.Itoa(maxFixSchemaColumns)+" text") {
		t.Error("a column past the cap was rendered")
	}
}

// Exactly at the cap is not truncation. The note is a claim about the data,
// so emitting it when nothing was dropped would be a (small) lie to the model.
func TestSchemaInfoFor_NoTruncationNoteAtExactlyTheCap(t *testing.T) {
	exact := make([]string, 0, maxFixSchemaColumns)
	for i := 0; i < maxFixSchemaColumns; i++ {
		exact = append(exact, "c"+strconv.Itoa(i))
	}
	f := &SQLFixer{}
	f.SetTableColumnResolver(func(string) []models.ColumnInfo { return cols(exact...) })

	if got := f.schemaInfoFor("SELECT 1 FROM shop.events"); strings.Contains(got, "more columns") {
		t.Errorf("claimed truncation with nothing dropped:\n%s", got)
	}
}

// Unnamed columns are skipped, and a skipped column is not a dropped one —
// the note must count only what the cap actually withheld.
func TestSchemaInfoFor_TruncationNoteIgnoresUnnamedColumns(t *testing.T) {
	withBlanks := make([]models.ColumnInfo, 0, maxFixSchemaColumns+5)
	for i := 0; i < maxFixSchemaColumns+2; i++ {
		withBlanks = append(withBlanks, models.ColumnInfo{Name: "c" + strconv.Itoa(i), Type: "text"})
	}
	withBlanks = append(withBlanks, models.ColumnInfo{Type: "text"}, models.ColumnInfo{Type: "text"})
	f := &SQLFixer{}
	f.SetTableColumnResolver(func(string) []models.ColumnInfo { return withBlanks })

	if got := f.schemaInfoFor("SELECT 1 FROM shop.events"); !strings.Contains(got, "(+2 more columns not shown)") {
		t.Errorf("want the 2 capped columns counted and the 2 unnamed ones ignored:\n%s", got)
	}
}
