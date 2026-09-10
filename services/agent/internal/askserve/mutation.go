package askserve

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

// MutationTool is a write action the ask loop may offer, supplied by the
// agentserver wiring from the go-common askmutation registry (enterprise fills
// it; a community build registers none, so none are offered — the read-only
// behaviour is unchanged). The loop treats it as an opaque named tool: it
// forwards the model's arguments and records the returned proposal id on the
// transcript. A mutation is NOT evidence — it never grounds a data answer — but
// a successful one lets the model finish the turn (e.g. to confirm "saved").
type MutationTool struct {
	Name        string
	Description string
	InputSchema map[string]any
	// Run performs the mutation. The agentserver binds any storage handle it
	// needs into the closure, so the loop stays storage-agnostic.
	Run func(ctx context.Context, in MutationInput) (MutationOutput, error)
}

// MutationInput is the per-call context handed to a mutation tool.
type MutationInput struct {
	ProjectID string
	SessionID string
	TurnID    string
	CallerSub string
	Args      map[string]any
}

// MutationOutput is a mutation tool's result: an optional approvable proposal id
// (stamped on the ToolEvent) and the payload fed back to the model.
type MutationOutput struct {
	ProposalID string
	Output     any
}

// mutationDefs projects the runtime's mutation tools to the LLM tool wire shape.
func mutationDefs(tools []MutationTool) []gollm.ToolDefinition {
	out := make([]gollm.ToolDefinition, 0, len(tools))
	for _, t := range tools {
		out = append(out, gollm.ToolDefinition{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out
}

// execMutation runs one mutation tool call and records a (non-grounding) tool
// event carrying any proposal id it produced. A failure is surfaced to the model
// as a tool error so it can retry or explain, never crashing the turn.
func (r *runner) execMutation(ctx context.Context, st *turnState, mt MutationTool, tc gollm.ToolCall) string {
	ev := commonmodels.ToolEvent{Round: st.round, Name: mt.Name, Args: tc.Input}
	start := time.Now()
	out, err := mt.Run(ctx, MutationInput{
		ProjectID: st.req.ProjectID,
		SessionID: st.req.SessionID,
		TurnID:    st.req.TurnID,
		CallerSub: st.req.CallerSub,
		Args:      tc.Input,
	})
	ev.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		ev.Error = err.Error()
		r.emitTool(ctx, st, ev, false)
		return fmt.Sprintf("%s failed: %s", mt.Name, err.Error())
	}
	ev.ProposalID = out.ProposalID
	ev.Output = out.Output
	st.mutationsDone++
	// A mutation is not evidence — it must not unlock a grounded DATA answer — so
	// it is emitted as non-grounding. mutationsDone (tracked above) separately
	// lets the model finish the turn to confirm the save.
	r.emitTool(ctx, st, ev, false)
	obs := fmt.Sprintf("%s succeeded. It created a pending change the user can review and apply; tell the user it was saved and awaits their approval.", mt.Name)
	// Feed the tool's own output back so a mutation that reports details (an
	// "already exists", a validation note, the created id) is visible to the
	// model for its confirmation, per the askmutation Result contract.
	if out.Output != nil {
		if raw, err := json.Marshal(out.Output); err == nil {
			obs += " Result: " + string(raw)
		}
	}
	return obs
}
