package askserve

import (
	"context"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// primeSeed runs an entity-anchored insight search on a seeded first turn: it
// grounds the turn, folds the hits into the citation set, and stashes the
// formatted result on primeContext.
func TestPrimeSeed_GathersGroundsAndFolds(t *testing.T) {
	ins := &fakeInsights{hits: []ai.InsightHit{{ID: "i1", Type: "insight", Name: "Churn spike", Description: "Q3 churn up", Score: 0.9}}}
	r := &runner{cfg: Config{MaxRounds: 8}, store: &fakeStore{}}
	rt := &ProjectRuntime{InsightsProvider: ins} // Schema nil → no table search
	st := &turnState{req: TurnRequest{
		TurnID:      "t1",
		ProjectID:   "p1",
		SeedContext: &SeedContext{Type: "insight", ID: "i1", Label: "Churn spike in EU", Text: "Users in the EU churning"},
	}}

	r.primeSeed(context.Background(), rt, st)

	if ins.calls != 1 {
		t.Fatalf("expected exactly one seed insight search, got %d", ins.calls)
	}
	if st.groundedEvents == 0 {
		t.Fatal("priming a successful search should ground the turn")
	}
	if len(st.insightHits) != 1 || st.insightHits[0].ID != "i1" {
		t.Fatalf("priming should fold hits into insightHits, got %+v", st.insightHits)
	}
	if !strings.Contains(st.primeContext, "Churn spike") {
		t.Fatalf("primeContext should carry the search result, got %q", st.primeContext)
	}
}

func TestPrimeSeed_SkipsFollowupAndUnseeded(t *testing.T) {
	r := &runner{cfg: Config{MaxRounds: 8}, store: &fakeStore{}}

	// Follow-up turn (non-empty history) is not primed.
	ins := &fakeInsights{hits: []ai.InsightHit{{ID: "i1", Type: "insight", Name: "x"}}}
	rt := &ProjectRuntime{InsightsProvider: ins}
	st := &turnState{req: TurnRequest{
		SeedContext: &SeedContext{Type: "insight", ID: "i1", Label: "x"},
		History:     []HistoryMessage{{Role: "user", Content: "earlier"}},
	}}
	r.primeSeed(context.Background(), rt, st)
	if ins.calls != 0 || st.primeContext != "" {
		t.Fatalf("follow-up turn should not prime (calls=%d prime=%q)", ins.calls, st.primeContext)
	}

	// Unseeded turn is not primed.
	ins2 := &fakeInsights{hits: []ai.InsightHit{{ID: "i1"}}}
	rt2 := &ProjectRuntime{InsightsProvider: ins2}
	st2 := &turnState{req: TurnRequest{}}
	r.primeSeed(context.Background(), rt2, st2)
	if ins2.calls != 0 || st2.primeContext != "" {
		t.Fatalf("unseeded turn should not prime (calls=%d)", ins2.calls)
	}
}

func TestPrimeSeed_FailedSearchNotAppended(t *testing.T) {
	// A provider error must not inject its error string as reference context, and
	// must not ground the turn.
	ins := &fakeInsights{err: context.DeadlineExceeded}
	r := &runner{cfg: Config{MaxRounds: 8}, store: &fakeStore{}}
	rt := &ProjectRuntime{InsightsProvider: ins}
	st := &turnState{req: TurnRequest{SeedContext: &SeedContext{Type: "insight", ID: "i1", Label: "x", Text: "y"}}}
	r.primeSeed(context.Background(), rt, st)
	if st.primeContext != "" {
		t.Fatalf("failed search should not append context, got %q", st.primeContext)
	}
	if st.groundedEvents != 0 {
		t.Fatal("failed search must not ground the turn")
	}
}

func TestInsightSources_SeedAlwaysCited(t *testing.T) {
	// Seeded turn cites its anchor even with no search hits.
	st := &turnState{req: TurnRequest{SeedContext: &SeedContext{Type: "recommendation", ID: "r9", Label: "Lower EU price", Text: "details"}}}
	src := st.insightSources()
	if len(src) != 1 || src[0].ID != "r9" || src[0].Type != "recommendation" || src[0].Name != "Lower EU price" {
		t.Fatalf("seed should be cited: %+v", src)
	}

	// Unseeded, no hits → nil (byte-identical to before this change).
	if got := (&turnState{}).insightSources(); got != nil {
		t.Fatalf("unseeded no-hit sources should be nil, got %+v", got)
	}

	// Seed id equal to a hit id is not duplicated; the seed leads.
	st3 := &turnState{
		req:         TurnRequest{SeedContext: &SeedContext{Type: "insight", ID: "i1", Label: "Anchor"}},
		insightHits: []ai.InsightHit{{ID: "i1", Type: "insight", Name: "full"}, {ID: "i2", Type: "insight", Name: "other"}},
	}
	src3 := st3.insightSources()
	if len(src3) != 2 || src3[0].ID != "i1" || src3[1].ID != "i2" {
		t.Fatalf("seed dedup/order wrong: %+v", src3)
	}

	// A non-entity seed (unknown type) is not cited.
	st4 := &turnState{req: TurnRequest{SeedContext: &SeedContext{Type: "item", ID: "x", Label: "L"}}}
	if got := st4.insightSources(); got != nil {
		t.Fatalf("non-entity seed should not be cited, got %+v", got)
	}
}

func TestQuestionWithPrime(t *testing.T) {
	if got := questionWithPrime("q", ""); got != "q" {
		t.Fatalf("empty prime should return the question unchanged, got %q", got)
	}
	got := questionWithPrime("How many users?", "Insight search results for ...")
	if !strings.Contains(got, "How many users?") || !strings.Contains(got, "Insight search results") {
		t.Fatalf("prime should be appended to the question, got %q", got)
	}
}

// End-to-end: a seeded first turn primes retrieval before the model runs, so the
// model's very first request is already grounded (answer offered) and sees the
// entity context, and the seed is cited on the finalized turn.
func TestSeedPriming_EndToEnd(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	ins := &fakeInsights{hits: []ai.InsightHit{{ID: "i1", Type: "insight", Name: "Churn spike", Description: "Q3 churn", Score: 0.9}}}
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		toolCall(string(actAnswer), map[string]any{"text": "Scoped to the churn-spike entity."}),
	}}
	cfg := Config{MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
	store := &fakeStore{}
	r := &runner{cfg: cfg, store: store}
	rt := toolRuntime(p, wh, nil, "")
	rt.InsightsProvider = ins

	r.run(context.Background(), rt, TurnRequest{
		TurnID: "t1", SessionID: "s1", ProjectID: "p1",
		Question:    "how many distinct users?",
		SeedContext: &SeedContext{Type: "insight", ID: "i1", Label: "Churn spike in EU", Text: "Users in the EU churning"},
	})

	if store.final == nil {
		t.Fatal("turn did not finalize")
	}
	if ins.calls != 1 {
		t.Fatalf("expected one priming insight search before the loop, calls=%d", ins.calls)
	}
	// The model's first request already carries the primed context and offers answer.
	if len(p.reqs) == 0 {
		t.Fatal("model was never called")
	}
	var firstUser string
	for _, m := range p.reqs[0].Messages {
		if m.Role == "user" {
			firstUser = m.Content
		}
	}
	if !strings.Contains(firstUser, "auto-gathered") || !strings.Contains(firstUser, "Churn spike") {
		t.Fatalf("first user message should carry primed context, got %q", firstUser)
	}
	if !hasTool(p.reqs[0].Tools, string(actAnswer)) {
		t.Fatal("answer should be offered on the first call (priming grounded the turn)")
	}
	if store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("status=%q", store.final.Status)
	}
	if len(store.final.Sources) == 0 || store.final.Sources[0].ID != "i1" {
		t.Fatalf("seed should be cited: %+v", store.final.Sources)
	}
}
