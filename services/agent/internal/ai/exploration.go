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
	dataset  string
	onStep   StepCallback
}

// StepCallback is called after each exploration step with live progress data.
type StepCallback func(stepNum int, thinking, query string, rowCount int, queryTimeMs int64, queryFixed bool, errMsg string)

// ExplorationEngineOptions configures the exploration engine.
type ExplorationEngineOptions struct {
	Client       *Client
	Executor     *queryexec.QueryExecutor
	MaxSteps     int
	Dataset      string
	OnStep       StepCallback // optional: called after each step for live status
}

// NewExplorationEngine creates a new exploration engine.
func NewExplorationEngine(opts ExplorationEngineOptions) *ExplorationEngine {
	if opts.MaxSteps == 0 {
		opts.MaxSteps = 100
	}

	return &ExplorationEngine{
		client:   opts.Client,
		executor: opts.Executor,
		maxSteps: opts.MaxSteps,
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

		// Call LLM to decide next action
		llmStart := time.Now()
		response, err := e.client.CreateMessage(
			ctx,
			conversation.GetMessages(),
			conversation.GetSystemPrompt(),
			4096,
		)
		if err != nil {
			logger.WithFields(logger.Fields{
				"step":  step,
				"error": err.Error(),
			}).Error("LLM call failed during exploration")
			result.Error = fmt.Errorf("step %d: failed to get LLM response: %w", step, err)
			result.Duration = time.Since(startTime)
			return result, result.Error
		}

		logger.WithFields(logger.Fields{
			"step":       step,
			"tokens_in":  response.Usage.InputTokens,
			"tokens_out": response.Usage.OutputTokens,
			"llm_ms":     time.Since(llmStart).Milliseconds(),
		}).Debug("LLM response received")

		// Extract response text
		responseText := ""
		if len(response.Content) > 0 {
			responseText = response.Content
		}

		// Add to conversation
		conversation.AddAssistantMessage(responseText)

		// Parse LLM's decision
		action, err := e.parseAction(responseText)
		if err != nil {
			logger.WithFields(logger.Fields{
				"step":     step,
				"error":    err.Error(),
				"response": responseText[:min(len(responseText), 200)],
			}).Warn("Failed to parse action from LLM response, treating as complete")
			action = &ExplorationAction{
				Action: "complete",
				Reason: "Could not parse action",
			}
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
			e.onStep(step, action.Thinking, explorationStep.Query, explorationStep.RowCount, explorationStep.ExecutionTimeMs, explorationStep.Fixed, errMsg)
		}

		// Check if exploration is complete
		if action.Action == "complete" {
			result.Completed = true
			result.CompletionMsg = action.Reason
			logger.WithField("step", step).Info("Exploration completed by Claude")
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

// parseAction parses Claude's response into an action
func (e *ExplorationEngine) parseAction(response string) (*ExplorationAction, error) {
	// Claude can respond in two formats:
	//
	// 1. Query: {"thinking": "...", "query": "SELECT ..."}
	// 2. Done: {"done": true, "summary": "..."}
	//
	// Legacy format also supported:
	// {"action": "query_data", "thinking": "...", "query": "..."}

	// Try to extract JSON from response
	jsonStr := e.extractJSON(response)
	if jsonStr == "" {
		// If no JSON, try to infer action from text
		return e.inferActionFromText(response)
	}

	var action ExplorationAction
	if err := json.Unmarshal([]byte(jsonStr), &action); err != nil {
		return nil, fmt.Errorf("failed to parse action JSON: %w", err)
	}

	// Normalize action field for backward compatibility
	switch {
	case action.Done:
		action.Action = "complete"
		if action.Reason == "" {
			action.Reason = action.Summary
		}
	case action.Query != "":
		action.Action = "query_data"
	case action.Action == "":
		// No action specified, assume complete
		action.Action = "complete"
		action.Done = true
	}

	return &action, nil
}

// extractJSON extracts JSON from response (handles markdown code blocks)
func (e *ExplorationEngine) extractJSON(text string) string {
	// Look for ```json code blocks
	if strings.Contains(text, "```json") {
		start := strings.Index(text, "```json")
		if start == -1 {
			return ""
		}
		start += 7 // len("```json")

		end := strings.Index(text[start:], "```")
		if end == -1 {
			return ""
		}

		return strings.TrimSpace(text[start : start+end])
	}

	// Look for generic ``` code blocks
	if strings.Contains(text, "```") {
		start := strings.Index(text, "```")
		if start == -1 {
			return ""
		}
		start += 3

		end := strings.Index(text[start:], "```")
		if end == -1 {
			return ""
		}

		jsonCandidate := strings.TrimSpace(text[start : start+end])
		// Check if it looks like JSON
		if strings.HasPrefix(jsonCandidate, "{") {
			return jsonCandidate
		}
	}

	// Look for raw JSON (starts with {)
	if strings.Contains(text, "{") {
		start := strings.Index(text, "{")
		// Find matching closing brace
		braceCount := 0
		for i := start; i < len(text); i++ {
			switch text[i] {
			case '{':
				braceCount++
			case '}':
				braceCount--
				if braceCount == 0 {
					return text[start : i+1]
				}
			}
		}
	}

	return ""
}

// inferActionFromText tries to infer action from plain text
func (e *ExplorationEngine) inferActionFromText(text string) (*ExplorationAction, error) {
	textLower := strings.ToLower(text)

	// Check for completion signals
	if strings.Contains(textLower, "complete") || strings.Contains(textLower, "done") ||
		strings.Contains(textLower, "finished") {
		return &ExplorationAction{
			Action:   "complete",
			Thinking: text,
			Reason:   "Exploration complete",
		}, nil
	}

	// Check for SQL query
	textUpper := strings.ToUpper(text)
	if strings.Contains(textUpper, "SELECT") {
		return &ExplorationAction{
			Action:       "query_data",
			Thinking:     "Executing query",
			QueryPurpose: "Data exploration",
			Query:        text,
		}, nil
	}

	// Default to complete if we can't parse
	return &ExplorationAction{
		Action:   "complete",
		Thinking: text,
		Reason:   "Could not parse action",
	}, nil
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

