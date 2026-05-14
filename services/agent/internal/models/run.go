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

	// DiscoveryID is the `_id` of the `discoveries` document this run
	// produced. Stamped by the agent in RunRepository.Complete
	// immediately before the status flip, so a run with
	// status="completed" always has it set. Run-completion hook
	// consumers (plugin-hooks.md Hook 5) read it to query insights
	// / recommendations / any collection keyed on discovery_id —
	// without this back-reference the link between run and
	// discovery is implicit and fragile.
	DiscoveryID string `bson:"discovery_id,omitempty" json:"discovery_id,omitempty"`

	// Live step log used to be embedded here as `Steps []RunStep`. The
	// $push streaming pattern produced unbounded growth on long runs and
	// hit the same 16MB BSON limit that killed discovery saves. Each
	// RunStep now lands in the discovery_run_steps collection
	// (RunStepRepository) keyed by run_id; the dashboard pulls them via
	// GET /api/v1/runs/{id}/steps with a `since` cursor for streaming.

	// Summary stats (updated as run progresses)
	TotalQueries     int `bson:"total_queries" json:"total_queries"`
	SuccessfulQueries int `bson:"successful_queries" json:"successful_queries"`
	FailedQueries    int `bson:"failed_queries" json:"failed_queries"`
	InsightsFound    int `bson:"insights_found" json:"insights_found"`

	// Schema-retrieval telemetry. Mirrors the API-side model.
	//
	// SchemaTokens / SchemaTableCount describe the boot context size.
	// SchemaLookupCalls / SchemaSearchCalls track the on-demand schema
	// actions the LLM issued during the run.
	SchemaTokens      int `bson:"schema_tokens,omitempty" json:"schema_tokens,omitempty"`
	SchemaTableCount  int `bson:"schema_table_count,omitempty" json:"schema_table_count,omitempty"`
	SchemaLookupCalls int `bson:"schema_lookup_calls,omitempty" json:"schema_lookup_calls,omitempty"`
	SchemaSearchCalls int `bson:"schema_search_calls,omitempty" json:"schema_search_calls,omitempty"`

	// Analysis-phase compaction telemetry. Counts how many steps the
	// run-scoped step index ingested, how many area-level searches
	// the picker issued, and how many steps the picker dropped (sum
	// across all areas in this run).
	AnalysisStepIndexUpserts     int `bson:"analysis_step_index_upserts,omitempty" json:"analysis_step_index_upserts,omitempty"`
	AnalysisStepIndexSearchCalls int `bson:"analysis_step_index_search_calls,omitempty" json:"analysis_step_index_search_calls,omitempty"`
	AnalysisStepsDropped         int `bson:"analysis_steps_dropped,omitempty" json:"analysis_steps_dropped,omitempty"`

	// CompletionHooksFiredAt mirrors the API-side field. The agent does
	// not read or write it (only the API's run-completion dispatcher
	// does), but it lives on the shared schema so the Mongo document
	// shape stays consistent and a hand-edited document with the field
	// set survives an agent rewrite.
	CompletionHooksFiredAt *time.Time `bson:"completion_hooks_fired_at,omitempty" json:"-"`
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

	// Per-step LLM token usage. Summed across any internal retries
	// that share the same RunStep — e.g. the three validation LLM
	// calls per insight collapse onto one validation RunStep.
	// omitempty so legacy rows render as absent rather than 0,
	// preserving the "unknown vs. zero" distinction.
	InputTokens  int `bson:"input_tokens,omitempty" json:"input_tokens,omitempty"`
	OutputTokens int `bson:"output_tokens,omitempty" json:"output_tokens,omitempty"`
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
