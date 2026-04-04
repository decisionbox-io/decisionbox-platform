package models

import "time"

// DiscoveryRun tracks the live status of an agent discovery run.
// Written by the agent as it progresses. Read by the API for dashboard status.
// Stored in the "discovery_runs" collection.
type DiscoveryRun struct {
	ID          string    `bson:"_id,omitempty" json:"id"`
	ProjectID   string    `bson:"project_id" json:"project_id"`
	Status      string    `bson:"status" json:"status"` // pending, running, completed, failed
	Phase       string    `bson:"phase" json:"phase"`   // current phase
	PhaseDetail string    `bson:"phase_detail" json:"phase_detail"`
	Progress    int       `bson:"progress" json:"progress"` // 0-100

	StartedAt   time.Time  `bson:"started_at" json:"started_at"`
	UpdatedAt   time.Time  `bson:"updated_at" json:"updated_at"`
	CompletedAt *time.Time `bson:"completed_at,omitempty" json:"completed_at,omitempty"`
	Error       string     `bson:"error,omitempty" json:"error,omitempty"`

	// Live step log — each step the agent takes is appended here.
	// The dashboard can show this as a real-time conversation/activity feed.
	Steps []RunStep `bson:"steps" json:"steps"`

	// Summary stats (updated as run progresses)
	TotalQueries     int `bson:"total_queries" json:"total_queries"`
	SuccessfulQueries int `bson:"successful_queries" json:"successful_queries"`
	FailedQueries    int `bson:"failed_queries" json:"failed_queries"`
	InsightsFound    int `bson:"insights_found" json:"insights_found"`
}

// RunStep is a single step in the discovery run log.
// Rich enough to render as a chat/conversation in the dashboard.
type RunStep struct {
	Phase     string    `bson:"phase" json:"phase"`
	StepNum   int       `bson:"step_num,omitempty" json:"step_num,omitempty"`
	Timestamp time.Time `bson:"timestamp" json:"timestamp"`
	Type      string    `bson:"type" json:"type"` // phase_start, phase_end, query, analysis, insight, error, info

	// Human-readable message
	Message string `bson:"message" json:"message"`

	// LLM conversation (for chat view)
	LLMThinking string `bson:"llm_thinking,omitempty" json:"llm_thinking,omitempty"`
	LLMQuery    string `bson:"llm_query,omitempty" json:"llm_query,omitempty"`

	// Query details
	Query         string `bson:"query,omitempty" json:"query,omitempty"`
	QueryResult   string `bson:"query_result,omitempty" json:"query_result,omitempty"` // summary, not full data
	RowCount      int    `bson:"row_count,omitempty" json:"row_count,omitempty"`
	QueryTimeMs   int64  `bson:"query_time_ms,omitempty" json:"query_time_ms,omitempty"`
	QueryFixed    bool   `bson:"query_fixed,omitempty" json:"query_fixed,omitempty"`

	// Insight details
	InsightName     string `bson:"insight_name,omitempty" json:"insight_name,omitempty"`
	InsightSeverity string `bson:"insight_severity,omitempty" json:"insight_severity,omitempty"`

	// Error details
	Error string `bson:"error,omitempty" json:"error,omitempty"`

	DurationMs int64 `bson:"duration_ms,omitempty" json:"duration_ms,omitempty"`
}

// Phase constants
const (
	PhaseInit            = "init"
	PhaseSchemaDiscovery = "schema_discovery"
	PhaseExploration     = "exploration"
	PhaseAnalysis        = "analysis"
	PhaseValidation      = "validation"
	PhaseRecommendations = "recommendations"
	PhaseSaving          = "saving"
	PhaseEmbedIndex      = "embed_index"
	PhaseComplete        = "complete"

	RunStatusPending   = "pending"
	RunStatusRunning   = "running"
	RunStatusCompleted = "completed"
	RunStatusFailed    = "failed"
)
