package askserve

import (
	"context"
	"testing"

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
		`{"datasources":[],"clarify":true,"question":"which datasource?","confidence":0.1}`, // router can't map it
		`{"search_knowledge":"refund policy"}`,       // loop: KB search grounds
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
