package askserve

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
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
	// groundedEvents counts evidence tool events that SUCCEEDED (no error). The
	// native-tools loop grounds on this, not on len(events): a failed query /
	// rejected tenant filter / unavailable schema search records an event but
	// observed no data, so it must not unlock the answer tool.
	groundedEvents int
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

// run drives one turn to a finalized outcome. It dispatches on whether the
// project's LLM provider supports native tool-calling: tool-wired providers
// (bedrock / claude / openai) take the native-tools loop, where the model is
// forced to call an evidence tool before it can answer; everyone else takes the
// JSON-in-text loop, which coaxes the same vocabulary through prose + nudges.
func (r *runner) run(ctx context.Context, rt *ProjectRuntime, req TurnRequest) {
	if toolsSupported(rt) {
		r.runWithTools(ctx, rt, req)
		return
	}
	r.runText(ctx, rt, req)
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
func (r *runner) runText(ctx context.Context, rt *ProjectRuntime, req TurnRequest) {
	st := &turnState{req: req, client: rt.AIClient, model: rt.Model}

	conv := ai.NewConversation(ai.ConversationOptions{
		SystemPrompt: buildSystemPrompt(rt, r.cfg),
		// Two messages per step (assistant action + observation) across the
		// round budget, plus history headroom — generous so the engine's own
		// caps, not this ceiling, bound the turn.
		MaxMessages: (r.cfg.MaxRounds + len(req.History) + 4) * 2,
	})
	for _, m := range trimHistory(req.History, r.cfg.HistoryCharBudget) {
		_ = conv.AddMessage(m.Role, m.Content)
	}
	conv.AddUserMessage(req.Question)

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
			// Grounding guard: an `answer` produced with no tool activity (no
			// query / lookup / search) is ungrounded — the model fabricated it.
			// Re-nudge it to gather evidence; if it still refuses after
			// maxGroundingNudges, DECLINE rather than emit fabricated content.
			// Any tool event (even a schema lookup) counts as grounding;
			// clarify / decline need no data and are always accepted.
			if act.Kind == actAnswer && len(st.events) == 0 {
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
		// Still enforce grounding: a synthesized answer with no tool activity
		// is fabricated — decline instead of emitting it.
		if act.Kind == actAnswer && len(st.events) == 0 {
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
func (r *runner) runWithTools(ctx context.Context, rt *ProjectRuntime, req TurnRequest) {
	st := &turnState{req: req, client: rt.AIClient, model: rt.Model}
	system := buildSystemPromptForTools(rt, r.cfg)
	hasSchema := rt.SchemaProvider != nil

	var messages []gollm.Message
	for _, m := range trimHistory(req.History, r.cfg.HistoryCharBudget) {
		messages = append(messages, gollm.Message{Role: m.Role, Content: m.Content})
	}
	messages = append(messages, gollm.Message{Role: "user", Content: req.Question})

	for st.round = 1; st.round <= r.cfg.MaxRounds; st.round++ {
		if err := ctx.Err(); err != nil {
			r.finishTimeout(ctx, st)
			return
		}

		grounded := st.groundedEvents > 0
		resp, err := st.callModelTools(ctx, messages, system, toolsForPhase(grounded, hasSchema), toolChoiceForPhase(grounded))
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
				r.runText(ctx, rt, req)
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
				r.runText(ctx, rt, req)
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
		// refused so the model reviews the data before finishing.
		results := make([]gollm.ToolResult, 0, len(resp.ToolCalls))
		for _, tc := range resp.ToolCalls {
			if actionKind(tc.Name).terminal() {
				results = append(results, gollm.ToolResult{CallID: tc.ID, Content: "Do not finish in the same step as a data tool. Review the query results first, then call answer/clarify/decline on its own.", IsError: true})
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
	default:
		return "Unknown action."
	}
}

func (r *runner) execQuery(ctx context.Context, rt *ProjectRuntime, st *turnState, act *turnAction) string {
	if st.queriesUsed >= r.cfg.MaxQueriesPerTurn {
		return fmt.Sprintf("Query budget exhausted (%d/%d). Answer with what you have, or decline.", st.queriesUsed, r.cfg.MaxQueriesPerTurn)
	}
	st.queriesUsed++
	// NB: we deliberately do NOT call rt.Executor.SetStep here. The executor is
	// shared across concurrent turns for the same project (pooled per project),
	// and SetStep mutates executor state that Execute reads — a data race. The
	// per-step number only stamps the executor's FixHistory, which ask-serve
	// doesn't persist (round/latency are captured on the tool event instead).
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

	start := time.Now()
	res, err := rt.Executor.Execute(qctx, act.Query, act.Purpose)
	latency := time.Since(start).Milliseconds()

	ev := commonmodels.ToolEvent{
		Round:     st.round,
		Name:      string(actQuery),
		Args:      map[string]any{"sql": act.Query, "purpose": act.Purpose},
		LatencyMS: latency,
	}
	if err != nil {
		ev.Error = err.Error()
		r.emit(ctx, st, ev)
		return fmt.Sprintf("Query failed: %s\nTry a different query, or answer/decline with what you have.", err.Error())
	}
	sum := summarizeResult(res, act.Purpose, r.cfg)
	ev.Output = sum
	r.emit(ctx, st, ev)
	return sum.observation()
}

func (r *runner) execLookup(ctx context.Context, rt *ProjectRuntime, st *turnState, act *turnAction) string {
	ev := commonmodels.ToolEvent{
		Round: st.round,
		Name:  string(actLookup),
		Args:  map[string]any{"tables": act.LookupSchema},
	}
	if rt.SchemaProvider == nil {
		ev.Error = "schema provider not configured"
		r.emit(ctx, st, ev)
		return "Schema lookup unavailable. Pick a table and query it directly; the SQL repair step can recover minor column mismatches."
	}
	start := time.Now()
	res, err := rt.SchemaProvider.Lookup(ctx, act.LookupSchema)
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

func (r *runner) execSearch(ctx context.Context, rt *ProjectRuntime, st *turnState, act *turnAction) string {
	ev := commonmodels.ToolEvent{
		Round: st.round,
		Name:  string(actSearch),
		Args:  map[string]any{"query": act.SearchTables, "top_k": act.SearchTopK},
	}
	if rt.SchemaProvider == nil {
		ev.Error = "schema provider not configured"
		r.emit(ctx, st, ev)
		return "Table search unavailable. Use lookup_schema with a table name you already know, or query directly."
	}
	start := time.Now()
	hits, err := rt.SchemaProvider.Search(ctx, act.SearchTables, act.SearchTopK)
	ev.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		ev.Error = err.Error()
		r.emit(ctx, st, ev)
		return fmt.Sprintf("Table search failed: %s", err.Error())
	}
	ev.Output = searchSummary(hits)
	r.emit(ctx, st, ev)
	return formatSearch(act.SearchTables, hits)
}

// emit appends a tool event to the in-memory transcript and persists it.
func (r *runner) emit(ctx context.Context, st *turnState, ev commonmodels.ToolEvent) {
	st.events = append(st.events, ev)
	if ev.Error == "" {
		// A successful evidence event — the model actually observed data.
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

	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := r.store.Finalize(pctx, fin); err != nil {
		applog.WithError(err).WithField("turn_id", fin.TurnID).Error("ask-serve: failed to finalize turn")
	}
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

func formatSearch(query string, hits []ai.SearchHit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Search results for %q:\n", query)
	if len(hits) == 0 {
		b.WriteString("(no matching tables)")
	}
	for i, h := range hits {
		fmt.Fprintf(&b, "%d. %s — %s\n", i+1, h.Table, h.Blurb)
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
// search tool event's output (ai.SearchHit has no json tags, so it would
// otherwise serialize with capitalized keys the dashboard can't read).
func searchSummary(hits []ai.SearchHit) []map[string]any {
	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		out = append(out, map[string]any{
			"table":     h.Table,
			"blurb":     h.Blurb,
			"row_count": h.RowCount,
			"score":     h.Score,
		})
	}
	return out
}
