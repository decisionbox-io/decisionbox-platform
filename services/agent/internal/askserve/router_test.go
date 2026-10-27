package askserve

import (
	"context"
	"sync"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// lookup_schema must default an omitted datasource_id to the turn's routing
// primary — the same default query_data uses — not to "" (which SchemaRouter
// would map to the project primary, possibly outside a router-narrowed subset).
func TestLookupDatasource(t *testing.T) {
	r := &runner{}
	t.Run("pinned turn forces its datasource", func(t *testing.T) {
		st := &turnState{routing: turnRouting{pinned: "wh_x", primary: "wh_p"}}
		if got := r.lookupDatasource(st, &turnAction{Datasource: "wh_ignored"}); got != "wh_x" {
			t.Errorf("got %q, want wh_x", got)
		}
	})
	t.Run("explicit id honoured", func(t *testing.T) {
		st := &turnState{routing: turnRouting{primary: "wh_p"}}
		if got := r.lookupDatasource(st, &turnAction{Datasource: "wh_b"}); got != "wh_b" {
			t.Errorf("got %q, want wh_b", got)
		}
	})
	t.Run("omitted id defaults to the routing primary", func(t *testing.T) {
		st := &turnState{routing: turnRouting{primary: "wh_routed"}}
		if got := r.lookupDatasource(st, &turnAction{Datasource: ""}); got != "wh_routed" {
			t.Errorf("got %q, want wh_routed (routing primary, not empty)", got)
		}
	})
}

// routerCfg is mwConfig with the evidence-grounded router turned on.
func routerCfg() Config {
	c := mwConfig()
	c.RouterEnabled = true
	return c
}

func runRouted(t *testing.T, rt *ProjectRuntime, question string) *fakeStore {
	t.Helper()
	store := &fakeStore{}
	(&runner{cfg: routerCfg(), store: store}).run(context.Background(),
		rt, TurnRequest{TurnID: "rt", SessionID: "s", ProjectID: "p", Question: question})
	if store.final == nil {
		t.Fatal("turn did not finalize")
	}
	return store
}

func TestRouter_PinsSingleDatasource(t *testing.T) {
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		`{"datasources":["wh_b"],"reason":"about CRM users","confidence":0.9}`, // router decision
		`{"query":"select count(*) from crm.users"}`,                           // loop query (no ds_id — pinned)
		`{"answer":"done"}`,
	}}, whA, whB, nil, nil)

	store := runRouted(t, rt, "how many flagged CRM users?")
	if store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("status = %q (%s), want done", store.final.Status, store.final.Error)
	}
	if len(whB.Calls) != 1 || len(whA.Calls) != 0 {
		t.Fatalf("router should have pinned the turn to wh_b; a=%d b=%d", len(whA.Calls), len(whB.Calls))
	}
	if store.final.RoutingReason != "about CRM users" || store.final.RoutingConfidence != 0.9 {
		t.Fatalf("routing telemetry not recorded: reason=%q conf=%v", store.final.RoutingReason, store.final.RoutingConfidence)
	}
	if store.final.RoutingClarify {
		t.Fatal("confident single-datasource route must not clarify")
	}
	// The one query event is stamped with the pinned datasource.
	if eventDatasource(store.events[0]) != "wh_b" {
		t.Fatalf("query datasource = %q, want wh_b", eventDatasource(store.events[0]))
	}
	// A router-pinned turn still records the routed datasource in telemetry
	// even though it flips multi off — it is a real routing decision, not the
	// single-warehouse path.
	if got := store.final.RoutedDatasourceIDs; len(got) != 1 || got[0] != "wh_b" {
		t.Fatalf("routed datasource ids = %v, want [wh_b]", got)
	}
}

func TestRouter_ClarifiesOnAmbiguity(t *testing.T) {
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		`{"clarify":true,"question":"Do you mean ticket sales or CRM accounts?","confidence":0.3}`,
	}}, whA, whB, nil, nil)

	store := runRouted(t, rt, "show me the users")
	if store.final.Disposition != commonmodels.AskTurnDispositionClarify {
		t.Fatalf("disposition = %q, want clarify", store.final.Disposition)
	}
	if store.final.Answer != "Do you mean ticket sales or CRM accounts?" {
		t.Fatalf("clarify question = %q", store.final.Answer)
	}
	if !store.final.RoutingClarify {
		t.Fatal("RoutingClarify should be true")
	}
	if len(whA.Calls) != 0 || len(whB.Calls) != 0 {
		t.Fatal("no datasource should be queried when the router clarifies")
	}
}

func TestRouter_LowConfidenceClarifies(t *testing.T) {
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	// A datasource is named but confidence is below the clarify threshold.
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		`{"datasources":["wh_a"],"reason":"maybe sales","confidence":0.2}`,
	}}, whA, whB, nil, nil)

	store := runRouted(t, rt, "how are things?")
	if store.final.Disposition != commonmodels.AskTurnDispositionClarify {
		t.Fatalf("low confidence must clarify, got disposition %q", store.final.Disposition)
	}
	if !store.final.RoutingClarify || store.final.Answer == "" {
		t.Fatalf("expected a clarifying question, got %+v", store.final)
	}
}

func TestRouter_ProjectLevelToolsBypassClarifyDeadEnd(t *testing.T) {
	// On a multi-datasource project, a knowledge-answerable question maps to NO
	// datasource. Rather than dead-ending on a datasource clarification, the router
	// falls through so the answering loop can offer search_knowledge.
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		`{"datasources":[],"project_level":true,"reason":"knowledge question","confidence":0.9}`, // router flags a non-data question
		`{"search_knowledge":"refund policy"}`,                                                   // loop: KB search grounds
		`{"answer":"Refunds are allowed within 30 days."}`,
	}}, whA, whB, nil, nil)
	rt.KnowledgeProvider = &fakeKnowledge{chunks: []KnowledgeChunk{
		{SourceID: "s1", Position: 0, SourceName: "policy.pdf", SourceType: "pdf", Text: "Refunds within 30 days.", Score: 0.9},
	}}

	store := runRouted(t, rt, "what is our refund policy?")
	if store.final.RoutingClarify {
		t.Fatal("a KB-answerable question must not dead-end on a datasource clarification")
	}
	if store.final.Status != commonmodels.AskTurnStatusDone || store.final.Answer == "" {
		t.Fatalf("KB turn should answer, got status=%q answer=%q", store.final.Status, store.final.Answer)
	}
	if len(whA.Calls) != 0 || len(whB.Calls) != 0 {
		t.Fatal("a KB answer should query no warehouse")
	}

	// Contrast: with NO project-level tools, the same unmappable decision still
	// clarifies (the bypass is scoped to len(valid)==0 AND project-level tools).
	rt2 := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		`{"datasources":[],"clarify":true,"question":"which datasource?","confidence":0.1}`,
	}}, testutil.NewMockWarehouseProvider("sales"), testutil.NewMockWarehouseProvider("crm"), nil, nil)
	store2 := runRouted(t, rt2, "how many users?")
	if !store2.final.RoutingClarify {
		t.Fatal("without project-level tools, an unmappable question should still clarify")
	}
}

func TestRouter_AmbiguousDataQuestionStillClarifiesWithKB(t *testing.T) {
	// Even with a knowledge base configured, an ambiguous DATA question (the router
	// did NOT flag project_level) must still get the router's clarification — not
	// fall through to guess a datasource.
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		`{"datasources":[],"clarify":true,"question":"Sales or CRM users?","confidence":0.2}`, // ambiguous DATA, project_level false
	}}, whA, whB, nil, nil)
	rt.KnowledgeProvider = &fakeKnowledge{chunks: []KnowledgeChunk{{SourceName: "x", Text: "y"}}}

	store := runRouted(t, rt, "show me the users")
	if !store.final.RoutingClarify {
		t.Fatal("an ambiguous data question must still clarify even when a KB is present")
	}
	if store.final.Answer != "Sales or CRM users?" {
		t.Fatalf("should ask the router's clarifying question, got %q", store.final.Answer)
	}
}

func TestRouter_NoDatasourceNeededReachesAnsweringLoop(t *testing.T) {
	// A request that needs no data at all (a poem, small talk) maps to NO
	// datasource, and the router says so without asking to clarify. Asking the
	// user "which datasource?" contradicts the router's own verdict; the turn
	// goes to the answering loop, which declines.
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		`{"datasources":[],"reason":"a creative request unrelated to the data","confidence":0.95,"clarify":false,"project_level":false}`,
		`{"decline":"I can only answer questions about this project's data."}`,
	}}, whA, whB, nil, nil)

	store := runRouted(t, rt, "write me a short poem about football")
	if store.final.RoutingClarify {
		t.Fatalf("no-datasource verdict must not become a datasource clarification, got %q", store.final.Answer)
	}
	if store.final.Disposition != commonmodels.AskTurnDispositionDecline {
		t.Fatalf("disposition = %q, want decline", store.final.Disposition)
	}
	if len(whA.Calls) != 0 || len(whB.Calls) != 0 {
		t.Fatal("a declined request should query no warehouse")
	}
}

func TestRouter_MutationBypassRequiresNativeTools(t *testing.T) {
	// On a non-tool-calling LLM (runText), mutation tools can't be dispatched, so a
	// save tool must NOT count as "available" for the router bypass — an unmapped
	// request still clarifies rather than entering a loop that can't save.
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		// project_level=true, but on a non-tool LLM the save tool isn't callable, so
		// the bypass must NOT trigger — the turn clarifies.
		`{"datasources":[],"project_level":true,"reason":"save request","confidence":0.9}`,
	}}, whA, whB, nil, nil)
	rt.MutationTools = []MutationTool{{
		Name: "save_note", Description: "Save.", InputSchema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, in MutationInput) (MutationOutput, error) {
			return MutationOutput{ProposalID: "p"}, nil
		},
	}} // no KnowledgeProvider

	store := &fakeStore{}
	(&runner{cfg: routerCfg(), store: store}).run(context.Background(), rt,
		TurnRequest{TurnID: "rt", SessionID: "s", ProjectID: "p", Question: "save this as a note", CallerRole: "member"})
	if store.final == nil || !store.final.RoutingClarify {
		t.Fatalf("mutations on a non-tool LLM must not bypass the router clarification, got %+v", store.final)
	}
}

func TestRouter_CrossSourceKeepsMultiHop(t *testing.T) {
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		`{"datasources":["wh_a","wh_b"],"reason":"join sales buyers to CRM flags","confidence":0.85}`, // router
		`{"query":"select buyerid from sales.orders limit 10","datasource_id":"wh_a"}`,
		`{"query":"select userid, flagged from crm.users where userid in (1,2,3)","datasource_id":"wh_b"}`,
		`{"answer":"cross-referenced"}`,
	}}, whA, whB, nil, nil)

	store := runRouted(t, rt, "which top ticket buyers are flagged in CRM?")
	if store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("status = %q (%s), want done", store.final.Status, store.final.Error)
	}
	if len(whA.Calls) != 1 || len(whB.Calls) != 1 {
		t.Fatalf("cross-source route should keep multi-hop; a=%d b=%d", len(whA.Calls), len(whB.Calls))
	}
	if got := store.final.RoutedDatasourceIDs; len(got) != 2 || got[0] != "wh_a" || got[1] != "wh_b" {
		t.Fatalf("RoutedDatasourceIDs = %v, want [wh_a wh_b]", got)
	}
	if store.final.RoutingReason != "join sales buyers to CRM flags" {
		t.Fatalf("routing reason = %q", store.final.RoutingReason)
	}
}

func TestRouter_UnknownDatasourceFallsBack(t *testing.T) {
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	// Router names an id that doesn't exist → filtered out → nothing valid →
	// clarify rather than guess.
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		`{"datasources":["wh_ghost"],"reason":"?","confidence":0.9}`,
	}}, whA, whB, nil, nil)

	store := runRouted(t, rt, "anything")
	if store.final.Disposition != commonmodels.AskTurnDispositionClarify {
		t.Fatalf("an all-unknown route must clarify, got %q", store.final.Disposition)
	}
}

func TestParseRouteDecision(t *testing.T) {
	d, err := parseRouteDecision("Here is my choice:\n{\"datasources\":[\"wh_a\"],\"reason\":\"sales\",\"confidence\":0.7,\"clarify\":false}\nThat's it.")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(d.Datasources) != 1 || d.Datasources[0] != "wh_a" || d.Confidence != 0.7 {
		t.Fatalf("parsed = %+v", d)
	}
	c, err := parseRouteDecision(`{"clarify":true,"question":"which?"}`)
	if err != nil || !c.Clarify || c.Question != "which?" {
		t.Fatalf("clarify parse = %+v err=%v", c, err)
	}
	if _, err := parseRouteDecision("no json here"); err == nil {
		t.Fatal("expected error on non-JSON")
	}
}

// recordingRouteProvider answers like the script but keeps every request, so
// the budget the router actually asked for is assertable.
type recordingRouteProvider struct {
	*scriptedProvider
	mu       sync.Mutex
	requests []gollm.ChatRequest
}

func (p *recordingRouteProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	return p.scriptedProvider.Chat(ctx, req)
}

// The router's reservation is small and fixed, but "small" is relative to
// the model: a router layer pointed at one that accepts less rejects the
// call, and a failed route silently costs the turn its datasource decision.
func TestRouter_RespectsItsLayersOutputCeiling(t *testing.T) {
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	prov := &recordingRouteProvider{scriptedProvider: &scriptedProvider{responses: []string{
		`{"datasources":["wh_b"],"reason":"crm","confidence":0.9}`,
		`{"query":"select 1"}`,
		`{"answer":"done"}`,
	}}}
	rt := twoDatasourceRuntime(prov, whA, whB, nil, nil)
	rt.RouterOutputCap = 128

	runRouted(t, rt, "how many flagged CRM users?")

	prov.mu.Lock()
	defer prov.mu.Unlock()
	if len(prov.requests) == 0 {
		t.Fatal("the router made no call")
	}
	if got := prov.requests[0].MaxTokens; got > 128 {
		t.Errorf("router asked for %d tokens from a layer capped at 128", got)
	}

	// With no known ceiling the reservation stands, so a layer that accepts
	// more is not quietly shrunk.
	prov2 := &recordingRouteProvider{scriptedProvider: &scriptedProvider{responses: []string{
		`{"datasources":["wh_b"],"reason":"crm","confidence":0.9}`,
		`{"query":"select 1"}`,
		`{"answer":"done"}`,
	}}}
	rt2 := twoDatasourceRuntime(prov2, testutil.NewMockWarehouseProvider("sales"), testutil.NewMockWarehouseProvider("crm"), nil, nil)
	runRouted(t, rt2, "how many flagged CRM users?")
	prov2.mu.Lock()
	defer prov2.mu.Unlock()
	if got := prov2.requests[0].MaxTokens; got != routeMaxOutputTokens {
		t.Errorf("router asked for %d, want the %d reservation when the layer has no ceiling", got, routeMaxOutputTokens)
	}
}
