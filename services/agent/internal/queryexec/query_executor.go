package queryexec

import (
	"context"
	"fmt"
	"strings"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/debug"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// QueryExecutor executes warehouse queries with self-healing capabilities.
type QueryExecutor struct {
	warehouse    gowarehouse.Provider
	sqlFixer     SQLFixer
	debugLogger  *debug.Logger
	maxRetries   int
	filterField  string
	filterValue  string
	currentStep  int
	currentPhase string
}

// SQLFixer defines the interface for fixing SQL queries.
type SQLFixer interface {
	FixSQL(ctx context.Context, query string, error string, attempt int) (string, error)
}

// QueryExecutorOptions configures the query executor.
type QueryExecutorOptions struct {
	Warehouse   gowarehouse.Provider
	SQLFixer    SQLFixer
	DebugLogger *debug.Logger
	MaxRetries  int
	FilterField string // optional: field to verify in queries (e.g., "app_id")
	FilterValue string // optional: value the field must match
}

// NewQueryExecutor creates a new query executor with self-healing.
func NewQueryExecutor(opts QueryExecutorOptions) *QueryExecutor {
	if opts.MaxRetries == 0 {
		opts.MaxRetries = 5
	}
	return &QueryExecutor{
		warehouse:    opts.Warehouse,
		sqlFixer:     opts.SQLFixer,
		debugLogger:  opts.DebugLogger,
		maxRetries:   opts.MaxRetries,
		filterField:  opts.FilterField,
		filterValue:  opts.FilterValue,
		currentPhase: "exploration",
	}
}

func (e *QueryExecutor) SetStep(step int)                { e.currentStep = step }
func (e *QueryExecutor) SetPhase(phase string)           { e.currentPhase = phase }
func (e *QueryExecutor) SetDebugLogger(dl *debug.Logger) { e.debugLogger = dl }

// ExecuteResult represents the result of a query execution.
type ExecuteResult struct {
	Data            []map[string]interface{}
	RowCount        int
	ExecutionTimeMs int64
	FixAttempts     int
	Fixed           bool
	OriginalQuery   string
	FinalQuery      string
	Errors          []string
}

// Execute executes a query with automatic self-healing.
func (e *QueryExecutor) Execute(ctx context.Context, query string, purpose string) (*ExecuteResult, error) {
	startTime := time.Now()

	result := &ExecuteResult{
		OriginalQuery: query,
		FinalQuery:    query,
		Errors:        make([]string, 0),
	}

	currentQuery := query

	if err := e.verifyFilter(currentQuery); err != nil {
		return nil, fmt.Errorf("security violation: %w", err)
	}

	for attempt := 0; attempt <= e.maxRetries; attempt++ {
		applog.WithFields(applog.Fields{
			"attempt":  attempt,
			"max":      e.maxRetries,
			"purpose":  purpose,
			"phase":    e.currentPhase,
			"step":     e.currentStep,
			"query_len": len(currentQuery),
		}).Debug("Executing warehouse query")

		qr, err := e.warehouse.Query(ctx, currentQuery, nil)
		executionTime := time.Since(startTime).Milliseconds()

		if err == nil {
			result.Data = qr.Rows
			result.RowCount = len(qr.Rows)
			result.ExecutionTimeMs = executionTime
			result.FinalQuery = currentQuery
			result.Fixed = attempt > 0

			applog.WithFields(applog.Fields{
				"rows":      result.RowCount,
				"time_ms":   executionTime,
				"fixed":     result.Fixed,
				"attempts":  attempt + 1,
				"purpose":   purpose,
			}).Debug("Query executed successfully")

			if e.debugLogger != nil {
				fixedQuery := ""
				if result.Fixed {
					fixedQuery = result.FinalQuery
				}
				e.debugLogger.LogBigQuery(ctx, e.currentStep, e.currentPhase,
					query, purpose, result.Data, result.RowCount, result.ExecutionTimeMs,
					nil, result.FixAttempts, fixedQuery)
			}

			return result, nil
		}

		result.Errors = append(result.Errors, err.Error())

		applog.WithFields(applog.Fields{
			"attempt": attempt,
			"max":     e.maxRetries,
			"error":   err.Error(),
			"purpose": purpose,
		}).Warn("Query failed")

		if attempt >= e.maxRetries {
			applog.WithFields(applog.Fields{
				"attempts": attempt + 1,
				"purpose":  purpose,
				"error":    err.Error(),
			}).Error("Query exhausted all retry attempts")

			if e.debugLogger != nil {
				e.debugLogger.LogBigQuery(ctx, e.currentStep, e.currentPhase,
					query, purpose, nil, 0, time.Since(startTime).Milliseconds(),
					err, result.FixAttempts, "")
			}
			return nil, fmt.Errorf("query failed after %d attempts: %w", attempt+1, err)
		}

		if e.sqlFixer == nil {
			applog.Error("Query failed and no SQL fixer available — cannot retry")
			return nil, fmt.Errorf("query failed and no SQL fixer available: %w", err)
		}

		applog.WithFields(applog.Fields{
			"attempt": attempt + 1,
			"error":   err.Error(),
		}).Info("Attempting SQL fix via LLM")

		fixedQuery, fixErr := e.sqlFixer.FixSQL(ctx, currentQuery, err.Error(), attempt)
		if fixErr != nil {
			applog.WithError(fixErr).Error("SQL fixer failed")
			return nil, fmt.Errorf("failed to fix SQL query: %w", fixErr)
		}

		if verifyErr := e.verifyFilter(fixedQuery); verifyErr != nil {
			applog.WithError(verifyErr).Error("Fixed query failed security filter check")
			return nil, fmt.Errorf("fixed query security violation: %w", verifyErr)
		}

		applog.Debug("SQL fix applied, retrying with corrected query")
		result.FixAttempts++
		currentQuery = fixedQuery
		startTime = time.Now()
	}

	return nil, fmt.Errorf("query execution failed unexpectedly")
}

// ExecuteWithHistory executes a query and returns a QueryHistory record.
func (e *QueryExecutor) ExecuteWithHistory(ctx context.Context, query string, purpose string) (*ExecuteResult, *models.QueryHistory) {
	result, err := e.Execute(ctx, query, purpose)

	history := &models.QueryHistory{
		Query:      query,
		Purpose:    purpose,
		ExecutedAt: time.Now(),
	}

	if err != nil {
		history.Success = false
		history.Error = err.Error()
		if result != nil {
			history.FixAttempts = result.FixAttempts
		}
		return result, history
	}

	history.Success = true
	history.RowsReturned = result.RowCount
	history.ExecutionTimeMs = result.ExecutionTimeMs
	history.FixAttempts = result.FixAttempts

	return result, history
}

// verifyFilter checks if the query contains the required filter field.
// If no filter is configured (self-hosted, dedicated dataset), all queries pass.
func (e *QueryExecutor) verifyFilter(query string) error {
	if e.filterField == "" {
		return nil // no filter required
	}
	if !strings.Contains(strings.ToLower(query), strings.ToLower(e.filterField)) {
		applog.WithFields(applog.Fields{
			"filter_field": e.filterField,
			"query_preview": query[:min(len(query), 80)],
		}).Warn("Query missing required filter field")
		return fmt.Errorf("query must filter by %s for security", e.filterField)
	}
	return nil
}

