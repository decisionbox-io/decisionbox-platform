package schema_render

import (
	"fmt"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// ---------- renderCatalog ----------

func TestRenderCatalog_Empty(t *testing.T) {
	got := renderCatalog(nil)
	if !strings.Contains(got, "no tables") {
		t.Errorf("empty catalog placeholder changed: %q", got)
	}
}

func TestRenderCatalog_SingleTable(t *testing.T) {
	got := renderCatalog([]CatalogEntry{
		{Table: "TBLSIPAMAS", ColumnCount: 14, RowCount: 2_100_000, Keywords: []string{"sales", "orders"}, JoinsTo: []string{"TBLSIPATRA"}},
	})
	want := "TBLSIPAMAS | 14c | 2.1M rows | sales, orders | -> TBLSIPATRA"
	if got != want {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
}

func TestRenderCatalog_NoKeywordsNoJoins(t *testing.T) {
	got := renderCatalog([]CatalogEntry{{Table: "x", ColumnCount: 3, RowCount: 10}})
	if got != "x | 3c | 10 rows" {
		t.Errorf("got %q", got)
	}
}

func TestRenderCatalog_MultipleTables_NoTrailingNewline(t *testing.T) {
	got := renderCatalog([]CatalogEntry{
		{Table: "a", ColumnCount: 1, RowCount: 5},
		{Table: "b", ColumnCount: 2, RowCount: 10},
	})
	if strings.HasSuffix(got, "\n") {
		t.Error("output should not end with newline")
	}
	if !strings.Contains(got, "a | 1c | 5 rows\nb | 2c | 10 rows") {
		t.Errorf("got %q", got)
	}
}

func TestRenderCatalog_DuplicateKeywordsCollapsed(t *testing.T) {
	got := renderCatalog([]CatalogEntry{
		{Table: "t", ColumnCount: 1, RowCount: 1, Keywords: []string{"sales", "sales", "orders"}},
	})
	if strings.Count(got, "sales") != 1 {
		t.Errorf("duplicate keyword not deduped: %q", got)
	}
}

func TestRenderCatalog_KeywordsCappedAtThree(t *testing.T) {
	got := renderCatalog([]CatalogEntry{
		{Table: "t", ColumnCount: 1, RowCount: 1, Keywords: []string{"a", "b", "c", "d", "e"}},
	})
	// d and e must NOT appear.
	if strings.Contains(got, "d") || strings.Contains(got, "e") {
		t.Errorf("keywords should cap at 3: %q", got)
	}
}

// ---------- renderRetrieved ----------

func TestRenderRetrieved_Empty(t *testing.T) {
	got := renderRetrieved(nil, 3)
	if !strings.Contains(got, "inspect_table") {
		t.Errorf("empty Level 1 should hint about inspect_table: %q", got)
	}
}

func TestRenderRetrieved_SingleTable_NullAndCategory(t *testing.T) {
	got := renderRetrieved([]TableDetail{
		{
			Table:    "users",
			RowCount: 150_000,
			Columns: []models.ColumnInfo{
				{Name: "user_id", Type: "INT64", Nullable: false, Category: "primary_key"},
				{Name: "email", Type: "STRING", Nullable: true, Category: "dimension"},
				{Name: "signup_ts", Type: "TIMESTAMP", Nullable: false, Category: "time"},
			},
			SampleRows: []map[string]interface{}{
				{"user_id": 1, "email": "a@example.com", "signup_ts": "2026-01-01T00:00:00Z"},
			},
		},
	}, 3)
	if !strings.Contains(got, "TABLE users (150K rows)") {
		t.Errorf("missing header line: %q", got)
	}
	if !strings.Contains(got, "- user_id INT64 NOT NULL [primary_key]") {
		t.Errorf("column line wrong:\n%s", got)
	}
	if !strings.Contains(got, "- email STRING NULL [dimension]") {
		t.Errorf("nullable column line wrong:\n%s", got)
	}
	if !strings.Contains(got, "user_id=1") {
		t.Errorf("sample row missing: %s", got)
	}
}

func TestRenderRetrieved_SampleRowTruncation(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := renderRetrieved([]TableDetail{{
		Table: "t", RowCount: 1,
		Columns:    []models.ColumnInfo{{Name: "payload", Type: "STRING"}},
		SampleRows: []map[string]interface{}{{"payload": long}},
	}}, 3)
	if strings.Contains(got, strings.Repeat("x", 500)) {
		t.Error("long sample value should be truncated")
	}
	if !strings.Contains(got, "…") {
		t.Error("truncated value should end with ellipsis")
	}
}

func TestRenderRetrieved_SampleRowWhitespaceCollapsed(t *testing.T) {
	multiline := "line1\nline2\nline3"
	got := renderRetrieved([]TableDetail{{
		Table: "t", RowCount: 1,
		Columns:    []models.ColumnInfo{{Name: "v", Type: "STRING"}},
		SampleRows: []map[string]interface{}{{"v": multiline}},
	}}, 3)
	if strings.Contains(got, "line1\n    line2") {
		t.Error("multi-line sample values should be collapsed to one line")
	}
}

func TestRenderRetrieved_NilValue(t *testing.T) {
	got := renderRetrieved([]TableDetail{{
		Table: "t", RowCount: 1,
		Columns:    []models.ColumnInfo{{Name: "v", Type: "STRING"}},
		SampleRows: []map[string]interface{}{{"v": nil}},
	}}, 3)
	if !strings.Contains(got, "v=NULL") {
		t.Errorf("nil sample value should render as NULL: %s", got)
	}
}

func TestRenderRetrieved_SampleRowCappedAtMax(t *testing.T) {
	rows := []map[string]interface{}{{"a": 1}, {"a": 2}, {"a": 3}, {"a": 4}, {"a": 5}}
	got := renderRetrieved([]TableDetail{{
		Table: "t", RowCount: 5,
		Columns:    []models.ColumnInfo{{Name: "a", Type: "INT64"}},
		SampleRows: rows,
	}}, 2)
	if strings.Count(got, "a=") != 2 {
		t.Errorf("expected 2 sample rows, got:\n%s", got)
	}
}

func TestRenderRetrieved_TableWithoutColumnMetadata(t *testing.T) {
	got := renderRetrieved([]TableDetail{{Table: "orphan", RowCount: 99}}, 3)
	if !strings.Contains(got, "(no column metadata available)") {
		t.Errorf("missing column placeholder: %s", got)
	}
}

func TestRenderRetrieved_TwoTablesSeparatedByBlankLine(t *testing.T) {
	got := renderRetrieved([]TableDetail{
		{Table: "a", RowCount: 1, Columns: []models.ColumnInfo{{Name: "id"}}},
		{Table: "b", RowCount: 2, Columns: []models.ColumnInfo{{Name: "id"}}},
	}, 3)
	if !strings.Contains(got, "TABLE a") || !strings.Contains(got, "TABLE b") {
		t.Errorf("both tables missing: %s", got)
	}
	if !strings.Contains(got, "\n\nTABLE b") {
		t.Error("tables should be separated by blank line")
	}
}

func TestRenderRetrieved_DeterministicColumnOrder(t *testing.T) {
	rows := []map[string]interface{}{{"b": 1, "a": 2, "c": 3}}
	got1 := renderRetrieved([]TableDetail{{Table: "t", RowCount: 1, SampleRows: rows}}, 3)
	got2 := renderRetrieved([]TableDetail{{Table: "t", RowCount: 1, SampleRows: rows}}, 3)
	if got1 != got2 {
		t.Error("sample-row rendering must be deterministic")
	}
	// a before b before c — alphabetical.
	if !strings.Contains(got1, "a=2, b=1, c=3") {
		t.Errorf("expected alphabetical order, got %s", got1)
	}
}

// ---------- compressCatalog ----------

func TestCompressCatalog_UnderBudget_Untouched(t *testing.T) {
	entries := []CatalogEntry{{Table: "a", ColumnCount: 1, RowCount: 10}}
	got, dropped, used := compressCatalog(entries, 10_000, CharCounter{})
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0", dropped)
	}
	if used != 1 {
		t.Errorf("used = %d, want 1", used)
	}
	if len(got) != 1 {
		t.Errorf("entries = %d", len(got))
	}
}

func TestCompressCatalog_DropsArchiveShapedFirst(t *testing.T) {
	entries := []CatalogEntry{
		{Table: "orders", ColumnCount: 10, RowCount: 1_000_000},
		{Table: "users_2023", ColumnCount: 10, RowCount: 500_000_000},
		{Table: "events_LOG", ColumnCount: 10, RowCount: 2_000_000},
		{Table: "orders_BKP", ColumnCount: 10, RowCount: 1_500_000},
		{Table: "customers", ColumnCount: 10, RowCount: 100_000},
	}
	// Tight budget: force archive-drop but leave headroom for the real tables.
	tiny := CharCounter{CharsPerToken: 1, SafetyFactor: 1}
	// Budget chosen so full rendering overshoots but the 2 non-archive
	// entries fit comfortably.
	budget := tiny.CountTokens("orders | 10c | 1M rows") + tiny.CountTokens("customers | 10c | 100K rows") + 10
	got, dropped, _ := compressCatalog(entries, budget, tiny)

	names := tableNames(got)
	for _, archive := range []string{"users_2023", "events_LOG", "orders_BKP"} {
		if contains(names, archive) {
			t.Errorf("archive-shaped %q should be dropped, kept: %v", archive, names)
		}
	}
	if dropped < 3 {
		t.Errorf("expected ≥3 drops (the 3 archives), got %d", dropped)
	}
}

func TestCompressCatalog_DropsSmallestRowCountsNext(t *testing.T) {
	entries := []CatalogEntry{
		{Table: "big", ColumnCount: 10, RowCount: 10_000_000},
		{Table: "tiny", ColumnCount: 10, RowCount: 1},
		{Table: "medium", ColumnCount: 10, RowCount: 1_000},
	}
	tiny := CharCounter{CharsPerToken: 1, SafetyFactor: 1}
	// Budget only fits 1 table.
	budget := tiny.CountTokens("big | 10c | 10M rows") + 2
	got, dropped, _ := compressCatalog(entries, budget, tiny)
	names := tableNames(got)
	if !contains(names, "big") {
		t.Errorf("largest table should survive, got %v", names)
	}
	if contains(names, "tiny") {
		t.Errorf("smallest table should be dropped, got %v", names)
	}
	if dropped == 0 {
		t.Error("expected drops under tight budget")
	}
}

func TestCompressCatalog_PreservesOriginalOrder(t *testing.T) {
	entries := []CatalogEntry{
		{Table: "z", ColumnCount: 10, RowCount: 1_000_000},
		{Table: "a_LOG", ColumnCount: 10, RowCount: 500},
		{Table: "m", ColumnCount: 10, RowCount: 500_000},
	}
	tiny := CharCounter{CharsPerToken: 1, SafetyFactor: 1}
	budget := tiny.CountTokens("z | 10c | 1M rows") + tiny.CountTokens("m | 10c | 500K rows") + 10
	got, _, _ := compressCatalog(entries, budget, tiny)
	names := tableNames(got)
	// Archive-shaped dropped; remaining preserves original "z then m" order.
	if len(names) != 2 || names[0] != "z" || names[1] != "m" {
		t.Errorf("order churned: %v", names)
	}
}

func TestCompressCatalog_PathologicallyTightBudget(t *testing.T) {
	entries := []CatalogEntry{{Table: "huge", ColumnCount: 100, RowCount: 1}}
	got, dropped, used := compressCatalog(entries, 1, CharCounter{CharsPerToken: 1})
	if len(got) != 0 {
		t.Errorf("budget=1 should drop everything, got %v", got)
	}
	if dropped != 1 {
		t.Errorf("dropped = %d", dropped)
	}
	if used != 0 {
		t.Errorf("used = %d", used)
	}
}

// ---------- Render (top-level) ----------

func TestRender_DefaultsApplied(t *testing.T) {
	r := Render(RenderOptions{
		Catalog:   []CatalogEntry{{Table: "a", ColumnCount: 1, RowCount: 1}},
		Retrieved: []TableDetail{{Table: "a", RowCount: 1}},
	})
	if r.Catalog == "" || r.Retrieved == "" {
		t.Error("both blocks should be populated")
	}
	if r.CatalogTokens == 0 || r.RetrievedTokens == 0 {
		t.Error("token counts should be positive")
	}
}

func TestRender_OverbudgetDropsReportCorrectly(t *testing.T) {
	// 50 tables, budget only fits a handful.
	entries := make([]CatalogEntry, 50)
	for i := range entries {
		entries[i] = CatalogEntry{Table: fmt.Sprintf("table_%d", i), ColumnCount: 10, RowCount: int64(i + 1)}
	}
	r := Render(RenderOptions{
		Catalog: entries,
		Budget:  10, // token budget; aggressive
		Counter: CharCounter{CharsPerToken: 1, SafetyFactor: 1},
	})
	if r.CatalogDropped == 0 {
		t.Error("tight budget should drop entries")
	}
	if r.CatalogTablesUsed+r.CatalogDropped != 50 {
		t.Errorf("used+dropped = %d, want 50", r.CatalogTablesUsed+r.CatalogDropped)
	}
}

// ---------- formatRowCount ----------

func TestFormatRowCount(t *testing.T) {
	cases := []struct {
		in  int64
		out string
	}{
		{-1, "unknown"},
		{0, "0"},
		{542, "542"},
		{1_000, "1K"},
		{14_300, "14.3K"},
		{1_000_000, "1M"},
		{2_100_000, "2.1M"},
		{1_000_000_000, "1B"},
		{4_300_000_000, "4.3B"},
	}
	for _, c := range cases {
		got := formatRowCount(c.in)
		if got != c.out {
			t.Errorf("formatRowCount(%d) = %q, want %q", c.in, got, c.out)
		}
	}
}

// ---------- IsArchiveShaped ----------

func TestIsArchiveShaped(t *testing.T) {
	yes := []string{
		"orders_2023",
		"events_LOG",
		"tbl_ARCHIVE",
		"data_BKP",
		"data_BACKUP",
		"stage_TMP",
		"stage_TEMP",
		"ingest_STG",
		"ingest_STAGING",
		"ORDERS_2025", // uppercase
	}
	no := []string{
		"orders",
		"2023_orders", // year must be at the END
		"log_events",
		"archive_view",
		"normal_table",
	}
	for _, s := range yes {
		if !IsArchiveShaped(s) {
			t.Errorf("%q should be archive-shaped", s)
		}
	}
	for _, s := range no {
		if IsArchiveShaped(s) {
			t.Errorf("%q should NOT be archive-shaped", s)
		}
	}
}

// ---------- helpers ----------

func tableNames(entries []CatalogEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Table
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
