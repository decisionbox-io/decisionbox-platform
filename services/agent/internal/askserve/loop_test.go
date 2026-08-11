package askserve

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// scriptedProvider returns canned LLM responses by call order.
type scriptedProvider struct {
	responses []string
	idx       int
}

func (p *scriptedProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	i := p.idx
	p.idx++
	content := `{"decline":"out of script"}`
	if i < len(p.responses) {
		content = p.responses[i]
	}
	return &gollm.ChatResponse{Content: content, Usage: gollm.Usage{InputTokens: 10, OutputTokens: 5}}, nil
}
func (p *scriptedProvider) Validate(ctx context.Context) error { return nil }

type fakeStore struct {
	mu     sync.Mutex
	events []commonmodels.ToolEvent
	final  *TurnFinal
}

func (s *fakeStore) AppendEvent(ctx context.Context, turnID, projectID string, ev commonmodels.ToolEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
	return nil
}
func (s *fakeStore) Finalize(ctx context.Context, fin TurnFinal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.final = &fin
	return nil
}

type fakeSchema struct {
	lookup    ai.LookupResult
	hits      []ai.SearchHit
	lookErr   error
	searchErr error
}

func (f *fakeSchema) Lookup(ctx context.Context, refs []string) (ai.LookupResult, error) {
	return f.lookup, f.lookErr
}
func (f *fakeSchema) Search(ctx context.Context, q string, k int) ([]ai.SearchHit, error) {
	return f.hits, f.searchErr
}

func testRuntime(provider gollm.Provider, wh *testutil.MockWarehouseProvider, sp ai.SchemaProvider, filterField string) *ProjectRuntime {
	client, _ := ai.New(provider, "test-model")
	exec := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
		Warehouse:   wh,
		MaxRetries:  1,
		FilterField: filterField,
		FilterValue: "TR",
	})
	var router *SchemaRouter
	if sp != nil {
		router = NewSchemaRouter(SchemaRouterOptions{
			Lookups: map[string]ai.SchemaProvider{"default": sp},
			Labels:  map[string]string{"default": "Default"},
			Primary: "default",
		})
	}
	return NewProjectRuntime(ProjectRuntimeOptions{
		AIClient:  client,
		Model:     "test-model",
		Schema:    router,
		PrimaryID: "default",
		Datasources: []DatasourceInfo{{
			ID:          "default",
			Label:       "Default",
			Dialect:     "Mock SQL",
			Datasets:    []string{"ds"},
			FilterField: filterField,
			FilterValue: "TR",
		}},
		Build: func(context.Context, string) (*WarehouseConn, error) {
			return &WarehouseConn{Executor: exec}, nil
		},
	})
}

func runOnce(t *testing.T, cfg Config, responses []string, wh *testutil.MockWarehouseProvider, sp ai.SchemaProvider, filterField string) *fakeStore {
	t.Helper()
	if cfg.MaxRounds == 0 {
		cfg = Config{MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
	}
	store := &fakeStore{}
	r := &runner{cfg: cfg, store: store}
	rt := testRuntime(&scriptedProvider{responses: responses}, wh, sp, filterField)
	r.run(context.Background(), rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "how many?"})
	if store.final == nil {
		t.Fatal("turn did not finalize")
	}
	return store
}

func TestLoop_HappyPath(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	store := runOnce(t, Config{}, []string{
		`{"query":"SELECT count(*) AS c FROM ds.t"}`,
		`{"answer":"There are 100 rows."}`,
	}, wh, nil, "")
	if store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("status = %q, want done", store.final.Status)
	}
	if store.final.Answer != "There are 100 rows." {
		t.Fatalf("answer = %q", store.final.Answer)
	}
	if len(store.events) != 1 || store.events[0].Name != "query_data" {
		t.Fatalf("events = %+v, want 1 query event", store.events)
	}
	if store.events[0].Output == nil {
		t.Fatal("query event missing summary output")
	}
}

func TestLoop_MultiQuery(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	store := runOnce(t, Config{}, []string{
		`{"query":"SELECT 1 FROM ds.a"}`,
		`{"query":"SELECT 2 FROM ds.b"}`,
		`{"answer":"done"}`,
	}, wh, nil, "")
	if store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("status = %q", store.final.Status)
	}
	if len(store.events) != 2 {
		t.Fatalf("events = %d, want 2", len(store.events))
	}
}

func TestLoop_RejectsUngroundedAnswer(t *testing.T) {
	// The model tries to answer immediately (no query). The loop must nudge it
	// to query first, so the final answer is grounded in a real result.
	wh := testutil.NewMockWarehouseProvider("ds")
	store := runOnce(t, Config{}, []string{
		`{"answer":"There are exactly 1,816,336 rows."}`, // ungrounded — must be rejected
		`{"query":"SELECT count(*) AS c FROM ds.t"}`,
		`{"answer":"There are 100 rows."}`,
	}, wh, nil, "")
	if store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("status = %q", store.final.Status)
	}
	if len(store.events) != 1 || store.events[0].Name != "query_data" {
		t.Fatalf("expected exactly one grounding query before the answer, got %+v", store.events)
	}
	if store.final.Answer != "There are 100 rows." {
		t.Fatalf("final answer should come after the query, got %q", store.final.Answer)
	}
}

func TestLoop_FabricatedAnswerDeclined(t *testing.T) {
	// The model keeps answering with no tool activity (fabrication). After the
	// bounded nudges the turn must DECLINE, never emit the ungrounded answer.
	wh := testutil.NewMockWarehouseProvider("ds")
	store := runOnce(t, Config{}, []string{
		`{"answer":"There are 8 tables; call_activity has 99,990 rows."}`,
		`{"answer":"There are 8 tables; call_activity has 99,990 rows."}`,
		`{"answer":"There are 8 tables; call_activity has 99,990 rows."}`,
		`{"answer":"There are 8 tables; call_activity has 99,990 rows."}`,
	}, wh, nil, "")
	if store.final.Status != commonmodels.AskTurnStatusDeclined {
		t.Fatalf("status = %q, want declined (ungrounded answer must not be emitted)", store.final.Status)
	}
	if store.final.Disposition != commonmodels.AskTurnDispositionDecline {
		t.Fatalf("disposition = %q, want decline", store.final.Disposition)
	}
	if len(store.events) != 0 || len(wh.Calls) != 0 {
		t.Fatalf("no tool should have run; events=%d calls=%d", len(store.events), len(wh.Calls))
	}
	if strings.Contains(store.final.Answer, "call_activity") {
		t.Fatalf("fabricated content leaked into the declined answer: %q", store.final.Answer)
	}
}

func TestLoop_DeclineNeedsNoQuery(t *testing.T) {
	// decline (and clarify) legitimately need no data — they must NOT be nudged
	// into running a query.
	wh := testutil.NewMockWarehouseProvider("ds")
	store := runOnce(t, Config{}, []string{`{"decline":"not in the data"}`}, wh, nil, "")
	if store.final.Status != commonmodels.AskTurnStatusDeclined {
		t.Fatalf("status = %q, want declined", store.final.Status)
	}
	if len(store.events) != 0 || len(wh.Calls) != 0 {
		t.Fatalf("decline must not run a query; events=%d calls=%d", len(store.events), len(wh.Calls))
	}
}

func TestLoop_Clarify(t *testing.T) {
	store := runOnce(t, Config{}, []string{`{"clarify":"which region?"}`}, testutil.NewMockWarehouseProvider("ds"), nil, "")
	if store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("status = %q, want done", store.final.Status)
	}
	if store.final.Disposition != commonmodels.AskTurnDispositionClarify {
		t.Fatalf("disposition = %q, want clarify", store.final.Disposition)
	}
}

func TestLoop_Decline(t *testing.T) {
	store := runOnce(t, Config{}, []string{`{"decline":"no such data"}`}, testutil.NewMockWarehouseProvider("ds"), nil, "")
	if store.final.Status != commonmodels.AskTurnStatusDeclined {
		t.Fatalf("status = %q, want declined", store.final.Status)
	}
}

func TestLoop_QueryBudget(t *testing.T) {
	cfg := Config{MaxRounds: 8, MaxQueriesPerTurn: 1, MaxFetchRows: 1000, PreviewRows: 50}
	wh := testutil.NewMockWarehouseProvider("ds")
	store := runOnce(t, cfg, []string{
		`{"query":"SELECT 1 FROM ds.a"}`,
		`{"query":"SELECT 2 FROM ds.b"}`, // over budget → not executed
		`{"answer":"done with one query"}`,
	}, wh, nil, "")
	if store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("status = %q", store.final.Status)
	}
	if len(store.events) != 1 {
		t.Fatalf("events = %d, want 1 (second query over budget)", len(store.events))
	}
}

func TestLoop_RoundCapThenSynthesizes(t *testing.T) {
	cfg := Config{MaxRounds: 2, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
	wh := testutil.NewMockWarehouseProvider("ds")
	store := runOnce(t, cfg, []string{
		`{"query":"SELECT 1 FROM ds.a"}`,
		`{"query":"SELECT 2 FROM ds.b"}`,
		`{"answer":"synthesized from gathered data"}`, // final synth call
	}, wh, nil, "")
	if store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("status = %q, want done via synthesis", store.final.Status)
	}
	if store.final.Answer != "synthesized from gathered data" {
		t.Fatalf("answer = %q", store.final.Answer)
	}
}

func TestLoop_RoundCapNoAnswerFails(t *testing.T) {
	cfg := Config{MaxRounds: 1, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
	wh := testutil.NewMockWarehouseProvider("ds")
	store := runOnce(t, cfg, []string{
		`{"query":"SELECT 1 FROM ds.a"}`,
		`{"query":"SELECT 2 FROM ds.b"}`, // synth still won't answer
	}, wh, nil, "")
	if store.final.Status != commonmodels.AskTurnStatusFailed {
		t.Fatalf("status = %q, want failed", store.final.Status)
	}
}

func TestLoop_ParseRetryRecovers(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	store := runOnce(t, Config{}, []string{
		"sorry, here are my thoughts with no json", // unparseable → reformat nudge
		`{"query":"SELECT 1 FROM ds.t"}`,           // recovers to a valid action
		`{"answer":"recovered after a nudge"}`,
	}, wh, nil, "")
	if store.final.Status != commonmodels.AskTurnStatusDone || store.final.Answer != "recovered after a nudge" {
		t.Fatalf("final = %+v", store.final)
	}
}

func TestLoop_QueryErrorRecorded(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	wh.QueryError = errors.New("table not found")
	store := runOnce(t, Config{}, []string{
		`{"query":"SELECT * FROM ds.missing"}`,
		`{"decline":"could not query"}`,
	}, wh, nil, "")
	if len(store.events) != 1 || store.events[0].Error == "" {
		t.Fatalf("expected a query event carrying the error, got %+v", store.events)
	}
}

func TestLoop_TenantFilterRejected(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	// Filter field enforced; the model's query omits it → security violation.
	store := runOnce(t, Config{}, []string{
		`{"query":"SELECT count(*) FROM ds.t"}`,
		`{"decline":"blocked"}`,
	}, wh, nil, "country")
	if len(store.events) != 1 || store.events[0].Error == "" {
		t.Fatalf("expected the unfiltered query to be rejected, got %+v", store.events)
	}
	if len(wh.Calls) != 0 {
		t.Fatalf("warehouse should not have been queried; calls = %d", len(wh.Calls))
	}
}

func TestLoop_LookupSchema(t *testing.T) {
	sp := &fakeSchema{lookup: ai.LookupResult{Tables: []ai.LookupTable{{Table: "ds.users", Columns: []ai.LookupColumn{{Name: "id", Type: "INT64"}}}}}}
	store := runOnce(t, Config{}, []string{
		`{"lookup_schema":["ds.users"]}`,
		`{"answer":"the users table has an id column"}`,
	}, testutil.NewMockWarehouseProvider("ds"), sp, "")
	if len(store.events) != 1 || store.events[0].Name != "lookup_schema" {
		t.Fatalf("events = %+v", store.events)
	}
	if store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("status = %q", store.final.Status)
	}
}

func TestLoop_SearchTablesSummaryIsLowercaseKeyed(t *testing.T) {
	sp := &fakeSchema{hits: []ai.SearchHit{{Table: "ds.users", Blurb: "user table", RowCount: 10, Score: 0.9}}}
	store := runOnce(t, Config{}, []string{
		`{"search_tables":"users"}`,
		`{"answer":"found ds.users"}`,
	}, testutil.NewMockWarehouseProvider("ds"), sp, "")
	if len(store.events) != 1 || store.events[0].Name != "search_tables" {
		t.Fatalf("events = %+v", store.events)
	}
	rows, ok := store.events[0].Output.([]map[string]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("search output not a summary slice: %#v", store.events[0].Output)
	}
	if rows[0]["table"] != "ds.users" {
		t.Fatalf("search summary should use lowercase 'table' key: %#v", rows[0])
	}
}

func TestLoop_SchemaToolUnavailableDegrades(t *testing.T) {
	// nil schema provider → search_tables returns an "unavailable" observation;
	// the loop continues and the model degrades gracefully by falling back to a
	// direct query, which grounds the answer. (An answer with only the failed
	// search — no successful evidence — would be declined as ungrounded.)
	store := runOnce(t, Config{}, []string{
		`{"search_tables":"orders"}`,
		`{"query":"SELECT 1 FROM ds.orders"}`,
		`{"answer":"answered after falling back to a direct query"}`,
	}, testutil.NewMockWarehouseProvider("ds"), nil, "")
	if store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("status = %q", store.final.Status)
	}
	// The unavailable search is recorded as an error event; the fallback query is
	// the grounding evidence.
	if len(store.events) != 2 || store.events[0].Name != "search_tables" || store.events[0].Error == "" {
		t.Fatalf("first event should be the unavailable search: %+v", store.events)
	}
}
