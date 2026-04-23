package models

import "time"

// DebugLogType represents the type of debug log entry
type DebugLogType string

const (
	DebugLogTypeBigQuery       DebugLogType = "bigquery"
	DebugLogTypeLLM            DebugLogType = "llm"
	DebugLogTypeAnalysis       DebugLogType = "analysis"
	DebugLogTypeValidation     DebugLogType = "validation"
	DebugLogTypeExploration    DebugLogType = "exploration"
	DebugLogTypeRecommendation DebugLogType = "recommendation"
	DebugLogTypeOrchestrator   DebugLogType = "orchestrator"
)

// DebugLog represents a single debug log entry for AI Discovery
// This is stored in the ai_discovery_debug_logs collection for debugging purposes
type DebugLog struct {
	// Core identifiers
	ID            string    `bson:"_id,omitempty" json:"id,omitempty"`
	AppID         string    `bson:"app_id" json:"app_id"`
	DiscoveryRunID string   `bson:"discovery_run_id" json:"discovery_run_id"` // Unique ID for this discovery run
	LogType       DebugLogType `bson:"log_type" json:"log_type"`

	// Timing
	Timestamp     time.Time `bson:"timestamp" json:"timestamp"`
	DurationMs    int64     `bson:"duration_ms,omitempty" json:"duration_ms,omitempty"`

	// Context
	Step          int       `bson:"step,omitempty" json:"step,omitempty"` // Exploration step number
	Phase         string    `bson:"phase,omitempty" json:"phase,omitempty"` // Discovery phase (schema, exploration, analysis, etc.)
	Component     string    `bson:"component" json:"component"` // Which component generated this log
	Operation     string    `bson:"operation" json:"operation"` // What operation was performed

	// BigQuery specific fields
	SQLQuery      string                   `bson:"sql_query,omitempty" json:"sql_query,omitempty"`
	SQLQueryFixed string                   `bson:"sql_query_fixed,omitempty" json:"sql_query_fixed,omitempty"` // If query was fixed
	QueryPurpose  string                   `bson:"query_purpose,omitempty" json:"query_purpose,omitempty"`
	QueryResults  []map[string]interface{} `bson:"query_results,omitempty" json:"query_results,omitempty"` // Full results (up to 100 rows)
	RowCount      int                      `bson:"row_count,omitempty" json:"row_count,omitempty"`
	BytesProcessed int64                   `bson:"bytes_processed,omitempty" json:"bytes_processed,omitempty"`
	QueryError    string                   `bson:"query_error,omitempty" json:"query_error,omitempty"`
	FixAttempts   int                      `bson:"fix_attempts,omitempty" json:"fix_attempts,omitempty"`

	// Claude specific fields
	LLMModel       string `bson:"llm_model,omitempty" json:"llm_model,omitempty"`
	LLMPrompt      string `bson:"llm_prompt,omitempty" json:"llm_prompt,omitempty"` // Full prompt sent
	LLMSystemPrompt string `bson:"llm_system_prompt,omitempty" json:"llm_system_prompt,omitempty"`
	LLMResponse    string `bson:"llm_response,omitempty" json:"llm_response,omitempty"` // Full response
	LLMInputTokens  int   `bson:"llm_input_tokens,omitempty" json:"llm_input_tokens,omitempty"`
	LLMOutputTokens int   `bson:"llm_output_tokens,omitempty" json:"llm_output_tokens,omitempty"`
	LLMError       string `bson:"llm_error,omitempty" json:"llm_error,omitempty"`

	// Analysis specific fields
	AnalysisCategory string                 `bson:"analysis_category,omitempty" json:"analysis_category,omitempty"` // churn, monetization, levels, engagement
	AnalysisInput    map[string]interface{} `bson:"analysis_input,omitempty" json:"analysis_input,omitempty"` // Data passed to analysis
	AnalysisOutput   map[string]interface{} `bson:"analysis_output,omitempty" json:"analysis_output,omitempty"` // Results from analysis
	ExtractedJSON    string                 `bson:"extracted_json,omitempty" json:"extracted_json,omitempty"` // JSON extracted from Claude response

	// Validation specific fields
	ValidationField    string      `bson:"validation_field,omitempty" json:"validation_field,omitempty"` // What field was validated
	ValidationExpected interface{} `bson:"validation_expected,omitempty" json:"validation_expected,omitempty"`
	ValidationActual   interface{} `bson:"validation_actual,omitempty" json:"validation_actual,omitempty"`
	ValidationPassed   bool        `bson:"validation_passed,omitempty" json:"validation_passed,omitempty"`
	ValidationMessage  string      `bson:"validation_message,omitempty" json:"validation_message,omitempty"`

	// User count tracking (for debugging inflated counts)
	UserCountField       string `bson:"user_count_field,omitempty" json:"user_count_field,omitempty"` // Field name containing user count
	UserCountValue       int    `bson:"user_count_value,omitempty" json:"user_count_value,omitempty"` // The value
	UserCountSource      string `bson:"user_count_source,omitempty" json:"user_count_source,omitempty"` // Where this count came from
	TotalAppUsers        int    `bson:"total_app_users,omitempty" json:"total_app_users,omitempty"` // Known total users for comparison
	UserCountIsValid     bool   `bson:"user_count_is_valid,omitempty" json:"user_count_is_valid,omitempty"` // Whether count <= total

	// Error tracking
	Success       bool   `bson:"success" json:"success"`
	ErrorMessage  string `bson:"error_message,omitempty" json:"error_message,omitempty"`
	ErrorStack    string `bson:"error_stack,omitempty" json:"error_stack,omitempty"`

	// Additional metadata
	Metadata      map[string]interface{} `bson:"metadata,omitempty" json:"metadata,omitempty"`

	// Timestamps
	CreatedAt     time.Time `bson:"created_at" json:"created_at"`
}

// NewDebugLog creates a new debug log entry with common fields populated
func NewDebugLog(appID, discoveryRunID string, logType DebugLogType, component, operation string) *DebugLog {
	now := time.Now()
	return &DebugLog{
		AppID:          appID,
		DiscoveryRunID: discoveryRunID,
		LogType:        logType,
		Timestamp:      now,
		Component:      component,
		Operation:      operation,
		Success:        true,
		CreatedAt:      now,
		Metadata:       make(map[string]interface{}),
	}
}

// SetBigQueryDetails sets BigQuery-specific fields
func (d *DebugLog) SetBigQueryDetails(query, purpose string, results []map[string]interface{}, rowCount int, durationMs int64) {
	d.SQLQuery = query
	d.QueryPurpose = purpose
	d.RowCount = rowCount
	d.DurationMs = durationMs

	// Store up to 100 rows of results for debugging
	maxRows := 100
	if len(results) < maxRows {
		maxRows = len(results)
	}
	if maxRows > 0 {
		d.QueryResults = results[:maxRows]
	}
}

// SetLLMDetails sets LLM-specific fields (applies to any provider — the
// struct names were historically Claude-specific but the schema is now
// provider-agnostic).
func (d *DebugLog) SetLLMDetails(model, systemPrompt, prompt, response string, inputTokens, outputTokens int, durationMs int64) {
	d.LLMModel = model
	d.LLMSystemPrompt = systemPrompt
	d.LLMPrompt = prompt
	d.LLMResponse = response
	d.LLMInputTokens = inputTokens
	d.LLMOutputTokens = outputTokens
	d.DurationMs = durationMs
}

// SetAnalysisDetails sets analysis-specific fields
func (d *DebugLog) SetAnalysisDetails(category string, input, output map[string]interface{}, extractedJSON string) {
	d.AnalysisCategory = category
	d.AnalysisInput = input
	d.AnalysisOutput = output
	d.ExtractedJSON = extractedJSON
}

// SetValidationDetails sets validation-specific fields
func (d *DebugLog) SetValidationDetails(field string, expected, actual interface{}, passed bool, message string) {
	d.ValidationField = field
	d.ValidationExpected = expected
	d.ValidationActual = actual
	d.ValidationPassed = passed
	d.ValidationMessage = message
}

// SetUserCountValidation sets user count validation fields
func (d *DebugLog) SetUserCountValidation(field string, value int, source string, totalAppUsers int) {
	d.UserCountField = field
	d.UserCountValue = value
	d.UserCountSource = source
	d.TotalAppUsers = totalAppUsers
	d.UserCountIsValid = value <= totalAppUsers
}

// SetError sets error fields and marks success as false
func (d *DebugLog) SetError(message string, stack string) {
	d.Success = false
	d.ErrorMessage = message
	d.ErrorStack = stack
}

// AddMetadata adds a key-value pair to metadata
func (d *DebugLog) AddMetadata(key string, value interface{}) {
	if d.Metadata == nil {
		d.Metadata = make(map[string]interface{})
	}
	d.Metadata[key] = value
}

// DebugLogFilter represents filters for querying debug logs
type DebugLogFilter struct {
	AppID          string
	DiscoveryRunID string
	LogType        DebugLogType
	Component      string
	Operation      string
	Phase          string
	SuccessOnly    bool
	ErrorOnly      bool
	StartTime      time.Time
	EndTime        time.Time
	Limit          int
}

// DebugLogSummary provides a summary of debug logs for a discovery run
type DebugLogSummary struct {
	AppID              string    `json:"app_id"`
	DiscoveryRunID     string    `json:"discovery_run_id"`
	StartTime          time.Time `json:"start_time"`
	EndTime            time.Time `json:"end_time"`
	TotalDurationMs    int64     `json:"total_duration_ms"`

	// Counts by type
	BigQueryLogCount   int `json:"bigquery_log_count"`
	LLMLogCount     int `json:"llm_log_count"`
	AnalysisLogCount   int `json:"analysis_log_count"`
	ValidationLogCount int `json:"validation_log_count"`
	ErrorCount         int `json:"error_count"`

	// Query stats
	TotalQueriesExecuted int   `json:"total_queries_executed"`
	TotalQueryTimeMs     int64 `json:"total_query_time_ms"`
	FailedQueries        int   `json:"failed_queries"`
	FixedQueries         int   `json:"fixed_queries"`

	// Claude stats
	TotalLLMCalls     int `json:"total_llm_calls"`
	TotalLLMInputTokens int `json:"total_llm_input_tokens"`
	TotalLLMOutputTokens int `json:"total_llm_output_tokens"`

	// Validation stats
	UserCountValidations     int `json:"user_count_validations"`
	UserCountValidationsFailed int `json:"user_count_validations_failed"`

	// Errors
	Errors []string `json:"errors,omitempty"`
}
