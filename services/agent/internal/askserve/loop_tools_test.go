package askserve

import (
	"context"
	"errors"
	"sync"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// toolProviderOnce registers a synthetic tool-capable provider ("test-tools")
// exactly once per test binary so toolsSupported() returns true for runtimes
// whose AI client is given that provenance. The factory is never invoked (tests
// inject the provider directly via ai.New); only the meta's SupportsTools flag
// matters.
var toolProviderOnce sync.Once

func ensureToolProvider() {
	toolProviderOnce.Do(func() {
		gollm.RegisterWithMeta(
			"test-tools",
			func(gollm.ProviderConfig) (gollm.Provider, error) { return nil, nil },
			gollm.ProviderMeta{SupportsTools: true},
		)
	})
}

// scriptedToolProvider returns canned tool-calling responses by call order and
// records every request it received (so tests can assert which tools were
// offered and the tool_result round-trip).
type scriptedToolProvider struct {
	responses []gollm.ChatResponse
	reqs      []gollm.ChatRequest
	idx       int
}

func (p *scriptedToolProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	p.reqs = append(p.reqs, req)
	i := p.idx
	p.idx++
	if i < len(p.responses) {
		resp := p.responses[i]
		return &resp, nil
	}
	return &gollm.ChatResponse{
		StopReason: "tool_use",
		ToolCalls:  []gollm.ToolCall{{ID: "end", Name: string(actDecline), Input: map[string]any{"reason": "out of script"}}},
		Usage:      gollm.Usage{InputTokens: 10, OutputTokens: 5},
	}, nil
}
func (p *scriptedToolProvider) Validate(ctx context.Context) error { return nil }

// toolCall builds a ChatResponse carrying one tool call.
func toolCall(name string, input map[string]any) gollm.ChatResponse {
	return gollm.ChatResponse{
		StopReason: "tool_use",
		ToolCalls:  []gollm.ToolCall{{ID: name + "-1", Name: name, Input: input}},
		Usage:      gollm.Usage{InputTokens: 10, OutputTokens: 5},
	}
}

func toolRuntime(provider gollm.Provider, wh *testutil.MockWarehouseProvider, sp ai.SchemaProvider, filterField string) *ProjectRuntime {
	ensureToolProvider()
	rt := testRuntime(provider, wh, sp, filterField)
	// Mark the client as a tool-capable provider so run() takes the native path.
	rt.AIClient.SetProvenance("p1", "", "test-tools")
	return rt
}

func runOnceTools(t *testing.T, cfg Config, p *scriptedToolProvider, wh *testutil.MockWarehouseProvider, sp ai.SchemaProvider, filterField string) *fakeStore {
	t.Helper()
	if cfg.MaxRounds == 0 {
		cfg = Config{MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
	}
	store := &fakeStore{}
	r := &runner{cfg: cfg, store: store}
	rt := toolRuntime(p, wh, sp, filterField)
	r.run(context.Background(), rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "how many?"})
	if store.final == nil {
		t.Fatal("turn did not finalize")
	}
	return store
}

func hasTool(tools []gollm.ToolDefinition, name string) bool {
	for _, td := range tools {
		if td.Name == name {
			return true
		}
	}
	return false
}

func TestToolsSupported_DetectsProvider(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	// Default test runtime has no provenance → unknown provider → fallback.
	if toolsSupported(testRuntime(&scriptedProvider{}, wh, nil, "")) {
		t.Fatal("unknown provider must not be treated as tool-capable")
	}
	// A runtime marked with the tool-capable provider → native path.
	if !toolsSupported(toolRuntime(&scriptedToolProvider{}, wh, nil, "")) {
		t.Fatal("test-tools provider should be tool-capable")
	}
}

func TestLoopTools_HappyPath(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		toolCall(string(actQuery), map[string]any{"query": "SELECT count(*) AS c FROM ds.t"}),
		toolCall(string(actAnswer), map[string]any{"text": "There are 100 rows."}),
	}}
	store := runOnceTools(t, Config{}, p, wh, nil, "")
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

func TestLoopTools_AnswerWithheldUntilGrounded(t *testing.T) {
	// The structural guarantee: while ungrounded the model is FORCED to call a
	// tool ("any") and the `answer` tool is not even offered — fabrication is
	// impossible. Once a query has run, `answer` appears and the choice relaxes.
	wh := testutil.NewMockWarehouseProvider("ds")
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		toolCall(string(actQuery), map[string]any{"query": "SELECT 1 FROM ds.t"}),
		toolCall(string(actAnswer), map[string]any{"text": "grounded"}),
	}}
	runOnceTools(t, Config{}, p, wh, nil, "")
	if len(p.reqs) < 2 {
		t.Fatalf("expected at least 2 model calls, got %d", len(p.reqs))
	}
	if hasTool(p.reqs[0].Tools, string(actAnswer)) {
		t.Fatal("answer tool must NOT be offered before any evidence is gathered")
	}
	if p.reqs[0].ToolChoice != "any" {
		t.Fatalf("ungrounded ToolChoice = %q, want any", p.reqs[0].ToolChoice)
	}
	if !hasTool(p.reqs[1].Tools, string(actAnswer)) {
		t.Fatal("answer tool must be offered once grounded")
	}
	if p.reqs[1].ToolChoice != "auto" {
		t.Fatalf("grounded ToolChoice = %q, want auto", p.reqs[1].ToolChoice)
	}
}

func TestLoopTools_UngroundedFreeTextNudged(t *testing.T) {
	// If the model replies with free text (no tool call) while ungrounded, the
	// loop must NOT accept it as an answer — it nudges and continues until a real
	// query backs the answer.
	wh := testutil.NewMockWarehouseProvider("ds")
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		{Content: "There are 5 tables.", StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 5}},
		toolCall(string(actQuery), map[string]any{"query": "SELECT 1 FROM ds.t"}),
		toolCall(string(actAnswer), map[string]any{"text": "grounded answer"}),
	}}
	store := runOnceTools(t, Config{}, p, wh, nil, "")
	if store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("status = %q, want done", store.final.Status)
	}
	if store.final.Answer != "grounded answer" {
		t.Fatalf("ungrounded free text must not win; answer = %q", store.final.Answer)
	}
	if len(store.events) != 1 {
		t.Fatalf("events = %d, want 1", len(store.events))
	}
}

func TestLoopTools_DeclineToolBeforeQuery(t *testing.T) {
	// decline is available even while ungrounded — a genuinely unanswerable
	// question terminates cleanly with no query.
	wh := testutil.NewMockWarehouseProvider("ds")
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		toolCall(string(actDecline), map[string]any{"reason": "not in the data"}),
	}}
	store := runOnceTools(t, Config{}, p, wh, nil, "")
	if store.final.Status != commonmodels.AskTurnStatusDeclined {
		t.Fatalf("status = %q, want declined", store.final.Status)
	}
	if len(store.events) != 0 || len(wh.Calls) != 0 {
		t.Fatalf("decline must run no query; events=%d calls=%d", len(store.events), len(wh.Calls))
	}
}

func TestLoopTools_ClarifyToolBeforeQuery(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		toolCall(string(actClarify), map[string]any{"question": "which region?"}),
	}}
	store := runOnceTools(t, Config{}, p, wh, nil, "")
	if store.final.Disposition != commonmodels.AskTurnDispositionClarify {
		t.Fatalf("disposition = %q, want clarify", store.final.Disposition)
	}
	if len(store.events) != 0 {
		t.Fatalf("clarify must run no query; events=%d", len(store.events))
	}
}

func TestLoopTools_ParallelToolCalls(t *testing.T) {
	// A single response with two tool calls: both execute, and both results are
	// fed back in one user message correlated by call id.
	wh := testutil.NewMockWarehouseProvider("ds")
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		{
			StopReason: "tool_use",
			ToolCalls: []gollm.ToolCall{
				{ID: "q1", Name: string(actQuery), Input: map[string]any{"query": "SELECT 1 FROM ds.a"}},
				{ID: "q2", Name: string(actQuery), Input: map[string]any{"query": "SELECT 2 FROM ds.b"}},
			},
			Usage: gollm.Usage{InputTokens: 10, OutputTokens: 5},
		},
		toolCall(string(actAnswer), map[string]any{"text": "both ran"}),
	}}
	store := runOnceTools(t, Config{}, p, wh, nil, "")
	if store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("status = %q", store.final.Status)
	}
	if len(store.events) != 2 {
		t.Fatalf("events = %d, want 2 (both parallel calls executed)", len(store.events))
	}
	// The second request must carry a user message with two tool results.
	last := p.reqs[1].Messages[len(p.reqs[1].Messages)-1]
	if last.Role != "user" || len(last.ToolResults) != 2 {
		t.Fatalf("expected a user message with 2 tool results, got role=%q results=%d", last.Role, len(last.ToolResults))
	}
	if last.ToolResults[0].CallID != "q1" || last.ToolResults[1].CallID != "q2" {
		t.Fatalf("tool results not correlated by call id: %+v", last.ToolResults)
	}
}

func TestLoopTools_RoundCapThenSynthesizes(t *testing.T) {
	cfg := Config{MaxRounds: 2, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
	wh := testutil.NewMockWarehouseProvider("ds")
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		toolCall(string(actQuery), map[string]any{"query": "SELECT 1 FROM ds.a"}),
		toolCall(string(actQuery), map[string]any{"query": "SELECT 2 FROM ds.b"}),
		toolCall(string(actAnswer), map[string]any{"text": "synthesized from gathered data"}),
	}}
	store := runOnceTools(t, cfg, p, wh, nil, "")
	if store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("status = %q, want done via synthesis", store.final.Status)
	}
	if store.final.Answer != "synthesized from gathered data" {
		t.Fatalf("answer = %q", store.final.Answer)
	}
}

func TestLoopTools_EmptyQueryNotGrounding(t *testing.T) {
	// A query_data call missing its required "query" arg must be rejected without
	// running — it must NOT emit a (spuriously grounding) event, so the `answer`
	// tool stays withheld until a real query has run.
	wh := testutil.NewMockWarehouseProvider("ds")
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		toolCall(string(actQuery), map[string]any{}),                              // missing query → rejected
		toolCall(string(actQuery), map[string]any{"query": "SELECT 1 FROM ds.t"}), // real query → grounds
		toolCall(string(actAnswer), map[string]any{"text": "grounded"}),
	}}
	store := runOnceTools(t, Config{}, p, wh, nil, "")
	if store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("status = %q, want done", store.final.Status)
	}
	if len(store.events) != 1 {
		t.Fatalf("events = %d, want 1 (empty query must not emit a grounding event)", len(store.events))
	}
	if len(wh.Calls) != 1 {
		t.Fatalf("warehouse calls = %d, want 1 (empty query must not reach the warehouse)", len(wh.Calls))
	}
	// After the rejected empty query the turn is still ungrounded, so answer must
	// not yet be offered.
	if hasTool(p.reqs[1].Tools, string(actAnswer)) {
		t.Fatal("answer offered after a rejected empty query — turn wrongly considered grounded")
	}
}

func TestLoopTools_MixedTerminalDefersToData(t *testing.T) {
	// A response carrying a data call AND a terminal answer in parallel must NOT
	// finalize on the answer (it could reference a query that never ran) — the
	// data call runs and the answer is deferred to a later round.
	wh := testutil.NewMockWarehouseProvider("ds")
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		toolCall(string(actQuery), map[string]any{"query": "SELECT 1 FROM ds.a"}),
		{
			StopReason: "tool_use",
			ToolCalls: []gollm.ToolCall{
				{ID: "q2", Name: string(actQuery), Input: map[string]any{"query": "SELECT 2 FROM ds.b"}},
				{ID: "a1", Name: string(actAnswer), Input: map[string]any{"text": "premature answer"}},
			},
			Usage: gollm.Usage{InputTokens: 10, OutputTokens: 5},
		},
		toolCall(string(actAnswer), map[string]any{"text": "final grounded answer"}),
	}}
	store := runOnceTools(t, Config{}, p, wh, nil, "")
	if store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("status = %q", store.final.Status)
	}
	if store.final.Answer != "final grounded answer" {
		t.Fatalf("mixed-batch answer must be deferred; got %q", store.final.Answer)
	}
	if len(store.events) != 2 {
		t.Fatalf("events = %d, want 2 (both data queries ran)", len(store.events))
	}
	// The deferred answer call must have received an error tool_result.
	round2Results := p.reqs[2].Messages[len(p.reqs[2].Messages)-1].ToolResults
	var sawAnswerRefusal bool
	for _, tr := range round2Results {
		if tr.CallID == "a1" && tr.IsError {
			sawAnswerRefusal = true
		}
	}
	if !sawAnswerRefusal {
		t.Fatal("the deferred answer call should receive an error tool_result")
	}
}

// errThenTextProvider errors on its first Chat (simulating a model that rejects
// tools) then returns scripted JSON-text responses for the fallback loop.
type errThenTextProvider struct {
	calls int
	text  []string
}

func (p *errThenTextProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	p.calls++
	if p.calls == 1 {
		return nil, errors.New("tools not supported by this model")
	}
	content := `{"decline":"out of script"}`
	if i := p.calls - 2; i < len(p.text) {
		content = p.text[i]
	}
	return &gollm.ChatResponse{Content: content, Usage: gollm.Usage{InputTokens: 10, OutputTokens: 5}}, nil
}
func (p *errThenTextProvider) Validate(ctx context.Context) error { return nil }

func TestLoopTools_FallbackOnToolErrorUsesTextLoop(t *testing.T) {
	// SupportsTools is provider-wide; if the specific model rejects tools the
	// first call errors before any evidence — the turn must fall back to the
	// JSON-text loop and still answer rather than failing.
	ensureToolProvider()
	wh := testutil.NewMockWarehouseProvider("ds")
	p := &errThenTextProvider{text: []string{
		`{"query":"SELECT count(*) AS c FROM ds.t"}`,
		`{"answer":"text-path answer"}`,
	}}
	store := &fakeStore{}
	r := &runner{cfg: Config{MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}, store: store}
	rt := testRuntime(p, wh, nil, "")
	rt.AIClient.SetProvenance("p1", "", "test-tools") // tool-capable provider → native path tried first
	r.run(context.Background(), rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "how many?"})
	if store.final == nil {
		t.Fatal("turn did not finalize")
	}
	if store.final.Status != commonmodels.AskTurnStatusDone || store.final.Answer != "text-path answer" {
		t.Fatalf("expected fallback text path to answer; got status=%q answer=%q", store.final.Status, store.final.Answer)
	}
	if len(store.events) != 1 {
		t.Fatalf("events = %d, want 1 from the fallback query", len(store.events))
	}
}

func TestLoopTools_SchemaToolsOfferedOnlyWithProvider(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	// No schema provider → search/lookup tools must not be offered.
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		toolCall(string(actQuery), map[string]any{"query": "SELECT 1 FROM ds.t"}),
		toolCall(string(actAnswer), map[string]any{"text": "ok"}),
	}}
	runOnceTools(t, Config{}, p, wh, nil, "")
	if hasTool(p.reqs[0].Tools, string(actSearch)) || hasTool(p.reqs[0].Tools, string(actLookup)) {
		t.Fatal("schema tools must not be offered when SchemaProvider is nil")
	}

	// With a schema provider → both are offered.
	sp := &fakeSchema{hits: []ai.SearchHit{{Table: "ds.users", Blurb: "u", RowCount: 1, Score: 0.5}}}
	p2 := &scriptedToolProvider{responses: []gollm.ChatResponse{
		toolCall(string(actSearch), map[string]any{"query": "users"}),
		toolCall(string(actAnswer), map[string]any{"text": "found"}),
	}}
	store := runOnceTools(t, Config{}, p2, testutil.NewMockWarehouseProvider("ds"), sp, "")
	if !hasTool(p2.reqs[0].Tools, string(actSearch)) || !hasTool(p2.reqs[0].Tools, string(actLookup)) {
		t.Fatal("schema tools should be offered when SchemaProvider is set")
	}
	if len(store.events) != 1 || store.events[0].Name != "search_tables" {
		t.Fatalf("events = %+v, want a search_tables event", store.events)
	}
}
