package models

import "time"

// DiscoveryResult represents the complete output of a discovery run.
// Every LLM interaction is stored for traceability and fine-tuning.
type DiscoveryResult struct {
	ID            string    `bson:"_id,omitempty" json:"id"`
	ProjectID     string    `bson:"project_id" json:"project_id"`
	Domain        string    `bson:"domain" json:"domain"`
	Category      string    `bson:"category" json:"category"`
	DiscoveryDate time.Time `bson:"discovery_date" json:"discovery_date"`

	RunType        string   `bson:"run_type" json:"run_type"`                 // "full" or "partial"
	AreasRequested []string `bson:"areas_requested,omitempty" json:"areas_requested,omitempty"` // for partial runs

	TotalSteps int           `bson:"total_steps" json:"total_steps"`
	Duration   time.Duration `bson:"duration" json:"duration"`

	Schemas map[string]TableSchema `bson:"schemas,omitempty" json:"schemas,omitempty"`

	// Final outputs
	Insights        []Insight        `bson:"insights" json:"insights"`
	Recommendations []Recommendation `bson:"recommendations" json:"recommendations"`
	Summary         Summary          `bson:"summary" json:"summary"`

	// Complete LLM dialog logs (for traceability and fine-tuning)
	ExplorationLog    []ExplorationStep  `bson:"exploration_log" json:"exploration_log"`
	AnalysisLog       []AnalysisStep     `bson:"analysis_log" json:"analysis_log"`
	RecommendationLog *RecommendationStep `bson:"recommendation_log,omitempty" json:"recommendation_log,omitempty"`
	ValidationLog     []ValidationResult `bson:"validation_log,omitempty" json:"validation_log,omitempty"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}

// TableSchema represents a warehouse table's schema.
type TableSchema struct {
	TableName    string                   `bson:"table_name" json:"table_name"`
	RowCount     int64                    `bson:"row_count" json:"row_count"`
	Columns      []ColumnInfo             `bson:"columns" json:"columns"`
	KeyColumns   []string                 `bson:"key_columns" json:"key_columns"`
	Metrics      []string                 `bson:"metrics" json:"metrics"`
	Dimensions   []string                 `bson:"dimensions" json:"dimensions"`
	SampleData   []map[string]interface{} `bson:"sample_data,omitempty" json:"sample_data,omitempty"`
	DiscoveredAt time.Time                `bson:"discovered_at" json:"discovered_at"`
}

// ColumnInfo represents a single column's metadata.
type ColumnInfo struct {
	Name     string `bson:"name" json:"name"`
	Type     string `bson:"type" json:"type"`
	Nullable bool   `bson:"nullable" json:"nullable"`
	Category string `bson:"category" json:"category"` // primary_key, time, metric, dimension
}

// ---------------------------------------------------------------------------
// Insight & Recommendation (final outputs)
// ---------------------------------------------------------------------------

// Insight is a domain-agnostic discovered pattern or finding.
type Insight struct {
	ID           string `bson:"id" json:"id"`
	AnalysisArea string `bson:"analysis_area" json:"analysis_area"` // "churn", "levels", etc.
	Name         string `bson:"name" json:"name"`
	Description  string `bson:"description" json:"description"`
	Severity     string `bson:"severity" json:"severity"` // "critical", "high", "medium", "low"

	AffectedCount int     `bson:"affected_count" json:"affected_count"`
	RiskScore     float64 `bson:"risk_score" json:"risk_score"`
	Confidence    float64 `bson:"confidence" json:"confidence"`

	// Flexible domain-specific metrics
	Metrics    map[string]interface{} `bson:"metrics,omitempty" json:"metrics,omitempty"`
	Indicators []string               `bson:"indicators,omitempty" json:"indicators,omitempty"`

	TargetSegment string `bson:"target_segment,omitempty" json:"target_segment,omitempty"`

	// Source exploration steps that this insight is based on.
	// Set by the LLM during analysis — cites which exploration queries it used.
	SourceSteps []int `bson:"source_steps,omitempty" json:"source_steps,omitempty"`

	SQLMetadata  *SQLMetadata `bson:"sql_metadata,omitempty" json:"sql_metadata,omitempty"`
	DiscoveredAt time.Time    `bson:"discovered_at" json:"discovered_at"`

	// Validation result (populated after warehouse verification)
	Validation *InsightValidation `bson:"validation,omitempty" json:"validation,omitempty"`
}

// InsightValidation holds the result of warehouse verification for an insight.
type InsightValidation struct {
	Status         string `bson:"status" json:"status"` // "confirmed", "adjusted", "rejected", "unverified"
	VerifiedCount  int    `bson:"verified_count,omitempty" json:"verified_count,omitempty"`
	OriginalCount  int    `bson:"original_count,omitempty" json:"original_count,omitempty"`
	Query          string `bson:"query,omitempty" json:"query,omitempty"`
	Reasoning      string `bson:"reasoning,omitempty" json:"reasoning,omitempty"`
	ValidatedAt    time.Time `bson:"validated_at" json:"validated_at"`
}

// Recommendation is an actionable suggestion based on discovered insights.
type Recommendation struct {
	ID          string `bson:"id" json:"id"`
	Category    string `bson:"category" json:"category"`
	Title       string `bson:"title" json:"title"`
	Description string `bson:"description" json:"description"`
	Priority    int    `bson:"priority" json:"priority"` // 1-5

	TargetSegment string `bson:"target_segment" json:"target_segment"`
	SegmentSize   int    `bson:"segment_size" json:"segment_size"`

	ExpectedImpact    Impact   `bson:"expected_impact" json:"expected_impact"`
	Actions           []string `bson:"actions" json:"actions"`
	RelatedInsightIDs []string `bson:"related_insight_ids,omitempty" json:"related_insight_ids,omitempty"`

	Confidence float64   `bson:"confidence" json:"confidence"`
	CreatedAt  time.Time `bson:"created_at" json:"created_at"`
}

// Impact represents the expected impact of a recommendation.
type Impact struct {
	Metric               string  `bson:"metric" json:"metric"`
	EstimatedImprovement string  `bson:"estimated_improvement" json:"estimated_improvement"`
	Reasoning            string  `bson:"reasoning" json:"reasoning"`
	ReturnRate           float64 `bson:"return_rate,omitempty" json:"return_rate,omitempty"`
	ConversionRate       float64 `bson:"conversion_rate,omitempty" json:"conversion_rate,omitempty"`
	EstimatedValue       float64 `bson:"estimated_value,omitempty" json:"estimated_value,omitempty"`
	TotalValue           float64 `bson:"total_value,omitempty" json:"total_value,omitempty"`
}

// Summary holds the executive summary of a discovery run.
type Summary struct {
	Date                 time.Time `bson:"date" json:"date"`
	Text                 string    `bson:"text" json:"text"`
	KeyFindings          []string  `bson:"key_findings" json:"key_findings"`
	TopRecommendations   []string  `bson:"top_recommendations" json:"top_recommendations"`
	TotalInsights        int       `bson:"total_insights" json:"total_insights"`
	TotalRecommendations int       `bson:"total_recommendations" json:"total_recommendations"`
	QueriesExecuted      int       `bson:"queries_executed" json:"queries_executed"`
	Errors               []string  `bson:"errors,omitempty" json:"errors,omitempty"`
}

// ---------------------------------------------------------------------------
// LLM Dialog Logs (for traceability and fine-tuning)
// ---------------------------------------------------------------------------

// ExplorationStep represents a single step in the autonomous exploration loop.
// Captures the complete LLM dialog for each step.
type ExplorationStep struct {
	Step      int       `bson:"step" json:"step"`
	Timestamp time.Time `bson:"timestamp" json:"timestamp"`

	// LLM decision
	Action       string `bson:"action" json:"action"` // query_data, lookup_schema, search_tables, complete, complete_rejected
	Thinking     string `bson:"thinking" json:"thinking"`
	QueryPurpose string `bson:"query_purpose,omitempty" json:"query_purpose,omitempty"`

	// Query execution (if action = query_data)
	Query           string                   `bson:"query,omitempty" json:"query,omitempty"`
	QueryResult     []map[string]interface{} `bson:"query_result,omitempty" json:"query_result,omitempty"`
	RowCount        int                      `bson:"row_count,omitempty" json:"row_count,omitempty"`
	ExecutionTimeMs int64                    `bson:"execution_time_ms,omitempty" json:"execution_time_ms,omitempty"`

	// Error handling
	Error       string `bson:"error,omitempty" json:"error,omitempty"`
	FixAttempts int    `bson:"fix_attempts,omitempty" json:"fix_attempts,omitempty"`
	Fixed       bool   `bson:"fixed,omitempty" json:"fixed,omitempty"`

	// Complete LLM dialog (for fine-tuning)
	LLMRequest  string `bson:"llm_request" json:"llm_request"`   // full prompt sent to LLM
	LLMResponse string `bson:"llm_response" json:"llm_response"` // full response from LLM
	TokensIn    int    `bson:"tokens_in,omitempty" json:"tokens_in,omitempty"`
	TokensOut   int    `bson:"tokens_out,omitempty" json:"tokens_out,omitempty"`
	DurationMs  int64  `bson:"duration_ms,omitempty" json:"duration_ms,omitempty"`

	IsInsight bool `bson:"is_insight" json:"is_insight"`
}

// AnalysisStep captures the complete LLM dialog for a single analysis area.
// One per analysis area (churn, engagement, levels, etc.).
type AnalysisStep struct {
	AreaID   string    `bson:"area_id" json:"area_id"`     // "churn", "levels", etc.
	AreaName string    `bson:"area_name" json:"area_name"` // "Churn Risks", "Level Difficulty"
	RunAt    time.Time `bson:"run_at" json:"run_at"`

	// Input
	Prompt          string `bson:"prompt" json:"prompt"`                     // full analysis prompt sent
	RelevantQueries int    `bson:"relevant_queries" json:"relevant_queries"` // how many exploration queries fed in

	// LLM output
	Response  string `bson:"response" json:"response"` // full LLM response
	TokensIn  int    `bson:"tokens_in" json:"tokens_in"`
	TokensOut int    `bson:"tokens_out" json:"tokens_out"`
	DurationMs int64 `bson:"duration_ms" json:"duration_ms"`

	// Parsed results
	Insights []Insight `bson:"insights" json:"insights"`

	// Validation
	ValidationResults []ValidationResult `bson:"validation_results,omitempty" json:"validation_results,omitempty"`

	Error string `bson:"error,omitempty" json:"error,omitempty"`
}

// RecommendationStep captures the complete LLM dialog for recommendation generation.
type RecommendationStep struct {
	RunAt time.Time `bson:"run_at" json:"run_at"`

	// Input
	Prompt       string `bson:"prompt" json:"prompt"`
	InsightCount int    `bson:"insight_count" json:"insight_count"`

	// LLM output
	Response   string `bson:"response" json:"response"`
	TokensIn   int    `bson:"tokens_in" json:"tokens_in"`
	TokensOut  int    `bson:"tokens_out" json:"tokens_out"`
	DurationMs int64  `bson:"duration_ms" json:"duration_ms"`

	// Parsed results
	Recommendations []Recommendation `bson:"recommendations" json:"recommendations"`

	Error string `bson:"error,omitempty" json:"error,omitempty"`
}

// ValidationResult captures warehouse verification for an insight or count.
type ValidationResult struct {
	InsightID     string    `bson:"insight_id" json:"insight_id"`
	AnalysisArea  string    `bson:"analysis_area" json:"analysis_area"`
	ValidatedAt   time.Time `bson:"validated_at" json:"validated_at"`

	// What was claimed
	ClaimedCount  int    `bson:"claimed_count" json:"claimed_count"`
	ClaimedMetric string `bson:"claimed_metric,omitempty" json:"claimed_metric,omitempty"`

	// What the warehouse returned
	VerifiedCount int    `bson:"verified_count" json:"verified_count"`
	Query         string `bson:"query" json:"query"`
	QueryError    string `bson:"query_error,omitempty" json:"query_error,omitempty"`

	// Assessment
	Status    string `bson:"status" json:"status"` // "confirmed", "adjusted", "rejected", "error"
	Reasoning string `bson:"reasoning" json:"reasoning"`
}

// ---------------------------------------------------------------------------
// Supporting types
// ---------------------------------------------------------------------------

// SQLMetadata represents metadata about a SQL query that produced an insight.
type SQLMetadata struct {
	Query           string    `bson:"query" json:"query"`
	ExecutionTimeMs int64     `bson:"execution_time_ms" json:"execution_time_ms"`
	RowsReturned    int       `bson:"rows_returned" json:"rows_returned"`
	ExecutedAt      time.Time `bson:"executed_at" json:"executed_at"`
}

// QueryHistory tracks queries executed during discovery.
type QueryHistory struct {
	Query           string    `bson:"query" json:"query"`
	Purpose         string    `bson:"purpose" json:"purpose"`
	ExecutedAt      time.Time `bson:"executed_at" json:"executed_at"`
	Success         bool      `bson:"success" json:"success"`
	Error           string    `bson:"error,omitempty" json:"error,omitempty"`
	FixAttempts     int       `bson:"fix_attempts,omitempty" json:"fix_attempts,omitempty"`
	RowsReturned    int       `bson:"rows_returned,omitempty" json:"rows_returned,omitempty"`
	ExecutionTimeMs int64     `bson:"execution_time_ms,omitempty" json:"execution_time_ms,omitempty"`
}
