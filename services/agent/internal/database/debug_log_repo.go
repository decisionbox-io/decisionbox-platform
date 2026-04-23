package database

import (
	"context"
	"fmt"
	"time"

	logger "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DebugLogRepository manages debug log persistence for AI Discovery
// All logs are stored in the ai_discovery_debug_logs collection
type DebugLogRepository struct {
	collection *mongo.Collection
	enabled    bool // Can be disabled in production to reduce overhead
}

// NewDebugLogRepository creates a new debug log repository
func NewDebugLogRepository(client *DB, enabled bool) *DebugLogRepository {
	return &DebugLogRepository{
		collection: client.Collection(CollectionDebugLogs),
		enabled:    enabled,
	}
}

// Log saves a debug log entry
// This is a non-blocking operation that logs errors but doesn't fail
func (r *DebugLogRepository) Log(ctx context.Context, log *models.DebugLog) {
	if !r.enabled || log == nil {
		return
	}

	// Use a separate context with timeout to avoid blocking
	saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	log.CreatedAt = time.Now()

	_, err := r.collection.InsertOne(saveCtx, log)
	if err != nil {
		logger.WithFields(logger.Fields{
			"error":     err.Error(),
			"app_id":    log.AppID,
			"log_type":  log.LogType,
			"component": log.Component,
			"operation": log.Operation,
		}).Warn("Failed to save debug log")
		return
	}

	logger.WithFields(logger.Fields{
		"app_id":    log.AppID,
		"log_type":  log.LogType,
		"component": log.Component,
		"operation": log.Operation,
	}).Debug("Debug log saved")
}

// LogAsync saves a debug log entry asynchronously
func (r *DebugLogRepository) LogAsync(log *models.DebugLog) {
	if !r.enabled || log == nil {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r.Log(ctx, log)
	}()
}

// LogBigQueryExecution logs a BigQuery execution with all details
func (r *DebugLogRepository) LogBigQueryExecution(
	ctx context.Context,
	appID, discoveryRunID string,
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
	if !r.enabled {
		return
	}

	log := models.NewDebugLog(appID, discoveryRunID, models.DebugLogTypeBigQuery, "bigquery", "execute_query")
	log.Step = step
	log.Phase = phase
	log.SetBigQueryDetails(query, purpose, results, rowCount, durationMs)
	log.FixAttempts = fixAttempts

	if fixedQuery != "" && fixedQuery != query {
		log.SQLQueryFixed = fixedQuery
	}

	if err != nil {
		log.SetError(err.Error(), "")
		log.QueryError = err.Error()
	}

	r.Log(ctx, log)
}

// LogLLMRequest logs a Claude API request with full prompt and response
func (r *DebugLogRepository) LogLLMRequest(
	ctx context.Context,
	appID, discoveryRunID string,
	step int,
	phase string,
	model, systemPrompt, prompt, response string,
	inputTokens, outputTokens int,
	durationMs int64,
	err error,
) {
	if !r.enabled {
		return
	}

	log := models.NewDebugLog(appID, discoveryRunID, models.DebugLogTypeLLM, "llm", "create_message")
	log.Step = step
	log.Phase = phase
	log.SetLLMDetails(model, systemPrompt, prompt, response, inputTokens, outputTokens, durationMs)

	if err != nil {
		log.SetError(err.Error(), "")
		log.LLMError = err.Error()
	}

	r.Log(ctx, log)
}

// LogAnalysis logs an analysis operation (churn, levels, engagement, monetization)
func (r *DebugLogRepository) LogAnalysis(
	ctx context.Context,
	appID, discoveryRunID string,
	phase string,
	category string,
	input, output map[string]interface{},
	extractedJSON string,
	durationMs int64,
	err error,
) {
	if !r.enabled {
		return
	}

	log := models.NewDebugLog(appID, discoveryRunID, models.DebugLogTypeAnalysis, "category_analysis", "analyze_"+category)
	log.Phase = phase
	log.DurationMs = durationMs
	log.SetAnalysisDetails(category, input, output, extractedJSON)

	if err != nil {
		log.SetError(err.Error(), "")
	}

	r.Log(ctx, log)
}

// LogUserCountValidation logs a user count validation check
func (r *DebugLogRepository) LogUserCountValidation(
	ctx context.Context,
	appID, discoveryRunID string,
	phase string,
	field string,
	value int,
	source string,
	totalAppUsers int,
	category string,
) {
	if !r.enabled {
		return
	}

	log := models.NewDebugLog(appID, discoveryRunID, models.DebugLogTypeValidation, "validation", "validate_user_count")
	log.Phase = phase
	log.SetUserCountValidation(field, value, source, totalAppUsers)
	log.AnalysisCategory = category

	isValid := value <= totalAppUsers
	log.ValidationPassed = isValid

	if !isValid {
		log.SetError(
			fmt.Sprintf("User count %d exceeds total app users %d (field: %s, source: %s, category: %s)",
				value, totalAppUsers, field, source, category),
			"",
		)
	}

	r.Log(ctx, log)
}

// LogValidation logs a general validation check
func (r *DebugLogRepository) LogValidation(
	ctx context.Context,
	appID, discoveryRunID string,
	phase string,
	field string,
	expected, actual interface{},
	passed bool,
	message string,
) {
	if !r.enabled {
		return
	}

	log := models.NewDebugLog(appID, discoveryRunID, models.DebugLogTypeValidation, "validation", "validate_"+field)
	log.Phase = phase
	log.SetValidationDetails(field, expected, actual, passed, message)

	if !passed {
		log.SetError(message, "")
	}

	r.Log(ctx, log)
}

// LogOrchestrator logs orchestrator operations
func (r *DebugLogRepository) LogOrchestrator(
	ctx context.Context,
	appID, discoveryRunID string,
	phase, operation string,
	metadata map[string]interface{},
	durationMs int64,
	err error,
) {
	if !r.enabled {
		return
	}

	log := models.NewDebugLog(appID, discoveryRunID, models.DebugLogTypeOrchestrator, "orchestrator", operation)
	log.Phase = phase
	log.DurationMs = durationMs
	log.Metadata = metadata

	if err != nil {
		log.SetError(err.Error(), "")
	}

	r.Log(ctx, log)
}

// GetLogsForDiscoveryRun retrieves all logs for a specific discovery run
func (r *DebugLogRepository) GetLogsForDiscoveryRun(
	ctx context.Context,
	appID, discoveryRunID string,
) ([]*models.DebugLog, error) {
	filter := bson.M{
		"app_id":           appID,
		"discovery_run_id": discoveryRunID,
	}
	opts := options.Find().SetSort(bson.D{{Key: "timestamp", Value: 1}})

	cursor, err := r.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to query debug logs: %w", err)
	}
	defer cursor.Close(ctx)

	var logs []*models.DebugLog
	if err := cursor.All(ctx, &logs); err != nil {
		return nil, fmt.Errorf("failed to decode debug logs: %w", err)
	}

	return logs, nil
}

// GetLogsByType retrieves logs of a specific type for a discovery run
func (r *DebugLogRepository) GetLogsByType(
	ctx context.Context,
	appID, discoveryRunID string,
	logType models.DebugLogType,
) ([]*models.DebugLog, error) {
	filter := bson.M{
		"app_id":           appID,
		"discovery_run_id": discoveryRunID,
		"log_type":         logType,
	}
	opts := options.Find().SetSort(bson.D{{Key: "timestamp", Value: 1}})

	cursor, err := r.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to query debug logs: %w", err)
	}
	defer cursor.Close(ctx)

	var logs []*models.DebugLog
	if err := cursor.All(ctx, &logs); err != nil {
		return nil, fmt.Errorf("failed to decode debug logs: %w", err)
	}

	return logs, nil
}

// GetErrors retrieves all error logs for a discovery run
func (r *DebugLogRepository) GetErrors(
	ctx context.Context,
	appID, discoveryRunID string,
) ([]*models.DebugLog, error) {
	filter := bson.M{
		"app_id":           appID,
		"discovery_run_id": discoveryRunID,
		"success":          false,
	}
	opts := options.Find().SetSort(bson.D{{Key: "timestamp", Value: 1}})

	cursor, err := r.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to query error logs: %w", err)
	}
	defer cursor.Close(ctx)

	var logs []*models.DebugLog
	if err := cursor.All(ctx, &logs); err != nil {
		return nil, fmt.Errorf("failed to decode error logs: %w", err)
	}

	return logs, nil
}

// GetUserCountValidationFailures retrieves logs where user count validation failed
func (r *DebugLogRepository) GetUserCountValidationFailures(
	ctx context.Context,
	appID string,
	limit int,
) ([]*models.DebugLog, error) {
	filter := bson.M{
		"app_id":              appID,
		"log_type":            models.DebugLogTypeValidation,
		"operation":           "validate_user_count",
		"user_count_is_valid": false,
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "timestamp", Value: -1}}).
		SetLimit(int64(limit))

	cursor, err := r.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to query validation failures: %w", err)
	}
	defer cursor.Close(ctx)

	var logs []*models.DebugLog
	if err := cursor.All(ctx, &logs); err != nil {
		return nil, fmt.Errorf("failed to decode validation logs: %w", err)
	}

	return logs, nil
}

// GetSummary generates a summary of debug logs for a discovery run
func (r *DebugLogRepository) GetSummary(
	ctx context.Context,
	appID, discoveryRunID string,
) (*models.DebugLogSummary, error) {
	logs, err := r.GetLogsForDiscoveryRun(ctx, appID, discoveryRunID)
	if err != nil {
		return nil, err
	}

	if len(logs) == 0 {
		return nil, fmt.Errorf("no logs found for discovery run %s", discoveryRunID)
	}

	summary := &models.DebugLogSummary{
		AppID:          appID,
		DiscoveryRunID: discoveryRunID,
		StartTime:      logs[0].Timestamp,
		EndTime:        logs[len(logs)-1].Timestamp,
		Errors:         make([]string, 0),
	}

	for _, log := range logs {
		switch log.LogType {
		case models.DebugLogTypeBigQuery:
			summary.BigQueryLogCount++
			summary.TotalQueriesExecuted++
			summary.TotalQueryTimeMs += log.DurationMs
			if !log.Success {
				summary.FailedQueries++
			}
			if log.SQLQueryFixed != "" {
				summary.FixedQueries++
			}
		case models.DebugLogTypeLLM:
			summary.LLMLogCount++
			summary.TotalLLMCalls++
			summary.TotalLLMInputTokens += log.LLMInputTokens
			summary.TotalLLMOutputTokens += log.LLMOutputTokens
		case models.DebugLogTypeAnalysis:
			summary.AnalysisLogCount++
		case models.DebugLogTypeValidation:
			summary.ValidationLogCount++
			if log.Operation == "validate_user_count" {
				summary.UserCountValidations++
				if !log.UserCountIsValid {
					summary.UserCountValidationsFailed++
				}
			}
		}

		if !log.Success {
			summary.ErrorCount++
			summary.Errors = append(summary.Errors, log.ErrorMessage)
		}
	}

	summary.TotalDurationMs = summary.EndTime.Sub(summary.StartTime).Milliseconds()

	return summary, nil
}

// DeleteOldLogs deletes debug logs older than specified days
func (r *DebugLogRepository) DeleteOldLogs(ctx context.Context, olderThanDays int) (int64, error) {
	cutoffDate := time.Now().AddDate(0, 0, -olderThanDays)

	filter := bson.M{
		"created_at": bson.M{
			"$lt": cutoffDate,
		},
	}

	result, err := r.collection.DeleteMany(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("failed to delete old logs: %w", err)
	}

	logger.WithFields(logger.Fields{
		"deleted_count":   result.DeletedCount,
		"older_than_days": olderThanDays,
	}).Info("Deleted old debug logs")

	return result.DeletedCount, nil
}

// EnsureIndexes creates necessary indexes for the debug log collection
func (r *DebugLogRepository) EnsureIndexes(ctx context.Context) error {
	logger.Info("Ensuring indexes for debug log collection")

	indexes := []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "app_id", Value: 1},
				{Key: "discovery_run_id", Value: 1},
				{Key: "timestamp", Value: 1},
			},
		},
		{
			Keys: bson.D{
				{Key: "app_id", Value: 1},
				{Key: "log_type", Value: 1},
				{Key: "timestamp", Value: -1},
			},
		},
		{
			Keys: bson.D{
				{Key: "app_id", Value: 1},
				{Key: "success", Value: 1},
			},
		},
		{
			Keys: bson.D{
				{Key: "app_id", Value: 1},
				{Key: "user_count_is_valid", Value: 1},
			},
		},
		{
			Keys: bson.D{
				{Key: "created_at", Value: 1},
			},
			Options: options.Index().SetExpireAfterSeconds(60 * 60 * 24 * 30), // TTL: 30 days
		},
	}

	_, err := r.collection.Indexes().CreateMany(ctx, indexes)
	if err != nil {
		return fmt.Errorf("failed to create indexes: %w", err)
	}

	logger.Info("Indexes created successfully")
	return nil
}

// IsEnabled returns whether debug logging is enabled
func (r *DebugLogRepository) IsEnabled() bool {
	return r.enabled
}

// SetEnabled enables or disables debug logging
func (r *DebugLogRepository) SetEnabled(enabled bool) {
	r.enabled = enabled
}
