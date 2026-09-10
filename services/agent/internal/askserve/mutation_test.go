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

	r.run(context.Background(), rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "save this as a note", CallerSub: "user-9"})

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
	out := buildSystemPromptForTools(rt, routing, Config{}, false, nil)
	if !strings.Contains(out, "save_note") || !strings.Contains(out, "Save an operator note.") {
		t.Fatalf("mutation tool not described in prompt:\n%s", out)
	}
	if !strings.Contains(out, "NOT read-only") {
		t.Fatalf("capability line missing when a mutation tool is present:\n%s", out)
	}

	// No mutation tools → no capability line (still read-only).
	out2 := buildSystemPromptForTools(&ProjectRuntime{}, routing, Config{}, false, nil)
	if strings.Contains(out2, "NOT read-only") {
		t.Fatalf("no mutation tool should leave the read-only framing intact:\n%s", out2)
	}
}
