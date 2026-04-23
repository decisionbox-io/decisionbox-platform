package debug

import (
	"context"
	"sync"
	"time"

	logger "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/google/uuid"
)

// Logger provides comprehensive debug logging for AI Discovery
// It wraps the MongoDB debug log repository and provides convenient methods
// for logging various types of operations
type Logger struct {
	repo           *database.DebugLogRepository
	appID          string
	discoveryRunID string
	mu             sync.RWMutex
	enabled        bool

	// Stats tracking
	totalQueries       int
	totalLLMCalls   int
	validationFailures int
}

// LoggerOptions configures the debug logger
type LoggerOptions struct {
	Repo    *database.DebugLogRepository
	AppID   string
	Enabled bool
	// DiscoveryRunID is the ID used to key all log entries written by this
	// logger. In production this is the hex string of the `discovery_runs._id`
	// ObjectId so the dashboard can join `discovery_debug_logs` back to the
	// run. Leave empty to auto-generate a UUID (useful in tests or when the
	// agent is invoked outside the API — the logger still works, but the logs
	// won't be joinable to a run).
	DiscoveryRunID string
}

// NewLogger creates a new debug logger
func NewLogger(opts LoggerOptions) *Logger {
	discoveryRunID := opts.DiscoveryRunID
	if discoveryRunID == "" {
		discoveryRunID = uuid.New().String()
	}

	l := &Logger{
		repo:           opts.Repo,
		appID:          opts.AppID,
		discoveryRunID: discoveryRunID,
		enabled:        opts.Enabled,
	}

	if opts.Enabled {
		logger.WithFields(logger.Fields{
			"app_id":           opts.AppID,
			"discovery_run_id": discoveryRunID,
		}).Info("Debug logging enabled for this discovery run")
	}

	return l
}

// GetDiscoveryRunID returns the unique ID for this discovery run
func (l *Logger) GetDiscoveryRunID() string {
	return l.discoveryRunID
}

// GetAppID returns the app ID
func (l *Logger) GetAppID() string {
	return l.appID
}

// IsEnabled returns whether debug logging is enabled
func (l *Logger) IsEnabled() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.enabled
}

// SetEnabled enables or disables debug logging
func (l *Logger) SetEnabled(enabled bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.enabled = enabled
}

// LogBigQuery logs a BigQuery query execution
func (l *Logger) LogBigQuery(
	ctx context.Context,
	step int,
	phase string,
	query, purpose string,
	results []map[string]interface{},
	rowCount int,
	durationMs int64,
	err error,
	fixAttempts int,
	fixedQuery string,
) {
	l.mu.Lock()
	l.totalQueries++
	l.mu.Unlock()

	if !l.IsEnabled() || l.repo == nil {
		return
	}

	l.repo.LogBigQueryExecution(
		ctx,
		l.appID,
		l.discoveryRunID,
		step,
		phase,
		query,
		purpose,
		results,
		rowCount,
		durationMs,
		err,
		fixAttempts,
		fixedQuery,
	)
}

// LogLLM logs an LLM API request (any provider — name is historical)
func (l *Logger) LogLLM(
	ctx context.Context,
	step int,
	phase string,
	model, systemPrompt, prompt, response string,
	inputTokens, outputTokens int,
	durationMs int64,
	err error,
) {
	l.mu.Lock()
	l.totalLLMCalls++
	l.mu.Unlock()

	if !l.IsEnabled() || l.repo == nil {
		return
	}

	l.repo.LogLLMRequest(
		ctx,
		l.appID,
		l.discoveryRunID,
		step,
		phase,
		model,
		systemPrompt,
		prompt,
		response,
		inputTokens,
		outputTokens,
		durationMs,
		err,
	)
}

// LogAnalysis logs a category analysis operation
func (l *Logger) LogAnalysis(
	ctx context.Context,
	phase string,
	category string,
	input, output map[string]interface{},
	extractedJSON string,
	durationMs int64,
	err error,
) {
	if !l.IsEnabled() || l.repo == nil {
		return
	}

	l.repo.LogAnalysis(
		ctx,
		l.appID,
		l.discoveryRunID,
		phase,
		category,
		input,
		output,
		extractedJSON,
		durationMs,
		err,
	)
}

// ValidateUserCount validates and logs a user count against total app users
// Returns true if valid, false if the count exceeds total users
func (l *Logger) ValidateUserCount(
	ctx context.Context,
	phase string,
	field string,
	value int,
	source string,
	totalAppUsers int,
	category string,
) bool {
	isValid := value <= totalAppUsers

	if !isValid {
		l.mu.Lock()
		l.validationFailures++
		l.mu.Unlock()

		logger.WithFields(logger.Fields{
			"app_id":          l.appID,
			"field":           field,
			"value":           value,
			"total_app_users": totalAppUsers,
			"source":          source,
			"category":        category,
		}).Warn("User count validation failed: value exceeds total app users")
	}

	if l.IsEnabled() && l.repo != nil {
		l.repo.LogUserCountValidation(
			ctx,
			l.appID,
			l.discoveryRunID,
			phase,
			field,
			value,
			source,
			totalAppUsers,
			category,
		)
	}

	return isValid
}

// LogValidation logs a general validation check
func (l *Logger) LogValidation(
	ctx context.Context,
	phase string,
	field string,
	expected, actual interface{},
	passed bool,
	message string,
) {
	if !l.IsEnabled() || l.repo == nil {
		return
	}

	if !passed {
		l.mu.Lock()
		l.validationFailures++
		l.mu.Unlock()
	}

	l.repo.LogValidation(
		ctx,
		l.appID,
		l.discoveryRunID,
		phase,
		field,
		expected,
		actual,
		passed,
		message,
	)
}

// LogOrchestrator logs orchestrator operations
func (l *Logger) LogOrchestrator(
	ctx context.Context,
	phase, operation string,
	metadata map[string]interface{},
	durationMs int64,
	err error,
) {
	if !l.IsEnabled() || l.repo == nil {
		return
	}

	l.repo.LogOrchestrator(
		ctx,
		l.appID,
		l.discoveryRunID,
		phase,
		operation,
		metadata,
		durationMs,
		err,
	)
}

// LogPhaseStart logs the start of a discovery phase
func (l *Logger) LogPhaseStart(ctx context.Context, phase string, metadata map[string]interface{}) {
	l.LogOrchestrator(ctx, phase, "phase_start", metadata, 0, nil)
}

// LogPhaseEnd logs the end of a discovery phase
func (l *Logger) LogPhaseEnd(ctx context.Context, phase string, durationMs int64, err error, metadata map[string]interface{}) {
	l.LogOrchestrator(ctx, phase, "phase_end", metadata, durationMs, err)
}

// GetStats returns current stats
func (l *Logger) GetStats() map[string]interface{} {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return map[string]interface{}{
		"discovery_run_id":    l.discoveryRunID,
		"total_queries":       l.totalQueries,
		"total_llm_calls":  l.totalLLMCalls,
		"validation_failures": l.validationFailures,
	}
}

// GetSummary retrieves the summary of debug logs for this discovery run
func (l *Logger) GetSummary(ctx context.Context) (*models.DebugLogSummary, error) {
	if l.repo == nil {
		return nil, nil
	}
	return l.repo.GetSummary(ctx, l.appID, l.discoveryRunID)
}

// GetLogs retrieves all logs for this discovery run
func (l *Logger) GetLogs(ctx context.Context) ([]*models.DebugLog, error) {
	if l.repo == nil {
		return nil, nil
	}
	return l.repo.GetLogsForDiscoveryRun(ctx, l.appID, l.discoveryRunID)
}

// GetErrors retrieves error logs for this discovery run
func (l *Logger) GetErrors(ctx context.Context) ([]*models.DebugLog, error) {
	if l.repo == nil {
		return nil, nil
	}
	return l.repo.GetErrors(ctx, l.appID, l.discoveryRunID)
}

// Timer is a utility for timing operations
type Timer struct {
	start time.Time
}

// NewTimer creates a new timer
func NewTimer() *Timer {
	return &Timer{start: time.Now()}
}

// ElapsedMs returns elapsed time in milliseconds
func (t *Timer) ElapsedMs() int64 {
	return time.Since(t.start).Milliseconds()
}

// Elapsed returns elapsed duration
func (t *Timer) Elapsed() time.Duration {
	return time.Since(t.start)
}
