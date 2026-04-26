package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	logger "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// ExplorationEngine manages autonomous data exploration with LLM.
type ExplorationEngine struct {
	client   *Client
	executor *queryexec.QueryExecutor
	maxSteps int
	minSteps int
	dataset  string
	onStep   StepCallback

	// schemaProvider serves the on-demand schema actions (lookup_schema,
	// search_tables). Optional — when nil the engine still parses those
	// actions and reports a graceful "schema service unavailable" reply
	// to the model so a misconfigured run doesn't crash the loop.
	schemaProvider SchemaProvider

	// Per-run budgets for the on-demand schema actions. Initialized from
	// ExplorationEngineOptions in NewExplorationEngine; decremented as
	// the engine serves each action. The remaining counts are surfaced
	// to the model in every action result so it can self-pace.
	maxLookupsPerRun  int
	maxSearchesPerRun int

	// Mutated state. Tracked on the engine (not the conversation) so
	// the budgets persist across retried steps and across action types.
	lookupsUsed   int
	searchesUsed  int
	fetchedTables map[string]struct{} // canonicalised refs already lookup'd; dedupes repeat asks
}

// maxParseRetries caps how many times we re-prompt the LLM on a single step
// when it returns a response we can't parse. Each retry injects a short
// "please respond in JSON" nudge into the conversation.
const maxParseRetries = 3

// StepCallback is called after each exploration step with live progress data.
// The action argument carries the step's action type — usually "query_data"
// for a real query, "complete_rejected" when the LLM signalled done before
// MinSteps and the engine rejected it. Downstream (StatusReporter) uses
// this to distinguish real queries from non-query events so the live UI
// doesn't render a rejected completion as an empty-SQL failed query and
// the per-run query counter only counts real queries.
type StepCallback func(stepNum int, action, thinking, query string, rowCount int, queryTimeMs int64, queryFixed bool, errMsg string)

// ExplorationEngineOptions configures the exploration engine.
type ExplorationEngineOptions struct {
	Client   *Client
	Executor *queryexec.QueryExecutor
	MaxSteps int
	// MinSteps is a floor on the number of exploration steps before the engine
	// accepts a "done" signal from the LLM. Early done signals below this
	// threshold are rejected with a nudge and exploration continues. Zero
	// disables the floor.
	MinSteps int
	Dataset  string
	OnStep   StepCallback // optional: called after each step for live status

	// SchemaProvider serves on-demand schema actions issued by the LLM
	// during a run (lookup_schema for L1 detail, search_tables for
	// semantic table discovery). Required for production wiring; may
	// be nil in tests that exercise only the query/done paths. When
	// nil, the engine reports "schema service unavailable" to the
	// model rather than crashing — see executeLookupSchema.
	SchemaProvider SchemaProvider

	// MaxLookupsPerRun caps total lookup_schema actions across the
	// whole run. 0 → DefaultMaxLookupsPerRun. Negative → 0 (off).
	MaxLookupsPerRun int

	// MaxSearchesPerRun caps total search_tables actions across the
	// whole run. 0 → DefaultMaxSearchesPerRun. Negative → 0 (off).
	MaxSearchesPerRun int
}

// NewExplorationEngine creates a new exploration engine.
func NewExplorationEngine(opts ExplorationEngineOptions) *ExplorationEngine {
	if opts.MaxSteps == 0 {
		opts.MaxSteps = 100
	}
	if opts.MinSteps < 0 {
		opts.MinSteps = 0
	}
	if opts.MinSteps > opts.MaxSteps {
		opts.MinSteps = opts.MaxSteps
	}

	// Lookup / search budgets: 0 means "use default", negative means
	// "off" (the engine treats them as out-of-budget from step 1, which
	// effectively disables the action). This split exists so a test can
	// disable an action surface without touching the conversation prompt.
	maxLookups := opts.MaxLookupsPerRun
	switch {
	case maxLookups == 0:
		maxLookups = DefaultMaxLookupsPerRun
	case maxLookups < 0:
		maxLookups = 0
	}
	maxSearches := opts.MaxSearchesPerRun
	switch {
	case maxSearches == 0:
		maxSearches = DefaultMaxSearchesPerRun
	case maxSearches < 0:
		maxSearches = 0
	}

	return &ExplorationEngine{
		client:            opts.Client,
		executor:          opts.Executor,
		maxSteps:          opts.MaxSteps,
		minSteps:          opts.MinSteps,
		dataset:           opts.Dataset,
		onStep:            opts.OnStep,
		schemaProvider:    opts.SchemaProvider,
		maxLookupsPerRun:  maxLookups,
		maxSearchesPerRun: maxSearches,
		fetchedTables:     make(map[string]struct{}),
	}
}

// ExplorationResult represents the result of an exploration run
type ExplorationResult struct {
	Steps         []models.ExplorationStep
	TotalSteps    int
	Duration      time.Duration
	Completed     bool
	CompletionMsg string
	Error         error
}

// ExplorationContext holds context for the exploration.
type ExplorationContext struct {
	ProjectID     string
	Dataset       string
	InitialPrompt string // The fully-prepared discovery prompt
}

// Explore runs the autonomous exploration loop
func (e *ExplorationEngine) Explore(
	ctx context.Context,
	explorationCtx ExplorationContext,
) (*ExplorationResult, error) {
	logger.WithFields(logger.Fields{
		"app_id":    explorationCtx.ProjectID,
		"max_steps": e.maxSteps,
	}).Info("Starting autonomous exploration")

	startTime := time.Now()

	// Create conversation with system prompt
	conversation := NewConversation(ConversationOptions{
		SystemPrompt: explorationCtx.InitialPrompt,
		MaxMessages:  e.maxSteps * 2, // User + assistant per step
	})

	// Start with initial user message
	initialMsg := e.buildInitialMessage(explorationCtx)
	conversation.AddUserMessage(initialMsg)

	result := &ExplorationResult{
		Steps:      make([]models.ExplorationStep, 0, e.maxSteps),
		TotalSteps: 0,
		Completed:  false,
	}

	// Exploration loop
	for step := 1; step <= e.maxSteps; step++ {
		logger.WithFields(logger.Fields{
			"step":     step,
			"max":      e.maxSteps,
			"messages": len(conversation.GetMessages()),
		}).Info("Exploration step starting")

		action, err := e.runStepWithRetry(ctx, conversation, step)
		if err != nil {
			result.Error = err
			result.Duration = time.Since(startTime)
			return result, err
		}

		// Reject premature completion: if the LLM says "done" before the min-step
		// floor, nudge it to keep exploring instead of terminating. This guards
		// against models (especially reasoning models) that are biased toward
		// declaring completion quickly.
		if action.Action == "complete" && step < e.minSteps {
			logger.WithFields(logger.Fields{
				"step":      step,
				"min_steps": e.minSteps,
			}).Warn("LLM signalled done before minimum steps — rejecting and continuing")

			nudge := fmt.Sprintf(
				"You've only completed %d of the required minimum %d exploration steps. "+
					"Do not signal completion yet — there are more analysis areas to cover. "+
					"Respond with the next query in the documented JSON format: "+
					`{"thinking": "...", "query": "SELECT ..."}.`,
				step, e.minSteps,
			)
			conversation.AddUserMessage(nudge)

			// Record the rejected completion as a step so it's visible in logs / UI
			// without short-circuiting the run.
			result.Steps = append(result.Steps, models.ExplorationStep{
				Step:      step,
				Timestamp: time.Now(),
				Action:    "complete_rejected",
				Thinking:  action.Thinking,
				Error:     fmt.Sprintf("rejected premature completion (%d < %d)", step, e.minSteps),
			})
			result.TotalSteps = step

			if e.onStep != nil {
				e.onStep(step, "complete_rejected", action.Thinking, "", 0, 0, false, fmt.Sprintf("rejected premature completion (%d < %d)", step, e.minSteps))
			}
			continue
		}

		// Create exploration step
		explorationStep := models.ExplorationStep{
			Step:      step,
			Timestamp: time.Now(),
			Action:    action.Action,
			Thinking:  action.Thinking,
		}

		// Execute the action
		logger.WithFields(logger.Fields{
			"step":     step,
			"action":   action.Action,
			"thinking": action.Thinking[:min(len(action.Thinking), 100)],
		}).Info("Executing exploration action")

		actionResult := e.executeAction(ctx, action, &explorationStep)

		// Add to results
		result.Steps = append(result.Steps, explorationStep)
		result.TotalSteps = step

		// Report step for live status
		if e.onStep != nil {
			errMsg := explorationStep.Error
			e.onStep(step, action.Action, action.Thinking, explorationStep.Query, explorationStep.RowCount, explorationStep.ExecutionTimeMs, explorationStep.Fixed, errMsg)
		}

		// Check if exploration is complete
		if action.Action == "complete" {
			result.Completed = true
			result.CompletionMsg = action.Reason
			logger.WithField("step", step).Info("Exploration completed")
			break
		}

		// Add action result to conversation
		conversation.AddUserMessage(actionResult)
	}

	result.Duration = time.Since(startTime)

	if !result.Completed {
		logger.WithField("steps", result.TotalSteps).Warn("Exploration reached max steps without completion")
		result.CompletionMsg = fmt.Sprintf("Reached maximum steps (%d)", e.maxSteps)
	}

	logger.WithFields(logger.Fields{
		"total_steps": result.TotalSteps,
		"duration":    result.Duration,
		"completed":   result.Completed,
	}).Info("Exploration finished")

	return result, nil
}

// ExplorationAction represents the LLM's decision for one exploration
// step. Exactly ONE action mode is taken per turn; the parser picks
// which mode applies based on the JSON keys present.
//
// Action modes (set by parseAction based on which fields are populated):
//
//	"query_data"   — Query is set; execute SQL against the warehouse.
//	"complete"     — Done==true OR Action=="complete"; end exploration.
//	"lookup_schema"— LookupSchema lists fully-qualified table refs the
//	                 LLM wants L1 detail (columns + samples) for. Served
//	                 by the engine's SchemaProvider; result becomes the
//	                 next user message in the conversation.
//	"search_tables"— SearchTables is a free-text query the LLM wants
//	                 ranked semantically against the per-project schema
//	                 index. Top hits flow back as the next user message.
//
// Legacy fields (Action, QueryPurpose, Reason) stay for the JSON
// parser's "explicit action" path — older prompts still emit
// `{"action": "query_data", ...}`.
type ExplorationAction struct {
	// Common
	Thinking string `json:"thinking"`

	// query_data
	Query string `json:"query"`

	// complete (modern shape)
	Done    bool   `json:"done"`
	Summary string `json:"summary"`

	// lookup_schema (new) — list of fully-qualified table refs
	// (canonically "dataset.table"). Bare "table" is accepted; the
	// SchemaProvider rehydrates the qualified form. Empty when this
	// turn isn't a lookup.
	LookupSchema []string `json:"lookup_schema"`

	// search_tables (new) — free-text semantic query. Empty when
	// this turn isn't a search. SearchTopK is optional; 0 falls
	// back to DefaultSearchTopK and values above MaxSearchTopK
	// are clamped at execution time.
	SearchTables string `json:"search_tables"`
	SearchTopK   int    `json:"search_top_k"`

	// Legacy / explicit-action shape — kept so a prompt can still
	// say {"action": "query_data", ...}. The parser normalises
	// modern key-driven shapes into Action so executeAction has
	// one switch.
	Action       string `json:"action"`
	QueryPurpose string `json:"query_purpose"`
	Reason       string `json:"reason"`
}

// runStepWithRetry calls the LLM for one exploration step and parses the
// response. If the response can't be parsed into an ExplorationAction the
// conversation is nudged to reformat and the turn retries up to
// maxParseRetries times before returning a hard error. This replaces the
// previous behaviour where an unparseable response silently terminated the
// run as "complete" — the main cause of short-runs on reasoning models like
// Qwen3 and DeepSeek R1.
func (e *ExplorationEngine) runStepWithRetry(ctx context.Context, conversation *Conversation, step int) (*ExplorationAction, error) {
	var lastParseErr error

	for attempt := 0; attempt <= maxParseRetries; attempt++ {
		llmStart := time.Now()
		response, err := e.client.CreateMessage(
			ctx,
			conversation.GetMessages(),
			conversation.GetSystemPrompt(),
			4096,
		)
		if err != nil {
			logger.WithFields(logger.Fields{
				"step":    step,
				"attempt": attempt,
				"error":   err.Error(),
			}).Error("LLM call failed during exploration")
			return nil, fmt.Errorf("step %d: failed to get LLM response: %w", step, err)
		}

		logger.WithFields(logger.Fields{
			"step":       step,
			"attempt":    attempt,
			"tokens_in":  response.Usage.InputTokens,
			"tokens_out": response.Usage.OutputTokens,
			"llm_ms":     time.Since(llmStart).Milliseconds(),
		}).Debug("LLM response received")

		responseText := ""
		if len(response.Content) > 0 {
			responseText = response.Content
		}

		conversation.AddAssistantMessage(responseText)

		action, err := e.parseAction(responseText)
		if err == nil {
			return action, nil
		}

		lastParseErr = err
		preview := responseText
		if len(preview) > 200 {
			preview = preview[:200]
		}
		logger.WithFields(logger.Fields{
			"step":     step,
			"attempt":  attempt,
			"error":    err.Error(),
			"response": preview,
		}).Warn("Failed to parse exploration action; nudging LLM to reformat")

		if attempt == maxParseRetries {
			break
		}

		conversation.AddUserMessage(
			"Your previous response could not be parsed as an exploration action. " +
				"Respond with exactly ONE JSON object, no prose around it, matching one of:\n" +
				`  {"thinking": "...", "query": "SELECT ..."}  — to run a query, or` + "\n" +
				`  {"done": true, "summary": "..."}            — only when exploration is truly finished.` + "\n" +
				"Do not wrap it in markdown fences unless necessary and do not emit planning JSON before the action.",
		)
	}

	return nil, fmt.Errorf("step %d: unable to parse LLM response after %d attempts: %w", step, maxParseRetries+1, lastParseErr)
}

// parseAction parses the LLM's response into an ExplorationAction.
//
// The response must contain a JSON object with ONE of:
//   - {"query": "SELECT ..."}              → execute the query
//   - {"done": true, "summary": "..."}     → exploration finished
//   - {"action": "query_data" | "complete" | ...}  (legacy)
//
// A response with no parseable action JSON is an error. The caller retries
// the turn rather than silently treating it as "complete" — early exploration
// termination (previously caused by prose matching "done"/"finished" or
// missing fields) is the bug this parser is designed to prevent.
func (e *ExplorationEngine) parseAction(response string) (*ExplorationAction, error) {
	jsonStr := e.extractJSON(response)
	if jsonStr == "" {
		return nil, fmt.Errorf("no action JSON object found in response")
	}

	var action ExplorationAction
	if err := json.Unmarshal([]byte(jsonStr), &action); err != nil {
		return nil, fmt.Errorf("failed to parse action JSON: %w", err)
	}

	switch {
	case action.Done:
		action.Action = "complete"
		if action.Reason == "" {
			action.Reason = action.Summary
		}
	case action.Query != "":
		action.Action = "query_data"
	case len(action.LookupSchema) > 0:
		// Modern key-driven: presence of lookup_schema list selects mode.
		action.Action = "lookup_schema"
	case strings.TrimSpace(action.SearchTables) != "":
		// Modern key-driven: a non-empty search query selects mode.
		action.Action = "search_tables"
	case action.Action == "complete":
		// Legacy explicit complete — accept.
	case action.Action == "query_data" && action.Query != "":
		// Legacy explicit query — accept.
	case action.Action == "lookup_schema" && len(action.LookupSchema) > 0:
		// Legacy explicit lookup — accept.
	case action.Action == "search_tables" && strings.TrimSpace(action.SearchTables) != "":
		// Legacy explicit search — accept.
	default:
		// JSON parsed but carries no recognised payload. Fail loudly so
		// the caller can re-prompt instead of silently terminating.
		return nil, fmt.Errorf("action JSON has no query, lookup_schema, search_tables, done flag, or recognized action (got action=%q)", action.Action)
	}

	return &action, nil
}

// extractJSON extracts a JSON action object from the LLM response.
//
// Reasoning / "thinking" models (Qwen3, DeepSeek R1, GPT-OSS, ...) emit
// multiple JSON-shaped blocks per turn — a planning / reasoning preamble,
// followed by the real action. We gather every candidate JSON object
// (both fenced and raw) into a single ordered list and return the LAST
// one that carries a recognized action key (query, done, or action). If
// no candidate has an action key, we fall back to the last balanced
// object overall. This way a fenced preamble without an action key
// cannot hijack parsing when the real action lives later outside fences.
func (e *ExplorationEngine) extractJSON(text string) string {
	candidates := collectJSONCandidates(text)
	if len(candidates) == 0 {
		return ""
	}
	for i := len(candidates) - 1; i >= 0; i-- {
		if jsonHasActionKey(candidates[i]) {
			return candidates[i]
		}
	}
	return candidates[len(candidates)-1]
}

// collectJSONCandidates returns every plausible JSON object in text:
// first the contents of each fenced block (```json / ``` / ```JSON) that
// starts with '{', then every balanced top-level { ... } found by a
// scan over the raw text. Fenced blocks are listed first so that, when
// no candidate carries an action key, the fallback prefers raw trailing
// JSON over a fenced preamble.
func collectJSONCandidates(text string) []string {
	var out []string
	out = append(out, fencedJSONBlocks(text)...)
	out = append(out, findBalancedJSONObjects(text)...)
	return out
}

// fencedJSONBlocks returns every markdown-fenced block whose body starts
// with '{'. Language tags json / JSON / (empty) are all accepted.
func fencedJSONBlocks(text string) []string {
	var out []string
	for rest := text; ; {
		idx := strings.Index(rest, "```")
		if idx < 0 {
			break
		}
		after := rest[idx+3:]
		if nl := strings.IndexByte(after, '\n'); nl >= 0 {
			maybeLang := strings.TrimSpace(after[:nl])
			if maybeLang == "" || strings.EqualFold(maybeLang, "json") {
				after = after[nl+1:]
			}
		}
		end := strings.Index(after, "```")
		if end < 0 {
			break
		}
		block := strings.TrimSpace(after[:end])
		if strings.HasPrefix(block, "{") {
			out = append(out, block)
		}
		rest = after[end+3:]
	}
	return out
}

// findBalancedJSONObjects returns every balanced top-level { ... } substring
// in text, in order. String literals are tracked so { / } inside strings
// (e.g., inside a SQL query) don't break the brace count.
func findBalancedJSONObjects(text string) []string {
	var out []string
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		depth := 0
		inString := false
		escaped := false
		for j := i; j < len(text); j++ {
			c := text[j]
			if inString {
				if escaped {
					escaped = false
					continue
				}
				switch c {
				case '\\':
					escaped = true
				case '"':
					inString = false
				}
				continue
			}
			switch c {
			case '"':
				inString = true
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					out = append(out, text[i:j+1])
					i = j
					goto next
				}
			}
		}
		// Unbalanced from i — stop scanning (no further balanced objects possible).
		break
	next:
	}
	return out
}

// jsonHasActionKey reports whether the JSON-encoded object declares a field
// the exploration parser understands (query, done, or action).
func jsonHasActionKey(s string) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &probe); err != nil {
		return false
	}
	_, hasQuery := probe["query"]
	_, hasDone := probe["done"]
	_, hasAction := probe["action"]
	return hasQuery || hasDone || hasAction
}

// executeAction executes the action and returns the user-message string
// the engine appends to the conversation. The string format is part of
// the engine's contract with prompts — domain-pack prompts assume the
// "Schema for `dataset.table`:" / "Search results for ..." shapes
// below when describing the actions to the LLM.
func (e *ExplorationEngine) executeAction(
	ctx context.Context,
	action *ExplorationAction,
	step *models.ExplorationStep,
) string {
	switch action.Action {
	case "query_data":
		return e.executeQuery(ctx, action, step)

	case "lookup_schema":
		return e.executeLookupSchema(ctx, action, step)

	case "search_tables":
		return e.executeSearchTables(ctx, action, step)

	case "complete":
		return fmt.Sprintf("Exploration complete: %s", action.Reason)

	default:
		logger.WithField("action", action.Action).Warn("Unknown action")
		return fmt.Sprintf("Unknown action: %s", action.Action)
	}
}

// executeQuery executes a BigQuery query
func (e *ExplorationEngine) executeQuery(
	ctx context.Context,
	action *ExplorationAction,
	step *models.ExplorationStep,
) string {
	step.QueryPurpose = action.QueryPurpose
	step.Query = action.Query

	queryStart := time.Now()

	result, err := e.executor.Execute(ctx, action.Query, action.QueryPurpose)

	step.ExecutionTimeMs = time.Since(queryStart).Milliseconds()

	if err != nil {
		step.Error = err.Error()
		step.Fixed = false
		logger.WithField("error", err.Error()).Error("Query execution failed")
		return fmt.Sprintf("Query failed: %s\n\nPlease try a different approach.", err.Error())
	}

	step.QueryResult = result.Data
	step.RowCount = result.RowCount
	step.FixAttempts = result.FixAttempts
	step.Fixed = result.Fixed

	// Format result for Claude
	resultMsg := "Query executed successfully.\n\n"
	resultMsg += fmt.Sprintf("Rows returned: %d\n", result.RowCount)
	resultMsg += fmt.Sprintf("Execution time: %dms\n", result.ExecutionTimeMs)

	if result.Fixed {
		resultMsg += fmt.Sprintf("Note: Query was automatically fixed (%d attempts)\n", result.FixAttempts)
	}

	resultMsg += "\n**Results**:\n"

	// Show first 10 rows
	maxRows := 10
	if len(result.Data) < maxRows {
		maxRows = len(result.Data)
	}

	resultMsg += fmt.Sprintf("```json\n%s\n```\n", e.formatResults(result.Data[:maxRows]))

	if len(result.Data) > maxRows {
		resultMsg += fmt.Sprintf("\n(Showing %d of %d rows)\n", maxRows, len(result.Data))
	}

	return resultMsg
}

// executeLookupSchema serves a lookup_schema action by asking the
// engine's SchemaProvider for L1 detail on the requested tables. The
// result string becomes the next user message, so its format is part
// of the prompt contract (see domain-packs/*/prompts/base/exploration.md).
//
// Budget rules (enforced here, not in the parser):
//   - Per-call cap: MaxLookupTablesPerCall. Excess refs are dropped
//     with a "truncated" hint so the model knows to issue follow-ups.
//   - Per-run cap: e.maxLookupsPerRun. When exhausted the engine
//     replies with a "lookup budget exceeded" message and refuses
//     to call the provider — the model can still issue queries
//     against tables it already saw.
//   - Dedup: refs already returned by a prior lookup_schema in this
//     run are short-circuited locally with a friendly "you already
//     have this; reuse it from earlier in the conversation" reply
//     rather than re-burning a slot of the budget.
//
// The step's ExplorationStep.Action is recorded as "lookup_schema"
// in the caller; here we just mutate Thinking and Error if needed.
func (e *ExplorationEngine) executeLookupSchema(
	ctx context.Context,
	action *ExplorationAction,
	step *models.ExplorationStep,
) string {
	step.QueryPurpose = "lookup_schema"

	// Provider unavailable → graceful degradation. The model still
	// has the catalog in the system prompt; it can use bare table
	// names and recover via the SQL fixer if columns mismatch.
	if e.schemaProvider == nil {
		step.Error = "schema provider not configured"
		return "Schema lookup unavailable: schema provider not configured. " +
			"Use the catalog in the system prompt to pick a table and run a SELECT — " +
			"the SQL fixer can recover from minor column mismatches."
	}

	// Budget exhausted before this call → refuse and explain.
	if e.maxLookupsPerRun > 0 && e.lookupsUsed >= e.maxLookupsPerRun {
		step.Error = fmt.Sprintf("lookup budget exhausted (%d/%d)", e.lookupsUsed, e.maxLookupsPerRun)
		return fmt.Sprintf(
			"Lookup budget exhausted — you have used %d of %d lookups this run. "+
				"No more schemas will be served. Continue with the tables you have already inspected.",
			e.lookupsUsed, e.maxLookupsPerRun,
		)
	}

	// Normalise + deduplicate refs the model named THIS turn. Empty
	// strings are dropped silently — they're a parser/escaping artefact,
	// not the model's intent.
	refs := normaliseRefs(action.LookupSchema)
	if len(refs) == 0 {
		step.Error = "lookup_schema with no tables"
		return "lookup_schema action had no tables. " +
			`Use {"thinking": "...", "lookup_schema": ["dataset.table_a", "dataset.table_b"]}.`
	}

	// Local dedup: anything already fetched short-circuits without
	// burning provider calls or budget.
	seen := make(map[string]struct{}, len(refs))
	fresh := make([]string, 0, len(refs))
	already := make([]string, 0)
	for _, r := range refs {
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		if _, ok := e.fetchedTables[r]; ok {
			already = append(already, r)
			continue
		}
		fresh = append(fresh, r)
	}

	// All requested refs already served — useful no-op feedback.
	if len(fresh) == 0 {
		var b strings.Builder
		b.WriteString("All requested tables were already inspected earlier in this run; reuse the previous lookup result.\n")
		b.WriteString("Already inspected: ")
		b.WriteString(strings.Join(already, ", "))
		return b.String()
	}

	// Per-call cap: anything beyond MaxLookupTablesPerCall is dropped
	// and the model is told (so it can issue follow-ups). The provider
	// also enforces this cap — duplicate enforcement is intentional so
	// fakes in tests don't have to replicate the rule.
	truncatedAtCallCap := false
	if len(fresh) > MaxLookupTablesPerCall {
		fresh = fresh[:MaxLookupTablesPerCall]
		truncatedAtCallCap = true
	}

	res, err := e.schemaProvider.Lookup(ctx, fresh)
	// Decrement budget regardless of outcome — even a failed lookup
	// burns server resources and we don't want to invite retry storms.
	e.lookupsUsed++

	if err != nil {
		step.Error = err.Error()
		logger.WithError(err).Warn("lookup_schema failed")
		return fmt.Sprintf(
			"Schema lookup failed: %s. "+
				"You can continue with tables you have already inspected, "+
				"or try again with different refs.",
			err.Error(),
		)
	}

	// Cache successful refs so the same request short-circuits next time.
	for _, t := range res.Tables {
		e.fetchedTables[t.Table] = struct{}{}
	}

	return formatLookupResult(res, already, truncatedAtCallCap, e.lookupsUsed, e.maxLookupsPerRun)
}

// executeSearchTables serves a search_tables action by ranking
// semantically against the per-project schema embedding index. The
// result string becomes the next user message; format is part of the
// prompt contract.
func (e *ExplorationEngine) executeSearchTables(
	ctx context.Context,
	action *ExplorationAction,
	step *models.ExplorationStep,
) string {
	step.QueryPurpose = "search_tables"

	if e.schemaProvider == nil {
		step.Error = "schema provider not configured"
		return "Table search unavailable: schema provider not configured. " +
			"Use the catalog in the system prompt to pick tables instead."
	}

	if e.maxSearchesPerRun > 0 && e.searchesUsed >= e.maxSearchesPerRun {
		step.Error = fmt.Sprintf("search budget exhausted (%d/%d)", e.searchesUsed, e.maxSearchesPerRun)
		return fmt.Sprintf(
			"Search budget exhausted — you have used %d of %d searches this run. "+
				"Use the catalog or already-known tables for the rest of the run.",
			e.searchesUsed, e.maxSearchesPerRun,
		)
	}

	query := strings.TrimSpace(action.SearchTables)
	if query == "" {
		step.Error = "search_tables with empty query"
		return "search_tables action had an empty query. " +
			`Use {"thinking": "...", "search_tables": "topic terms describing what you're looking for"}.`
	}

	k := action.SearchTopK
	if k <= 0 {
		k = DefaultSearchTopK
	}
	if k > MaxSearchTopK {
		k = MaxSearchTopK
	}

	hits, err := e.schemaProvider.Search(ctx, query, k)
	e.searchesUsed++

	if err != nil {
		step.Error = err.Error()
		logger.WithError(err).Warn("search_tables failed")
		return fmt.Sprintf(
			"Table search failed: %s. "+
				"Use the catalog in the system prompt to pick tables instead.",
			err.Error(),
		)
	}

	return formatSearchResult(query, hits, e.searchesUsed, e.maxSearchesPerRun)
}

// formatResults formats query results as JSON
func (e *ExplorationEngine) formatResults(data []map[string]interface{}) string {
	jsonBytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Sprintf("Error formatting results: %v", err)
	}
	return string(jsonBytes)
}

// buildInitialMessage builds the first message to Claude.
// The system prompt already contains the schema catalog, filter rules,
// analysis areas, and profile. This message kicks off the exploration
// loop and announces the on-demand schema budget so the model can
// pace itself across the run.
func (e *ExplorationEngine) buildInitialMessage(explorationCtx ExplorationContext) string {
	var msg strings.Builder

	msg.WriteString("Begin your data exploration.\n\n")
	fmt.Fprintf(&msg, "You have up to %d exploration steps. ", e.maxSteps)
	msg.WriteString("Follow the rules and format described in the system prompt.\n")

	if e.schemaProvider != nil {
		fmt.Fprintf(&msg,
			"\nOn-demand schema budget for this run: %d lookup_schema calls (max %d tables per call), %d search_tables calls.\n",
			e.maxLookupsPerRun, MaxLookupTablesPerCall, e.maxSearchesPerRun,
		)
	}

	return msg.String()
}

// normaliseRefs canonicalises and dedupes the table refs an LLM emits
// in lookup_schema. Whitespace is trimmed; backticks (BigQuery) and
// surrounding quotes (Snowflake/Postgres) are stripped; empty refs
// are dropped. Order is preserved so the engine renders results in
// the order the model asked for them — useful when the model labels
// expected behaviour in its thinking ("first I want users, then
// orders").
func normaliseRefs(refs []string) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		r = strings.TrimSpace(r)
		// Strip a single outermost layer of common quoting so "`db.t`" or
		// "\"db.t\"" resolves to "db.t". We only ever peel one layer total —
		// a model emitting "`\"db.t\"`" is already wrong; over-stripping
		// would silently mask the bug instead of surfacing it via NotFound.
		if stripped := stripWrappingQuote(r, '`'); stripped != r {
			r = stripped
		} else {
			r = stripWrappingQuote(r, '"')
		}
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		out = append(out, r)
	}
	return out
}

// stripWrappingQuote removes one layer of `q` quoting if `s` is wrapped
// in it (e.g. `"foo"` → `foo`).
func stripWrappingQuote(s string, q byte) string {
	if len(s) >= 2 && s[0] == q && s[len(s)-1] == q {
		return s[1 : len(s)-1]
	}
	return s
}

// formatLookupResult renders the user message for a successful
// lookup_schema action. Returned string is what gets appended to the
// conversation, so prompts can rely on "Schema for `dataset.table`:"
// being the marker the model can scan for in its own history.
func formatLookupResult(res LookupResult, already []string, truncatedAtCallCap bool, lookupsUsed, maxLookups int) string {
	var b strings.Builder

	if len(res.Tables) == 0 {
		b.WriteString("No schemas resolved for the requested tables.")
	} else {
		for i, t := range res.Tables {
			if i > 0 {
				b.WriteString("\n\n")
			}
			fmt.Fprintf(&b, "Schema for `%s` (%s rows):\n", t.Table, formatRowCountShort(t.RowCount))
			if len(t.Columns) == 0 {
				b.WriteString("  columns: (no column metadata available)\n")
			} else {
				b.WriteString("  columns:\n")
				for _, c := range t.Columns {
					nullable := "NOT NULL"
					if c.Nullable {
						nullable = "NULL"
					}
					fmt.Fprintf(&b, "    - %s %s %s", c.Name, c.Type, nullable)
					if c.Category != "" {
						fmt.Fprintf(&b, " [%s]", c.Category)
					}
					b.WriteByte('\n')
				}
			}
			if len(t.SampleRows) > 0 {
				b.WriteString("  sample rows:\n")
				for _, row := range t.SampleRows {
					b.WriteString("    ")
					b.WriteString(formatLookupRow(row))
					b.WriteByte('\n')
				}
			}
		}
	}

	if len(res.NotFound) > 0 {
		b.WriteString("\n\nNot found (typo, dropped, or wrong dataset): ")
		b.WriteString(strings.Join(res.NotFound, ", "))
	}
	if len(already) > 0 {
		b.WriteString("\n\nAlready inspected earlier in this run (reuse from prior context): ")
		b.WriteString(strings.Join(already, ", "))
	}
	if res.Truncated || truncatedAtCallCap {
		fmt.Fprintf(&b, "\n\nNote: per-call cap is %d tables — extra refs were dropped. Issue another lookup_schema for the remainder.", MaxLookupTablesPerCall)
	}

	if maxLookups > 0 {
		fmt.Fprintf(&b, "\n\nLookup budget: %d/%d used (%d remaining).",
			lookupsUsed, maxLookups, maxLookups-lookupsUsed)
	}

	return strings.TrimRight(b.String(), "\n")
}

// formatSearchResult renders the user message for a search_tables
// action. Includes the query so the model can refer back to it when
// chaining a follow-up lookup_schema.
func formatSearchResult(query string, hits []SearchHit, searchesUsed, maxSearches int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Search results for %q:\n", query)

	if len(hits) == 0 {
		b.WriteString("(no matching tables; try different terms or pick from the catalog in the system prompt)")
	} else {
		for i, h := range hits {
			fmt.Fprintf(&b, "%d. `%s` — %s rows — score=%.3f", i+1, h.Table, formatRowCountShort(h.RowCount), h.Score)
			if h.Blurb != "" {
				b.WriteString("\n   ")
				b.WriteString(h.Blurb)
			}
			b.WriteByte('\n')
		}
		b.WriteString("\nIssue lookup_schema with the table refs you want full column detail for before querying them.")
	}

	if maxSearches > 0 {
		fmt.Fprintf(&b, "\n\nSearch budget: %d/%d used (%d remaining).",
			searchesUsed, maxSearches, maxSearches-searchesUsed)
	}

	return strings.TrimRight(b.String(), "\n")
}

// formatRowCountShort renders a row count with a K/M/B suffix. -1
// means unknown (some warehouses don't expose row counts cheaply).
// Mirrors schema_render.formatRowCount but duplicated here to avoid
// an ai → schema_render dependency for one helper — schema_render
// already imports models which already imports ai indirectly through
// llm and we keep the engine's import graph flat.
func formatRowCountShort(n int64) string {
	switch {
	case n < 0:
		return "unknown"
	case n < 1_000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		v := float64(n) / 1_000
		if n%1_000 == 0 {
			return fmt.Sprintf("%.0fK", v)
		}
		return fmt.Sprintf("%.1fK", v)
	case n < 1_000_000_000:
		v := float64(n) / 1_000_000
		if n%1_000_000 == 0 {
			return fmt.Sprintf("%.0fM", v)
		}
		return fmt.Sprintf("%.1fM", v)
	default:
		v := float64(n) / 1_000_000_000
		if n%1_000_000_000 == 0 {
			return fmt.Sprintf("%.0fB", v)
		}
		return fmt.Sprintf("%.1fB", v)
	}
}

// formatLookupRow renders a sample row with stable alphabetical key
// order. NULLs become "NULL"; long string values get truncated so
// one wide JSON column can't blow up the prompt.
func formatLookupRow(row map[string]interface{}) string {
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	// Sort for deterministic output (Go map iteration is randomised).
	// Imported sort would tie our hands; the inline insertion sort below
	// keeps this file's import set lean.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, formatLookupValue(row[k])))
	}
	return strings.Join(parts, ", ")
}

// maxLookupValueLen caps how many characters of a single sample value
// are shown. Longer values are truncated with an ellipsis so a single
// JSON / text-blob column doesn't dominate the prompt.
const maxLookupValueLen = 80

func formatLookupValue(v interface{}) string {
	if v == nil {
		return "NULL"
	}
	s := fmt.Sprintf("%v", v)
	// Collapse internal whitespace so a multi-line cell renders on one line.
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxLookupValueLen {
		return s[:maxLookupValueLen] + "…"
	}
	return s
}

