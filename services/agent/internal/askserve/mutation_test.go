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

func TestMutation_ReservedNamesDropped(t *testing.T) {
	run := func(ctx context.Context, in MutationInput) (MutationOutput, error) { return MutationOutput{}, nil }
	tools := []MutationTool{
		{Name: "save_note", Run: run},
		{Name: "query_data", Run: run},     // shadows a built-in
		{Name: "search_tables", Run: run},  // shadows a built-in
	}
	names := map[string]bool{}
	for _, d := range mutationDefs(tools) {
		names[d.Name] = true
	}
	if !names["save_note"] || names["query_data"] || names["search_tables"] {
		t.Fatalf("reserved built-in names must be dropped from offered mutation tools: %v", names)
	}
	rt := &ProjectRuntime{MutationTools: tools}
	if _, ok := rt.mutationTool("query_data"); ok {
		t.Fatal("a mutation tool named query_data must not resolve (built-in wins)")
	}
	if _, ok := rt.mutationTool("save_note"); !ok {
		t.Fatal("save_note should resolve as a mutation tool")
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

func TestLoopTools_DeferredWriteNudgedBeforeAnswer(t *testing.T) {
	// "calculate X and save it": the provider batches query_data + save_note. The
	// save is deferred (runs later); when the model then tries to answer, it is
	// nudged to complete the outstanding write first, so the save isn't lost.
	wh := testutil.NewMockWarehouseProvider("ds")
	saved := 0
	mt := MutationTool{
		Name: "save_note", Description: "Save.", InputSchema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, in MutationInput) (MutationOutput, error) { saved++; return MutationOutput{ProposalID: "p1"}, nil },
	}
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		{ // round 1: query + save in one batch → save deferred, query runs (grounds)
			StopReason: "tool_use",
			ToolCalls: []gollm.ToolCall{
				{ID: "q1", Name: string(actQuery), Input: map[string]any{"query": "SELECT COUNT(*) c FROM ds.t"}},
				{ID: "n1", Name: "save_note", Input: map[string]any{"title": "T", "body": "B"}},
			},
			Usage: gollm.Usage{InputTokens: 10, OutputTokens: 5},
		},
		toolCall(string(actAnswer), map[string]any{"text": "The count is 100."}), // round 2: answer → nudged (write still pending)
		toolCall("save_note", map[string]any{"title": "T", "body": "B"}),          // round 3: save on its own → proposal created
		toolCall(string(actAnswer), map[string]any{"text": "Saved; the count is 100."}), // round 4: answer → finishes
	}}
	cfg := Config{MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
	store := &fakeStore{}
	r := &runner{cfg: cfg, store: store}
	rt := toolRuntime(p, wh, nil, "")
	rt.MutationTools = []MutationTool{mt}

	r.run(context.Background(), rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "count rows and save it", CallerRole: "member"})

	if saved != 1 {
		t.Fatalf("the deferred save should run exactly once (after the nudge), got %d", saved)
	}
	if store.final == nil || store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("turn should finish done after the save, got %+v", store.final)
	}
	// The model was nudged: round-2 answer was refused, so it took >3 calls.
	if len(p.reqs) < 4 {
		t.Fatalf("expected the answer to be nudged for the pending write (>=4 calls), got %d", len(p.reqs))
	}
}

func TestLoopTools_DeferredWriteDisclosedWhenNudgeIgnored(t *testing.T) {
	// "count rows and save it": the batch defers the save; the model then answers,
	// gets nudged once, and answers AGAIN (ignoring the nudge) with steps to spare.
	// The save was never created, so the finishing answer must disclose it rather
	// than end with a clean answer while nothing was persisted.
	wh := testutil.NewMockWarehouseProvider("ds")
	saved := 0
	mt := MutationTool{
		Name: "save_note", Description: "Save.", InputSchema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, in MutationInput) (MutationOutput, error) { saved++; return MutationOutput{ProposalID: "p1"}, nil },
	}
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		{ // round 1: query + save batched → query grounds, save deferred
			StopReason: "tool_use",
			ToolCalls: []gollm.ToolCall{
				{ID: "q1", Name: string(actQuery), Input: map[string]any{"query": "SELECT COUNT(*) c FROM ds.t"}},
				{ID: "n1", Name: "save_note", Input: map[string]any{"title": "T", "body": "B"}},
			},
			Usage: gollm.Usage{InputTokens: 10, OutputTokens: 5},
		},
		toolCall(string(actAnswer), map[string]any{"text": "The count is 100."}), // round 2: answer → nudged
		toolCall(string(actAnswer), map[string]any{"text": "The count is 100."}), // round 3: answer again → finishes
	}}
	cfg := Config{MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
	store := &fakeStore{}
	r := &runner{cfg: cfg, store: store}
	rt := toolRuntime(p, wh, nil, "")
	rt.MutationTools = []MutationTool{mt}

	r.run(context.Background(), rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "count rows and save it", CallerRole: "member"})

	if saved != 0 {
		t.Fatalf("the model never re-issued the write, so nothing should have been saved (ran %d)", saved)
	}
	if store.final == nil || store.final.Answer == "" {
		t.Fatalf("turn should finish with an answer, got %+v", store.final)
	}
	if !strings.Contains(store.final.Answer, "count is 100") {
		t.Fatalf("the grounded answer should be preserved, got %q", store.final.Answer)
	}
	if !strings.Contains(store.final.Answer, pendingWriteNotice) {
		t.Fatalf("a dropped write must be disclosed even off the budget path, got %q", store.final.Answer)
	}
}

func TestLoopTools_DeferredWriteDisclosedOnDecline(t *testing.T) {
	// The disclosure must fire on ANY terminal: if the model responds to the nudge
	// with a decline (not an answer), the dropped save must still be surfaced.
	wh := testutil.NewMockWarehouseProvider("ds")
	saved := 0
	mt := MutationTool{
		Name: "save_note", Description: "Save.", InputSchema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, in MutationInput) (MutationOutput, error) { saved++; return MutationOutput{ProposalID: "p1"}, nil },
	}
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		{ // round 1: query + save batched → save deferred
			StopReason: "tool_use",
			ToolCalls: []gollm.ToolCall{
				{ID: "q1", Name: string(actQuery), Input: map[string]any{"query": "SELECT COUNT(*) c FROM ds.t"}},
				{ID: "n1", Name: "save_note", Input: map[string]any{"title": "T", "body": "B"}},
			},
			Usage: gollm.Usage{InputTokens: 10, OutputTokens: 5},
		},
		toolCall(string(actDecline), map[string]any{"reason": "I cannot answer."}), // round 2 → nudged
		toolCall(string(actDecline), map[string]any{"reason": "I cannot answer."}), // round 3 → finishes (declined)
	}}
	cfg := Config{MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
	store := &fakeStore{}
	r := &runner{cfg: cfg, store: store}
	rt := toolRuntime(p, wh, nil, "")
	rt.MutationTools = []MutationTool{mt}

	r.run(context.Background(), rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "count rows and save it", CallerRole: "member"})

	if saved != 0 {
		t.Fatalf("the model never re-issued the write; nothing should be saved (ran %d)", saved)
	}
	if store.final == nil || store.final.Status != commonmodels.AskTurnStatusDeclined {
		t.Fatalf("turn should decline, got %+v", store.final)
	}
	if !strings.Contains(store.final.Answer, pendingWriteNotice) {
		t.Fatalf("a dropped write must be disclosed even on a decline, got %q", store.final.Answer)
	}
}

func TestLoopTools_MultipleDeferredWritesTracked(t *testing.T) {
	// Two writes deferred in one batch: completing ONE must not clear the guard for
	// the other. The straggler (never re-issued) is disclosed; and when BOTH are
	// completed, no straggler notice appears.
	newProvider := func() *scriptedToolProvider {
		return &scriptedToolProvider{responses: []gollm.ChatResponse{
			{ // round 1: two saves batched → both deferred (writesPending=2)
				StopReason: "tool_use",
				ToolCalls: []gollm.ToolCall{
					{ID: "a", Name: "save_note", Input: map[string]any{"title": "A", "body": "a"}},
					{ID: "b", Name: "save_note", Input: map[string]any{"title": "B", "body": "b"}},
				},
				Usage: gollm.Usage{InputTokens: 10, OutputTokens: 5},
			},
			toolCall("save_note", map[string]any{"title": "A", "body": "a"}),  // round 2: first save alone → writesPending=1
			toolCall(string(actAnswer), map[string]any{"text": "Saved A."}),    // round 3: answer → nudged (one still pending)
			toolCall(string(actAnswer), map[string]any{"text": "Saved A."}),    // round 4: answer → discloses the straggler
		}}
	}
	run := func(p *scriptedToolProvider) *fakeStore {
		saved := 0
		mt := MutationTool{
			Name: "save_note", Description: "Save.", InputSchema: map[string]any{"type": "object"},
			Run: func(ctx context.Context, in MutationInput) (MutationOutput, error) { saved++; return MutationOutput{ProposalID: "p"}, nil },
		}
		cfg := Config{MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
		store := &fakeStore{}
		r := &runner{cfg: cfg, store: store}
		rt := toolRuntime(p, testutil.NewMockWarehouseProvider("ds"), nil, "")
		rt.MutationTools = []MutationTool{mt}
		r.run(context.Background(), rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "save two notes", CallerRole: "member"})
		return store
	}

	// One completed, one straggler → disclosed with the PARTIAL wording (a proposal
	// was created, so it must not claim "nothing was persisted").
	store := run(newProvider())
	if store.final == nil || !strings.Contains(store.final.Answer, partialWriteNotice) {
		t.Fatalf("a straggler after a partial save must use the partial notice, got %+v", store.final)
	}
	if strings.Contains(store.final.Answer, pendingWriteNotice) {
		t.Fatalf("a partial save must not claim nothing was persisted, got %q", store.final.Answer)
	}

	// Both completed → no straggler notice.
	both := &scriptedToolProvider{responses: []gollm.ChatResponse{
		{
			StopReason: "tool_use",
			ToolCalls: []gollm.ToolCall{
				{ID: "a", Name: "save_note", Input: map[string]any{"title": "A", "body": "a"}},
				{ID: "b", Name: "save_note", Input: map[string]any{"title": "B", "body": "b"}},
			},
			Usage: gollm.Usage{InputTokens: 10, OutputTokens: 5},
		},
		toolCall("save_note", map[string]any{"title": "A", "body": "a"}), // writesPending=1
		toolCall("save_note", map[string]any{"title": "B", "body": "b"}), // writesPending=0
		toolCall(string(actAnswer), map[string]any{"text": "Saved both."}),
	}}
	store2 := run(both)
	if store2.final == nil {
		t.Fatal("turn did not finalize")
	}
	if strings.Contains(store2.final.Answer, pendingWriteNotice) || strings.Contains(store2.final.Answer, partialWriteNotice) {
		t.Fatalf("no straggler should be disclosed when both writes completed, got %q", store2.final.Answer)
	}
}

func TestLoopTools_SaveThenDeclineReportsSaveNotFailure(t *testing.T) {
	// A save-only turn that created a proposal and then (oddly) declines must NOT be
	// reported as a decline — that misreports a successful pending change as a
	// failure. It finishes done, confirming the save.
	wh := testutil.NewMockWarehouseProvider("ds")
	mt := MutationTool{
		Name: "save_note", Description: "Save.", InputSchema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, in MutationInput) (MutationOutput, error) { return MutationOutput{ProposalID: "p1"}, nil },
	}
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		toolCall("save_note", map[string]any{"title": "T", "body": "B"}),
		toolCall(string(actDecline), map[string]any{"reason": "no figures to report"}),
	}}
	cfg := Config{MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
	store := &fakeStore{}
	r := &runner{cfg: cfg, store: store}
	rt := toolRuntime(p, wh, nil, "")
	rt.MutationTools = []MutationTool{mt}
	r.run(context.Background(), rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "save this as a note", CallerRole: "member"})

	if store.final == nil || store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("a save-only turn that created a proposal must not decline, got %+v", store.final)
	}
	if store.final.Answer != writeAckText {
		t.Fatalf("the turn should confirm the save, got %q", store.final.Answer)
	}
}

func TestLoopTools_SaveThenClarifyAcknowledgesSave(t *testing.T) {
	// A save-only turn that created a proposal and then asks a follow-up (clarify)
	// must still acknowledge the save, while preserving the model's question.
	wh := testutil.NewMockWarehouseProvider("ds")
	mt := MutationTool{
		Name: "save_note", Description: "Save.", InputSchema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, in MutationInput) (MutationOutput, error) { return MutationOutput{ProposalID: "p1"}, nil },
	}
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		toolCall("save_note", map[string]any{"title": "T", "body": "B"}),
		toolCall(string(actClarify), map[string]any{"question": "Anything else to save?"}),
	}}
	cfg := Config{MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
	store := &fakeStore{}
	r := &runner{cfg: cfg, store: store}
	rt := toolRuntime(p, wh, nil, "")
	rt.MutationTools = []MutationTool{mt}
	r.run(context.Background(), rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "save this as a note", CallerRole: "member"})

	if store.final == nil || store.final.Disposition != commonmodels.AskTurnDispositionClarify {
		t.Fatalf("a save-then-follow-up should remain a clarify, got %+v", store.final)
	}
	if !strings.Contains(store.final.Answer, "saved as a pending item") {
		t.Fatalf("clarify should acknowledge the save, got %q", store.final.Answer)
	}
	if !strings.Contains(store.final.Answer, "Anything else to save?") {
		t.Fatalf("clarify should preserve the model's question, got %q", store.final.Answer)
	}
}

func TestExecMutation_ArgMutationDoesNotStrandPendingWrite(t *testing.T) {
	// A plugin that normalizes/defaults its args (mutating the map in place) must
	// not leave a completed write marked pending — the key is captured before Run.
	r := &runner{cfg: Config{}, store: &fakeStore{}}
	mt := MutationTool{Name: "save_note", Run: func(ctx context.Context, in MutationInput) (MutationOutput, error) {
		in.Args["category"] = "default" // executor mutates the args map after the fact
		return MutationOutput{ProposalID: "p1"}, nil
	}}
	tc := gollm.ToolCall{ID: "1", Name: "save_note", Input: map[string]any{"title": "T", "body": "B"}}
	st := &turnState{req: TurnRequest{TurnID: "t", ProjectID: "p"}}
	st.deferWrite(tc) // deferred earlier (key from the pristine args)
	r.execMutation(context.Background(), st, mt, tc)
	if st.hasPendingWrite() {
		t.Fatal("a completed write whose args the plugin mutated must still clear its pending entry")
	}
}

func TestLoopTools_ReDeferredWriteNotDoubleCounted(t *testing.T) {
	// The model batches the SAME save with a query twice (ignoring "call it alone"),
	// then finally issues it alone. Re-deferring the same write must not inflate the
	// pending set — after the lone completion there is no straggler, so the finishing
	// answer carries NO "not saved" notice.
	wh := testutil.NewMockWarehouseProvider("ds")
	saved := 0
	mt := MutationTool{
		Name: "save_note", Description: "Save.", InputSchema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, in MutationInput) (MutationOutput, error) { saved++; return MutationOutput{ProposalID: "p1"}, nil },
	}
	batch := gollm.ChatResponse{
		StopReason: "tool_use",
		ToolCalls: []gollm.ToolCall{
			{ID: "q", Name: string(actQuery), Input: map[string]any{"query": "SELECT COUNT(*) c FROM ds.t"}},
			{ID: "n", Name: "save_note", Input: map[string]any{"title": "T", "body": "B"}},
		},
		Usage: gollm.Usage{InputTokens: 10, OutputTokens: 5},
	}
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		batch, // round 1: save deferred
		batch, // round 2: SAME save re-batched → re-deferred (idempotent, set stays size 1)
		toolCall("save_note", map[string]any{"title": "T", "body": "B"}),          // round 3: save alone → completes, clears the entry
		toolCall(string(actAnswer), map[string]any{"text": "The count is 100."}), // round 4: answer → no straggler
	}}
	cfg := Config{MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
	store := &fakeStore{}
	r := &runner{cfg: cfg, store: store}
	rt := toolRuntime(p, wh, nil, "")
	rt.MutationTools = []MutationTool{mt}
	r.run(context.Background(), rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "count rows and save it", CallerRole: "member"})

	if saved != 1 {
		t.Fatalf("the save should run exactly once, got %d", saved)
	}
	if store.final == nil || store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("turn should finish done, got %+v", store.final)
	}
	if strings.Contains(store.final.Answer, pendingWriteNotice) || strings.Contains(store.final.Answer, partialWriteNotice) {
		t.Fatalf("a completed (re-deferred) write must not leave a stale straggler notice, got %q", store.final.Answer)
	}
}

func TestFinishUngrounded_DisclosesPendingWrite(t *testing.T) {
	// An ungrounded decline (e.g. a save-only request whose batched write never
	// completed before the budget ran out) must still disclose the dropped save —
	// finishUngrounded is a terminal path too.
	r := &runner{cfg: Config{}, store: &fakeStore{}}
	st := &turnState{req: TurnRequest{TurnID: "t", SessionID: "s", ProjectID: "p"}}
	st.deferWrite(gollm.ToolCall{Name: "save_note", Input: map[string]any{"title": "T"}})
	r.finishUngrounded(context.Background(), st)

	store := r.store.(*fakeStore)
	if store.final == nil || store.final.Status != commonmodels.AskTurnStatusDeclined {
		t.Fatalf("expected a declined finalize, got %+v", store.final)
	}
	if !strings.Contains(store.final.Answer, pendingWriteNotice) {
		t.Fatalf("a pending write must be disclosed on an ungrounded decline, got %q", store.final.Answer)
	}

	// No pending write → the plain ungrounded message, no notice.
	r2 := &runner{cfg: Config{}, store: &fakeStore{}}
	st2 := &turnState{req: TurnRequest{TurnID: "t", SessionID: "s", ProjectID: "p"}}
	r2.finishUngrounded(context.Background(), st2)
	if strings.Contains(r2.store.(*fakeStore).final.Answer, pendingWriteNotice) {
		t.Fatalf("no pending write → no notice, got %q", r2.store.(*fakeStore).final.Answer)
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
	if st.writesSaved != 1 {
		t.Fatalf("a real proposal should count as a saved write, writesSaved=%d", st.writesSaved)
	}
	if st.groundedEvents != 0 {
		t.Fatal("a mutation is not evidence — it must not increment groundedEvents")
	}
	if !st.canAnswer() {
		t.Fatal("a successful mutation should let the model finish the turn")
	}
}

func TestLoopTools_NoOpMutationFinishesWithoutFalseSaveClaim(t *testing.T) {
	// A save_note that completes as a no-op (nil error, no proposal id) and no query
	// runs: the turn finishes (so the outcome is reported) but the deterministic ack
	// must NOT claim anything was saved.
	wh := testutil.NewMockWarehouseProvider("ds")
	mt := MutationTool{
		Name: "save_note", Description: "Save.", InputSchema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, in MutationInput) (MutationOutput, error) {
			return MutationOutput{Output: map[string]any{"status": "exists"}}, nil // no proposal id
		},
	}
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		toolCall("save_note", map[string]any{"title": "T", "body": "B"}),
		toolCall(string(actAnswer), map[string]any{"text": "It already existed."}),
	}}
	cfg := Config{MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
	store := &fakeStore{}
	r := &runner{cfg: cfg, store: store}
	rt := toolRuntime(p, wh, nil, "")
	rt.MutationTools = []MutationTool{mt}

	r.run(context.Background(), rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "save this", CallerRole: "member"})

	if store.final == nil || store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("a completed no-op mutation should finish the turn, got %+v", store.final)
	}
	if store.final.Answer != noWriteAckText {
		t.Fatalf("ungrounded no-op finish must use the no-save ack, got %q", store.final.Answer)
	}
	if strings.Contains(store.final.Answer, "saved") {
		t.Fatalf("a no-op finish must not claim a save, got %q", store.final.Answer)
	}
}

func TestLoopTools_PendingWriteDisclosedAtBudget(t *testing.T) {
	// A write batched with a query in the ONLY allowed round is deferred; the query
	// grounds the turn, the budget is exhausted, and final synthesis answers. The
	// requested save was never created, so the answer must disclose that rather than
	// silently drop it.
	wh := testutil.NewMockWarehouseProvider("ds")
	saved := 0
	mt := MutationTool{
		Name: "save_note", Description: "Save.", InputSchema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, in MutationInput) (MutationOutput, error) { saved++; return MutationOutput{ProposalID: "p1"}, nil },
	}
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		{ // round 1 (the only round): query + save batched → query runs, save deferred
			StopReason: "tool_use",
			ToolCalls: []gollm.ToolCall{
				{ID: "q1", Name: string(actQuery), Input: map[string]any{"query": "SELECT COUNT(*) c FROM ds.t"}},
				{ID: "n1", Name: "save_note", Input: map[string]any{"title": "T", "body": "B"}},
			},
			Usage: gollm.Usage{InputTokens: 10, OutputTokens: 5},
		},
		toolCall(string(actAnswer), map[string]any{"text": "The count is 100."}), // final synthesis
	}}
	cfg := Config{MaxRounds: 1, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50}
	store := &fakeStore{}
	r := &runner{cfg: cfg, store: store}
	rt := toolRuntime(p, wh, nil, "")
	rt.MutationTools = []MutationTool{mt}

	r.run(context.Background(), rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "count rows and save it", CallerRole: "member"})

	if saved != 0 {
		t.Fatalf("the deferred save had no later step to run, should not have executed (ran %d)", saved)
	}
	if store.final == nil || store.final.Answer == "" {
		t.Fatalf("turn should finish with an answer, got %+v", store.final)
	}
	if !strings.Contains(store.final.Answer, "count is 100") {
		t.Fatalf("the grounded answer should be preserved, got %q", store.final.Answer)
	}
	if !strings.Contains(store.final.Answer, pendingWriteNotice) {
		t.Fatalf("the dropped write must be disclosed, got %q", store.final.Answer)
	}
}

func TestExecMutation_NoProposalCompletesButIsNotSaved(t *testing.T) {
	// A mutation that returns nil error but no proposal id (no-op / already-exists)
	// still RAN: it lets the turn finish to report the outcome (mutationsDone) and
	// clears the outstanding-write guard, but it does NOT count as a saved write
	// (writesSaved stays 0) so an ungrounded finish never falsely claims a save.
	r := &runner{cfg: Config{}, store: &fakeStore{}}
	mt := MutationTool{Name: "save_note", Run: func(ctx context.Context, in MutationInput) (MutationOutput, error) {
		return MutationOutput{Output: map[string]any{"status": "exists"}}, nil // empty ProposalID
	}}
	tc := gollm.ToolCall{ID: "1", Name: "save_note", Input: map[string]any{}}
	st := &turnState{req: TurnRequest{TurnID: "t", ProjectID: "p"}}
	st.deferWrite(tc) // this write was deferred earlier
	obs := r.execMutation(context.Background(), st, mt, tc)

	if st.mutationsDone != 1 || !st.canAnswer() {
		t.Fatalf("a completed no-op mutation should let the turn finish (done=%d canAnswer=%v)", st.mutationsDone, st.canAnswer())
	}
	if st.writesSaved != 0 {
		t.Fatalf("a no-proposal mutation must NOT count as a saved write, writesSaved=%d", st.writesSaved)
	}
	if st.hasPendingWrite() {
		t.Fatal("a completed write must retire its outstanding-write entry, even a no-op")
	}
	if st.groundedEvents != 0 {
		t.Fatal("a mutation is not evidence — it must not ground the turn")
	}
	if !strings.Contains(obs, "no pending change") {
		t.Fatalf("observation should report no pending change, got %q", obs)
	}
	if !strings.Contains(obs, "exists") {
		t.Fatalf("the tool output should be surfaced to the model, got %q", obs)
	}
	if len(st.events) != 1 || st.events[0].ProposalID != "" {
		t.Fatalf("event should be recorded with an empty proposal id, got %+v", st.events)
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
	// The grounding rule must carve out an exception for confirming a write, so a
	// save-only request isn't pushed into irrelevant SQL or a decline.
	if !strings.Contains(out, "you do NOT need a data-evidence call to confirm a write") {
		t.Fatalf("grounding exception for write confirmations missing:\n%s", out)
	}

	// mutationsAvailable=false (e.g. a viewer) → no capability line, tool undescribed.
	out2 := buildSystemPromptForTools(rt, routing, Config{}, false, false, nil)
	if strings.Contains(out2, "NOT read-only") || strings.Contains(out2, "save_note") {
		t.Fatalf("a viewer (mutations unavailable) should not see the write tool:\n%s", out2)
	}

	// A reserved (built-in-shadowing) mutation name is dropped from the offered set
	// by mutationDefs, so it must NOT be described in the prompt either — only the
	// real write tool is listed.
	mixed := &ProjectRuntime{MutationTools: []MutationTool{
		{Name: "save_note", Description: "Save an operator note."},
		{Name: "query_data", Description: "SHADOW built-in."},
	}}
	out3 := buildSystemPromptForTools(mixed, routing, Config{}, false, true, nil)
	if !strings.Contains(out3, "save_note") {
		t.Fatalf("the real write tool should still be described:\n%s", out3)
	}
	if strings.Contains(out3, "SHADOW built-in.") {
		t.Fatalf("a reserved-named mutation tool must not be described in the prompt:\n%s", out3)
	}
}
