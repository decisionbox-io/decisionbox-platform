package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// -----------------------------------------------------------------------------
// parseAction — new shapes
// -----------------------------------------------------------------------------

func TestParseAction_LookupSchemaShape(t *testing.T) {
	engine := &ExplorationEngine{}
	cases := []struct {
		name      string
		input     string
		wantRefs  []string
		wantErr   bool
		wantThink string
	}{
		{
			name:      "single table",
			input:     `{"thinking": "need users", "lookup_schema": ["ds.users"]}`,
			wantRefs:  []string{"ds.users"},
			wantThink: "need users",
		},
		{
			name:     "multiple tables",
			input:    `{"lookup_schema": ["ds.users", "ds.orders", "ds.payments"]}`,
			wantRefs: []string{"ds.users", "ds.orders", "ds.payments"},
		},
		{
			name:    "empty list — accepted by parser; engine handles policy",
			input:   `{"lookup_schema": []}`,
			wantErr: true, // empty list = no recognised payload
		},
		{
			name:     "explicit legacy action with refs",
			input:    `{"action": "lookup_schema", "lookup_schema": ["ds.t"]}`,
			wantRefs: []string{"ds.t"},
		},
		{
			name:     "lookup wins when both query and lookup are present",
			input:    `{"query": "SELECT 1", "lookup_schema": ["ds.t"]}`,
			wantRefs: nil, // query takes precedence in parseAction switch order
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := engine.parseAction(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got action=%+v", a)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseAction error: %v", err)
			}
			if tc.wantRefs == nil {
				// Path where query takes precedence — verify we got query_data, not lookup_schema.
				if a.Action == "lookup_schema" {
					t.Errorf("expected query to win over lookup, got action=%q", a.Action)
				}
				return
			}
			if a.Action != "lookup_schema" {
				t.Errorf("action = %q, want lookup_schema", a.Action)
			}
			if len(a.LookupSchema) != len(tc.wantRefs) {
				t.Fatalf("LookupSchema len = %d, want %d", len(a.LookupSchema), len(tc.wantRefs))
			}
			for i, r := range a.LookupSchema {
				if r != tc.wantRefs[i] {
					t.Errorf("LookupSchema[%d] = %q, want %q", i, r, tc.wantRefs[i])
				}
			}
			if tc.wantThink != "" && a.Thinking != tc.wantThink {
				t.Errorf("Thinking = %q, want %q", a.Thinking, tc.wantThink)
			}
		})
	}
}

func TestParseAction_SearchTablesShape(t *testing.T) {
	engine := &ExplorationEngine{}
	cases := []struct {
		name        string
		input       string
		wantQuery   string
		wantTopK    int
		wantErr     bool
		wantActErr  bool // true if Action shouldn't be search_tables
	}{
		{
			name:      "minimal",
			input:     `{"thinking": "look for refunds", "search_tables": "refund cancellation"}`,
			wantQuery: "refund cancellation",
		},
		{
			name:      "with explicit top_k",
			input:     `{"search_tables": "users", "search_top_k": 5}`,
			wantQuery: "users",
			wantTopK:  5,
		},
		{
			name:    "empty string is rejected",
			input:   `{"search_tables": ""}`,
			wantErr: true,
		},
		{
			name:    "whitespace-only string is rejected",
			input:   `{"search_tables": "   "}`,
			wantErr: true,
		},
		{
			name:      "explicit legacy action",
			input:     `{"action": "search_tables", "search_tables": "audit"}`,
			wantQuery: "audit",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := engine.parseAction(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got action=%+v", a)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseAction error: %v", err)
			}
			if a.Action != "search_tables" {
				t.Errorf("action = %q, want search_tables", a.Action)
			}
			if a.SearchTables != tc.wantQuery {
				t.Errorf("SearchTables = %q, want %q", a.SearchTables, tc.wantQuery)
			}
			if tc.wantTopK > 0 && a.SearchTopK != tc.wantTopK {
				t.Errorf("SearchTopK = %d, want %d", a.SearchTopK, tc.wantTopK)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Tool-use envelope shape (Anthropic / OpenAI function-calling)
// -----------------------------------------------------------------------------

func TestParseAction_ToolEnvelope_LookupSchema(t *testing.T) {
	// This is the exact shape Claude emits in the wild on this codebase
	// even when the prompt asks for the key-driven shape — verified
	// against the discovery_debug_logs of a failed run on the customer
	// project. The parser must accept it and route to lookup_schema.
	engine := &ExplorationEngine{}
	resp := `{"name": "lookup_schema", "input": {"tables": ["TBLSIPAMAS", "TBLSIPATRA"]}}`
	a, err := engine.parseAction(resp)
	if err != nil {
		t.Fatalf("parseAction error: %v", err)
	}
	if a.Action != "lookup_schema" {
		t.Errorf("Action = %q, want lookup_schema", a.Action)
	}
	if len(a.LookupSchema) != 2 || a.LookupSchema[0] != "TBLSIPAMAS" {
		t.Errorf("LookupSchema = %v, want [TBLSIPAMAS, TBLSIPATRA]", a.LookupSchema)
	}
}

func TestParseAction_ToolEnvelope_SearchTables(t *testing.T) {
	engine := &ExplorationEngine{}
	resp := `{"name": "search_tables", "input": {"query": "customer order tables", "top_k": 7}}`
	a, err := engine.parseAction(resp)
	if err != nil {
		t.Fatalf("parseAction error: %v", err)
	}
	if a.Action != "search_tables" {
		t.Errorf("Action = %q, want search_tables", a.Action)
	}
	if a.SearchTables != "customer order tables" {
		t.Errorf("SearchTables = %q", a.SearchTables)
	}
	if a.SearchTopK != 7 {
		t.Errorf("SearchTopK = %d, want 7", a.SearchTopK)
	}
}

func TestParseAction_ToolEnvelope_QueryData(t *testing.T) {
	engine := &ExplorationEngine{}
	resp := `{"name": "query_data", "input": {"query": "SELECT 1", "purpose": "smoke"}}`
	a, err := engine.parseAction(resp)
	if err != nil {
		t.Fatalf("parseAction error: %v", err)
	}
	if a.Action != "query_data" {
		t.Errorf("Action = %q, want query_data", a.Action)
	}
	if a.Query != "SELECT 1" {
		t.Errorf("Query = %q", a.Query)
	}
	if a.QueryPurpose != "smoke" {
		t.Errorf("QueryPurpose = %q", a.QueryPurpose)
	}
}

func TestParseAction_ToolEnvelope_Complete(t *testing.T) {
	engine := &ExplorationEngine{}
	a, err := engine.parseAction(`{"name": "complete", "input": {"summary": "all good"}}`)
	if err != nil {
		t.Fatalf("parseAction error: %v", err)
	}
	if a.Action != "complete" {
		t.Errorf("Action = %q, want complete", a.Action)
	}
	if a.Summary != "all good" {
		t.Errorf("Summary = %q", a.Summary)
	}
}

func TestParseAction_ToolEnvelope_KeyDrivenWinsOnConflict(t *testing.T) {
	// If the model sends BOTH shapes — key-driven wins. We don't want
	// a malformed envelope to silently override a clean key-driven
	// payload in the same turn.
	engine := &ExplorationEngine{}
	resp := `{"name": "lookup_schema", "input": {"tables": ["WRONG"]}, "lookup_schema": ["RIGHT"]}`
	a, err := engine.parseAction(resp)
	if err != nil {
		t.Fatalf("parseAction error: %v", err)
	}
	if len(a.LookupSchema) != 1 || a.LookupSchema[0] != "RIGHT" {
		t.Errorf("LookupSchema = %v, want [RIGHT]", a.LookupSchema)
	}
}

func TestParseAction_ToolEnvelope_UnknownNameIgnored(t *testing.T) {
	// An unknown tool name must not silently succeed — let the parser's
	// "no recognised payload" branch reject it so the caller re-prompts.
	engine := &ExplorationEngine{}
	_, err := engine.parseAction(`{"name": "do_something_weird", "input": {"foo": 1}}`)
	if err == nil {
		t.Fatal("expected error for unknown tool name")
	}
}

func TestParseAction_DonePrecedence(t *testing.T) {
	// Done flag must take precedence over every other field — the
	// model is signalling completion. Anything else in the same JSON
	// is decorative.
	engine := &ExplorationEngine{}
	a, err := engine.parseAction(`{"done": true, "summary": "wrap", "lookup_schema": ["x"], "query": "SELECT 1"}`)
	if err != nil {
		t.Fatal(err)
	}
	if a.Action != "complete" {
		t.Errorf("action = %q, want complete", a.Action)
	}
}

// -----------------------------------------------------------------------------
// normaliseRefs
// -----------------------------------------------------------------------------

func TestNormaliseRefs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{name: "trim whitespace", in: []string{"  ds.t  "}, want: []string{"ds.t"}},
		{name: "strip backticks", in: []string{"`ds.t`"}, want: []string{"ds.t"}},
		{name: "strip double quotes", in: []string{`"ds.t"`}, want: []string{"ds.t"}},
		{name: "preserve order", in: []string{"b", "a", "c"}, want: []string{"b", "a", "c"}},
		{name: "drop empty", in: []string{"", "a", "  "}, want: []string{"a"}},
		{name: "single layer of quoting only", in: []string{"`\"ds.t\"`"}, want: []string{`"ds.t"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normaliseRefs(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d (%v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// -----------------------------------------------------------------------------
// formatLookupResult
// -----------------------------------------------------------------------------

func TestFormatLookupResult_RendersColumnsAndSamples(t *testing.T) {
	res := LookupResult{
		Tables: []LookupTable{
			{
				Table:    "ds.users",
				RowCount: 1500,
				Columns: []LookupColumn{
					{Name: "user_id", Type: "INT64", Nullable: false, Category: "primary_key"},
					{Name: "email", Type: "STRING", Nullable: true},
				},
				SampleRows: []map[string]interface{}{
					{"user_id": 1, "email": "a@example.com"},
				},
			},
		},
	}
	got := formatLookupResult(res, nil, false, 1, 30)

	for _, must := range []string{
		"Schema for `ds.users` (1.5K rows):",
		"- user_id INT64 NOT NULL [primary_key]",
		"- email STRING NULL",
		"sample rows:",
		"email=a@example.com, user_id=1",
		"Lookup budget: 1/30 used (29 remaining).",
	} {
		if !strings.Contains(got, must) {
			t.Errorf("missing %q in output:\n%s", must, got)
		}
	}
}

func TestFormatLookupResult_NotFoundAndAlready(t *testing.T) {
	res := LookupResult{
		Tables:   []LookupTable{{Table: "ds.users", Columns: []LookupColumn{{Name: "id", Type: "INT64"}}}},
		NotFound: []string{"ds.gone", "ds.typo"},
	}
	got := formatLookupResult(res, []string{"ds.cached"}, false, 5, 30)

	if !strings.Contains(got, "Not found (typo, dropped, or wrong dataset): ds.gone, ds.typo") {
		t.Errorf("missing not-found section: %s", got)
	}
	if !strings.Contains(got, "Already inspected earlier in this run") {
		t.Errorf("missing already-inspected section: %s", got)
	}
	if !strings.Contains(got, "ds.cached") {
		t.Errorf("missing already ref: %s", got)
	}
}

func TestFormatLookupResult_TruncatedNote(t *testing.T) {
	res := LookupResult{
		Tables:    []LookupTable{{Table: "ds.users", Columns: []LookupColumn{{Name: "id", Type: "INT64"}}}},
		Truncated: true,
	}
	got := formatLookupResult(res, nil, true, 1, 30)
	if !strings.Contains(got, "per-call cap is 10 tables") {
		t.Errorf("missing per-call cap message: %s", got)
	}
}

func TestFormatLookupResult_NoBudgetSection_WhenMaxIsZero(t *testing.T) {
	res := LookupResult{Tables: []LookupTable{{Table: "ds.t", Columns: []LookupColumn{{Name: "id"}}}}}
	got := formatLookupResult(res, nil, false, 1, 0)
	if strings.Contains(got, "Lookup budget") {
		t.Errorf("budget line should be omitted when max is 0: %s", got)
	}
}

func TestFormatLookupResult_NoTablesResolved(t *testing.T) {
	res := LookupResult{NotFound: []string{"ds.x"}}
	got := formatLookupResult(res, nil, false, 1, 5)
	if !strings.Contains(got, "No schemas resolved") {
		t.Errorf("missing 'No schemas resolved' message: %s", got)
	}
	if !strings.Contains(got, "Not found") {
		t.Errorf("not-found should still appear: %s", got)
	}
}

// -----------------------------------------------------------------------------
// formatSearchResult
// -----------------------------------------------------------------------------

func TestFormatSearchResult_RendersHits(t *testing.T) {
	hits := []SearchHit{
		{Table: "ds.users", Blurb: "core users", RowCount: 1000, Score: 0.91},
		{Table: "ds.orders", Blurb: "order events", RowCount: 5000, Score: 0.82},
	}
	got := formatSearchResult("users", hits, 1, 30)

	for _, must := range []string{
		`Search results for "users":`,
		"1. `ds.users` — 1K rows — score=0.910",
		"core users",
		"2. `ds.orders` — 5K rows — score=0.820",
		"Issue lookup_schema with the table refs",
		"Search budget: 1/30 used (29 remaining).",
	} {
		if !strings.Contains(got, must) {
			t.Errorf("missing %q in output:\n%s", must, got)
		}
	}
}

func TestFormatSearchResult_EmptyHits(t *testing.T) {
	got := formatSearchResult("nope", nil, 1, 5)
	if !strings.Contains(got, "no matching tables") {
		t.Errorf("expected empty-hits message: %s", got)
	}
}

func TestFormatSearchResult_NoBudgetSection_WhenMaxIsZero(t *testing.T) {
	got := formatSearchResult("x", []SearchHit{{Table: "t"}}, 1, 0)
	if strings.Contains(got, "Search budget") {
		t.Errorf("budget line should be omitted when max is 0: %s", got)
	}
}

// -----------------------------------------------------------------------------
// formatRowCountShort
// -----------------------------------------------------------------------------

func TestFormatRowCountShort(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{-1, "unknown"},
		{0, "0"},
		{500, "500"},
		{1_500, "1.5K"},
		{2_000, "2K"},
		{1_500_000, "1.5M"},
		{4_300_000_000, "4.3B"},
	}
	for _, c := range cases {
		got := formatRowCountShort(c.in)
		if got != c.want {
			t.Errorf("formatRowCountShort(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// -----------------------------------------------------------------------------
// formatLookupRow
// -----------------------------------------------------------------------------

func TestFormatLookupRow_DeterministicOrder(t *testing.T) {
	row := map[string]interface{}{"b": 1, "a": 2, "c": 3}
	got1 := formatLookupRow(row)
	got2 := formatLookupRow(row)
	if got1 != got2 {
		t.Errorf("not deterministic:\n %q\n %q", got1, got2)
	}
	if got1 != "a=2, b=1, c=3" {
		t.Errorf("alphabetical order broken: %q", got1)
	}
}

func TestFormatLookupRow_NULLAndTruncation(t *testing.T) {
	row := map[string]interface{}{
		"a": nil,
		"b": strings.Repeat("x", maxLookupValueLen+50),
	}
	got := formatLookupRow(row)
	if !strings.Contains(got, "a=NULL") {
		t.Errorf("NULL not rendered: %s", got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("long value not truncated: %s", got)
	}
	if strings.Contains(got, strings.Repeat("x", maxLookupValueLen+50)) {
		t.Errorf("long value should be truncated: %s", got)
	}
}

func TestFormatLookupRow_WhitespaceCollapsed(t *testing.T) {
	row := map[string]interface{}{"v": "line1\nline2\nline3"}
	got := formatLookupRow(row)
	if strings.Contains(got, "\n") {
		t.Errorf("multi-line should collapse: %q", got)
	}
}

// -----------------------------------------------------------------------------
// executeLookupSchema — full engine wiring
// -----------------------------------------------------------------------------

// buildEngineForActions returns an engine wired with a fake schema provider.
// The provider is returned so tests can rewire its hooks per case.
func buildEngineForActions(t *testing.T) (*ExplorationEngine, *fakeSchemaProvider) {
	t.Helper()
	wh := testutil.NewMockWarehouseProvider("ds")
	exec := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 1,
	})
	provider := &fakeSchemaProvider{}
	engine := NewExplorationEngine(ExplorationEngineOptions{
		Executor:       exec,
		MaxSteps:       10,
		Dataset:        "ds",
		SchemaProvider: provider,
	})
	return engine, provider
}

func TestExecuteLookupSchema_Success(t *testing.T) {
	engine, provider := buildEngineForActions(t)
	step := &models.ExplorationStep{Step: 1}
	out := engine.executeLookupSchema(context.Background(), &ExplorationAction{
		Action:       "lookup_schema",
		LookupSchema: []string{"ds.users", "ds.orders"},
	}, step)

	if !strings.Contains(out, "Schema for `ds.users`") || !strings.Contains(out, "Schema for `ds.orders`") {
		t.Errorf("both tables should appear:\n%s", out)
	}
	if engine.lookupsUsed != 1 {
		t.Errorf("lookupsUsed = %d, want 1", engine.lookupsUsed)
	}
	if provider.lookupCalls != 1 {
		t.Errorf("provider should be called once, got %d", provider.lookupCalls)
	}
	if len(engine.fetchedTables) != 2 {
		t.Errorf("fetchedTables size = %d, want 2", len(engine.fetchedTables))
	}
}

func TestExecuteLookupSchema_DedupesAcrossCalls(t *testing.T) {
	engine, provider := buildEngineForActions(t)

	// First call — both tables get fetched.
	_ = engine.executeLookupSchema(context.Background(), &ExplorationAction{
		Action: "lookup_schema", LookupSchema: []string{"ds.users", "ds.orders"},
	}, &models.ExplorationStep{})

	if provider.lookupCalls != 1 {
		t.Fatalf("expected 1 provider call after first lookup")
	}

	// Second call — same tables. Engine should short-circuit.
	out := engine.executeLookupSchema(context.Background(), &ExplorationAction{
		Action: "lookup_schema", LookupSchema: []string{"ds.users", "ds.orders"},
	}, &models.ExplorationStep{})

	if provider.lookupCalls != 1 {
		t.Errorf("provider should NOT be called again, got %d total", provider.lookupCalls)
	}
	if engine.lookupsUsed != 1 {
		t.Errorf("budget should not be debited for full-dedup turn, got lookupsUsed=%d", engine.lookupsUsed)
	}
	if !strings.Contains(out, "All requested tables were already inspected") {
		t.Errorf("dedup user message missing:\n%s", out)
	}
}

func TestExecuteLookupSchema_PartialDedup_OnlyFetchesNew(t *testing.T) {
	engine, provider := buildEngineForActions(t)

	_ = engine.executeLookupSchema(context.Background(), &ExplorationAction{
		Action: "lookup_schema", LookupSchema: []string{"ds.users"},
	}, &models.ExplorationStep{})

	provider.lookupFn = func(ctx context.Context, refs []string) (LookupResult, error) {
		// Verify the engine only forwards the NEW ref to the provider.
		if len(refs) != 1 || refs[0] != "ds.orders" {
			t.Errorf("provider should only receive ds.orders, got %v", refs)
		}
		return LookupResult{Tables: []LookupTable{{Table: "ds.orders"}}}, nil
	}

	out := engine.executeLookupSchema(context.Background(), &ExplorationAction{
		Action: "lookup_schema", LookupSchema: []string{"ds.users", "ds.orders"},
	}, &models.ExplorationStep{})

	if !strings.Contains(out, "Schema for `ds.orders`") {
		t.Errorf("ds.orders should be in output:\n%s", out)
	}
	if !strings.Contains(out, "Already inspected earlier") || !strings.Contains(out, "ds.users") {
		t.Errorf("ds.users should appear in already-inspected section:\n%s", out)
	}
}

func TestExecuteLookupSchema_PerCallCap(t *testing.T) {
	engine, provider := buildEngineForActions(t)
	provider.lookupFn = func(ctx context.Context, refs []string) (LookupResult, error) {
		if len(refs) > MaxLookupTablesPerCall {
			t.Errorf("engine should cap at %d, got %d", MaxLookupTablesPerCall, len(refs))
		}
		return LookupResult{Tables: refsToTables(refs)}, nil
	}

	refs := make([]string, 25)
	for i := range refs {
		refs[i] = fmt.Sprintf("ds.t%02d", i)
	}
	out := engine.executeLookupSchema(context.Background(), &ExplorationAction{
		Action: "lookup_schema", LookupSchema: refs,
	}, &models.ExplorationStep{})

	if !strings.Contains(out, "per-call cap is 10 tables") {
		t.Errorf("truncation note missing:\n%s", out)
	}
}

func TestExecuteLookupSchema_BudgetExhausted(t *testing.T) {
	engine, provider := buildEngineForActions(t)
	engine.lookupsUsed = engine.maxLookupsPerRun // pre-exhaust

	out := engine.executeLookupSchema(context.Background(), &ExplorationAction{
		Action: "lookup_schema", LookupSchema: []string{"ds.users"},
	}, &models.ExplorationStep{})

	if provider.lookupCalls != 0 {
		t.Errorf("provider should NOT be called when budget exhausted, got %d", provider.lookupCalls)
	}
	if !strings.Contains(out, "Lookup budget exhausted") {
		t.Errorf("budget-exhausted message missing:\n%s", out)
	}
}

func TestExecuteLookupSchema_ProviderError(t *testing.T) {
	engine, provider := buildEngineForActions(t)
	provider.lookupFn = func(ctx context.Context, refs []string) (LookupResult, error) {
		return LookupResult{}, errors.New("cache offline")
	}
	step := &models.ExplorationStep{}
	out := engine.executeLookupSchema(context.Background(), &ExplorationAction{
		Action: "lookup_schema", LookupSchema: []string{"ds.x"},
	}, step)

	if !strings.Contains(out, "Schema lookup failed") || !strings.Contains(out, "cache offline") {
		t.Errorf("error message wrong:\n%s", out)
	}
	if step.Error == "" {
		t.Errorf("step.Error should be set on provider failure")
	}
	// Budget DOES get debited even on failure (intentional — see executeLookupSchema docstring).
	if engine.lookupsUsed != 1 {
		t.Errorf("budget should be debited on failure, got lookupsUsed=%d", engine.lookupsUsed)
	}
}

func TestExecuteLookupSchema_EmptyRefs(t *testing.T) {
	engine, provider := buildEngineForActions(t)
	out := engine.executeLookupSchema(context.Background(), &ExplorationAction{
		Action:       "lookup_schema",
		LookupSchema: []string{"", "  "},
	}, &models.ExplorationStep{})

	if !strings.Contains(out, "lookup_schema action had no tables") {
		t.Errorf("empty-refs message missing:\n%s", out)
	}
	if provider.lookupCalls != 0 {
		t.Errorf("provider should NOT be called for empty refs, got %d", provider.lookupCalls)
	}
}

func TestExecuteLookupSchema_NoProvider_GracefulDegradation(t *testing.T) {
	engine := NewExplorationEngine(ExplorationEngineOptions{
		MaxSteps:       1,
		Dataset:        "ds",
		SchemaProvider: nil,
	})
	out := engine.executeLookupSchema(context.Background(), &ExplorationAction{
		Action:       "lookup_schema",
		LookupSchema: []string{"ds.users"},
	}, &models.ExplorationStep{})

	if !strings.Contains(out, "Schema lookup unavailable") {
		t.Errorf("expected unavailable message, got:\n%s", out)
	}
}

// -----------------------------------------------------------------------------
// executeSearchTables
// -----------------------------------------------------------------------------

func TestExecuteSearchTables_Success(t *testing.T) {
	engine, provider := buildEngineForActions(t)
	out := engine.executeSearchTables(context.Background(), &ExplorationAction{
		Action: "search_tables", SearchTables: "users",
	}, &models.ExplorationStep{})

	if !strings.Contains(out, `Search results for "users"`) {
		t.Errorf("expected search result header, got:\n%s", out)
	}
	if engine.searchesUsed != 1 || provider.searchCalls != 1 {
		t.Errorf("counters wrong: searchesUsed=%d providerCalls=%d", engine.searchesUsed, provider.searchCalls)
	}
}

func TestExecuteSearchTables_BudgetExhausted(t *testing.T) {
	engine, provider := buildEngineForActions(t)
	engine.searchesUsed = engine.maxSearchesPerRun
	out := engine.executeSearchTables(context.Background(), &ExplorationAction{
		Action: "search_tables", SearchTables: "x",
	}, &models.ExplorationStep{})

	if provider.searchCalls != 0 {
		t.Errorf("provider should NOT be called, got %d", provider.searchCalls)
	}
	if !strings.Contains(out, "Search budget exhausted") {
		t.Errorf("budget message missing:\n%s", out)
	}
}

func TestExecuteSearchTables_TopKClamped(t *testing.T) {
	engine, provider := buildEngineForActions(t)
	provider.searchFn = func(ctx context.Context, query string, k int) ([]SearchHit, error) {
		if k != MaxSearchTopK {
			t.Errorf("k = %d, want clamp to %d", k, MaxSearchTopK)
		}
		return nil, nil
	}
	_ = engine.executeSearchTables(context.Background(), &ExplorationAction{
		Action: "search_tables", SearchTables: "x", SearchTopK: MaxSearchTopK + 50,
	}, &models.ExplorationStep{})
}

func TestExecuteSearchTables_TopKDefault(t *testing.T) {
	engine, provider := buildEngineForActions(t)
	provider.searchFn = func(ctx context.Context, query string, k int) ([]SearchHit, error) {
		if k != DefaultSearchTopK {
			t.Errorf("k = %d, want default %d", k, DefaultSearchTopK)
		}
		return nil, nil
	}
	_ = engine.executeSearchTables(context.Background(), &ExplorationAction{
		Action: "search_tables", SearchTables: "x", SearchTopK: 0,
	}, &models.ExplorationStep{})
}

func TestExecuteSearchTables_EmptyQuery(t *testing.T) {
	engine, provider := buildEngineForActions(t)
	out := engine.executeSearchTables(context.Background(), &ExplorationAction{
		Action: "search_tables", SearchTables: "   ",
	}, &models.ExplorationStep{})

	if provider.searchCalls != 0 {
		t.Errorf("empty query should not call provider")
	}
	if !strings.Contains(out, "empty query") {
		t.Errorf("expected empty-query message:\n%s", out)
	}
}

func TestExecuteSearchTables_ProviderError(t *testing.T) {
	engine, provider := buildEngineForActions(t)
	provider.searchFn = func(ctx context.Context, query string, k int) ([]SearchHit, error) {
		return nil, errors.New("qdrant down")
	}
	step := &models.ExplorationStep{}
	out := engine.executeSearchTables(context.Background(), &ExplorationAction{
		Action: "search_tables", SearchTables: "x",
	}, step)

	if !strings.Contains(out, "Table search failed") {
		t.Errorf("expected error message:\n%s", out)
	}
	if step.Error == "" {
		t.Errorf("step.Error should be set on failure")
	}
}

func TestExecuteSearchTables_NoProvider(t *testing.T) {
	engine := NewExplorationEngine(ExplorationEngineOptions{MaxSteps: 1})
	out := engine.executeSearchTables(context.Background(), &ExplorationAction{
		Action: "search_tables", SearchTables: "x",
	}, &models.ExplorationStep{})
	if !strings.Contains(out, "Table search unavailable") {
		t.Errorf("expected unavailable message:\n%s", out)
	}
}

// -----------------------------------------------------------------------------
// Engine wiring — defaults
// -----------------------------------------------------------------------------

func TestNewExplorationEngine_Defaults_BudgetsApplied(t *testing.T) {
	engine := NewExplorationEngine(ExplorationEngineOptions{})
	if engine.maxLookupsPerRun != DefaultMaxLookupsPerRun {
		t.Errorf("default lookup budget wrong: %d", engine.maxLookupsPerRun)
	}
	if engine.maxSearchesPerRun != DefaultMaxSearchesPerRun {
		t.Errorf("default search budget wrong: %d", engine.maxSearchesPerRun)
	}
	if engine.fetchedTables == nil {
		t.Errorf("fetchedTables should be initialised")
	}
}

func TestNewExplorationEngine_NegativeBudgets_Disabled(t *testing.T) {
	engine := NewExplorationEngine(ExplorationEngineOptions{
		MaxLookupsPerRun:  -1,
		MaxSearchesPerRun: -1,
	})
	if engine.maxLookupsPerRun != 0 || engine.maxSearchesPerRun != 0 {
		t.Errorf("negative budgets should clamp to 0 (disabled), got lookup=%d search=%d",
			engine.maxLookupsPerRun, engine.maxSearchesPerRun)
	}
}

func TestNewExplorationEngine_BudgetsAnnouncedInInitialMessage(t *testing.T) {
	engine := NewExplorationEngine(ExplorationEngineOptions{
		MaxSteps:       7,
		SchemaProvider: &fakeSchemaProvider{},
	})
	msg := engine.buildInitialMessage(ExplorationContext{})
	if !strings.Contains(msg, "lookup_schema calls") {
		t.Errorf("initial message should announce lookup budget: %s", msg)
	}
	if !strings.Contains(msg, "search_tables calls") {
		t.Errorf("initial message should announce search budget: %s", msg)
	}
	if !strings.Contains(msg, "max 10 tables per call") {
		t.Errorf("initial message should announce per-call cap: %s", msg)
	}
}

func TestNewExplorationEngine_NoProvider_NoBudgetAnnouncement(t *testing.T) {
	engine := NewExplorationEngine(ExplorationEngineOptions{MaxSteps: 7})
	msg := engine.buildInitialMessage(ExplorationContext{})
	if strings.Contains(msg, "lookup_schema calls") {
		t.Errorf("budget should not be announced without a provider: %s", msg)
	}
}

// -----------------------------------------------------------------------------
// End-to-end: scripted LLM exercises lookup_schema → query → done
// -----------------------------------------------------------------------------

func TestExploration_E2E_LookupThenQuery(t *testing.T) {
	scripted := []string{
		`{"thinking": "inspect users", "lookup_schema": ["ds.users"]}`,
		`{"thinking": "search audit", "search_tables": "audit log"}`,
		`{"thinking": "now query", "query": "SELECT 1 FROM ds.users"}`,
		`{"done": true, "summary": "all done"}`,
	}

	provider := testutil.NewMockLLMProvider()
	for _, s := range scripted {
		provider.ResponseQueue = append(provider.ResponseQueue, &gollm.ChatResponse{
			Content:    s,
			Model:      "mock",
			StopReason: "end_turn",
			Usage:      gollm.Usage{InputTokens: 10, OutputTokens: 10},
		})
	}
	provider.DefaultResponse = &gollm.ChatResponse{
		Content:    `{"done": true, "summary": "fallback"}`,
		Model:      "mock",
		StopReason: "end_turn",
		Usage:      gollm.Usage{InputTokens: 1, OutputTokens: 1},
	}

	client, err := New(provider, "mock")
	if err != nil {
		t.Fatal(err)
	}
	wh := testutil.NewMockWarehouseProvider("ds")
	exec := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{Warehouse: wh, MaxRetries: 1})
	schemaProv := &fakeSchemaProvider{}
	engine := NewExplorationEngine(ExplorationEngineOptions{
		Client: client, Executor: exec, MaxSteps: 6, Dataset: "ds",
		SchemaProvider: schemaProv,
	})

	result, err := engine.Explore(context.Background(), ExplorationContext{
		ProjectID: "proj-e2e", Dataset: "ds", InitialPrompt: "Explore.",
	})
	if err != nil {
		t.Fatalf("Explore: %v", err)
	}
	if !result.Completed {
		t.Fatalf("expected completion, got %+v", result)
	}
	if result.TotalSteps != 4 {
		t.Errorf("TotalSteps = %d, want 4 (lookup, search, query, done)", result.TotalSteps)
	}

	wantActions := []string{"lookup_schema", "search_tables", "query_data", "complete"}
	if len(result.Steps) != len(wantActions) {
		t.Fatalf("Steps len = %d, want %d", len(result.Steps), len(wantActions))
	}
	for i, want := range wantActions {
		if result.Steps[i].Action != want {
			t.Errorf("Steps[%d].Action = %q, want %q", i, result.Steps[i].Action, want)
		}
	}

	if schemaProv.lookupCalls != 1 {
		t.Errorf("provider lookup calls = %d, want 1", schemaProv.lookupCalls)
	}
	if schemaProv.searchCalls != 1 {
		t.Errorf("provider search calls = %d, want 1", schemaProv.searchCalls)
	}
}

// refsToTables is a tiny helper for tests that want the provider to
// echo back the requested refs as canned LookupTable entries.
func refsToTables(refs []string) []LookupTable {
	out := make([]LookupTable, len(refs))
	for i, r := range refs {
		out[i] = LookupTable{Table: r}
	}
	return out
}
