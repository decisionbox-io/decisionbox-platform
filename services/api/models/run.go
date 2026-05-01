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

	// Steps used to be embedded here. They now live in the
	// discovery_run_steps collection (RunStepRepository); the dashboard
	// pulls them via GET /api/v1/runs/{id}/steps with a `since` cursor.

	TotalQueries      int `bson:"total_queries" json:"total_queries"`
	SuccessfulQueries int `bson:"successful_queries" json:"successful_queries"`
	FailedQueries     int `bson:"failed_queries" json:"failed_queries"`
	InsightsFound     int `bson:"insights_found" json:"insights_found"`

	// Schema-retrieval telemetry. SchemaTokens / SchemaTableCount are
	// stamped once at run start from the rendered catalog. The Lookup /
	// Search counters increment as the engine serves on-demand schema
	// actions (lookup_schema and search_tables) issued by the LLM.
	SchemaTokens      int `bson:"schema_tokens,omitempty" json:"schema_tokens,omitempty"`
	SchemaTableCount  int `bson:"schema_table_count,omitempty" json:"schema_table_count,omitempty"`
	SchemaLookupCalls int `bson:"schema_lookup_calls,omitempty" json:"schema_lookup_calls,omitempty"`
	SchemaSearchCalls int `bson:"schema_search_calls,omitempty" json:"schema_search_calls,omitempty"`

	// Analysis-phase compaction telemetry. Mirrored on the agent
	// model. Counts how many exploration steps were indexed for
	// analysis selection, how many area-level vector searches the
	// picker issued, and how many steps the picker dropped (sum
	// across all areas in this run).
	AnalysisStepIndexUpserts     int `bson:"analysis_step_index_upserts,omitempty" json:"analysis_step_index_upserts,omitempty"`
	AnalysisStepIndexSearchCalls int `bson:"analysis_step_index_search_calls,omitempty" json:"analysis_step_index_search_calls,omitempty"`
	AnalysisStepsDropped         int `bson:"analysis_steps_dropped,omitempty" json:"analysis_steps_dropped,omitempty"`

	// PolicyReservationID is the reservation the API opened when the run
	// was triggered (plan-gated concurrent-runs-per-project and
	// runs-per-period counters). Empty when the policy Checker is Noop
	// (self-hosted) or when the reservation has already been confirmed
	// or released. Persisted so exit handlers outside the trigger
	// request scope (cancel, agent-completion callback, crash sweeper)
	// can resolve it back to the control plane.
	PolicyReservationID string `bson:"policy_reservation_id,omitempty" json:"-"`
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
