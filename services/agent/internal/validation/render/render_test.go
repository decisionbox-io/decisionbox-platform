package render

import (
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func TestRenderVerificationContext_NilLogReturnsEmpty(t *testing.T) {
	got := RenderVerificationContext(nil, []int{1, 2}, DefaultBudgetChars)
	if got != "" {
		t.Fatalf("expected empty string for nil log, got %q", got)
	}
}

func TestRenderVerificationContext_EmptySourceStepsReturnsEmpty(t *testing.T) {
	log := []models.ExplorationStep{
		{Step: 1, Action: "query_data", Query: "SELECT 1", RowCount: 1},
	}
	got := RenderVerificationContext(log, nil, DefaultBudgetChars)
	if got != "" {
		t.Fatalf("expected empty string for nil sourceStepIDs, got %q", got)
	}
	got = RenderVerificationContext(log, []int{}, DefaultBudgetChars)
	if got != "" {
		t.Fatalf("expected empty string for empty sourceStepIDs, got %q", got)
	}
}

func TestRenderVerificationContext_NoCitedStepInLogReturnsEmpty(t *testing.T) {
	log := []models.ExplorationStep{
		{Step: 1, Action: "query_data", Query: "SELECT 1", RowCount: 1},
	}
	got := RenderVerificationContext(log, []int{42}, DefaultBudgetChars)
	if got != "" {
		t.Fatalf("expected empty string when no cited step matches, got %q", got)
	}
}

func TestRenderVerificationContext_SingleStepGolden(t *testing.T) {
	log := []models.ExplorationStep{
		{
			Step:         3,
			Action:       "query_data",
			QueryPurpose: "count active users last 7 days",
			Query:        "SELECT COUNT(DISTINCT user_id) AS active_users FROM events WHERE event_date >= '2026-04-23'",
			RowCount:     1,
		},
	}
	got := RenderVerificationContext(log, []int{3}, DefaultBudgetChars)

	want := SectionHeader + "\n\n" +
		"### Step 3 — count active users last 7 days\n\n" +
		"```sql\nSELECT COUNT(DISTINCT user_id) AS active_users FROM events WHERE event_date >= '2026-04-23'\n```\n\n" +
		"Returned 1 row(s).\n\n"

	if got != want {
		t.Fatalf("rendered output mismatch.\nGot:\n%s\nWant:\n%s", got, want)
	}
}

func TestRenderVerificationContext_MultipleStepsPreservesCitationOrder(t *testing.T) {
	log := []models.ExplorationStep{
		{Step: 1, Action: "query_data", QueryPurpose: "p1", Query: "SELECT 1 FROM t1", RowCount: 1},
		{Step: 2, Action: "query_data", QueryPurpose: "p2", Query: "SELECT 2 FROM t2", RowCount: 2},
		{Step: 3, Action: "query_data", QueryPurpose: "p3", Query: "SELECT 3 FROM t3", RowCount: 3},
	}
	// Cite in reverse order — output must reflect the citation order, not the log order.
	got := RenderVerificationContext(log, []int{3, 1, 2}, DefaultBudgetChars)

	idx3 := strings.Index(got, "### Step 3")
	idx1 := strings.Index(got, "### Step 1")
	idx2 := strings.Index(got, "### Step 2")
	if idx3 == -1 || idx1 == -1 || idx2 == -1 {
		t.Fatalf("missing one of the cited steps in output: %s", got)
	}
	if idx3 >= idx1 || idx1 >= idx2 {
		t.Fatalf("citation order not preserved.\nGot:\n%s", got)
	}
}

func TestRenderVerificationContext_DuplicateCitationsAreDeduped(t *testing.T) {
	log := []models.ExplorationStep{
		{Step: 5, Action: "query_data", QueryPurpose: "purpose", Query: "SELECT 1", RowCount: 1},
	}
	got := RenderVerificationContext(log, []int{5, 5, 5}, DefaultBudgetChars)
	count := strings.Count(got, "### Step 5")
	if count != 1 {
		t.Fatalf("expected step rendered once after dedup, got %d occurrences:\n%s", count, got)
	}
}

func TestRenderVerificationContext_NonQueryStepsAreSkipped(t *testing.T) {
	log := []models.ExplorationStep{
		{Step: 1, Action: "lookup_schema", QueryPurpose: "lookup", Query: ""},
		{Step: 2, Action: "search_tables", QueryPurpose: "search", Query: ""},
		{Step: 3, Action: "query_data", QueryPurpose: "real", Query: "SELECT 1", RowCount: 1},
	}
	got := RenderVerificationContext(log, []int{1, 2, 3}, DefaultBudgetChars)

	if strings.Contains(got, "### Step 1") {
		t.Errorf("lookup_schema step should be skipped: %s", got)
	}
	if strings.Contains(got, "### Step 2") {
		t.Errorf("search_tables step should be skipped: %s", got)
	}
	if !strings.Contains(got, "### Step 3") {
		t.Errorf("query_data step should be rendered: %s", got)
	}
}

func TestRenderVerificationContext_EmptyQueryStepSkipped(t *testing.T) {
	log := []models.ExplorationStep{
		{Step: 1, Action: "query_data", Query: "   ", RowCount: 0},
		{Step: 2, Action: "query_data", QueryPurpose: "real", Query: "SELECT 1", RowCount: 1},
	}
	got := RenderVerificationContext(log, []int{1, 2}, DefaultBudgetChars)
	if strings.Contains(got, "### Step 1") {
		t.Errorf("step with whitespace-only query should be skipped: %s", got)
	}
	if !strings.Contains(got, "### Step 2") {
		t.Errorf("step 2 should be rendered: %s", got)
	}
}

func TestRenderVerificationContext_MissingPurposeRendersFallback(t *testing.T) {
	log := []models.ExplorationStep{
		{Step: 7, Action: "query_data", Query: "SELECT 1", RowCount: 1},
	}
	got := RenderVerificationContext(log, []int{7}, DefaultBudgetChars)
	if !strings.Contains(got, "Step 7 — (no purpose recorded)") {
		t.Errorf("expected fallback purpose marker, got:\n%s", got)
	}
}

func TestRenderVerificationContext_ErrorStepRendersErrorNotRowCount(t *testing.T) {
	log := []models.ExplorationStep{
		{
			Step:         1,
			Action:       "query_data",
			QueryPurpose: "broken query",
			Query:        "SELECT * FROM nope",
			Error:        "table not found",
			RowCount:     0,
		},
	}
	got := RenderVerificationContext(log, []int{1}, DefaultBudgetChars)
	if !strings.Contains(got, "Returned an error: table not found") {
		t.Errorf("expected error annotation, got:\n%s", got)
	}
	if strings.Contains(got, "Returned 0 row(s)") {
		t.Errorf("error step must not render row count, got:\n%s", got)
	}
}

func TestRenderVerificationContext_MultilineSQLIsFenced(t *testing.T) {
	multiline := "WITH cohort AS (\n  SELECT user_id, MIN(event_date) AS first_seen\n  FROM events\n  GROUP BY user_id\n)\nSELECT COUNT(*) FROM cohort"
	log := []models.ExplorationStep{
		{Step: 1, Action: "query_data", QueryPurpose: "cohort", Query: multiline, RowCount: 42},
	}
	got := RenderVerificationContext(log, []int{1}, DefaultBudgetChars)
	if !strings.Contains(got, "```sql\n"+multiline+"\n```") {
		t.Errorf("multiline SQL should be fenced cleanly, got:\n%s", got)
	}
}

func TestRenderVerificationContext_TrailingNewlineInQueryIsStripped(t *testing.T) {
	log := []models.ExplorationStep{
		{Step: 1, Action: "query_data", QueryPurpose: "p", Query: "SELECT 1\n\n\n", RowCount: 1},
	}
	got := RenderVerificationContext(log, []int{1}, DefaultBudgetChars)
	if !strings.Contains(got, "```sql\nSELECT 1\n```") {
		t.Errorf("trailing newlines in Query should be normalized, got:\n%s", got)
	}
}

func TestRenderVerificationContext_BudgetDropsOldestStepFirst(t *testing.T) {
	// Each step's rendered SQL is ~250 chars. Budget that fits ~2 of 4 — older
	// steps (lower Step number) should be dropped first per v2 plan §4.1.
	bigSQL := strings.Repeat("SELECT 1 FROM t WHERE x = 'y' AND z = 'q'\n", 4)
	log := []models.ExplorationStep{
		{Step: 1, Action: "query_data", QueryPurpose: "old", Query: bigSQL, RowCount: 1},
		{Step: 2, Action: "query_data", QueryPurpose: "mid1", Query: bigSQL, RowCount: 2},
		{Step: 3, Action: "query_data", QueryPurpose: "mid2", Query: bigSQL, RowCount: 3},
		{Step: 4, Action: "query_data", QueryPurpose: "newest", Query: bigSQL, RowCount: 4},
	}
	// Cite in citation order (1,2,3,4) so we can verify drop policy is by Step
	// number, not citation order.
	got := RenderVerificationContext(log, []int{1, 2, 3, 4}, 600)

	if strings.Contains(got, "### Step 1") {
		t.Errorf("oldest step (1) should be dropped first, but it remained:\n%s", got)
	}
	if !strings.Contains(got, "### Step 4") {
		t.Errorf("newest step (4) should be retained:\n%s", got)
	}
}

func TestRenderVerificationContext_BudgetDropsByStepNumberRegardlessOfCitationOrder(t *testing.T) {
	bigSQL := strings.Repeat("SELECT 1 FROM t WHERE x = 'y' AND z = 'q'\n", 4)
	log := []models.ExplorationStep{
		{Step: 10, Action: "query_data", QueryPurpose: "earliest_in_log", Query: bigSQL, RowCount: 10},
		{Step: 20, Action: "query_data", QueryPurpose: "latest_in_log", Query: bigSQL, RowCount: 20},
	}
	// Cite latest-first. Drop policy is by Step number (oldest=10 first), not
	// by citation index.
	got := RenderVerificationContext(log, []int{20, 10}, 400)
	if strings.Contains(got, "### Step 10") {
		t.Errorf("oldest step (10) should be dropped first regardless of citation order:\n%s", got)
	}
	if !strings.Contains(got, "### Step 20") {
		t.Errorf("latest step (20) should be retained:\n%s", got)
	}
}

func TestRenderVerificationContext_ZeroBudgetUsesDefault(t *testing.T) {
	log := []models.ExplorationStep{
		{Step: 1, Action: "query_data", QueryPurpose: "p", Query: "SELECT 1", RowCount: 1},
	}
	got := RenderVerificationContext(log, []int{1}, 0)
	if !strings.Contains(got, "### Step 1") {
		t.Errorf("zero budget should fall through to DefaultBudgetChars, got:\n%s", got)
	}
}

func TestRenderVerificationContext_AllStepsExceedBudgetReturnsEmpty(t *testing.T) {
	bigSQL := strings.Repeat("X", 100)
	log := []models.ExplorationStep{
		{Step: 1, Action: "query_data", QueryPurpose: "p", Query: bigSQL, RowCount: 1},
	}
	// Budget of 1 — even one step's framing exceeds it.
	got := RenderVerificationContext(log, []int{1}, 1)
	if got != "" {
		t.Errorf("expected empty when no step fits budget, got:\n%s", got)
	}
}

func TestRenderVerificationContext_ContainsSectionHeader(t *testing.T) {
	log := []models.ExplorationStep{
		{Step: 1, Action: "query_data", QueryPurpose: "p", Query: "SELECT 1", RowCount: 1},
	}
	got := RenderVerificationContext(log, []int{1}, DefaultBudgetChars)
	if !strings.HasPrefix(got, SectionHeader+"\n\n") {
		preview := got
		if len(preview) > 60 {
			preview = preview[:60]
		}
		t.Errorf("rendered block must start with section header, got prefix %q", preview)
	}
}

// TestIsExecutableQueryStep_NonQueryActionWithSQL covers the rare case where
// a step has a non-empty Query but a non-`query_data` Action — the Action
// check is the gating reason it's skipped, not the empty-query short circuit.
func TestIsExecutableQueryStep_NonQueryActionWithSQL(t *testing.T) {
	log := []models.ExplorationStep{
		// Action is "complete" but somehow Query was populated. Should not render.
		{Step: 1, Action: "complete", QueryPurpose: "wrap-up", Query: "SELECT 1"},
	}
	got := RenderVerificationContext(log, []int{1}, DefaultBudgetChars)
	if got != "" {
		t.Errorf("non-query_data action with SQL should still be skipped, got:\n%s", got)
	}
}

// TestTrimToBudget_EmptyStepsEarlyReturn calls the unexported helper to cover
// its defensive empty-input branch. Reachable from any future caller that
// might bypass RenderVerificationContext's pre-filter.
func TestTrimToBudget_EmptyStepsEarlyReturn(t *testing.T) {
	got := trimToBudget(nil, DefaultBudgetChars)
	if len(got) != 0 {
		t.Errorf("trimToBudget(nil) = %v, want empty", got)
	}
}

// TestRenderSection_EmptyStepsEarlyReturn covers renderSection's defensive
// empty-input branch (RenderVerificationContext's pre-filter normally avoids
// the call, but the helper must still behave for direct callers).
func TestRenderSection_EmptyStepsEarlyReturn(t *testing.T) {
	if got := renderSection(nil); got != "" {
		t.Errorf("renderSection(nil) = %q, want empty", got)
	}
}

// renderLookupSection coverage: empty input, single + multi-table render,
// type-vs-untyped columns, hidden row count when negative.
func TestRenderLookupSection_Empty(t *testing.T) {
	if got := renderLookupSection(nil); got != "" {
		t.Errorf("renderLookupSection(nil) = %q, want empty", got)
	}
	if got := renderLookupSection([]ai.LookupTable{}); got != "" {
		t.Errorf("renderLookupSection([]) = %q, want empty", got)
	}
}

func TestRenderLookupSection_TypedColumnsAndRowCount(t *testing.T) {
	got := renderLookupSection([]ai.LookupTable{
		{
			Table:    "ds.events",
			RowCount: 1234,
			Columns: []ai.LookupColumn{
				{Name: "user_id", Type: "INT64"},
				{Name: "country", Type: "STRING"},
			},
		},
	})
	if !strings.Contains(got, LookupSectionHeader) {
		t.Errorf("missing section header:\n%s", got)
	}
	if !strings.Contains(got, "`ds.events`") {
		t.Errorf("missing fully-qualified table name:\n%s", got)
	}
	if !strings.Contains(got, "user_id INT64, country STRING") {
		t.Errorf("typed columns mis-rendered:\n%s", got)
	}
	if !strings.Contains(got, "Approximate row count: 1234") {
		t.Errorf("row count mis-rendered:\n%s", got)
	}
}

func TestRenderLookupSection_BareColumnsNoType(t *testing.T) {
	got := renderLookupSection([]ai.LookupTable{
		{Table: "ds.t", Columns: []ai.LookupColumn{{Name: "id"}, {Name: "name"}}, RowCount: -1},
	})
	if !strings.Contains(got, "Columns: id, name.") {
		t.Errorf("bare columns mis-rendered:\n%s", got)
	}
	if strings.Contains(got, "Approximate row count") {
		t.Errorf("RowCount=-1 should hide the row count line, got:\n%s", got)
	}
}

func TestRenderLookupSection_NoColumns(t *testing.T) {
	got := renderLookupSection([]ai.LookupTable{
		{Table: "ds.t", RowCount: 0},
	})
	if strings.Contains(got, "Columns:") {
		t.Errorf("empty columns shouldn't render Columns line, got:\n%s", got)
	}
	if !strings.Contains(got, "Approximate row count: 0") {
		t.Errorf("RowCount=0 should still render (nonneg is the cutoff), got:\n%s", got)
	}
}

func TestRenderLookupSection_MultipleTables(t *testing.T) {
	got := renderLookupSection([]ai.LookupTable{
		{Table: "ds.t1", Columns: []ai.LookupColumn{{Name: "a"}}, RowCount: 1},
		{Table: "ds.t2", Columns: []ai.LookupColumn{{Name: "b"}}, RowCount: 2},
	})
	if !strings.Contains(got, "`ds.t1`") || !strings.Contains(got, "`ds.t2`") {
		t.Errorf("both tables should appear, got:\n%s", got)
	}
}

// RenderVerificationContextWithLookups coverage: lookups-only path (no source
// steps) and combined paths.
func TestRenderVerificationContextWithLookups_LookupsOnly(t *testing.T) {
	got := RenderVerificationContextWithLookups(
		nil, nil,
		[]ai.LookupTable{{Table: "ds.t", Columns: []ai.LookupColumn{{Name: "x"}}, RowCount: 1}},
		DefaultBudgetChars,
	)
	if !strings.Contains(got, LookupSectionHeader) {
		t.Errorf("expected lookup section, got:\n%s", got)
	}
	if strings.Contains(got, SectionHeader) {
		t.Errorf("source-queries header should not appear when only lookups present, got:\n%s", got)
	}
}

func TestRenderVerificationContextWithLookups_BothPresent(t *testing.T) {
	log := []models.ExplorationStep{
		{Step: 1, Action: "query_data", QueryPurpose: "p", Query: "SELECT 1", RowCount: 1},
	}
	got := RenderVerificationContextWithLookups(
		log, []int{1},
		[]ai.LookupTable{{Table: "ds.t", Columns: []ai.LookupColumn{{Name: "x"}}, RowCount: 1}},
		DefaultBudgetChars,
	)
	idxSrc := strings.Index(got, SectionHeader)
	idxLookup := strings.Index(got, LookupSectionHeader)
	if idxSrc == -1 || idxLookup == -1 {
		t.Fatalf("both sections should be present: src=%d lookup=%d", idxSrc, idxLookup)
	}
	if idxSrc >= idxLookup {
		t.Errorf("source-queries section should come before lookup section (src=%d, lookup=%d)", idxSrc, idxLookup)
	}
}

func TestRuleInstructionMentionsSourceQueryPrecedence(t *testing.T) {
	// Regression on the central instruction — drift here is a real bug.
	if !strings.Contains(RuleInstruction, "source exploration queries") {
		t.Errorf("RuleInstruction lost the source-query precedence wording: %q", RuleInstruction)
	}
	if !strings.Contains(RuleInstruction, "Do not invent new column names") {
		t.Errorf("RuleInstruction lost the no-invented-columns wording: %q", RuleInstruction)
	}
}
