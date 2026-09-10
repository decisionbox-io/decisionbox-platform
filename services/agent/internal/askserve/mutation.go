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

// writeKey identifies a write tool call by its name + canonical arguments so a
// re-issued write matches the entry recorded when it was deferred. Go marshals
// map keys in sorted order, so the encoding is deterministic for the same args.
func writeKey(tc gollm.ToolCall) string {
	raw, _ := json.Marshal(tc.Input)
	return tc.Name + "\x00" + string(raw)
}

// deferWrite records a write that was refused because it was batched with other
// calls. It is a SET keyed by writeKey (not a counter) so a model that re-batches
// the SAME write several times before finally issuing it alone doesn't inflate
// the pending state, while a genuinely-distinct unfinished write is still tracked.
func (st *turnState) deferWrite(tc gollm.ToolCall) {
	if st.pendingWrites == nil {
		st.pendingWrites = make(map[string]struct{})
	}
	st.pendingWrites[writeKey(tc)] = struct{}{}
}

// completeWrite retires the pending entry for a write that ran to completion,
// identified by a key captured from its ORIGINAL args (before the plugin could
// mutate them). A key that isn't pending (a write issued alone the first time) is
// a no-op, so it never spuriously clears an unrelated deferred write.
func (st *turnState) completeWrite(key string) {
	delete(st.pendingWrites, key)
}

// hasPendingWrite reports whether any requested write was deferred and not since
// completed — gates the outstanding-write nudge and the terminal disclosure.
func (st *turnState) hasPendingWrite() bool { return len(st.pendingWrites) > 0 }

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
	// Capture the pending-write key from the ORIGINAL args before Run — the plugin
	// executor receives the args map by reference and may normalize/default it,
	// which would change the key and leave a completed write falsely marked pending.
	key := writeKey(tc)
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
	st.completeWrite(key)

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
