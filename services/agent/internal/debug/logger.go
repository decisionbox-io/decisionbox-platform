package debug

import (
	"context"
	"sync"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	logger "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/google/uuid"
)

// Logger provides comprehensive debug logging for AI Discovery
// It wraps the MongoDB debug log repository and provides convenient methods
// for logging various types of operations
type Logger struct {
	repo           *database.DebugLogRepository
	appID          string
	discoveryRunID string
	// warehouseProvider is the provider id (e.g. "mssql", "bigquery",
	// "snowflake") used as log_type and component on warehouse-query
	// debug rows. Set from the project's warehouse config — empty
	// falls back to "warehouse" so the dashboard's "where did this
	// query come from" column never reads as a blank.
	warehouseProvider string
	// warehouseID is the datasource id stamped on warehouse-query rows so a
	// multi-warehouse run attributes each SQL query to the datasource it
	// actually hit. Empty on a single-warehouse run. Derive a per-datasource
	// logger with ForWarehouse.
	warehouseID string
	// parent, when set, is the run logger a ForWarehouse-derived logger was
	// cloned from; IsEnabled delegates to it so a derived per-datasource
	// logger always reflects the run's live enabled state rather than a
	// snapshot taken at derivation time.
	parent  *Logger
	mu      sync.RWMutex
	enabled bool

	// Stats tracking
	totalQueries       int
	totalLLMCalls      int
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
	// WarehouseProvider is the warehouse provider id from the project
	// config (e.g. "mssql"). Stamped on every warehouse-query debug
	// row as log_type + component so the dashboard correctly labels
	// queries by their actual source. Empty falls back to "warehouse".
	WarehouseProvider string
}

// NewLogger creates a new debug logger
func NewLogger(opts LoggerOptions) *Logger {
	discoveryRunID := opts.DiscoveryRunID
	if discoveryRunID == "" {
		discoveryRunID = uuid.New().String()
	}
	whProvider := opts.WarehouseProvider
	if whProvider == "" {
		whProvider = "warehouse"
	}

	l := &Logger{
		repo:              opts.Repo,
		appID:             opts.AppID,
		discoveryRunID:    discoveryRunID,
		warehouseProvider: whProvider,
		enabled:           opts.Enabled,
	}

	if opts.Enabled {
		logger.WithFields(logger.Fields{
			"app_id":           opts.AppID,
			"discovery_run_id": discoveryRunID,
		}).Info("Debug logging enabled for this discovery run")
	}

	return l
}

// ForWarehouse returns a logger that stamps a specific datasource's provider
// (as log_type + component) and warehouse id on its warehouse-query rows, so
// each per-datasource query executor on a multi-warehouse run attributes its
// SQL to the datasource it actually hit instead of the run's primary. The
// derived logger shares the repo, run id, and enabled flag; only the
// warehouse labels differ. Empty provider keeps the base label.
func (l *Logger) ForWarehouse(warehouseID, provider string) *Logger {
	if l == nil {
		return nil
	}
	whProvider := provider
	if whProvider == "" {
		whProvider = l.warehouseProvider
	}
	return &Logger{
		repo:              l.repo,
		appID:             l.appID,
		discoveryRunID:    l.discoveryRunID,
		warehouseProvider: whProvider,
		warehouseID:       warehouseID,
		parent:            l,
	}
}

// GetDiscoveryRunID returns the unique ID for this discovery run
func (l *Logger) GetDiscoveryRunID() string {
	return l.discoveryRunID
}

// GetAppID returns the app ID
func (l *Logger) GetAppID() string {
	return l.appID
}

// IsEnabled returns whether debug logging is enabled. A ForWarehouse-derived
// logger delegates to its parent so it reflects the run's live enabled state.
func (l *Logger) IsEnabled() bool {
	if l == nil {
		return false
	}
	if l.parent != nil {
		return l.parent.IsEnabled()
	}
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

// LogWarehouseQuery records a warehouse query execution under the
// project's actual provider id (mssql, bigquery, snowflake, …) so the
// dashboard's debug-log table labels queries correctly. The historical
// "BigQuery" name was misleading once we shipped other warehouse
// providers — every query was stamped log_type=bigquery regardless of
// the real source.
func (l *Logger) LogWarehouseQuery(
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

	l.repo.LogWarehouseQueryExecution(
		ctx,
		l.appID,
		l.discoveryRunID,
		l.warehouseProvider,
		l.warehouseID,
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
		"total_llm_calls":     l.totalLLMCalls,
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
