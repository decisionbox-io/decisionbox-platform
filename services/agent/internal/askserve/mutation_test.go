package askserve

import (
	"context"
	"errors"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// A registered mutation tool is offered, executed with the turn identity, and
// its proposal id is recorded on a non-grounding tool event; a successful write
// lets the model finish the turn to confirm the save.
func TestLoopTools_MutationToolOfferedAndRecordsProposal(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	var got MutationInput
	mt := MutationTool{
		Name:        "save_note",
		Description: "Save a note.",
		InputSchema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, in MutationInput) (MutationOutput, error) {
			got = in
			return MutationOutput{ProposalID: "prop-1", Output: map[string]any{"status": "pending"}}, nil
		},
	}
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		toolCall("save_note", map[string]any{"title": "T", "body": "B"}),
		toolCall(string(actAnswer), map[string]any{"text": "Saved it as a pending note."}),
	}}
	cfg := Config{MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
	store := &fakeStore{}
	r := &runner{cfg: cfg, store: store}
	rt := toolRuntime(p, wh, nil, "")
	rt.MutationTools = []MutationTool{mt}

	r.run(context.Background(), rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "save this as a note", CallerSub: "user-9", CallerRole: "member"})

	if !hasTool(p.reqs[0].Tools, "save_note") {
		t.Fatal("save_note should be offered when a mutation tool is registered")
	}
	if got.ProjectID != "p1" || got.SessionID != "s1" || got.TurnID != "t1" || got.CallerSub != "user-9" {
		t.Fatalf("mutation input identity wrong: %+v", got)
	}
	if len(store.events) != 1 || store.events[0].Name != "save_note" || store.events[0].ProposalID != "prop-1" {
		t.Fatalf("expected one save_note event carrying the proposal id, got %+v", store.events)
	}
	if store.events[0].Error != "" {
		t.Fatalf("mutation event should be clean, got error %q", store.events[0].Error)
	}
	// answer offered on the 2nd call → the successful write unlocked finishing.
	if !hasTool(p.reqs[1].Tools, string(actAnswer)) {
		t.Fatal("answer should be offered after a successful mutation")
	}
	if store.final.Status != commonmodels.AskTurnStatusDone || store.final.Answer == "" {
		t.Fatalf("final = %+v", store.final)
	}
}

func TestLoopTools_MutationNotOfferedWithoutRegistration(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		toolCall(string(actQuery), map[string]any{"query": "SELECT 1 FROM ds.t"}),
		toolCall(string(actAnswer), map[string]any{"text": "ok"}),
	}}
	cfg := Config{MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
	store := &fakeStore{}
	r := &runner{cfg: cfg, store: store}
	rt := toolRuntime(p, wh, nil, "") // no MutationTools
	r.run(context.Background(), rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "x"})
	if hasTool(p.reqs[0].Tools, "save_note") {
		t.Fatal("no mutation tool registered → none offered")
	}
}

func TestLoopTools_MutationNotOfferedToViewer(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	mt := MutationTool{
		Name: "save_note", Description: "Save.", InputSchema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, in MutationInput) (MutationOutput, error) {
			return MutationOutput{ProposalID: "p"}, nil
		},
	}
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		toolCall(string(actQuery), map[string]any{"query": "SELECT 1 FROM ds.t"}),
		toolCall(string(actAnswer), map[string]any{"text": "ok"}),
	}}
	cfg := Config{MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
	store := &fakeStore{}
	r := &runner{cfg: cfg, store: store}
	rt := toolRuntime(p, wh, nil, "")
	rt.MutationTools = []MutationTool{mt}

	r.run(context.Background(), rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "save it", CallerRole: "viewer"})

	for _, req := range p.reqs {
		if hasTool(req.Tools, "save_note") {
			t.Fatal("save_note must not be offered to a viewer")
		}
	}
}

func TestLoopTools_MutationBatchedWithQueryIsDeferred(t *testing.T) {
	// A save_note returned in the SAME batch as a query_data must be refused (so
	// it can't persist figures the model hasn't observed yet); the query runs and
	// the model re-issues the save on its own step.
	wh := testutil.NewMockWarehouseProvider("ds")
	saved := 0
	mt := MutationTool{
		Name: "save_note", Description: "Save.", InputSchema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, in MutationInput) (MutationOutput, error) {
			saved++
			return MutationOutput{ProposalID: "p1"}, nil
		},
	}
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		{
			StopReason: "tool_use",
			ToolCalls: []gollm.ToolCall{
				{ID: "q1", Name: string(actQuery), Input: map[string]any{"query": "SELECT count(*) c FROM ds.t"}},
				{ID: "n1", Name: "save_note", Input: map[string]any{"title": "T", "body": "B"}},
			},
			Usage: gollm.Usage{InputTokens: 10, OutputTokens: 5},
		},
		toolCall(string(actAnswer), map[string]any{"text": "The count is 100."}),
	}}
	cfg := Config{MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
	store := &fakeStore{}
	r := &runner{cfg: cfg, store: store}
	rt := toolRuntime(p, wh, nil, "")
	rt.MutationTools = []MutationTool{mt}

	r.run(context.Background(), rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "count rows and save it", CallerRole: "member"})

	if saved != 0 {
		t.Fatalf("save_note batched with a query must be deferred, not executed (ran %d times)", saved)
	}
	// The query still ran (evidence gathered), and the batched save_note produced
	// a refusal tool result rather than a proposal event.
	var hasQueryEvent, hasSaveEvent bool
	for _, ev := range store.events {
		switch ev.Name {
		case "query_data":
			hasQueryEvent = true
		case "save_note":
			hasSaveEvent = true
		}
	}
	if !hasQueryEvent {
		t.Fatal("the batched query should still execute")
	}
	if hasSaveEvent {
		t.Fatal("the deferred save_note must not emit a tool event")
	}
}

func TestMayMutate_FailClosed(t *testing.T) {
	cases := map[string]bool{"member": true, "admin": true, "viewer": false, "": false, "editor": false, "Member": false}
	for role, want := range cases {
		st := &turnState{req: TurnRequest{CallerRole: role}}
		if got := st.mayMutate(); got != want {
			t.Fatalf("role %q: mayMutate=%v, want %v", role, got, want)
		}
	}
}

func TestLoopTools_ParallelMutationsDeferred(t *testing.T) {
	// Two save_note calls in one batch must both be refused (only a lone write
	// runs) so the model can't create duplicate/dependent proposals in one step.
	wh := testutil.NewMockWarehouseProvider("ds")
	saved := 0
	mt := MutationTool{
		Name: "save_note", Description: "Save.", InputSchema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, in MutationInput) (MutationOutput, error) { saved++; return MutationOutput{ProposalID: "p"}, nil },
	}
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		{
			StopReason: "tool_use",
			ToolCalls: []gollm.ToolCall{
				{ID: "n1", Name: "save_note", Input: map[string]any{"title": "A", "body": "a"}},
				{ID: "n2", Name: "save_note", Input: map[string]any{"title": "B", "body": "b"}},
			},
			Usage: gollm.Usage{InputTokens: 10, OutputTokens: 5},
		},
		toolCall(string(actDecline), map[string]any{"reason": "done"}),
	}}
	cfg := Config{MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
	store := &fakeStore{}
	r := &runner{cfg: cfg, store: store}
	rt := toolRuntime(p, wh, nil, "")
	rt.MutationTools = []MutationTool{mt}
	r.run(context.Background(), rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "save two notes", CallerRole: "member"})
	if saved != 0 {
		t.Fatalf("parallel save_note calls must both be deferred, ran %d", saved)
	}
}

func TestRoutingQuestion(t *testing.T) {
	// Unseeded → the raw question.
	st := &turnState{req: TurnRequest{Question: "how many users?"}}
	if got := st.routingQuestion(); got != "how many users?" {
		t.Fatalf("unseeded routing question changed: %q", got)
	}
	// Seeded → the seed label/text is appended so the router can anchor.
	st2 := &turnState{req: TurnRequest{Question: "how many?", SeedContext: &SeedContext{Type: "insight", Label: "Churn spike in EU", Text: "EU users churning"}}}
	got := st2.routingQuestion()
	if !strings.Contains(got, "how many?") || !strings.Contains(got, "Churn spike in EU") {
		t.Fatalf("seeded routing question should carry the seed anchor: %q", got)
	}
}

func TestExecMutation_FailureIsNotGrounding(t *testing.T) {
	r := &runner{cfg: Config{}, store: &fakeStore{}}
	mt := MutationTool{Name: "save_note", Run: func(ctx context.Context, in MutationInput) (MutationOutput, error) {
		return MutationOutput{}, errors.New("mongo down")
	}}
	st := &turnState{req: TurnRequest{TurnID: "t", ProjectID: "p"}}
	obs := r.execMutation(context.Background(), st, mt, gollm.ToolCall{ID: "1", Name: "save_note", Input: map[string]any{"title": "T"}})
	if !strings.Contains(obs, "failed") {
		t.Fatalf("failure should report failed, got %q", obs)
	}
	if st.mutationsDone != 0 || st.groundedEvents != 0 || st.canAnswer() {
		t.Fatalf("a failed mutation must not ground or unlock answering (done=%d grounded=%d canAnswer=%v)", st.mutationsDone, st.groundedEvents, st.canAnswer())
	}
	if len(st.events) != 1 || st.events[0].Error == "" {
		t.Fatalf("failed mutation should record an error event, got %+v", st.events)
	}
}

func TestExecMutation_SuccessUnlocksAnswerButDoesNotGround(t *testing.T) {
	r := &runner{cfg: Config{}, store: &fakeStore{}}
	mt := MutationTool{Name: "save_note", Run: func(ctx context.Context, in MutationInput) (MutationOutput, error) {
		return MutationOutput{ProposalID: "p9"}, nil
	}}
	st := &turnState{req: TurnRequest{TurnID: "t", ProjectID: "p"}}
	r.execMutation(context.Background(), st, mt, gollm.ToolCall{ID: "1", Name: "save_note", Input: map[string]any{}})
	if st.mutationsDone != 1 {
		t.Fatalf("mutationsDone = %d, want 1", st.mutationsDone)
	}
	if st.groundedEvents != 0 {
		t.Fatal("a mutation is not evidence — it must not increment groundedEvents")
	}
	if !st.canAnswer() {
		t.Fatal("a successful mutation should let the model finish the turn")
	}
}

func TestBuildSystemPromptForTools_MutationCapabilityLine(t *testing.T) {
	routing := turnRouting{datasources: []DatasourceInfo{{ID: "default", Dialect: "bigquery"}}}

	rt := &ProjectRuntime{MutationTools: []MutationTool{{Name: "save_note", Description: "Save an operator note."}}}
	out := buildSystemPromptForTools(rt, routing, Config{}, false, true, nil)
	if !strings.Contains(out, "save_note") || !strings.Contains(out, "Save an operator note.") {
		t.Fatalf("mutation tool not described in prompt:\n%s", out)
	}
	if !strings.Contains(out, "NOT read-only") {
		t.Fatalf("capability line missing when a mutation tool is present:\n%s", out)
	}

	// mutationsAvailable=false (e.g. a viewer) → no capability line, tool undescribed.
	out2 := buildSystemPromptForTools(rt, routing, Config{}, false, false, nil)
	if strings.Contains(out2, "NOT read-only") || strings.Contains(out2, "save_note") {
		t.Fatalf("a viewer (mutations unavailable) should not see the write tool:\n%s", out2)
	}
}
