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

// deferWrite records that a write TOOL was refused because it was batched with
// other calls. Keyed by tool NAME (a set, not a counter, and not keyed on args):
// re-batching the same write is idempotent, and the model's re-issue of a deferred
// write legitimately carries different args (observed figures, normalized fields),
// so a completion of the SAME tool clears it. This trades exact per-call precision
// (a rare "two distinct saves, one dropped" corner) for correctness on the common
// re-issue-with-different-args path — no false "not saved" warning after a save.
func (st *turnState) deferWrite(tc gollm.ToolCall) {
	if st.pendingWrites == nil {
		st.pendingWrites = make(map[string]struct{})
	}
	st.pendingWrites[tc.Name] = struct{}{}
}

// completeWrite retires the pending entry for a write tool that ran to completion,
// by tool name. A name that isn't pending (a write issued alone the first time) is
// a no-op, so it never spuriously clears an unrelated deferred write.
func (st *turnState) completeWrite(name string) {
	delete(st.pendingWrites, name)
}

// hasPendingWrite reports whether any requested write was deferred and not since
// completed — gates the outstanding-write nudge and the terminal disclosure.
func (st *turnState) hasPendingWrite() bool { return len(st.pendingWrites) > 0 }

// cloneArgs returns a shallow copy of a tool-call args map, so the persisted tool
// event keeps the model's pristine input even if a plugin executor normalizes or
// defaults the map it receives in place.
func cloneArgs(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// reservedToolName reports whether name collides with a built-in read-only or
// terminal tool. A registered mutation tool that shadows one is dropped — never
// offered (mutationDefs) and never dispatched (ProjectRuntime.mutationTool) — so
// a misnamed enterprise tool can't intercept a core query/search/answer call or
// send a duplicate tool definition to the provider.
func reservedToolName(name string) bool {
	switch actionKind(name) {
	case actQuery, actLookup, actSearch, actSearchInsights, actSearchKnowledge, actRenderChart, actAnswer, actClarify, actDecline:
		return true
	}
	return false
}

// mutationDefs projects the runtime's mutation tools to the LLM tool wire shape,
// skipping any that shadow a built-in tool name.
func mutationDefs(tools []MutationTool) []gollm.ToolDefinition {
	out := make([]gollm.ToolDefinition, 0, len(tools))
	for _, t := range tools {
		if reservedToolName(t.Name) {
			continue
		}
		out = append(out, gollm.ToolDefinition{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out
}

// execMutation runs one mutation tool call and records a (non-grounding) tool
// event carrying any proposal id it produced. A failure is surfaced to the model
// as a tool error so it can retry or explain, never crashing the turn.
func (r *runner) execMutation(ctx context.Context, st *turnState, mt MutationTool, tc gollm.ToolCall) string {
	// Hand the executor its OWN copy of the args: a plugin that normalizes/defaults
	// them in place must not mutate the model's input, so the persisted event keeps
	// the pristine args the model emitted (replay/audit) and tc.Input is untouched.
	ev := commonmodels.ToolEvent{Round: st.round, Name: mt.Name, Args: tc.Input}
	start := time.Now()
	out, err := mt.Run(ctx, MutationInput{
		ProjectID: st.req.ProjectID,
		SessionID: st.req.SessionID,
		TurnID:    st.req.TurnID,
		CallerSub: st.req.CallerSub,
		Args:      cloneArgs(tc.Input),
	})
	ev.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		ev.Error = err.Error()
		r.emitTool(ctx, st, ev, false)
		return fmt.Sprintf("%s failed: %s", mt.Name, err.Error())
	}
	ev.ProposalID = out.ProposalID
	ev.Output = out.Output
	// A mutation is not evidence — it must not unlock a grounded DATA answer — so
	// it is always emitted as non-grounding.
	r.emitTool(ctx, st, ev, false)

	// The write tool ran to completion (nil error): the user's requested write has
	// been serviced, so retire its matching outstanding-write entry (by key — so
	// re-issuing a specific deferred write clears exactly that one, and completing
	// an un-deferred write clears nothing) and let the model finish the turn to
	// report the outcome (mutationsDone gates canAnswer). This holds whether or not
	// a proposal came back — a no-op / already-exists is still a completed outcome
	// the user should hear about.
	st.mutationsDone++
	st.completeWrite(mt.Name)

	// Feed the tool's own output back so a mutation that reports details (an
	// "already exists", a validation note, the created id) is visible to the
	// model, per the askmutation Result contract.
	suffix := ""
	if out.Output != nil {
		if raw, err := json.Marshal(out.Output); err == nil {
			suffix = " Result: " + string(raw)
		}
	}
	if out.ProposalID == "" {
		// Nil error but no proposal id (a no-op / already-exists / validation-only
		// outcome — ProposalID is optional). Report the outcome, but do NOT claim a
		// pending change was created (writesSaved stays flat → an ungrounded finish
		// acknowledges the no-op without a false "saved").
		return fmt.Sprintf("%s completed but created no pending change; report the outcome to the user based on the result.", mt.Name) + suffix
	}
	// A real proposal was created: acknowledge the save.
	st.writesSaved++
	return fmt.Sprintf("%s succeeded — it created a pending change (id %s) the user can review and apply; tell the user it was saved and awaits their approval.", mt.Name, out.ProposalID) + suffix
}
