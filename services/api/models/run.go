package models

import "time"

// DiscoveryRun tracks the live status of an agent discovery run.
// Same schema as agent's model (both read/write same collection).
type DiscoveryRun struct {
	ID          string    `bson:"_id,omitempty" json:"id"`
	ProjectID   string    `bson:"project_id" json:"project_id"`
	Status      string    `bson:"status" json:"status"`
	Phase       string    `bson:"phase" json:"phase"`
	PhaseDetail string    `bson:"phase_detail" json:"phase_detail"`
	Progress    int       `bson:"progress" json:"progress"`

	StartedAt   time.Time  `bson:"started_at" json:"started_at"`
	UpdatedAt   time.Time  `bson:"updated_at" json:"updated_at"`
	CompletedAt *time.Time `bson:"completed_at,omitempty" json:"completed_at,omitempty"`
	Error       string     `bson:"error,omitempty" json:"error,omitempty"`

	Steps []RunStep `bson:"steps" json:"steps"`

	TotalQueries      int `bson:"total_queries" json:"total_queries"`
	SuccessfulQueries int `bson:"successful_queries" json:"successful_queries"`
	FailedQueries     int `bson:"failed_queries" json:"failed_queries"`
	InsightsFound     int `bson:"insights_found" json:"insights_found"`
}

type RunStep struct {
	Phase           string    `bson:"phase" json:"phase"`
	StepNum         int       `bson:"step_num,omitempty" json:"step_num,omitempty"`
	Timestamp       time.Time `bson:"timestamp" json:"timestamp"`
	Type            string    `bson:"type" json:"type"`
	Message         string    `bson:"message" json:"message"`
	LLMThinking     string    `bson:"llm_thinking,omitempty" json:"llm_thinking,omitempty"`
	LLMQuery        string    `bson:"llm_query,omitempty" json:"llm_query,omitempty"`
	Query           string    `bson:"query,omitempty" json:"query,omitempty"`
	QueryResult     string    `bson:"query_result,omitempty" json:"query_result,omitempty"`
	RowCount        int       `bson:"row_count,omitempty" json:"row_count,omitempty"`
	QueryTimeMs     int64     `bson:"query_time_ms,omitempty" json:"query_time_ms,omitempty"`
	QueryFixed      bool      `bson:"query_fixed,omitempty" json:"query_fixed,omitempty"`
	InsightName     string    `bson:"insight_name,omitempty" json:"insight_name,omitempty"`
	InsightSeverity string    `bson:"insight_severity,omitempty" json:"insight_severity,omitempty"`
	Error           string    `bson:"error,omitempty" json:"error,omitempty"`
	DurationMs      int64     `bson:"duration_ms,omitempty" json:"duration_ms,omitempty"`
}
