package models

import "time"

// DiscoveryRun tracks the live status of an agent discovery run.
// Same schema as agent's model (both read/write same collection).
type DiscoveryRun struct {
	ID          string `bson:"_id,omitempty" json:"id"`
	ProjectID   string `bson:"project_id" json:"project_id"`
	Status      string `bson:"status" json:"status"`
	Phase       string `bson:"phase" json:"phase"`
	PhaseDetail string `bson:"phase_detail" json:"phase_detail"`
	Progress    int    `bson:"progress" json:"progress"`

	StartedAt   time.Time  `bson:"started_at" json:"started_at"`
	UpdatedAt   time.Time  `bson:"updated_at" json:"updated_at"`
	CompletedAt *time.Time `bson:"completed_at,omitempty" json:"completed_at,omitempty"`
	Error       string     `bson:"error,omitempty" json:"error,omitempty"`

	// DiscoveryID is the `_id` of the `discoveries` document this run
	// produced. Stamped by the agent immediately before it flips the
	// run to `completed`, so a run with status="completed" has this
	// field set. Run-completion hook consumers read it to query
	// `insights` / `recommendations` / any other collection keyed on
	// `discovery_id` — without this back-reference the link between
	// the run and its discovery is implicit (created-around-the-same-
	// time-for-the-same-project) and fragile.
	DiscoveryID string `bson:"discovery_id,omitempty" json:"discovery_id,omitempty"`

	// Steps used to be embedded here. They now live in the
	// discovery_run_steps collection (RunStepRepository); the dashboard
	// pulls them via GET /api/v1/runs/{id}/steps with a `since` cursor.

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
	// It is the highest step CHECKPOINTED, which is normally also the
	// replayable prefix. The two differ only if an earlier checkpoint write
	// failed — writes are best-effort so they can never abort a run — in
	// which case replay honestly stops at the gap and this reads as an
	// over-estimate of the work a resume would skip.
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

	// Schema-retrieval telemetry. SchemaTokens / SchemaTableCount are
	// stamped once at run start from the rendered catalog. The Lookup /
	// Search counters increment as the engine serves on-demand schema
	// actions (lookup_schema and search_tables) issued by the LLM.
	// CorrelationLookupCalls counts get_correlations the same way.
	SchemaTokens           int `bson:"schema_tokens,omitempty" json:"schema_tokens,omitempty"`
	SchemaTableCount       int `bson:"schema_table_count,omitempty" json:"schema_table_count,omitempty"`
	SchemaLookupCalls      int `bson:"schema_lookup_calls,omitempty" json:"schema_lookup_calls,omitempty"`
	SchemaSearchCalls      int `bson:"schema_search_calls,omitempty" json:"schema_search_calls,omitempty"`
	CorrelationLookupCalls int `bson:"correlation_lookup_calls,omitempty" json:"correlation_lookup_calls,omitempty"`

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

	// CompletionHooksFiredAt records the moment the API's run-completion
	// dispatcher fired the registered completion hooks for this run
	// (plugin-hooks.md, Hook 5). Nil until every hook has returned nil.
	// The dispatcher uses this field to find runs that have terminated
	// (status in {completed, failed, cancelled}) but still need hook
	// dispatch, so the work is idempotent across API restarts and ticks.
	CompletionHooksFiredAt *time.Time `bson:"completion_hooks_fired_at,omitempty" json:"-"`
}

// SupersededByResumeReason is the outcome recorded against a plan reservation
// opened by an attempt that a resume has since superseded. One definition so
// the resume path, the background confirmer and the cancel path cannot drift
// into reporting the same thing three ways.
const SupersededByResumeReason = "attempt superseded by a resume"

// ReservationBelongsToASupersededAttempt reports whether the reservation
// still recorded on this run was opened by an attempt a resume replaced.
//
// Knowable without storing anything extra because a resume opens NO
// reservation of its own: one still present on a run past its first attempt
// can only have been opened by an earlier attempt. The resume path confirms
// and clears it, so finding one here means that confirm failed — and
// whatever closes it afterwards must report the SUPERSEDED attempt's
// outcome, not the outcome of whatever the run went on to do.
// LatestAttemptAt is when this run was most recently STARTED — its original
// start, or the moment of its last resume if it has been resumed.
//
// started_at is when the run was created and never moves, which is right for
// history and wrong for "which run is the operator looking at". Resuming an
// older failed run makes it the live one while keeping the older timestamp,
// so ordering by started_at hides it behind any newer run the moment it is
// no longer active.
func (r *DiscoveryRun) LatestAttemptAt() time.Time {
	if r.LastResumedAt != nil && r.LastResumedAt.After(r.StartedAt) {
		return *r.LastResumedAt
	}
	return r.StartedAt
}

func (r *DiscoveryRun) ReservationBelongsToASupersededAttempt() bool {
	return r.PolicyReservationID != "" && r.Attempt > 1
}

// SupersededAttemptEndedAt is when the attempt that owns a lingering
// reservation stopped being the live one.
//
// A superseded attempt never writes an end time of its own — it is replaced
// mid-flight, not finished — so the moment of the resume is the closest thing
// there is. Zero when even that is unknown, which callers pass through as
// "unknown" rather than substituting a later clock reading: the reservation's
// recorded duration is accounting, and the gap between being superseded and
// whatever closes it later can be hours.
//
// Shared deliberately. Two places close such a reservation — the cancel
// handler and the background confirmer that retries a failed confirm — and
// the outcome must not depend on which arrives first.
func (r *DiscoveryRun) SupersededAttemptEndedAt() time.Time {
	if r.LastResumedAt != nil {
		return *r.LastResumedAt
	}
	if r.CompletedAt != nil {
		return *r.CompletedAt
	}
	return time.Time{}
}

// RunParams is the shape of one discovery run: the budget and scope the
// caller asked for.
//
// Persisted on the run document at creation so a resume can replay it
// verbatim. Without it the only record of a run's own parameters was the
// agent process's argv, which does not survive the process.
type RunParams struct {
	MaxSteps int
	MinSteps int
	Areas    []string
	Effort   string
	// Source is what triggered the run ("manual", "scheduler", ...). Recorded
	// as the reason on the first lifecycle event.
	Source string
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

type RunStep struct {
	Phase       string    `bson:"phase" json:"phase"`
	StepNum     int       `bson:"step_num,omitempty" json:"step_num,omitempty"`
	Timestamp   time.Time `bson:"timestamp" json:"timestamp"`
	Type        string    `bson:"type" json:"type"`
	Message     string    `bson:"message" json:"message"`
	LLMThinking string    `bson:"llm_thinking,omitempty" json:"llm_thinking,omitempty"`
	LLMQuery    string    `bson:"llm_query,omitempty" json:"llm_query,omitempty"`
	Query       string    `bson:"query,omitempty" json:"query,omitempty"`
	QueryResult string    `bson:"query_result,omitempty" json:"query_result,omitempty"`
	RowCount    int       `bson:"row_count,omitempty" json:"row_count,omitempty"`
	QueryTimeMs int64     `bson:"query_time_ms,omitempty" json:"query_time_ms,omitempty"`
	QueryFixed  bool      `bson:"query_fixed,omitempty" json:"query_fixed,omitempty"`
	// WarehouseID is the datasource this step's query ran against
	// (multi-warehouse). Empty on non-query steps + single-warehouse runs.
	WarehouseID     string `bson:"warehouse_id,omitempty" json:"warehouse_id,omitempty"`
	InsightName     string `bson:"insight_name,omitempty" json:"insight_name,omitempty"`
	InsightSeverity string `bson:"insight_severity,omitempty" json:"insight_severity,omitempty"`
	Error           string `bson:"error,omitempty" json:"error,omitempty"`
	DurationMs      int64  `bson:"duration_ms,omitempty" json:"duration_ms,omitempty"`

	// Per-step LLM token usage. Mirror of the agent-side field.
	// omitempty so legacy rows render as absent rather than 0.
	InputTokens  int `bson:"input_tokens,omitempty" json:"input_tokens,omitempty"`
	OutputTokens int `bson:"output_tokens,omitempty" json:"output_tokens,omitempty"`
}
