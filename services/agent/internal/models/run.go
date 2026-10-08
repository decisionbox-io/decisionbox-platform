package models

import "time"

// DiscoveryRun tracks the live status of an agent discovery run.
// Written by the agent as it progresses. Read by the API for dashboard status.
// Stored in the "discovery_runs" collection.
type DiscoveryRun struct {
	ID          string `bson:"_id,omitempty" json:"id"`
	ProjectID   string `bson:"project_id" json:"project_id"`
	Status      string `bson:"status" json:"status"` // pending, running, completed, failed
	Phase       string `bson:"phase" json:"phase"`   // current phase
	PhaseDetail string `bson:"phase_detail" json:"phase_detail"`
	Progress    int    `bson:"progress" json:"progress"` // 0-100

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
	TotalQueries      int `bson:"total_queries" json:"total_queries"`
	SuccessfulQueries int `bson:"successful_queries" json:"successful_queries"`
	FailedQueries     int `bson:"failed_queries" json:"failed_queries"`
	InsightsFound     int `bson:"insights_found" json:"insights_found"`

	// --- Resume lifecycle (issue #438) -------------------------------------
	//
	// All omitempty and all additive: a run that predates resume reads back
	// as attempt 0 with no checkpoint, which is exactly right — it offers no
	// Resume affordance because it has nothing to resume from.

	// Attempt counts how many times this run has been started. 1 on create,
	// incremented by each resume. It is also the handle a future per-attempt
	// charge would key on (runID:attempt) instead of silently no-op'ing
	// against the original run-keyed charge.
	Attempt int `bson:"attempt,omitempty" json:"attempt,omitempty"`

	// LastResumedAt is when the latest attempt was requested.
	LastResumedAt *time.Time `bson:"last_resumed_at,omitempty" json:"last_resumed_at,omitempty"`

	// LastCheckpointStep is the highest exploration step this run has a
	// checkpoint for. It drives the dashboard's Resume affordance and is
	// zeroed on completion (a completed run is not resumable).
	//
	// Stamped only AFTER the checkpoint row is durably written, so it never
	// advertises a checkpoint that does not exist — otherwise the dashboard
	// would offer a Resume that the API then refuses for want of a prefix.
	//
	// It can still exceed the REPLAYABLE prefix: if an earlier write failed
	// (writes are best-effort so they can never abort a run) and a later one
	// succeeded, replay stops at the gap while this reflects the later step.
	// A resume with nothing left to replay is refused rather than silently
	// re-exploring.
	LastCheckpointStep int `bson:"last_checkpoint_step,omitempty" json:"last_checkpoint_step,omitempty"`

	// ActiveMs is cumulative ACTIVE compute time across attempts, so a run
	// resumed the next morning does not report fourteen hours of work.
	// Without it, elapsed time is updated_at - started_at, which counts the
	// hours a failed run sat waiting for someone to notice.
	//
	// Best-effort on hard crashes: the increment lands at the terminal write,
	// so an attempt killed before it reaches that point (OOM, pod eviction,
	// marked failed out-of-process by the sweeper) never records its slice.
	// So this is the active time of attempts that ended cleanly enough to
	// record it — a slight undercount we accept, since a dead attempt's
	// compute is not worth counting.
	ActiveMs int64 `bson:"active_ms,omitempty" json:"active_ms,omitempty"`

	// Lifecycle is the append-only transition log. See RunLifecycleEvent.
	Lifecycle []RunLifecycleEvent `bson:"lifecycle,omitempty" json:"lifecycle,omitempty"`

	// --- The run's own parameters ------------------------------------------
	//
	// Persisted because nothing recorded them before: the run document knew
	// nothing about max_steps, min_steps, areas or effort, so a resumed run
	// would be spawned with the agent's defaults rather than the budget the
	// operator chose — silently changing the run's own shape halfway
	// through. Resume replays them verbatim.
	MaxSteps int      `bson:"max_steps,omitempty" json:"max_steps,omitempty"`
	MinSteps int      `bson:"min_steps,omitempty" json:"min_steps,omitempty"`
	Areas    []string `bson:"areas,omitempty" json:"areas,omitempty"`
	Effort   string   `bson:"effort,omitempty" json:"effort,omitempty"`

	// Schema-retrieval telemetry. Mirrors the API-side model.
	//
	// SchemaTokens / SchemaTableCount describe the boot context size.
	// SchemaLookupCalls / SchemaSearchCalls track the on-demand schema
	// actions the LLM issued during the run. CorrelationLookupCalls tracks
	// get_correlations — how often the run asked what had been decided about
	// correlating two datasources, which is the only way to tell a contract
	// the model followed from one it read past.
	SchemaTokens           int `bson:"schema_tokens,omitempty" json:"schema_tokens,omitempty"`
	SchemaTableCount       int `bson:"schema_table_count,omitempty" json:"schema_table_count,omitempty"`
	SchemaLookupCalls      int `bson:"schema_lookup_calls,omitempty" json:"schema_lookup_calls,omitempty"`
	SchemaSearchCalls      int `bson:"schema_search_calls,omitempty" json:"schema_search_calls,omitempty"`
	CorrelationLookupCalls int `bson:"correlation_lookup_calls,omitempty" json:"correlation_lookup_calls,omitempty"`

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

// RunLifecycleEvent is one append-only entry in a run's lifecycle log.
//
// A run used to be a single mutable status, which is enough while a run has
// exactly one attempt. Once a run can be resumed, "what happened to this run"
// stops being answerable from a status field: the document shows the LATEST
// attempt and silently overwrites every earlier one.
//
// It also answers the question resume raises about provenance — which model
// ran which attempt. Nothing on the run document records a run-level LLM
// model today (provenance lives on debug-log rows), so rather than inventing
// a run-level field that only resume would read, the per-attempt event
// carries it: that is exactly the granularity the question has.
//
// Bounded by the attempt count, which is operator-driven — v1 never resumes
// a run automatically.
type RunLifecycleEvent struct {
	// Status is the status the run entered: pending, running, completed,
	// failed or cancelled.
	Status string    `bson:"status" json:"status"`
	At     time.Time `bson:"at" json:"at"`
	// Reason is free text explaining the transition (the failure message, or
	// what triggered a resume). Empty for uneventful transitions.
	Reason string `bson:"reason,omitempty" json:"reason,omitempty"`
	// Attempt is the attempt this event belongs to, 1-based.
	Attempt int `bson:"attempt,omitempty" json:"attempt,omitempty"`
	// LLMProvider / LLMModel are the models that served this attempt. Set on
	// the terminal event the agent writes, where they are known.
	LLMProvider string `bson:"llm_provider,omitempty" json:"llm_provider,omitempty"`
	LLMModel    string `bson:"llm_model,omitempty" json:"llm_model,omitempty"`
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
	Query       string `bson:"query,omitempty" json:"query,omitempty"`
	QueryResult string `bson:"query_result,omitempty" json:"query_result,omitempty"` // summary, not full data
	RowCount    int    `bson:"row_count,omitempty" json:"row_count,omitempty"`
	QueryTimeMs int64  `bson:"query_time_ms,omitempty" json:"query_time_ms,omitempty"`
	QueryFixed  bool   `bson:"query_fixed,omitempty" json:"query_fixed,omitempty"`
	// WarehouseID is the datasource this step's query ran against
	// (multi-warehouse). Empty on non-query steps + single-warehouse runs.
	WarehouseID string `bson:"warehouse_id,omitempty" json:"warehouse_id,omitempty"`

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
	PhaseQuestions       = "questions"
	PhaseReflection      = "reflection"
	PhaseComplete        = "complete"

	RunStatusPending   = "pending"
	RunStatusRunning   = "running"
	RunStatusCompleted = "completed"
	RunStatusFailed    = "failed"
	// RunStatusCancelled is written by the API, never by the agent — but
	// the agent has to recognise it, because a cancellation outranks
	// anything the agent goes on to conclude.
	RunStatusCancelled = "cancelled"
)
