package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/decisionbox-io/decisionbox/libs/go-common/config"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	logger "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
)

// defaultDatasourceID is the reserved warehouse (datasource) id for the
// primary of a legacy / single-warehouse project. Mirrors
// models.DefaultWarehouseID / schema_retrieve.DefaultWarehouseID; declared
// locally so the exploration engine keeps a flat dependency set. An empty
// datasource_id from the model resolves to the run's primary, and a bare
// "default" always maps to the legacy primary.
const defaultDatasourceID = "default"

// StepIndexer captures one step's worth of "ship this to the run-
// scoped vector index" work. Defined as an interface so the
// exploration engine doesn't depend on the concrete
// discovery.RunStepIndex type (which would create an import cycle).
//
// Implementations must be concurrency-safe — exploration calls Upsert
// once per step, in sequence, so a simple wrapper around an HTTP /
// gRPC client is enough.
type StepIndexer interface {
	Upsert(ctx context.Context, step models.ExplorationStep) error

	// Nearest reports the cosine score of the closest step already
	// indexed for this run, which is how the engine tells a step that
	// breaks new ground from one that repeats work already done.
	//
	// found is false when there is nothing to compare against — an empty
	// index, or a step with nothing worth embedding. The caller must treat
	// that as "cannot tell" rather than as "not similar".
	//
	// Required rather than an optional interface discovered by assertion:
	// this seam is wrapped (see the orchestrator's telemetry decorator),
	// and a wrapper that forgot to forward an optional method would strip
	// the whole rule with nothing to notice it.
	Nearest(ctx context.Context, step models.ExplorationStep) (score float64, found bool, err error)
}

// ExplorationEngine manages autonomous data exploration with LLM.
type ExplorationEngine struct {
	client *Client
	// executors holds one query executor per datasource (warehouse) id,
	// so each query_data step routes to the datasource the model targets.
	// A single-warehouse project has exactly one entry keyed by
	// primaryDatasource. Keys are normalised warehouse ids (the empty id
	// stored as "default").
	executors map[string]*queryexec.QueryExecutor
	// primaryDatasource is the datasource a query_data targets when the
	// action names none — the run's primary warehouse.
	primaryDatasource string
	// datasourceIDs lists the routable datasource ids (sorted) for the
	// "unknown datasource" error the engine returns to the model.
	datasourceIDs []string
	// tableDatasource maps a canonical qualified "dataset.table" → the
	// datasource id that owns it, on a multi-warehouse run. Used to reject
	// a query_data statement that references a table from a DIFFERENT
	// datasource than its datasource_id (an attempted cross-engine join)
	// before it reaches the wrong engine — where the per-datasource SQL
	// fixer would otherwise rewrite it into a valid-but-wrong query. Nil on
	// single-warehouse.
	tableDatasource map[string]string
	maxSteps        int
	minSteps        int
	// stop decides whether a "done" signal is accepted — the step floor,
	// or the marginal-value rule on a run that can query a cube. See
	// exploration_stopping.go.
	stop    stopRule
	dataset string
	onStep  StepCallback

	// window / outputCap / reasoningEffective drive the reasoning-aware
	// per-step output budget (R3). window and outputCap are the model's
	// effective context window and output cap resolved at run start (#347
	// chain); reasoningEffective is true when the operator enabled reasoning or
	// the catalog flags the model. They are consulted ONLY on the
	// reasoning-effective path in explorationOutputTokens — a non-reasoning
	// model keeps exactly today's fixed ceiling, so big models are unaffected.
	window             int
	outputCap          int
	reasoningEffective bool

	// schemaProvider serves the on-demand schema actions (lookup_schema,
	// search_tables). Optional — when nil the engine still parses those
	// actions and reports a graceful "schema service unavailable" reply
	// to the model so a misconfigured run doesn't crash the loop.
	schemaProvider SchemaProvider

	// stepsIndexed and stepsIndexOffered count what the run-scoped index
	// accepted and what it was asked to accept. The stopping rule reads both,
	// because an empty search result means opposite things depending on them
	// — see emptySearchIsAboutTheStep.
	stepsIndexed      int
	stepsIndexOffered int

	// stepIndexer ships each completed step to the run-scoped vector
	// index. Optional — when nil the engine continues without
	// indexing, which downgrades the analysis phase to keyword-only
	// step selection. The orchestrator gates the run on a non-nil
	// indexer in production wiring.
	stepIndexer StepIndexer

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

// emptyAssistantTurnPlaceholder stands in for an assistant turn whose Content
// (and Reasoning) came back empty, so the conversation never carries an empty
// assistant message — some providers (Moonshot/Kimi via LiteLLM) reject that on
// the follow-up request with a hard 400. It is intentionally not valid JSON so
// parseAction never mistakes it for an action.
const emptyAssistantTurnPlaceholder = "(no output returned; see the instruction below)"

// defaultExplorationMaxOutputTokens is the per-step output ceiling for the
// exploration LLM call. The old value was a hard-coded 4096 (Rule 2); it is now
// the default of the EXPLORATION_MAX_OUTPUT_TOKENS knob. The default is kept at
// the proven-safe 4096 so the reservation cannot overflow a small-context
// deployment (e.g. an 8K/4K Ollama num_ctx); operators running reasoning models
// on large-context backends raise it to give the <think> block room without
// truncating the action (issue #341). The effective budget is
// min(catalogued output cap, this).
const defaultExplorationMaxOutputTokens = 4096

const explorationMaxOutputTokensEnv = "EXPLORATION_MAX_OUTPUT_TOKENS"

// reasoningExplorationOutputTokens is the raised per-step output ceiling for a
// reasoning-EFFECTIVE model, giving the hidden <think> block room so the action
// that follows it isn't truncated — the main cause of short-runs on Qwen3 /
// DeepSeek-R1 (issue #341). It is only the *intended* ceiling: it is still
// bounded by the model's output cap and budgeted against the context window, so
// it can never overflow. A non-reasoning model (Opus, GPT, ...) never reaches
// this path and keeps exactly defaultExplorationMaxOutputTokens.
const reasoningExplorationOutputTokens = 16384

// explorationReservedSystemTokens is the flat headroom kept for chat-template
// scaffolding on top of the measured conversation input when budgeting the
// reasoning output ceiling against the window (mirrors
// analysisReservedSystemTokens in the analysis phase).
const explorationReservedSystemTokens = 512

// explorationOutputTokens returns the per-step output budget. It is constant
// across a step's parse retries, so it is computed once per step.
//
// For a NON-reasoning model it is exactly today's value — the catalogued output
// cap bounded by the EXPLORATION_MAX_OUTPUT_TOKENS ceiling (default 4096) — so
// big models that already work are byte-identical. For a reasoning-EFFECTIVE
// model with a known window it is raised toward reasoningExplorationOutputTokens
// (or higher if the operator raised EXPLORATION_MAX_OUTPUT_TOKENS above it),
// bounded by the model's output cap AND budgeted against the window so input +
// output stays inside it. The #347 adaptive context-overflow retry is the net
// if the rune/4 input estimate still overshoots on a tight window.
//
// inputEst (rune/4 tokens of the conversation so far) is consulted ONLY on the
// reasoning path; the non-reasoning path ignores it, preserving the
// byte-identical guarantee.
func (e *ExplorationEngine) explorationOutputTokens(inputEst int) int {
	catalogCap := gollm.GetMaxOutputTokens(e.client.ProviderName(), e.client.ModelName())
	envCeiling := config.GetEnvAsInt(explorationMaxOutputTokensEnv, defaultExplorationMaxOutputTokens)

	// Reasoning-effective path: window-budgeted headroom. Requires a known
	// window; without one (tests / unresolved) we fall through to today's fixed
	// ceiling so an unknown window can never cause a regression.
	if e.reasoningEffective && e.window > 0 {
		outCap := e.outputCap
		if outCap <= 0 {
			outCap = catalogCap
		}
		// Intended ceiling: the reasoning default, or the operator's explicitly
		// raised EXPLORATION_MAX_OUTPUT_TOKENS when that is higher (don't shrink
		// a deployment that was already tuned up for reasoning, issue #341).
		ceiling := reasoningExplorationOutputTokens
		if envCeiling > ceiling {
			ceiling = envCeiling
		}
		if outCap > 0 && ceiling > outCap {
			ceiling = outCap
		}
		// Budget against the window: leave room for the measured input, the
		// reserved-system headroom, and the safety margin (same arithmetic the
		// analysis phase uses). ReservedOutput is 0 because output is exactly
		// what we are solving for.
		avail := gollm.NewBudget(e.window, 0, explorationReservedSystemTokens, false).Available() - inputEst
		if ceiling > avail {
			ceiling = avail
		}
		// Never drop below today's proven-safe floor (itself bounded by the cap
		// so a model whose output limit is under the floor is not over-asked).
		floor := defaultExplorationMaxOutputTokens
		if outCap > 0 && floor > outCap {
			floor = outCap
		}
		if ceiling < floor {
			ceiling = floor
		}
		return ceiling
	}

	// Non-reasoning path (today, byte-identical): catalogued cap bounded by the
	// configurable ceiling.
	maxTokens := catalogCap
	if envCeiling > 0 && maxTokens > envCeiling {
		maxTokens = envCeiling
	}
	return maxTokens
}

// conversationInputEst estimates the input-token size of the conversation with
// the tokenizer-free rune/4 heuristic (consistent with the analysis phase's
// approxTokens and /ask's budget walk). It sums the system prompt and every
// message body; used only to budget the reasoning-effective exploration output
// ceiling against the window.
func conversationInputEst(conv *Conversation) int {
	total := utf8.RuneCountInString(conv.GetSystemPrompt())
	for _, m := range conv.GetMessages() {
		total += utf8.RuneCountInString(m.Content)
	}
	return total / 4
}

// StepCallback is called after each exploration step with live progress data.
//
// inputTokens / outputTokens are the totals for the LLM call(s) the engine
// issued for this step — summed across any internal parse-retry rounds so
// the callback always sees a single home-document figure. Both are 0 when
// the engine did not issue an LLM call for the step (today: never — every
// step makes at least one call, but the contract leaves the door open).
// The action argument carries the step's action type — usually "query_data"
// for a real query, "complete_rejected" when the LLM signalled done before
// MinSteps and the engine rejected it. Downstream (StatusReporter) uses
// this to distinguish real queries from non-query events so the live UI
// doesn't render a rejected completion as an empty-SQL failed query and
// the per-run query counter only counts real queries.
type StepCallback func(stepNum int, action, thinking, query string, rowCount int, queryTimeMs int64, queryFixed bool, errMsg string, inputTokens, outputTokens int, warehouseID string)

// ExplorationEngineOptions configures the exploration engine.
type ExplorationEngineOptions struct {
	Client *Client
	// Executor is the single-warehouse executor. Kept for back-compat with
	// single-warehouse wiring and tests; when Executors is empty it becomes
	// the sole executor keyed by PrimaryDatasource (default "default").
	Executor *queryexec.QueryExecutor
	// Executors is the per-datasource executor map for multi-warehouse
	// discovery — one executor per warehouse id. When set it takes
	// precedence over Executor. Keys are warehouse ids (empty stored as
	// "default").
	Executors map[string]*queryexec.QueryExecutor
	// PrimaryDatasource is the datasource a query_data targets when the
	// model names none. Empty resolves to "default".
	PrimaryDatasource string
	// TableDatasource maps canonical "dataset.table" → owning datasource id
	// for the cross-datasource reference guard (multi-warehouse only). Nil
	// disables the guard (single-warehouse).
	TableDatasource map[string]string
	MaxSteps        int
	// MinSteps is a floor on the number of exploration steps before the engine
	// accepts a "done" signal from the LLM. Early done signals below this
	// threshold are rejected with a nudge and exploration continues. Zero
	// disables the floor.
	MinSteps int

	// StopOnNoNewSignal replaces the MinSteps floor with a marginal-value
	// rule: a "done" signal is rejected while recent steps are still
	// turning up something the run has not already seen, and accepted once
	// they stop. Set for a run that can query a cube-shaped datasource,
	// where a step count says nothing about coverage — see
	// exploration_stopping.go. MaxSteps is unaffected and remains the
	// runaway cap.
	//
	// A run that sets this without a StepIndexer, or whose index fails,
	// keeps the floor: novelty that cannot be measured decides nothing.
	StopOnNoNewSignal bool
	Dataset           string
	OnStep            StepCallback // optional: called after each step for live status

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

	// StepIndexer is the run-scoped vector index that receives one
	// upsert per completed step. Optional in tests, required in
	// production wiring (the orchestrator surfaces a clear error
	// when it's nil at run start).
	StepIndexer StepIndexer

	// Window is the model's effective context window (input tokens), resolved
	// at run start via the #347 chain (operator override → live auto-detect →
	// catalog → default). Used only on the reasoning-effective output-headroom
	// path (R3) to budget the raised exploration ceiling against the window.
	// 0 (tests / unresolved) disables that path — the engine keeps today's
	// fixed ceiling.
	Window int

	// OutputCap is the model's effective max output tokens (same resolution
	// chain). Bounds the raised reasoning ceiling. 0 falls back to the catalog
	// output cap for the model.
	OutputCap int

	// ReasoningEffective marks the run's analysis model as reasoning-capable —
	// either the operator enabled reasoning (the "Enable reasoning" checkbox)
	// or the catalog flags the model. Only then does the exploration output
	// ceiling get window-budgeted headroom; a non-reasoning model (Opus, GPT,
	// ...) keeps exactly today's fixed 4096 ceiling.
	ReasoningEffective bool
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

	// Normalise the executor wiring into the per-datasource map. Multi-
	// warehouse callers pass Executors + PrimaryDatasource; single-warehouse
	// callers (and tests) pass a single Executor, which becomes the sole
	// entry keyed by the primary. The empty warehouse id is stored as
	// "default" so an omitted datasource_id and a literal "default" resolve
	// to the same executor.
	primaryDS := normDatasourceID(opts.PrimaryDatasource)
	executors := make(map[string]*queryexec.QueryExecutor, len(opts.Executors)+1)
	for id, ex := range opts.Executors {
		executors[normDatasourceID(id)] = ex
	}
	if len(executors) == 0 && opts.Executor != nil {
		executors[primaryDS] = opts.Executor
	}
	datasourceIDs := make([]string, 0, len(executors))
	for id := range executors {
		datasourceIDs = append(datasourceIDs, id)
	}
	sort.Strings(datasourceIDs)

	return &ExplorationEngine{
		client:            opts.Client,
		executors:         executors,
		primaryDatasource: primaryDS,
		datasourceIDs:     datasourceIDs,
		tableDatasource:   opts.TableDatasource,
		maxSteps:          opts.MaxSteps,
		minSteps:          opts.MinSteps,
		// Not armed without an index: the rule cannot measure anything, so
		// it would reject every completion until the runaway cap while
		// reporting a reason that had never been evaluated. A run wired
		// this way keeps the floor, which is what the option documents.
		stop:              stopRule{minSteps: opts.MinSteps, byNovelty: opts.StopOnNoNewSignal && opts.StepIndexer != nil},
		dataset:           opts.Dataset,
		onStep:            opts.OnStep,
		schemaProvider:    opts.SchemaProvider,
		stepIndexer:       opts.StepIndexer,
		maxLookupsPerRun:  maxLookups,
		maxSearchesPerRun: maxSearches,
		fetchedTables:     make(map[string]struct{}),

		window:             opts.Window,
		outputCap:          opts.OutputCap,
		reasoningEffective: opts.ReasoningEffective,
	}
}

// normDatasourceID maps the empty warehouse id to the reserved "default"
// so an omitted datasource_id, a legacy single-warehouse project, and a
// literal "default" all resolve to the same executor / catalog section.
func normDatasourceID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return defaultDatasourceID
	}
	return id
}

// crossDatasourceRef returns the first qualified "dataset.table" referenced
// in the query that belongs to a datasource OTHER than targetID, plus that
// owner. Returns ("","") when the query references no foreign-datasource
// table, and always on a single-warehouse run (tableDatasource nil). The
// match strips identifier quoting ("dataset"."table" / `dataset`.`table`)
// and is case-insensitive; only dotted (qualified) names are matched so a
// bare column can't collide with a table name.
func (e *ExplorationEngine) crossDatasourceRef(query, targetID string) (string, string) {
	if len(e.tableDatasource) == 0 {
		return "", ""
	}
	// Drop the double-quote / backtick identifier quoting the prompts use
	// so `"public"."invoice_line"` collapses to `public.invoice_line`.
	norm := strings.ToLower(query)
	norm = strings.ReplaceAll(norm, `"`, "")
	norm = strings.ReplaceAll(norm, "`", "")

	// Deterministic order so the same query always reports the same table.
	tables := make([]string, 0, len(e.tableDatasource))
	for t := range e.tableDatasource {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, table := range tables {
		if !strings.Contains(table, ".") {
			continue // only qualified names are safe to match
		}
		owner := normDatasourceID(e.tableDatasource[table])
		if owner == targetID {
			continue
		}
		if containsIdentifier(norm, strings.ToLower(table)) {
			return table, owner
		}
	}
	return "", ""
}

// containsIdentifier reports whether needle appears in haystack as a whole
// identifier reference — bounded on both sides by a non-identifier byte —
// so a short table name is not matched inside a longer one (e.g.
// "public.order" must not match "public.orders_archive"). Both inputs are
// already lowercased + quote-stripped by the caller. '.' counts as a
// boundary so "dataset.table" matches even when adjacent to punctuation.
func containsIdentifier(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	for from := 0; ; {
		i := strings.Index(haystack[from:], needle)
		if i < 0 {
			return false
		}
		i += from
		var before, after byte = ' ', ' '
		if i > 0 {
			before = haystack[i-1]
		}
		if end := i + len(needle); end < len(haystack) {
			after = haystack[end]
		}
		if !isIdentByte(before) && !isIdentByte(after) {
			return true
		}
		from = i + 1
	}
}

// isIdentByte reports whether b is part of a SQL identifier (letters,
// digits, underscore). '.' is deliberately excluded so a qualified
// "dataset.table" is bounded correctly.
func isIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

// executorFor resolves the executor for a model-supplied datasource_id.
// An empty id targets the run's primary datasource. Returns the executor,
// the resolved (normalised) datasource id, and whether it is a known
// datasource — the caller surfaces a routable error to the model on false.
func (e *ExplorationEngine) executorFor(datasourceID string) (*queryexec.QueryExecutor, string, bool) {
	id := strings.TrimSpace(datasourceID)
	if id == "" {
		id = e.primaryDatasource
	}
	id = normDatasourceID(id)
	ex, ok := e.executors[id]
	return ex, id, ok
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

		action, inputTokens, outputTokens, err := e.runStepWithRetry(ctx, conversation, step)
		if err != nil {
			result.Error = err
			result.Duration = time.Since(startTime)
			return result, err
		}

		// Reject premature completion. Models — reasoning models especially —
		// are biased toward declaring completion early, so a "done" signal has
		// to earn it: either by clearing the step floor, or, on a run where a
		// step count means nothing, by the exploration having stopped turning
		// up anything new. See exploration_stopping.go.
		if action.Action == "complete" {
			if accepted, reason := e.stop.acceptDone(step, e.noveltyMeasurable()); !accepted {
				nudge, why := e.rejectionFor(reason, step)
				logger.WithFields(logger.Fields{
					"step":              step,
					"min_steps":         e.minSteps,
					"reason":            reason,
					"judged_steps":      e.stop.judged,
					"repeated_in_a_row": e.stop.consecutiveRepeats,
					"unjudged_in_a_row": e.stop.consecutiveUnjudged,
					"steps_indexed":     e.stepsIndexed,
					"steps_offered":     e.stepsIndexOffered,
				}).Warn("LLM signalled done too early — rejecting and continuing")

				conversation.AddUserMessage(nudge)

				// Record the rejected completion as a step so it's visible in
				// logs / UI without short-circuiting the run.
				result.Steps = append(result.Steps, models.ExplorationStep{
					Step:      step,
					Timestamp: time.Now(),
					Action:    "complete_rejected",
					Thinking:  action.Thinking,
					Error:     why,
					TokensIn:  inputTokens,
					TokensOut: outputTokens,
				})
				result.TotalSteps = step

				if e.onStep != nil {
					e.onStep(step, "complete_rejected", action.Thinking, "", 0, 0, false, why, inputTokens, outputTokens, "")
				}
				continue
			}
		}

		// Create exploration step. Tokens are stamped here so the per-phase
		// ExplorationStep doc carries usage data.
		explorationStep := models.ExplorationStep{
			Step:      step,
			Timestamp: time.Now(),
			Action:    action.Action,
			Thinking:  action.Thinking,
			TokensIn:  inputTokens,
			TokensOut: outputTokens,
		}

		// Execute the action
		logger.WithFields(logger.Fields{
			"step":     step,
			"action":   action.Action,
			"thinking": action.Thinking[:min(len(action.Thinking), 100)],
		}).Info("Executing exploration action")

		actionResult := e.executeAction(ctx, action, &explorationStep)

		// Index the completed step into the per-run vector index so
		// the analysis phase can semantically rank steps against
		// each area's identity. Failure is non-fatal — it degrades
		// the analysis selection back to keyword-only behaviour but
		// must not abort exploration.
		// Judge how much new ground this step broke, BEFORE indexing it —
		// once it is in the index it is its own nearest neighbour. Only the
		// novelty rule consumes this; a floor run skips the call entirely
		// rather than paying for a measurement it will not read.
		if e.stop.byNovelty && noveltySubject(explorationStep) {
			e.stop.observe(e.repeatsEarlierWork(ctx, explorationStep))
		}

		if e.stepIndexer != nil {
			compactRowCount := 0
			if explorationStep.CompactResult != nil {
				compactRowCount = explorationStep.CompactResult.RowCount
			}
			logger.WithFields(logger.Fields{
				"step":              step,
				"action":            explorationStep.Action,
				"row_count":         explorationStep.RowCount,
				"compact_row_count": compactRowCount,
				"has_error":         explorationStep.Error != "",
			}).Debug("exploration: indexing step into per-run vector index")
			e.stepsIndexOffered++
			if err := e.stepIndexer.Upsert(ctx, explorationStep); err != nil {
				logger.WithFields(logger.Fields{
					"step":  step,
					"error": err.Error(),
				}).Warn("Run-step index upsert failed; analysis ranking quality will degrade for this step")
			} else {
				// What the index is actually holding. The stopping rule reads
				// it to tell "this run has found nothing like this before"
				// from "this index is not storing anything" — the two look
				// identical from a search, and only one of them is evidence.
				e.stepsIndexed++
			}
		}

		// Add to results
		result.Steps = append(result.Steps, explorationStep)
		result.TotalSteps = step

		// Report step for live status
		if e.onStep != nil {
			errMsg := explorationStep.Error
			e.onStep(step, action.Action, action.Thinking, explorationStep.Query, explorationStep.RowCount, explorationStep.ExecutionTimeMs, explorationStep.Fixed, errMsg, inputTokens, outputTokens, explorationStep.WarehouseID)
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

	// Datasource is the target warehouse (datasource) id for a
	// query_data / lookup_schema action on a multi-warehouse project.
	// Empty resolves to the run's primary datasource, so single-warehouse
	// projects and legacy prompts (which never set it) keep working. Each
	// SQL statement still runs against exactly one datasource — the agent
	// hops between datasources across steps, never within one statement.
	Datasource string `json:"datasource_id"`

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
func (e *ExplorationEngine) runStepWithRetry(ctx context.Context, conversation *Conversation, step int) (*ExplorationAction, int, int, error) {
	var lastParseErr error
	// Parse-retry rounds collapse onto one home document (the
	// ExplorationStep / RunStep), so we sum across them.
	var usage gollm.UsageAccumulator

	// NOTE: exploration deliberately does NOT constrain the action via
	// provider-native structured output. The action is a union (query /
	// lookup_schema / search_tables / done) with an optional `thinking`
	// field; forcing a response_format made models emit only the minimal
	// shape — dropping `thinking` (so the run-step log lost its reasoning)
	// and, on Bedrock's OpenAI-compat open models (Qwen, GLM), corrupting
	// the JSON outright. The tolerant extractor + repair retry are the net;
	// they don't degrade the model. See the #341 follow-up.
	//
	// inputEst is measured once per step (the conversation is fixed for the
	// duration of a step's parse retries) and only affects the reasoning path.
	maxTokens := e.explorationOutputTokens(conversationInputEst(conversation))

	for attempt := 0; attempt <= maxParseRetries; attempt++ {
		llmStart := time.Now()
		response, err := e.client.CreateMessage(
			ctx,
			conversation.GetMessages(),
			conversation.GetSystemPrompt(),
			maxTokens,
		)
		if err != nil {
			logger.WithFields(logger.Fields{
				"step":    step,
				"attempt": attempt,
				"error":   err.Error(),
			}).Error("LLM call failed during exploration")
			in, out := usage.Totals()
			return nil, in, out, fmt.Errorf("step %d: failed to get LLM response: %w", step, err)
		}

		usage.Add(response.Usage.InputTokens, response.Usage.OutputTokens)

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

		// The assistant turn recorded in the conversation must never be empty:
		// a reasoning model can return its output on the reasoning channel with
		// an empty Content, and some providers (observed: Moonshot/Kimi via
		// LiteLLM) reject an empty assistant message on the FOLLOW-UP request —
		// turning a recoverable parse-retry into a hard 400. Carry the reasoning
		// (or a short placeholder) as the turn text so the retry conversation
		// stays valid on every provider. parseAction still runs on Content, so an
		// empty Content correctly falls through to a reformat nudge + retry.
		turnText := responseText
		if turnText == "" {
			if strings.TrimSpace(response.Reasoning) != "" {
				turnText = response.Reasoning
			} else {
				turnText = emptyAssistantTurnPlaceholder
			}
		}
		conversation.AddAssistantMessage(turnText)

		action, err := e.parseAction(responseText)
		if err == nil {
			in, out := usage.Totals()
			return action, in, out, nil
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

		conversation.AddUserMessage(explorationRepairNudge(err))
	}

	in, out := usage.Totals()
	return nil, in, out, fmt.Errorf("step %d: unable to parse LLM response after %d attempts: %w", step, maxParseRetries+1, lastParseErr)
}

// ParseAction parses the LLM's response into an ExplorationAction, restricted
// to the action types in `allowed`. Pass nil or empty for "any action" — the
// explorer's full set. The verifier (insight validation) passes
// {"lookup_schema", "query_data"} to keep the model from "completing"
// mid-verify.
//
// The response must contain a JSON object with ONE of:
//   - {"query": "SELECT ..."}              → execute the query
//   - {"done": true, "summary": "..."}     → exploration finished
//   - {"action": "query_data" | "complete" | ...}  (legacy)
//
// A response with no parseable action JSON, or one whose dispatched action is
// not in `allowed`, is an error. Callers retry the turn rather than silently
// treating it as "complete" — early termination (previously caused by prose
// matching "done"/"finished" or missing fields) is the bug this parser is
// designed to prevent.
func ParseAction(response string, allowed []string) (*ExplorationAction, error) {
	jsonStr := extractJSON(response)
	if jsonStr == "" {
		return nil, fmt.Errorf("no action JSON object found in response")
	}

	var action ExplorationAction
	if err := json.Unmarshal([]byte(jsonStr), &action); err != nil {
		return nil, fmt.Errorf("failed to parse action JSON: %w", err)
	}

	// Tool-use envelope normalisation. Anthropic Claude and OpenAI
	// function-calling models emit `{"name": "lookup_schema", "input":
	// {"tables": [...]}}` even when the prompt asks for the key-driven
	// shape — that's how they were trained. Translate it into the
	// key-driven fields the rest of the parser already handles, so a
	// single switch below dispatches both shapes.
	normaliseToolEnvelope(jsonStr, &action)

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

	if len(allowed) > 0 && !actionAllowed(action.Action, allowed) {
		return nil, fmt.Errorf("action %q is not in this caller's allow-list (allowed: %v)", action.Action, allowed)
	}

	return &action, nil
}

func actionAllowed(action string, allowed []string) bool {
	for _, a := range allowed {
		if a == action {
			return true
		}
	}
	return false
}

// parseAction is the explorer's thin wrapper that delegates to ParseAction
// with the explorer's full action set (nil = any). Kept as a method so
// existing exploration tests don't need refactoring.
func (e *ExplorationEngine) parseAction(response string) (*ExplorationAction, error) {
	return ParseAction(response, nil)
}

// normaliseToolEnvelope detects an Anthropic / OpenAI tool-use call
// envelope (`{"name": "<tool>", "input": {...}}`) inside the parsed
// JSON and rewrites the key-driven fields on the action so the
// downstream switch in parseAction can dispatch it without caring
// which shape the model used.
//
// The envelope arrives from models that were RLHF'd into emitting
// tool-use even when the prompt asks for inline JSON actions —
// observed on Claude 4 and gpt-4.1 against this codebase. Rather
// than fight the model, we accept both shapes.
//
// Supported tool names: lookup_schema, search_tables, query_data,
// complete. Inputs we know about:
//
//	lookup_schema: {"tables": ["dataset.t1", ...]} — array of refs.
//	search_tables: {"query": "...", "top_k": <int>} — query + optional k.
//	query_data:    {"query": "SELECT ...", "purpose": "..."} — SQL.
//	complete:      {"summary": "..."} — exploration done.
//
// We do NOT touch fields the action already populated (key-driven
// shape wins on conflict) so a malformed envelope can't silently
// override a clean key-driven payload in the same turn.
func normaliseToolEnvelope(jsonStr string, action *ExplorationAction) {
	var env struct {
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &env); err != nil || env.Name == "" {
		return
	}

	switch env.Name {
	case "lookup_schema":
		if len(action.LookupSchema) > 0 {
			return
		}
		var in struct {
			Tables []string `json:"tables"`
		}
		if err := json.Unmarshal(env.Input, &in); err == nil && len(in.Tables) > 0 {
			action.LookupSchema = in.Tables
		}
	case "search_tables":
		if strings.TrimSpace(action.SearchTables) != "" {
			return
		}
		var in struct {
			Query string `json:"query"`
			TopK  int    `json:"top_k"`
		}
		if err := json.Unmarshal(env.Input, &in); err == nil && strings.TrimSpace(in.Query) != "" {
			action.SearchTables = in.Query
			if action.SearchTopK == 0 {
				action.SearchTopK = in.TopK
			}
		}
	case "query_data":
		if action.Query != "" {
			return
		}
		var in struct {
			Query   string `json:"query"`
			Purpose string `json:"purpose"`
		}
		if err := json.Unmarshal(env.Input, &in); err == nil && strings.TrimSpace(in.Query) != "" {
			action.Query = in.Query
			if action.QueryPurpose == "" {
				action.QueryPurpose = in.Purpose
			}
		}
	case "complete":
		if action.Done || action.Action == "complete" {
			return
		}
		var in struct {
			Summary string `json:"summary"`
			Reason  string `json:"reason"`
		}
		_ = json.Unmarshal(env.Input, &in)
		action.Done = true
		if action.Summary == "" {
			action.Summary = in.Summary
		}
		if action.Reason == "" {
			if in.Reason != "" {
				action.Reason = in.Reason
			} else {
				action.Reason = in.Summary
			}
		}
	}
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
// extractJSON is a free function — the body never uses *ExplorationEngine
// state. Lifting it to package level lets the verifier reuse the same parser
// without depending on the engine.
func extractJSON(text string) string {
	// Mask <think>/<thinking> reasoning blocks — position-preserving: each is
	// replaced by an equal-length run of spaces — so their contents are ignored
	// when scanning for the action, while candidates are sliced from the
	// ORIGINAL text. An action JSON that legitimately contains "<think>" inside
	// a string literal (e.g. a query filtering log text) is therefore never
	// mutated (issue #341). Selection: the last candidate carrying a recognised
	// action key wins, with the last balanced object as the lenient fallback so
	// ParseAction can emit its precise error and drive a targeted retry.
	mask := maskReasoningBlocks(text)
	candidates := collectJSONCandidates(mask, text)
	if got := lastRecognizedAction(candidates); got != "" {
		return got
	}
	if len(candidates) > 0 {
		return candidates[len(candidates)-1]
	}
	return ""
}

// lastRecognizedAction returns the last candidate carrying a recognised action
// key, or "" if none.
func lastRecognizedAction(candidates []string) string {
	for i := len(candidates) - 1; i >= 0; i-- {
		if jsonHasActionKey(candidates[i]) {
			return candidates[i]
		}
	}
	return ""
}

// reasoningBlockRe matches a complete <think>…</think> / <thinking>…</thinking>
// pair (case-insensitive, across newlines). reasoningDanglingRe matches an
// unclosed trailing block — truncated output whose reasoning ran to the token
// limit before any action was emitted.
var (
	reasoningBlockRe    = regexp.MustCompile(`(?is)<(think|thinking)\b[^>]*>.*?</(think|thinking)>`)
	reasoningDanglingRe = regexp.MustCompile(`(?is)<(think|thinking)\b[^>]*>.*$`)
)

// maskReasoningBlocks replaces <think>/<thinking> reasoning blocks (matched
// pairs, then any unclosed trailing block) with equal-length runs of spaces.
// Byte positions are preserved so a caller can scan the mask for structure and
// slice the original text at the same offsets without mutating it.
func maskReasoningBlocks(text string) string {
	blank := func(s string) string { return strings.Repeat(" ", len(s)) }
	text = reasoningBlockRe.ReplaceAllStringFunc(text, blank)
	text = reasoningDanglingRe.ReplaceAllStringFunc(text, blank)
	return text
}

// collectJSONCandidates returns every plausible JSON object, scanning `scan`
// for structure (so masked reasoning is ignored) and slicing the returned
// bytes from `source` (so the originals are never mutated). scan and source
// must be the same length. Fenced blocks are listed first so that, when no
// candidate carries an action key, the fallback prefers raw trailing JSON.
func collectJSONCandidates(scan, source string) []string {
	out := fencedJSONBlocks(scan, source)
	out = append(out, findBalancedJSONObjects(scan, source)...)
	return out
}

// fencedJSONBlocks returns every markdown-fenced block whose body starts with
// '{'. Language tags json / JSON / (empty) are accepted. Fences are located in
// `scan`; each body is sliced from `source` at the same offsets.
func fencedJSONBlocks(scan, source string) []string {
	var out []string
	pos := 0
	for {
		rel := strings.Index(scan[pos:], "```")
		if rel < 0 {
			break
		}
		start := pos + rel + 3
		bodyStart := start
		if nl := strings.IndexByte(scan[start:], '\n'); nl >= 0 {
			if lang := strings.TrimSpace(scan[start : start+nl]); lang == "" || strings.EqualFold(lang, "json") {
				bodyStart = start + nl + 1
			}
		}
		rel2 := strings.Index(scan[bodyStart:], "```")
		if rel2 < 0 {
			break
		}
		bodyEnd := bodyStart + rel2
		lo, hi := trimSpaceBounds(scan, bodyStart, bodyEnd)
		if lo < hi && scan[lo] == '{' {
			out = append(out, source[lo:hi])
		}
		pos = bodyEnd + 3
	}
	return out
}

// trimSpaceBounds returns the [lo,hi) sub-range of s with leading and trailing
// ASCII whitespace removed.
func trimSpaceBounds(s string, lo, hi int) (int, int) {
	for lo < hi && isASCIISpace(s[lo]) {
		lo++
	}
	for hi > lo && isASCIISpace(s[hi-1]) {
		hi--
	}
	return lo, hi
}

func isASCIISpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// findBalancedJSONObjects returns every balanced top-level { ... } substring,
// in order. Braces are located by scanning `scan` (so masked reasoning is
// ignored); each object is sliced from `source` at the same offsets (so an
// object containing "<think>" inside a string literal is returned verbatim).
// String literals are tracked so { / } inside strings (e.g. inside a SQL
// query) don't break the brace count. scan and source must be the same length.
func findBalancedJSONObjects(scan, source string) []string {
	var out []string
	for i := 0; i < len(scan); i++ {
		if scan[i] != '{' {
			continue
		}
		depth := 0
		inString := false
		escaped := false
		balancedEnd := -1
		for j := i; j < len(scan); j++ {
			c := scan[j]
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
					balancedEnd = j
				}
			}
			if balancedEnd >= 0 {
				break
			}
		}
		if balancedEnd < 0 {
			// Unbalanced from this '{' — stop. A genuinely malformed or
			// truncated object (e.g. an unterminated `{"plan":{…}` or
			// `{"plan":[{…}]`) must NOT have a nested fragment extracted as an
			// action; extraction returns nothing so the caller re-prompts
			// (issue #341). Reasoning prose that would otherwise carry stray
			// braces is already masked out before we reach here, so this
			// rarely fires on well-formed turns.
			break
		}
		out = append(out, source[i:balancedEnd+1])
		i = balancedEnd // resume after this object
	}
	return out
}

// jsonHasActionKey reports whether the JSON-encoded object declares a field the
// exploration parser understands: a key-driven action (query, done, action,
// lookup_schema, search_tables) or a tool-use envelope ({"name": "<known
// tool>", "input": {…}}). Recognition is presence-based and lenient — e.g.
// {"query": ""} still counts — so a recognised-but-incomplete action reaches
// ParseAction, which emits a precise error and drives a targeted retry, rather
// than extractJSON silently guessing (issue #341).
func jsonHasActionKey(s string) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &probe); err != nil {
		return false
	}
	for _, k := range []string{"query", "done", "action", "lookup_schema", "search_tables"} {
		if _, ok := probe[k]; ok {
			return true
		}
	}
	// Tool-use envelope: {"name": "<known tool>", "input": {…}}.
	nameRaw, hasName := probe["name"]
	if _, hasInput := probe["input"]; hasName && hasInput {
		var name string
		if json.Unmarshal(nameRaw, &name) == nil {
			switch name {
			case "query_data", "lookup_schema", "search_tables", "complete":
				return true
			}
		}
	}
	return false
}

// explorationRepairNudge builds the corrective instruction appended to the
// conversation when a response could not be parsed. It is reason-aware:
// "no JSON at all" and "JSON but no usable action" get different leads so the
// model corrects the specific failure instead of getting the same generic
// re-ask each time (issue #341). The menu lists every recognised action shape
// (including the schema-discovery actions the old nudge omitted).
func explorationRepairNudge(parseErr error) string {
	const menu = "Respond with EXACTLY ONE JSON object, no prose and no <think> blocks around it, matching one of:\n" +
		`  {"thinking": "...", "query": "SELECT ..."}        — run one read-only query, or` + "\n" +
		`  {"thinking": "...", "lookup_schema": ["ds.tbl"]}  — inspect table schemas, or` + "\n" +
		`  {"thinking": "...", "search_tables": "..."}       — find relevant tables, or` + "\n" +
		`  {"done": true, "summary": "..."}                  — only when exploration is truly finished.`
	lead := "Your previous response could not be parsed as an exploration action."
	if parseErr != nil {
		switch msg := parseErr.Error(); {
		case strings.Contains(msg, "no action JSON"):
			lead = "Your previous response contained no JSON action object (only prose or reasoning)."
		case strings.Contains(msg, "no query, lookup_schema"):
			lead = "Your previous JSON had no usable action — it must carry one of query, lookup_schema, search_tables, or done."
		}
	}
	return lead + " " + menu + "\nDo not emit planning JSON before the action, and do not wrap it in markdown fences."
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

	// Route the statement to the datasource the model targets (empty →
	// the run's primary). Each SQL statement runs against exactly one
	// datasource; the agent hops between datasources across steps. An
	// unknown id is reported back so the model can retry against a valid
	// datasource rather than the run failing.
	executor, datasourceID, ok := e.executorFor(action.Datasource)
	if !ok {
		step.WarehouseID = ""
		step.Error = fmt.Sprintf("unknown datasource_id %q", action.Datasource)
		return fmt.Sprintf(
			"Query rejected: unknown datasource_id %q. Target one of these datasource ids: %s.",
			action.Datasource, strings.Join(e.datasourceIDs, ", "),
		)
	}
	// Cross-datasource reference guard: reject a statement that names a
	// table owned by a DIFFERENT datasource than its datasource_id before
	// it reaches the wrong engine — otherwise that engine's SQL fixer may
	// rewrite the attempted cross-engine join into a valid-but-wrong query.
	// Point the model at the owning datasource + the value-passing pattern.
	if foreignTable, owner := e.crossDatasourceRef(action.Query, datasourceID); foreignTable != "" {
		step.WarehouseID = datasourceID
		step.Error = fmt.Sprintf("cross-datasource reference: %q belongs to datasource %q, not %q", foreignTable, owner, datasourceID)
		return fmt.Sprintf(
			"Query rejected: table `%s` belongs to datasource %q, but this statement targets datasource %q. "+
				"A single query_data statement may reference only tables in its datasource_id — there is no cross-datasource join. "+
				"Query %q for a bounded set of key values first, then filter a %q query with those values inlined (value-passing).",
			foreignTable, owner, datasourceID, owner, datasourceID,
		)
	}

	// Attribute the step to the datasource it ran against, and stamp the
	// warehouse id on the context so the per-datasource governance /
	// read-only middleware scopes this statement to that datasource.
	step.WarehouseID = datasourceID
	ctx = gowarehouse.WithWarehouseID(ctx, datasourceID)

	// Bind the executor to this step number before invoking so the
	// per-attempt FixHistory entries (and the executor's debug-log
	// emissions) record the parent step the fix loop ran for. Without
	// this every FixAttempt.Step would default to 0, indistinguishable
	// across steps in a flattened export.
	executor.SetStep(step.Step)

	queryStart := time.Now()

	result, err := executor.Execute(ctx, action.Query, action.QueryPurpose)

	step.ExecutionTimeMs = time.Since(queryStart).Milliseconds()

	if err != nil {
		step.Error = err.Error()
		step.Fixed = false
		// Carry the partial fix history even on failure paths — the
		// executor now returns a non-nil result containing every
		// attempt it made (including failed fix calls with FixerError
		// set), and dropping them here would lose exactly the
		// negative-example data downstream tooling cares about.
		if result != nil {
			step.FixAttempts = result.FixAttempts
			step.FixHistory = result.FixHistory
		}
		logger.WithField("error", err.Error()).Error("Query execution failed")
		return fmt.Sprintf("Query failed: %s\n\nPlease try a different approach.", err.Error())
	}

	step.QueryResult = result.Data
	step.RowCount = result.RowCount
	step.FixAttempts = result.FixAttempts
	step.Fixed = result.Fixed
	step.FixHistory = result.FixHistory
	// What the source said about the fidelity of these rows. It is knowable
	// only here — the query succeeded and the rows look complete, so nothing
	// downstream could re-derive that some were withheld.
	step.Quality = result.Quality

	// Build the per-step compact digest exactly once. Storing it on
	// the step means the analysis phase can render the digest into
	// the prompt without re-walking the raw rows, and the legacy
	// keyword-match selector path stops being the only consumer of
	// step.QueryResult — analysis no longer ships the raw blob.
	compact := gomodels.BuildCompactResult(result.Data)
	step.CompactResult = &compact
	logger.WithFields(logger.Fields{
		"step":          step.Step,
		"row_count":     compact.RowCount,
		"columns":       len(compact.Columns),
		"has_all_rows":  compact.AllRows != nil,
		"has_tail_rows": compact.TailRows != nil,
	}).Debug("exploration: built compact digest for step")

	return e.formatQuerySuccess(result)
}

// formatQuerySuccess renders the message the exploring model reads after a
// query succeeds.
//
// Its own function so the message can be asserted on directly. What it does
// with a degraded result is the part worth pinning, and that was previously
// reachable only through a full query execution — which is how a caveat could
// be carried onto the step and still never reach the model.
func (e *ExplorationEngine) formatQuerySuccess(result *queryexec.ExecuteResult) string {
	resultMsg := "Query executed successfully.\n\n"
	resultMsg += fmt.Sprintf("Rows returned: %d\n", result.RowCount)
	resultMsg += fmt.Sprintf("Execution time: %dms\n", result.ExecutionTimeMs)

	if result.Fixed {
		resultMsg += fmt.Sprintf("Note: Query was automatically fixed (%d attempts)\n", result.FixAttempts)
	}

	// The source's own caveats go in FRONT of the rows, not after them. The
	// rows look complete either way, so a model that reads them first has
	// already formed its conclusion by the time it reaches a footnote.
	resultMsg += gowarehouse.CaveatInstruction(result.Quality)

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

	return formatLookupResult(res, already, truncatedAtCallCap, e.lookupsUsed, e.maxLookupsPerRun, len(e.datasourceIDs) > 1)
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

	return formatSearchResult(query, hits, e.searchesUsed, e.maxSearchesPerRun, len(e.datasourceIDs) > 1)
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
func formatLookupResult(res LookupResult, already []string, truncatedAtCallCap bool, lookupsUsed, maxLookups int, showDatasource bool) string {
	var b strings.Builder

	if len(res.Tables) == 0 {
		b.WriteString("No schemas resolved for the requested tables.")
	} else {
		for i, t := range res.Tables {
			if i > 0 {
				b.WriteString("\n\n")
			}
			// On a multi-warehouse run, name the datasource so the model
			// targets the right datasource_id when it queries this table.
			if showDatasource && t.Datasource != "" {
				fmt.Fprintf(&b, "Schema for `%s` (datasource: %s) (%s rows):\n", t.Table, t.Datasource, formatRowCountShort(t.RowCount))
			} else {
				fmt.Fprintf(&b, "Schema for `%s` (%s rows):\n", t.Table, formatRowCountShort(t.RowCount))
			}
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
func formatSearchResult(query string, hits []SearchHit, searchesUsed, maxSearches int, showDatasource bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Search results for %q:\n", query)

	if len(hits) == 0 {
		b.WriteString("(no matches; try different terms or pick from the catalog in the system prompt)")
	} else {
		anyTable := false
		for i, h := range hits {
			// A hit that is not a table must not be rendered as one. Backticked
			// and followed by "issue lookup_schema with the table refs", a
			// metric reads as a SQL table — and the model then looks up a
			// schema that does not exist, or writes a FROM clause against it.
			if h.Kind != "" {
				fmt.Fprintf(&b, "%d. `%s` (%s) — score=%.3f", i+1, h.Table, h.Kind, h.Score)
			} else {
				anyTable = true
				fmt.Fprintf(&b, "%d. `%s` — %s rows — score=%.3f", i+1, h.Table, formatRowCountShort(h.RowCount), h.Score)
			}
			// On a multi-warehouse run, tag each hit with its datasource so
			// the model knows which datasource_id to target on a follow-up.
			if showDatasource && h.Datasource != "" {
				fmt.Fprintf(&b, " — datasource: %s", h.Datasource)
			}
			if h.Blurb != "" {
				b.WriteString("\n   ")
				b.WriteString(h.Blurb)
			}
			b.WriteByte('\n')
		}
		// Only advise lookup_schema when something in the result actually has
		// a schema to look up. Telling the model to do it for a result made
		// only of metrics and dimensions sends it after a table that does not
		// exist.
		if anyTable {
			b.WriteString("\nIssue lookup_schema with the table refs you want full column detail for before querying them.")
		}
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
