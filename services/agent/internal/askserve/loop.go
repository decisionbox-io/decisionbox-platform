package askserve

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
	req         TurnRequest
	client      *ai.Client
	events      []commonmodels.ToolEvent
	round       int
	queriesUsed int
	tokensIn    int
	tokensOut   int
}

// run drives the bounded ReAct loop for one turn. It always finalizes the turn
// (done/declined/timeout/failed) so the reader's poll terminates, preserving
// whatever transcript was gathered.
func (r *runner) run(ctx context.Context, rt *ProjectRuntime, req TurnRequest) {
	st := &turnState{req: req, client: rt.AIClient}

	conv := ai.NewConversation(ai.ConversationOptions{
		SystemPrompt: buildSystemPrompt(rt, r.cfg, rt.FilterField),
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
	rt.Executor.SetStep(st.round)

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
	ev.Output = hits
	r.emit(ctx, st, ev)
	return formatSearch(act.SearchTables, hits)
}

// emit appends a tool event to the in-memory transcript and persists it.
func (r *runner) emit(ctx context.Context, st *turnState, ev commonmodels.ToolEvent) {
	st.events = append(st.events, ev)
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
