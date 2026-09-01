package askserve

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/charts"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
)

// maxParseRetries bounds how many times the loop re-prompts the model within a
// single step when its response can't be parsed into an action — mirrors the
// exploration engine's behaviour so reasoning models that wander get nudged
// back to the JSON contract instead of stalling the turn.
const maxParseRetries = 3

// llmMaxOutputTokens caps the model's output per reasoning step. Matches the
// exploration loop's budget; it bounds one assistant message, not the turn.
const llmMaxOutputTokens = 4096

// turnPersister is the slice of the turn store the loop needs: append a tool
// event as it completes and write the terminal outcome. An interface so the
// loop is unit-testable without Mongo.
type turnPersister interface {
	AppendEvent(ctx context.Context, turnID, projectID string, ev commonmodels.ToolEvent) error
	Finalize(ctx context.Context, fin TurnFinal) error
}

// runner executes a single Q&A turn end-to-end against a built runtime and
// persists its progress + outcome through the turn store.
type runner struct {
	cfg   Config
	store turnPersister
}

// turnState is the mutable bookkeeping for one turn.
type turnState struct {
	req             TurnRequest
	client          *ai.Client
	model           string
	events          []commonmodels.ToolEvent
	round           int
	queriesUsed     int
	tokensIn        int
	tokensOut       int
	groundingNudges int
	// routing is the per-turn datasource plan (which datasources the model may
	// target, whether the turn is pinned to one). Computed once at turn start.
	routing turnRouting
	// touched records the datasource ids the turn queried, in first-seen order,
	// for routing telemetry. Populated only in multi mode.
	touched []string
	// routeReason / routeConfidence / routeClarify are the evidence-grounded
	// router's decision for this turn (multi mode). Empty/zero when the router
	// did not run (single datasource, explicit pin, or router disabled).
	routeReason     string
	routeConfidence float64
	routeClarify    bool
	// routeCandidates / routeChosen are the ballot and the verdict: what the
	// router was offered, and what it picked. Recorded separately from
	// st.touched (what the turn actually queried) because only the ballot can
	// tell a datasource that lost from one that was never in the running.
	routeCandidates []string
	routeChosen     []string
	// groundedEvents counts evidence tool events that SUCCEEDED (no error). The
	// native-tools loop grounds on this, not on len(events): a failed query /
	// rejected tenant filter / unavailable schema search records an event but
	// observed no data, so it must not unlock the answer tool. render_chart is
	// deliberately NOT evidence — a chart consumes a query result, it does not
	// produce one — so a successful render_chart never increments this (see emit).
	groundedEvents int
	// insightHits accumulates the insights/recommendations surfaced by
	// search_insights across the turn, mapped to the message's Sources at
	// finalize so the dashboard renders them as citations.
	insightHits []ai.InsightHit

	// queryStepSeq is a monotonic counter assigning each successful query a
	// unique step id ("q1", "q2", …) the model references as a chart's
	// source_step_id. Round is not unique (native mode batches queries), so a
	// separate sequence is needed.
	queryStepSeq int
	// queriesChartable counts successful, non-truncated queries — a chart needs
	// at least one to ground against, and only these can be its source.
	queriesChartable int
	// chartsRendered counts accepted render_chart events (for the per-answer cap).
	chartsRendered int
	// querySummariesByID maps a step id to the query summary the chart validator
	// grounds against, for O(1) lookup of a chart's referenced source.
	querySummariesByID map[string]QuerySummary
	// chartsEnabled is the per-turn entitlement (caller EnableCharts AND the ops
	// kill-switch). It gates EXECUTION, not just tool offering: the text-fallback
	// parser accepts render_chart regardless, and a provider can return an
	// unoffered tool call, so execRenderChart must recheck this.
	chartsEnabled bool
}

// maxGroundingNudges bounds how many times the loop re-prompts a model that
// tries to answer with no tool activity. After this many refusals the turn is
// DECLINED rather than emitting an ungrounded (fabricated) answer — accepting
// such an answer is the worst failure mode for a data tool.
const maxGroundingNudges = 2

// groundingNudge is the correction when the model tries to answer without
// having run any query — it forbids ungrounded answers and tells it how to
// start gathering evidence even with no schema catalog in hand.
const groundingNudge = "Do NOT answer yet — you have run no query, so you have no data to ground an answer in. " +
	"Run a query_data action first to gather evidence; never state a table, count, total, or value you have not seen in a query result this turn. " +
	"If you don't know the tables or columns, discover them with a query against INFORMATION_SCHEMA (e.g. `SELECT table_name FROM <dataset>.INFORMATION_SCHEMA.TABLES`) or use search_tables / lookup_schema. " +
	"Only use clarify or decline if the question genuinely cannot be turned into any query."

// turnRouting is the per-turn datasource plan: which datasources the model may
// target and whether the turn is pinned to exactly one. Computed once at turn
// start from the request's explicit datasource_id and the project's
// datasources, so every query in the turn routes against a stable set.
type turnRouting struct {
	// datasources are the datasources visible to this turn (all of the
	// project's, or just the pinned one), primary first.
	datasources []DatasourceInfo
	// pinned, when non-empty, forces every query onto that datasource and hides
	// the datasource_id choice from the model. Set for an explicit user
	// override AND for a single-datasource project (where there is no choice).
	pinned string
	// primary is the datasource a query targets when it names none.
	primary string
	// multi reports whether the model chooses datasources per query (more than
	// one visible, not pinned). When false the turn behaves exactly like the
	// single-warehouse path — same prompt, same tools, same telemetry.
	multi bool
	// routed reports that the evidence-grounded router made a real (non-clarify)
	// decision for this turn. It stays true even when the router confidently
	// pinned a single datasource (multi=false), so that datasource is still
	// recorded in routing telemetry — a router-pinned turn is a real routing
	// decision, not the single-warehouse path.
	routed bool
}

// resolveTurnRouting computes the turn's datasource plan. An explicit,
// non-empty datasource id must name a real datasource (else the turn fails
// fast — the caller sent a bad override); empty defers to the model on a
// multi-datasource project, or pins the sole datasource on a single-datasource
// one.
func (rt *ProjectRuntime) resolveTurnRouting(explicit string) (turnRouting, error) {
	if len(rt.Datasources) == 0 {
		return turnRouting{}, fmt.Errorf("project has no configured datasource")
	}
	primary := rt.PrimaryID
	if primary == "" {
		primary = rt.Datasources[0].ID
	}
	if explicit != "" {
		d, ok := rt.datasource(explicit)
		if !ok {
			return turnRouting{}, fmt.Errorf("unknown datasource %q", explicit)
		}
		return turnRouting{datasources: []DatasourceInfo{d}, pinned: explicit, primary: explicit}, nil
	}
	if len(rt.Datasources) == 1 {
		only := rt.Datasources[0].ID
		return turnRouting{datasources: rt.Datasources, pinned: only, primary: only}, nil
	}
	return turnRouting{datasources: rt.Datasources, primary: primary, multi: true}, nil
}

// trackDatasource records a queried datasource for routing telemetry (deduped,
// first-seen order). Active in multi mode and for router-decided turns (which
// includes a confident single-datasource pin), so the routed datasource is
// captured; a no-op on the plain single-warehouse path.
func (st *turnState) trackDatasource(id string) {
	if id == "" || (!st.routing.multi && !st.routing.routed) {
		return
	}
	for _, x := range st.touched {
		if x == id {
			return
		}
	}
	st.touched = append(st.touched, id)
}

// routedDatasources returns the datasource ids the turn queried. It is nil on
// the plain single-warehouse path (no router decision, not multi), keeping
// those turns' persisted records byte-identical to before; for a multi turn or
// a router-decided turn (including a confident pin) it reports what was queried.
func (st *turnState) routedDatasources() []string {
	if !st.routing.multi && !st.routing.routed {
		return nil
	}
	return st.touched
}

// run drives one turn to a finalized outcome. It dispatches on whether the
// project's LLM provider supports native tool-calling: tool-wired providers
// (bedrock / claude / openai) take the native-tools loop, where the model is
// forced to call an evidence tool before it can answer; everyone else takes the
// JSON-in-text loop, which coaxes the same vocabulary through prose + nudges.
func (r *runner) run(ctx context.Context, rt *ProjectRuntime, req TurnRequest) {
	routing, err := rt.resolveTurnRouting(req.DatasourceID)
	if err != nil {
		st := &turnState{req: req, model: rt.Model}
		r.finishFailed(ctx, st, fmt.Sprintf("invalid datasource selection: %v", err))
		return
	}
	st := &turnState{req: req, client: rt.AIClient, model: rt.Model, routing: routing}
	// Per-turn charting entitlement: the caller's EnableCharts AND the ops
	// kill-switch. Set here, once, so both loops and the router path agree.
	st.chartsEnabled = req.EnableCharts && r.cfg.ChartsEnabled

	// Evidence-grounded routing: on a multi-datasource turn without an explicit
	// pin, decide which datasource(s) the question needs (and clarify when it's
	// genuinely ambiguous) before the answering loop runs. A single-datasource
	// project, an explicit pin, or a disabled router skips this entirely.
	if routing.multi && r.cfg.RouterEnabled {
		if r.route(ctx, rt, st) {
			return // the router asked a clarifying question and finalized the turn
		}
	}

	if toolsSupported(rt) {
		r.runWithTools(ctx, rt, st)
		return
	}
	r.runText(ctx, rt, st)
}

// toolsSupported reports whether the runtime's LLM provider honours native tool
// definitions. The provider name is set on the AI client at build time
// (SetProvenance with project.LLM.Provider). When it's unknown or tools are
// unsupported, the caller falls back to the JSON-text loop.
func toolsSupported(rt *ProjectRuntime) bool {
	if rt == nil || rt.AIClient == nil {
		return false
	}
	meta, ok := gollm.GetProviderMeta(rt.AIClient.ProviderName())
	return ok && meta.SupportsTools
}

// runText drives the bounded ReAct loop on the JSON-in-text path. It always
// finalizes the turn (done/declined/timeout/failed) so the reader's poll
// terminates, preserving whatever transcript was gathered. This is the fallback
// for providers without native tool-calling; its grounding guard (re-nudge then
// decline rather than emit an ungrounded answer) is the safety net there.
func (r *runner) runText(ctx context.Context, rt *ProjectRuntime, st *turnState) {
	conv := ai.NewConversation(ai.ConversationOptions{
		SystemPrompt: buildSystemPrompt(rt, st.routing, r.cfg, st.chartsEnabled),
		// Two messages per step (assistant action + observation) across the
		// round budget, plus history headroom — generous so the engine's own
		// caps, not this ceiling, bound the turn.
		MaxMessages: (r.cfg.MaxRounds + len(st.req.History) + 4) * 2,
	})
	for _, m := range trimHistory(st.req.History, r.cfg.HistoryCharBudget) {
		_ = conv.AddMessage(m.Role, m.Content)
	}
	conv.AddUserMessage(st.req.Question)

	for st.round = 1; st.round <= r.cfg.MaxRounds; st.round++ {
		if err := ctx.Err(); err != nil {
			r.finishTimeout(ctx, st)
			return
		}

		act, err := r.decide(ctx, conv, st)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				r.finishTimeout(ctx, st)
				return
			}
			r.finishFailed(ctx, st, fmt.Sprintf("could not parse a valid action from the model: %v", err))
			return
		}

		if act.Kind.terminal() {
			// Grounding guard: an `answer` produced with no SUCCESSFUL tool
			// activity (no query / lookup / search that returned data) is
			// ungrounded — the model fabricated it. Re-nudge it to gather
			// evidence; if it still refuses after maxGroundingNudges, DECLINE
			// rather than emit fabricated content. Gate on groundedEvents, not
			// len(events): a failed query, a rejected datasource_id or a
			// rejected render_chart records an event but observed no data, so it
			// must NOT unlock the answer (same rule the native-tools path uses).
			// clarify / decline need no data.
			if act.Kind == actAnswer && st.groundedEvents == 0 {
				if st.groundingNudges < maxGroundingNudges {
					st.groundingNudges++
					conv.AddUserMessage(groundingNudge)
					continue
				}
				r.finishUngrounded(ctx, st)
				return
			}
			r.finishTerminal(ctx, st, act)
			return
		}

		obs := r.execute(ctx, rt, st, act)
		if ctx.Err() != nil {
			r.finishTimeout(ctx, st)
			return
		}
		conv.AddUserMessage(obs)
	}

	// Round budget exhausted without a terminal action. Give the model one
	// final, non-tool synthesis chance to answer with what it has gathered.
	if act := r.synthesizeFinal(ctx, conv, st); act != nil {
		// Still enforce grounding: a synthesized answer with no SUCCESSFUL tool
		// activity is fabricated — decline instead of emitting it.
		if act.Kind == actAnswer && st.groundedEvents == 0 {
			r.finishUngrounded(ctx, st)
			return
		}
		r.finishTerminal(ctx, st, act)
		return
	}
	if ctx.Err() != nil {
		r.finishTimeout(ctx, st)
		return
	}
	r.finishFailed(ctx, st, fmt.Sprintf("reached the step budget (%d) without producing an answer", r.cfg.MaxRounds))
}

// decide calls the model and parses one action, re-prompting on unparseable
// responses up to maxParseRetries.
func (r *runner) decide(ctx context.Context, conv *ai.Conversation, st *turnState) (*turnAction, error) {
	var lastErr error
	for attempt := 0; attempt <= maxParseRetries; attempt++ {
		resp, err := st.callModel(ctx, conv, r)
		if err != nil {
			return nil, err
		}
		conv.AddAssistantMessage(resp)
		act, perr := parseTurnAction(resp)
		if perr == nil {
			return act, nil
		}
		lastErr = perr
		if attempt == maxParseRetries {
			break
		}
		conv.AddUserMessage("Your previous response could not be parsed. Reply with EXACTLY ONE JSON object using one of the documented actions (query / lookup_schema / search_tables / answer / clarify / decline) and no other text.")
	}
	return nil, lastErr
}

// synthesizeFinal makes one last call instructing the model to answer now.
// Returns a terminal action, or nil if the model still won't answer.
func (r *runner) synthesizeFinal(ctx context.Context, conv *ai.Conversation, st *turnState) *turnAction {
	if ctx.Err() != nil {
		return nil
	}
	conv.AddUserMessage("You have reached the step budget. Answer the original question now using only the results already gathered in this conversation. Respond with a single JSON object: {\"answer\":\"...\"} — or {\"decline\":\"...\"} if the gathered data is insufficient.")
	resp, err := st.callModel(ctx, conv, r)
	if err != nil {
		return nil
	}
	conv.AddAssistantMessage(resp)
	act, perr := parseTurnAction(resp)
	if perr != nil || !act.Kind.terminal() {
		return nil
	}
	return act
}

// callModel issues one LLM call and accumulates token usage onto the state.
func (st *turnState) callModel(ctx context.Context, conv *ai.Conversation, r *runner) (string, error) {
	resp, err := st.client.CreateMessage(ctx, conv.GetMessages(), conv.GetSystemPrompt(), llmMaxOutputTokens)
	if err != nil {
		return "", err
	}
	st.tokensIn += resp.Usage.InputTokens
	st.tokensOut += resp.Usage.OutputTokens
	return resp.Content, nil
}

// runWithTools drives the bounded loop using native tool-calling. The model is
// given the Q&A tools and forced (ToolChoice="any") to call one until the turn
// is grounded — the `answer` tool is withheld until at least one query/lookup/
// search has produced a result, so the model literally cannot finish with an
// ungrounded answer. Once grounded it may keep querying or answer (ToolChoice=
// "auto"). It always finalizes the turn. Conversation state is held as a raw
// []gollm.Message because ai.Conversation cannot carry tool_use / tool_result
// blocks.
func (r *runner) runWithTools(ctx context.Context, rt *ProjectRuntime, st *turnState) {
	system := buildSystemPromptForTools(rt, st.routing, r.cfg, st.chartsEnabled)
	hasSchema := rt.Schema != nil
	hasInsights := rt.InsightsProvider != nil

	var messages []gollm.Message
	for _, m := range trimHistory(st.req.History, r.cfg.HistoryCharBudget) {
		messages = append(messages, gollm.Message{Role: m.Role, Content: m.Content})
	}
	messages = append(messages, gollm.Message{Role: "user", Content: st.req.Question})

	for st.round = 1; st.round <= r.cfg.MaxRounds; st.round++ {
		if err := ctx.Err(); err != nil {
			r.finishTimeout(ctx, st)
			return
		}

		grounded := st.groundedEvents > 0
		resp, err := st.callModelTools(ctx, messages, system, toolsForPhase(grounded, hasSchema, hasInsights, st.routing.multi, st.chartsEnabled, st.queriesChartable > 0), toolChoiceForPhase(grounded))
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				r.finishTimeout(ctx, st)
				return
			}
			// The first tool-enabled call failed before any work was done.
			// SupportsTools is provider-wide, so a specific model can still
			// reject tools (e.g. an OpenAI reasoning model under the tool-capable
			// openai provider, or a backend that 400s on tools). Fall back to the
			// JSON-text loop rather than failing the turn.
			if st.round == 1 {
				// Carry any tokens already spent on the (ignored) native call so
				// the turn's usage accounting stays accurate.
				r.runText(ctx, rt, st)
				return
			}
			r.finishFailed(ctx, st, fmt.Sprintf("model call failed: %v", err))
			return
		}

		// Record the assistant turn (text + any tool_use blocks) so the next
		// request carries the tool_use the provider correlates tool_result to.
		messages = append(messages, gollm.Message{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls})

		if len(resp.ToolCalls) == 0 {
			// No tool call. A grounded free-text reply is the answer.
			if grounded && strings.TrimSpace(resp.Content) != "" {
				r.finishTerminal(ctx, st, &turnAction{Kind: actAnswer, Text: strings.TrimSpace(resp.Content)})
				return
			}
			// Ungrounded with no tool call means ToolChoice="any" was not honoured
			// (a backend that accepts `tools` but ignores `tool_choice`). On the
			// first round (no work yet) fall back to the JSON-text loop, which
			// carries the action contract; otherwise nudge.
			if st.round == 1 {
				// Carry any tokens already spent on the (ignored) native call so
				// the turn's usage accounting stays accurate.
				r.runText(ctx, rt, st)
				return
			}
			messages = append(messages, gollm.Message{Role: "user", Content: groundingNudge})
			continue
		}

		// A LONE terminal tool call (answer/clarify/decline) ends the turn.
		// `answer` is in the tool set only once grounded, so an ungrounded answer
		// can't reach here; the guard below is defensive. A terminal call that
		// arrives ALONGSIDE data calls is NOT finalized — the answer could
		// reference a query we have not run — it is refused in the batch loop
		// below so the model re-issues it after seeing the results.
		if len(resp.ToolCalls) == 1 && actionKind(resp.ToolCalls[0].Name).terminal() {
			tc := resp.ToolCalls[0]
			act, aerr := toolCallToAction(tc)
			if aerr != nil {
				messages = append(messages, gollm.Message{Role: "user", ToolResults: []gollm.ToolResult{{CallID: tc.ID, Content: aerr.Error(), IsError: true}}})
				continue
			}
			if act.Kind == actAnswer && st.groundedEvents == 0 {
				messages = append(messages, gollm.Message{Role: "user", ToolResults: []gollm.ToolResult{{CallID: tc.ID, Content: groundingNudge, IsError: true}}})
				continue
			}
			r.finishTerminal(ctx, st, act)
			return
		}

		// Execute every call and feed all results back in one user message so the
		// provider correlates each tool_result to its tool_use by id. A malformed
		// call yields an error result without running (so it never emits a
		// spuriously grounding event); a terminal call mixed into the batch is
		// refused so the model reviews the data before finishing; a render_chart
		// mixed with the query it would chart (or with a terminal) is refused so
		// charts only ever reference a prior-round, already-observed result.
		batchHasQuery, batchHasTerminal := false, false
		for _, tc := range resp.ToolCalls {
			k := actionKind(tc.Name)
			if k == actQuery {
				batchHasQuery = true
			}
			if k.terminal() {
				batchHasTerminal = true
			}
		}
		results := make([]gollm.ToolResult, 0, len(resp.ToolCalls))
		for _, tc := range resp.ToolCalls {
			if actionKind(tc.Name).terminal() {
				results = append(results, gollm.ToolResult{CallID: tc.ID, Content: "Do not finish in the same step as a data tool. Review the query results first, then call answer/clarify/decline on its own.", IsError: true})
				continue
			}
			if actionKind(tc.Name) == actRenderChart && (batchHasQuery || batchHasTerminal) {
				results = append(results, gollm.ToolResult{CallID: tc.ID, Content: "Call render_chart in its own step, after the query result is in — not together with a query or with answer/clarify/decline.", IsError: true})
				continue
			}
			act, aerr := toolCallToAction(tc)
			if aerr != nil {
				results = append(results, gollm.ToolResult{CallID: tc.ID, Content: aerr.Error(), IsError: true})
				continue
			}
			obs := r.execute(ctx, rt, st, act)
			if ctx.Err() != nil {
				r.finishTimeout(ctx, st)
				return
			}
			results = append(results, gollm.ToolResult{CallID: tc.ID, Content: obs})
		}
		messages = append(messages, gollm.Message{Role: "user", ToolResults: results})
	}

	// Round budget exhausted. One final, forced synthesis from gathered evidence.
	if act := r.synthesizeFinalTools(ctx, messages, system, st); act != nil {
		if act.Kind == actAnswer && st.groundedEvents == 0 {
			r.finishUngrounded(ctx, st)
			return
		}
		r.finishTerminal(ctx, st, act)
		return
	}
	if ctx.Err() != nil {
		r.finishTimeout(ctx, st)
		return
	}
	r.finishFailed(ctx, st, fmt.Sprintf("reached the step budget (%d) without producing an answer", r.cfg.MaxRounds))
}

// callModelTools issues one tool-enabled LLM call and accumulates token usage.
func (st *turnState) callModelTools(ctx context.Context, messages []gollm.Message, system string, tools []gollm.ToolDefinition, choice string) (*gollm.ChatResponse, error) {
	resp, err := st.client.CreateMessageWithTools(ctx, messages, system, llmMaxOutputTokens, tools, choice)
	if err != nil {
		return nil, err
	}
	st.tokensIn += resp.Usage.InputTokens
	st.tokensOut += resp.Usage.OutputTokens
	return resp, nil
}

// synthesizeFinalTools makes one last tool-enabled call after the round budget
// is spent, offering only answer/decline and forcing a choice. Returns a
// terminal action, or nil if the model produced neither.
func (r *runner) synthesizeFinalTools(ctx context.Context, messages []gollm.Message, system string, st *turnState) *turnAction {
	if ctx.Err() != nil {
		return nil
	}
	messages = append(messages, gollm.Message{Role: "user", Content: "You have reached the step budget. Answer the original question now using only the results already gathered in this conversation, or decline if they are insufficient."})
	resp, err := st.callModelTools(ctx, messages, system, []gollm.ToolDefinition{toolAnswer(), toolDecline()}, "any")
	if err != nil {
		return nil
	}
	if term := firstTerminalCall(resp.ToolCalls); term != nil {
		if act, err := toolCallToAction(*term); err == nil {
			return act
		}
	}
	if strings.TrimSpace(resp.Content) != "" {
		return &turnAction{Kind: actAnswer, Text: strings.TrimSpace(resp.Content)}
	}
	return nil
}

// firstTerminalCall returns the first answer/clarify/decline tool call in the
// response, or nil if there is none.
func firstTerminalCall(calls []gollm.ToolCall) *gollm.ToolCall {
	for i := range calls {
		if actionKind(calls[i].Name).terminal() {
			return &calls[i]
		}
	}
	return nil
}

// execute runs a tool action and returns the observation to feed back to the
// model. It records a tool event for every action, success or failure.
func (r *runner) execute(ctx context.Context, rt *ProjectRuntime, st *turnState, act *turnAction) string {
	switch act.Kind {
	case actQuery:
		return r.execQuery(ctx, rt, st, act)
	case actLookup:
		return r.execLookup(ctx, rt, st, act)
	case actSearch:
		return r.execSearch(ctx, rt, st, act)
	case actSearchInsights:
		return r.execSearchInsights(ctx, rt, st, act)
	case actRenderChart:
		return r.execRenderChart(ctx, st, act)
	default:
		return "Unknown action."
	}
}

func (r *runner) execQuery(ctx context.Context, rt *ProjectRuntime, st *turnState, act *turnAction) string {
	if st.queriesUsed >= r.cfg.MaxQueriesPerTurn {
		return fmt.Sprintf("Query budget exhausted (%d/%d). Answer with what you have, or decline.", st.queriesUsed, r.cfg.MaxQueriesPerTurn)
	}
	st.queriesUsed++

	// Resolve which datasource this statement runs against — one warehouse per
	// statement. A pinned turn forces its datasource; otherwise the model's
	// datasource_id (or the primary when it names none) is honoured, and an
	// unknown/unconfigured id is REJECTED rather than silently falling back to
	// another datasource (running the query against the wrong data is the worst
	// failure mode here).
	dsID, derr := r.resolveQueryDatasource(rt, st, act)
	ev := commonmodels.ToolEvent{
		Round: st.round,
		Name:  string(actQuery),
		Args:  map[string]any{"sql": act.Query, "purpose": act.Purpose, "datasource_id": dsID},
	}
	if derr != nil {
		// Record what the model asked for so the transcript shows the bad id.
		ev.Args["datasource_id"] = strings.TrimSpace(act.Datasource)
		ev.Error = derr.Error()
		r.emit(ctx, st, ev)
		return fmt.Sprintf("Query rejected: %s. Target a valid datasource_id from the DATASOURCES list.", derr.Error())
	}

	// NB: we deliberately do NOT call the executor's SetStep here. The runtime
	// is shared across concurrent turns for the same project, and SetStep
	// mutates executor state that Execute reads — a data race. The per-step
	// number only stamps the executor's FixHistory, which ask-serve doesn't
	// persist (round/latency are captured on the tool event instead).
	//
	// Read-only is enforced by the warehouse's read-only credentials
	// (ValidateReadOnly at connect) + the governance middleware, and the tenant
	// scope by queryexec's filter check (re-run after any self-heal). We do NOT
	// parse/regex the SQL here: the warehouse layer is multi-provider and the
	// read-only-credential boundary is the documented contract (the prompt
	// instructs the model to write read-only, tenant-scoped SQL).

	// Per-query deadline, capped by whatever remains of the turn wall-clock
	// (ctx already carries the turn deadline, so WithTimeout takes the min).
	qctx := ctx
	var cancel context.CancelFunc
	if r.cfg.QueryTimeout > 0 {
		qctx, cancel = context.WithTimeout(ctx, r.cfg.QueryTimeout)
		defer cancel()
	}
	// Stamp the datasource on the context so a warehouse middleware (the
	// enterprise governance wrapper) can scope its per-warehouse policies to the
	// datasource this hop runs against. The turn ctx already carries the project
	// id (set in runTurn); this adds the warehouse dimension for multi-hop turns.
	qctx = gowarehouse.WithWarehouseID(qctx, dsID)

	// Acquire the datasource's warehouse connection (built lazily on first use).
	// A build/connect failure fails just this query, not the whole turn — a turn
	// that avoids a broken datasource still works.
	conn, release, cerr := rt.acquireConn(qctx, dsID)
	if cerr != nil {
		ev.Error = cerr.Error()
		r.emit(ctx, st, ev)
		return fmt.Sprintf("Query failed: could not connect to datasource %q: %s\nTry a different datasource, or answer/decline with what you have.", dsID, cerr.Error())
	}
	defer release()

	start := time.Now()
	res, err := conn.Executor.Execute(qctx, act.Query, act.Purpose)
	ev.LatencyMS = time.Since(start).Milliseconds()

	if err != nil {
		ev.Error = err.Error()
		r.emit(ctx, st, ev)
		return fmt.Sprintf("Query failed: %s\nTry a different query, or answer/decline with what you have.", err.Error())
	}
	// Record the datasource only after it returned results — RoutedDatasourceIDs
	// is the evidence provenance of the answer, so a failed exploratory attempt
	// against a datasource must not be attributed as one of its sources.
	st.trackDatasource(dsID)
	sum := summarizeResult(res, act.Purpose, r.cfg)
	// Assign the query a unique, model-visible step id so a later render_chart
	// can bind to it via source_step_id. Only successful queries get one; a
	// non-truncated one is chartable (a chart is an exact projection of the
	// preview, so a truncated result — whose preview omits rows — cannot ground).
	st.queryStepSeq++
	sum.Step = fmt.Sprintf("q%d", st.queryStepSeq)
	ev.Args["step"] = sum.Step
	ev.Output = sum
	if st.querySummariesByID == nil {
		st.querySummariesByID = make(map[string]QuerySummary)
	}
	st.querySummariesByID[sum.Step] = sum
	if !sum.Truncated {
		st.queriesChartable++
	}
	r.emit(ctx, st, ev)
	return sum.observation()
}

// execRenderChart validates and grounds a render_chart action, then persists it
// as a (non-grounding) tool event carrying the accepted ChartSpec. A rejected
// spec is persisted with its Error set and returned as an error observation so
// the model can repair it or re-query — reusing the loop's existing self-heal
// path (no separate machinery). It grounds against the referenced query's
// preview: the chart data must be an exact projection of cells already observed.
func (r *runner) execRenderChart(ctx context.Context, st *turnState, act *turnAction) string {
	// Entitlement gate — defense in depth. render_chart is only OFFERED when
	// charts are enabled for the turn, but the JSON-text parser accepts it
	// regardless and a provider could return an unoffered tool call. Never
	// execute or persist a chart for a non-entitled turn (nothing is emitted, so
	// no artifact is created).
	if !st.chartsEnabled {
		return "render_chart is not available for this turn; do not call it — answer with prose."
	}

	ev := commonmodels.ToolEvent{Round: st.round, Name: string(actRenderChart)}

	if r.cfg.ChartMaxPerAnswer > 0 && st.chartsRendered >= r.cfg.ChartMaxPerAnswer {
		ev.Error = fmt.Sprintf("chart limit reached (%d per answer)", r.cfg.ChartMaxPerAnswer)
		r.emit(ctx, st, ev)
		return ev.Error + "; do not render more — answer with the charts you have."
	}

	spec, err := charts.Decode(act.Chart, r.cfg.ChartCaps)
	if err != nil {
		ev.Error = err.Error()
		r.emit(ctx, st, ev)
		return "Chart rejected: " + err.Error() + ". Fix the spec or re-query, then try again."
	}

	src, ok := st.querySummariesByID[spec.SourceStepID]
	if !ok {
		ev.Args = map[string]any{"source_step_id": spec.SourceStepID, "type": string(spec.Type)}
		ev.Error = fmt.Sprintf("unknown source_step_id %q", spec.SourceStepID)
		r.emit(ctx, st, ev)
		return fmt.Sprintf("Chart rejected: no query step %q in this turn. Set source_step_id to the q<N> id of a query you ran (shown in its result), then try again.", spec.SourceStepID)
	}

	gsrc := charts.GroundingSource{StepID: src.Step, Columns: src.Columns, Preview: src.Preview, Truncated: src.Truncated}
	if err := charts.ValidateGrounded(spec, gsrc, r.cfg.ChartCaps); err != nil {
		ev.Args = map[string]any{"source_step_id": spec.SourceStepID, "type": string(spec.Type)}
		ev.Error = err.Error()
		r.emit(ctx, st, ev)
		// The validator is generic and cannot name the preview cap, but the model
		// needs the concrete number: told only that a step "was truncated", it
		// retries with a LIMIT set to the full row count and is truncated again.
		if src.Truncated {
			n := r.cfg.ChartableRowCap()
			return fmt.Sprintf("Chart rejected: %s. A chartable result is at most %d rows, so the re-run must return %d rows or fewer — a LIMIT above that is truncated again, exactly as this one was (step %q returned %d rows).",
				err.Error(), n, n, src.Step, src.RowCount)
		}
		return "Chart rejected: " + err.Error() + ". Fix the spec (chart only the exact cells you observed) or re-query, then try again."
	}

	ev.Args = map[string]any{"source_step_id": spec.SourceStepID, "type": string(spec.Type)}
	ev.Output = spec
	st.chartsRendered++
	r.emit(ctx, st, ev)
	return fmt.Sprintf("Chart accepted (%s from %s). Render another chart if useful, then answer.", spec.Type, spec.SourceStepID)
}

// resolveQueryDatasource picks the datasource a query_data statement runs
// against. A pinned turn always uses its datasource (ignoring any model-chosen
// id — the pin is the user's override). Otherwise the model's datasource_id is
// honoured (validated against the project's datasources); an empty id falls
// back to the primary and an unknown id is an error the model must correct.
func (r *runner) resolveQueryDatasource(rt *ProjectRuntime, st *turnState, act *turnAction) (string, error) {
	if st.routing.pinned != "" {
		return st.routing.pinned, nil
	}
	id := strings.TrimSpace(act.Datasource)
	if id == "" {
		return st.routing.primary, nil
	}
	// Validate against the whole project, not the router's narrowed set, on
	// purpose. In a multi turn search_tables spans every datasource (SearchAll)
	// so the model can discover across sources and recover when the upfront
	// router under-selected; the router is a soft prior, not a hard gate.
	// Confining query_data to the routed subset would surface tables the model
	// then can't query. Safety doesn't depend on this list: each query is
	// governed + tenant-scoped by the datasource it actually runs against, and
	// routed telemetry records what was touched. (Codex flagged this twice;
	// it's a deliberate design choice per the multi-hop cross-source
	// requirement, not an oversight.)
	if _, ok := rt.datasource(id); !ok {
		return "", fmt.Errorf("unknown datasource %q", id)
	}
	return id, nil
}

func (r *runner) execLookup(ctx context.Context, rt *ProjectRuntime, st *turnState, act *turnAction) string {
	lookupDS := r.lookupDatasource(st, act)
	ev := commonmodels.ToolEvent{
		Round: st.round,
		Name:  string(actLookup),
		Args:  map[string]any{"tables": act.LookupSchema, "datasource_id": lookupDS},
	}
	if rt.Schema == nil {
		ev.Error = "schema provider not configured"
		r.emit(ctx, st, ev)
		return "Schema lookup unavailable. Pick a table and query it directly; the SQL repair step can recover minor column mismatches."
	}
	start := time.Now()
	res, err := rt.Schema.Lookup(ctx, lookupDS, act.LookupSchema)
	ev.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		ev.Error = err.Error()
		r.emit(ctx, st, ev)
		return fmt.Sprintf("Schema lookup failed: %s", err.Error())
	}
	ev.Output = lookupSummary(res)
	r.emit(ctx, st, ev)
	return formatLookup(res)
}

// lookupDatasource picks the datasource a lookup_schema resolves against: a
// pinned turn forces its datasource; otherwise the model's datasource_id, and an
// omitted id defaults to the turn's routing primary — the same default query_data
// uses. (Passing "" would make SchemaRouter fall back to the project primary,
// which can differ from the routed primary when the router narrowed to a subset.)
func (r *runner) lookupDatasource(st *turnState, act *turnAction) string {
	if st.routing.pinned != "" {
		return st.routing.pinned
	}
	if id := strings.TrimSpace(act.Datasource); id != "" {
		return id
	}
	return st.routing.primary
}

func (r *runner) execSearch(ctx context.Context, rt *ProjectRuntime, st *turnState, act *turnAction) string {
	ev := commonmodels.ToolEvent{
		Round: st.round,
		Name:  string(actSearch),
		Args:  map[string]any{"query": act.SearchTables, "top_k": act.SearchTopK},
	}
	if rt.Schema == nil {
		ev.Error = "schema provider not configured"
		r.emit(ctx, st, ev)
		return "Table search unavailable. Use lookup_schema with a table name you already know, or query directly."
	}
	start := time.Now()
	// Multi-datasource turns search ACROSS every datasource so the model can see
	// which one holds what; a pinned / single-datasource turn searches only its
	// datasource (spanning would surface tables it can't query).
	var hits []TaggedHit
	var err error
	if st.routing.multi {
		hits, err = rt.Schema.SearchAll(ctx, act.SearchTables, act.SearchTopK)
	} else {
		hits, err = rt.Schema.SearchOne(ctx, st.routing.pinned, act.SearchTables, act.SearchTopK)
	}
	ev.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		ev.Error = err.Error()
		r.emit(ctx, st, ev)
		return fmt.Sprintf("Table search failed: %s", err.Error())
	}
	ev.Output = searchSummary(hits)
	r.emit(ctx, st, ev)
	return formatSearch(act.SearchTables, hits, st.routing.multi)
}

func (r *runner) execSearchInsights(ctx context.Context, rt *ProjectRuntime, st *turnState, act *turnAction) string {
	ev := commonmodels.ToolEvent{
		Round: st.round,
		Name:  string(actSearchInsights),
		Args:  map[string]any{"query": act.SearchInsights, "limit": act.InsightsLimit},
	}
	if rt.InsightsProvider == nil {
		ev.Error = "insights provider not configured"
		r.emit(ctx, st, ev)
		return "Insight search unavailable. Answer from a query instead, or decline."
	}
	start := time.Now()
	hits, err := rt.InsightsProvider.SearchInsights(ctx, act.SearchInsights, act.InsightsLimit)
	ev.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		ev.Error = err.Error()
		r.emit(ctx, st, ev)
		return fmt.Sprintf("Insight search failed: %s", err.Error())
	}
	ev.Output = insightsSummary(hits)
	r.emit(ctx, st, ev)
	// Accumulate for the final message's Sources (deduped at finalize) so the
	// dashboard renders these as citations.
	st.insightHits = append(st.insightHits, hits...)
	return formatInsights(act.SearchInsights, hits)
}

// emit appends a tool event to the in-memory transcript and persists it.
func (r *runner) emit(ctx context.Context, st *turnState, ev commonmodels.ToolEvent) {
	st.events = append(st.events, ev)
	if ev.Error == "" && ev.Name != string(actRenderChart) {
		// A successful EVIDENCE event — the model actually observed data. A
		// render_chart consumes a result rather than producing one, so it never
		// grounds: charting must not be a way to reach the answer tool.
		st.groundedEvents++
	}
	// Persist under a short, detached deadline so a turn ctx already past its
	// wall-clock can still durably record the event it just produced.
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := r.store.AppendEvent(pctx, st.req.TurnID, st.req.ProjectID, ev); err != nil {
		applog.WithError(err).WithField("turn_id", st.req.TurnID).Warn("ask-serve: failed to persist tool event")
	}
}

func (r *runner) finishTerminal(ctx context.Context, st *turnState, act *turnAction) {
	status := commonmodels.AskTurnStatusDone
	disposition := commonmodels.AskTurnDispositionAnswer
	switch act.Kind {
	case actClarify:
		disposition = commonmodels.AskTurnDispositionClarify
	case actDecline:
		status = commonmodels.AskTurnStatusDeclined
		disposition = commonmodels.AskTurnDispositionDecline
	}
	r.finalize(ctx, st, TurnFinal{
		Status:      status,
		Disposition: disposition,
		Answer:      act.Text,
	})
}

// finishUngrounded declines a turn whose model insisted on answering without
// running any query — emitting that answer would surface fabricated data, so
// we decline instead.
func (r *runner) finishUngrounded(ctx context.Context, st *turnState) {
	r.finalize(ctx, st, TurnFinal{
		Status:      commonmodels.AskTurnStatusDeclined,
		Disposition: commonmodels.AskTurnDispositionDecline,
		Error:       "model would not gather evidence; declined rather than answer ungrounded",
		Answer:      "I couldn't answer this from the data — I wasn't able to gather any query results to ground a response. Please rephrase the question, or check that the project's warehouse and schema are available.",
	})
}

func (r *runner) finishTimeout(ctx context.Context, st *turnState) {
	r.finalize(ctx, st, TurnFinal{
		Status: commonmodels.AskTurnStatusTimedOut,
		Error:  "the turn exceeded its time budget",
		Answer: "This question exceeded the allotted time budget before an answer could be produced. The steps gathered so far are shown above.",
	})
}

func (r *runner) finishFailed(ctx context.Context, st *turnState, reason string) {
	r.finalize(ctx, st, TurnFinal{
		Status: commonmodels.AskTurnStatusFailed,
		Error:  reason,
		Answer: "This question could not be answered. " + reason + ". The steps gathered so far are shown above.",
	})
}

// finalize writes the terminal record + session message under a detached
// deadline (the turn ctx may already be past its wall-clock).
func (r *runner) finalize(ctx context.Context, st *turnState, fin TurnFinal) {
	fin.TurnID = st.req.TurnID
	fin.SessionID = st.req.SessionID
	fin.Question = st.req.Question
	fin.Model = st.model
	fin.ToolEvents = st.events
	fin.InputTokens = st.tokensIn
	fin.OutputTokens = st.tokensOut
	fin.Sources = st.insightSources()
	// Routing telemetry — the datasources this turn actually queried (nil on a
	// single-datasource / pinned turn) plus the evidence-grounded router's
	// decision (reason / confidence / clarify) when it ran.
	fin.RoutedDatasourceIDs = st.routedDatasources()
	fin.RoutingReason = st.routeReason
	fin.RoutingConfidence = st.routeConfidence
	fin.RoutingClarify = st.routeClarify
	fin.RoutingCandidateIDs = st.routeCandidates
	fin.RoutingChosenIDs = st.routeChosen

	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := r.store.Finalize(pctx, fin); err != nil {
		applog.WithError(err).WithField("turn_id", fin.TurnID).Error("ask-serve: failed to finalize turn")
	}
}

// insightSources maps the insights/recommendations surfaced this turn to the
// dashboard's citation shape, deduped by id and preserving first-seen (highest
// score) order. Returns nil when no insight was searched.
func (st *turnState) insightSources() []commonmodels.AskSessionSource {
	if len(st.insightHits) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(st.insightHits))
	out := make([]commonmodels.AskSessionSource, 0, len(st.insightHits))
	for _, h := range st.insightHits {
		if h.ID == "" || seen[h.ID] {
			continue
		}
		seen[h.ID] = true
		out = append(out, commonmodels.AskSessionSource{
			ID:           h.ID,
			Type:         h.Type,
			Name:         h.Name,
			Score:        h.Score,
			Severity:     h.Severity,
			AnalysisArea: h.AnalysisArea,
			Description:  h.Description,
			DiscoveryID:  h.DiscoveryID,
		})
	}
	return out
}

// trimHistory drops the oldest messages until the running character total is
// within budget, preserving chronological order of the kept tail. Keeps token
// cost bounded as a session grows.
func trimHistory(history []HistoryMessage, charBudget int) []HistoryMessage {
	if charBudget <= 0 || len(history) == 0 {
		return history
	}
	total := 0
	for _, m := range history {
		total += len(m.Content)
	}
	if total <= charBudget {
		return history
	}
	// Walk from the newest backwards, keeping messages until the budget is hit.
	kept := 0
	running := 0
	for i := len(history) - 1; i >= 0; i-- {
		running += len(history[i].Content)
		if running > charBudget {
			break
		}
		kept++
	}
	return history[len(history)-kept:]
}

// --- small lookup/search renderers (observation text + compact event output) ---

func formatLookup(res ai.LookupResult) string {
	var b strings.Builder
	if len(res.Tables) == 0 {
		b.WriteString("No schemas resolved for the requested tables.")
	}
	for i, t := range res.Tables {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Schema for %s:\n", t.Table)
		for _, c := range t.Columns {
			fmt.Fprintf(&b, "  - %s %s\n", c.Name, c.Type)
		}
	}
	if len(res.NotFound) > 0 {
		fmt.Fprintf(&b, "\nNot found: %s", strings.Join(res.NotFound, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatSearch(query string, hits []TaggedHit, multi bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Search results for %q:\n", query)
	if len(hits) == 0 {
		b.WriteString("(no matching tables)")
	}
	for i, h := range hits {
		if multi {
			// Lead with the datasource id so the model knows which datasource_id
			// to pass to query_data / lookup_schema for this table.
			tag := h.DatasourceID
			if h.DatasourceLabel != "" {
				tag = fmt.Sprintf("%s (%s)", h.DatasourceID, h.DatasourceLabel)
			}
			fmt.Fprintf(&b, "%d. [datasource: %s] %s — %s\n", i+1, tag, h.Table, h.Blurb)
		} else {
			fmt.Fprintf(&b, "%d. %s — %s\n", i+1, h.Table, h.Blurb)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// lookupSummary is the compact, JSON-marshalable record persisted as the
// lookup tool event's output (column lists, not full schemas).
func lookupSummary(res ai.LookupResult) map[string]any {
	tables := make([]map[string]any, 0, len(res.Tables))
	for _, t := range res.Tables {
		cols := make([]string, 0, len(t.Columns))
		for _, c := range t.Columns {
			cols = append(cols, c.Name)
		}
		tables = append(tables, map[string]any{"table": t.Table, "columns": cols, "row_count": t.RowCount})
	}
	out := map[string]any{"tables": tables}
	if len(res.NotFound) > 0 {
		out["not_found"] = res.NotFound
	}
	return out
}

// searchSummary is the compact, lowercase-keyed record persisted as the
// search tool event's output. Carries the owning datasource id so the stored
// transcript shows which datasource each hit belongs to.
func searchSummary(hits []TaggedHit) []map[string]any {
	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		out = append(out, map[string]any{
			"table":         h.Table,
			"blurb":         h.Blurb,
			"row_count":     h.RowCount,
			"score":         h.Score,
			"datasource_id": h.DatasourceID,
		})
	}
	return out
}

func formatInsights(query string, hits []ai.InsightHit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Insight search results for %q:\n", query)
	if len(hits) == 0 {
		b.WriteString("(no matching insights or recommendations)")
	}
	for i, h := range hits {
		typ := h.Type
		if typ == "" {
			typ = "insight"
		}
		fmt.Fprintf(&b, "%d. [%s] %s", i+1, typ, h.Name)
		if h.Severity != "" {
			fmt.Fprintf(&b, " (severity: %s)", h.Severity)
		}
		if h.Description != "" {
			fmt.Fprintf(&b, " — %s", h.Description)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// insightsSummary is the compact, lowercase-keyed record persisted as the
// search_insights tool event's output (ai.InsightHit has no json tags).
func insightsSummary(hits []ai.InsightHit) []map[string]any {
	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		out = append(out, map[string]any{
			"id":            h.ID,
			"type":          h.Type,
			"name":          h.Name,
			"description":   h.Description,
			"severity":      h.Severity,
			"analysis_area": h.AnalysisArea,
			"discovery_id":  h.DiscoveryID,
			"score":         h.Score,
		})
	}
	return out
}
