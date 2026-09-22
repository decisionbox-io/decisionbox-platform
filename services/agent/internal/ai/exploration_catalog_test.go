package ai

import (
	"strings"
	"testing"
)

// TestFormatSearchResult_DoesNotPresentCatalogItemsAsTables is the exploration
// loop's half of the same guard. Backticked and followed by "issue
// lookup_schema with the table refs", a metric reads as a SQL table — and the
// model then looks up a schema that does not exist, or writes a FROM clause
// against it.
func TestFormatSearchResult_DoesNotPresentCatalogItemsAsTables(t *testing.T) {
	out := formatSearchResult("traffic", []SearchHit{
		{Table: "sessions", Kind: "metric", Blurb: "Sessions.", Score: 0.9},
		{Table: "country", Kind: "dimension", Blurb: "Country.", Score: 0.8},
	}, 1, 5, false)

	if !strings.Contains(out, "(metric)") || !strings.Contains(out, "(dimension)") {
		t.Errorf("output = %q, want each catalog item identified", out)
	}
	// Nothing here has a schema to look up.
	if strings.Contains(out, "lookup_schema") {
		t.Errorf("output = %q, want no lookup_schema advice when the result holds no tables", out)
	}
	// Row counts are meaningless for a catalog item and must not be invented.
	if strings.Contains(out, "rows") {
		t.Errorf("output = %q, want no row counts on catalog items", out)
	}
}

// TestFormatSearchResult_TableRenderingIsUnchanged pins that a table-only
// result renders exactly as before — row counts, backticks and the
// lookup_schema advice all intact.
func TestFormatSearchResult_TableRenderingIsUnchanged(t *testing.T) {
	out := formatSearchResult("users", []SearchHit{
		{Table: "events.users", Blurb: "Users.", RowCount: 1000, Score: 0.9},
	}, 1, 5, false)

	if !strings.Contains(out, "`events.users`") {
		t.Errorf("output = %q, want the backticked table ref", out)
	}
	if !strings.Contains(out, "rows") {
		t.Errorf("output = %q, want the row count", out)
	}
	if !strings.Contains(out, "lookup_schema") {
		t.Errorf("output = %q, want the lookup_schema advice for a table result", out)
	}
}

// TestFormatSearchResult_MixedResultStillAdvisesLookup: a result holding both
// keeps the advice, because some of it does have a schema to look up.
func TestFormatSearchResult_MixedResultStillAdvisesLookup(t *testing.T) {
	out := formatSearchResult("everything", []SearchHit{
		{Table: "sessions", Kind: "metric", Blurb: "Sessions.", Score: 0.9},
		{Table: "events.users", Blurb: "Users.", RowCount: 10, Score: 0.8},
	}, 1, 5, false)

	if !strings.Contains(out, "lookup_schema") {
		t.Errorf("output = %q, want the advice retained when a table is present", out)
	}
	if !strings.Contains(out, "sessions` (metric)") {
		t.Errorf("output = %q, want the metric still identified", out)
	}
}
