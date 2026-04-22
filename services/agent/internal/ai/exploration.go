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

	return &ExplorationEngine{
		client:   opts.Client,
		executor: opts.Executor,
		maxSteps: opts.MaxSteps,
		minSteps: opts.MinSteps,
		dataset:  opts.Dataset,
		onStep:   opts.OnStep,
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

// ExplorationAction represents Claude's decision
type ExplorationAction struct {
	// Simple query mode
	Thinking string                 // Claude's reasoning
	Query    string                 // SQL query to execute
	Done     bool                   // True when exploration is complete
	Summary  string                 // Summary when done

	// Legacy fields (deprecated but kept for compatibility)
	Action       string
	QueryPurpose string
	AnalysisType string
	Data         map[string]interface{}
	Reason       string
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
	case action.Action == "complete":
		// Legacy explicit complete — accept.
	case action.Action == "query_data" && action.Query != "":
		// Legacy explicit query — accept.
	default:
		// JSON parsed but carries neither a query nor an explicit completion signal.
		// Fail loudly so the caller can re-prompt, instead of silently terminating.
		return nil, fmt.Errorf("action JSON has no query, done flag, or recognized action (got action=%q)", action.Action)
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

// executeAction executes the action and returns result message
func (e *ExplorationEngine) executeAction(
	ctx context.Context,
	action *ExplorationAction,
	step *models.ExplorationStep,
) string {
	switch action.Action {
	case "query_data":
		return e.executeQuery(ctx, action, step)

	case "explore_schema":
		return e.exploreSchema(ctx, action, step)

	case "analyze_pattern":
		return e.analyzePattern(ctx, action, step)

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

// exploreSchema explores table schemas
func (e *ExplorationEngine) exploreSchema(
	ctx context.Context,
	action *ExplorationAction,
	step *models.ExplorationStep,
) string {
	// Schema exploration would typically be provided upfront
	// For now, return a message
	return "Schema information is available in the initial context. Please refer to it for table structures."
}

// analyzePattern analyzes a pattern
func (e *ExplorationEngine) analyzePattern(
	ctx context.Context,
	action *ExplorationAction,
	step *models.ExplorationStep,
) string {
	step.IsInsight = true

	return fmt.Sprintf("Pattern analysis recorded: %s\n\nContinue with next step.", action.AnalysisType)
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
// The system prompt already contains schema, filter rules, analysis areas, and profile.
// This message just kicks off the exploration loop.
func (e *ExplorationEngine) buildInitialMessage(explorationCtx ExplorationContext) string {
	var msg strings.Builder

	msg.WriteString("Begin your data exploration.\n\n")
	fmt.Fprintf(&msg, "You have up to %d exploration steps. ", e.maxSteps)
	msg.WriteString("Follow the rules and format described in the system prompt.\n")

	return msg.String()
}

