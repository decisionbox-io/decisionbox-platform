package models

import "time"

// SQLValidationJob is one queued / running / completed batch SQL
// compile-check request. Persisted in the `sql_validation_jobs`
// collection. A job carries a project ID and a batch of SQL statements;
// the agent (`--mode=validate-sql --job-id <id>`) compiles each
// statement against the project's warehouse via
// `warehouse.Provider.ValidateSQL` (compile-only, no execution) and
// writes a per-statement verdict back to Results.
//
// The state machine and liveness fields mirror ValidationJob exactly so
// the same stale-recovery / heartbeat reasoning applies:
//
//	pending  ─> running ─┬─> completed   (agent finished, verdicts persisted)
//	                     ├─> failed      (provider construction or job-level error)
//	                     └─> cancelled   (caller-initiated abort)
//
// Status constants are shared with ValidationJob (ValidationJobStatus*)
// — the lifecycle is identical, only the payload differs (statements +
// results instead of a single doc reference).
//
// Schema notes:
//   - ID is a UUID, NOT a Mongo ObjectID — callers generate one on
//     enqueue so the response body can name a stable id immediately.
//   - Unlike ValidationJob there is no (discovery, doc) active-job
//     uniqueness invariant: a batch is keyed only by its own id, and
//     two callers may legitimately validate overlapping statement sets
//     for the same project concurrently.
//   - TTL on terminal rows is enforced by a partial TTL index over
//     CompletedAt for status ∈ {completed, failed, cancelled}, the same
//     shape validation_jobs uses (see init.go).
type SQLValidationJob struct {
	ID        string `bson:"_id" json:"id"`
	ProjectID string `bson:"project_id" json:"project_id"`
	// WarehouseID selects the datasource the agent compiles these statements
	// against (multi-warehouse). Empty = the project's primary/only warehouse,
	// so legacy jobs keep validating against the primary.
	WarehouseID string `bson:"warehouse_id,omitempty" json:"warehouse_id,omitempty"`

	// Statements is the batch of SQL strings to compile-check. May be
	// empty — an empty batch completes immediately with no results.
	Statements []string `bson:"statements" json:"statements"`

	// Results carries one verdict per statement, in the same order as
	// Statements. Populated by the agent on completion; empty while the
	// job is pending / running.
	Results []SQLValidationResult `bson:"results,omitempty" json:"results,omitempty"`

	// Status is one of ValidationJobStatus* — pending, running,
	// completed, failed, cancelled.
	Status string `bson:"status" json:"status"`

	// Error is populated on Status=failed (provider construction failure
	// or a job-level error). Per-statement compile failures are NOT
	// job-level errors — they are recorded as Results[i].OK=false.
	Error string `bson:"error,omitempty" json:"error,omitempty"`

	// Attempt counts how many times this job has been claimed. Starts at
	// 1 on first claim; incremented when stale recovery requeues.
	Attempt int `bson:"attempt" json:"attempt"`

	// RequestedBy carries the user identifier from the auth provider
	// (when wired). Empty in unauthenticated dev mode.
	RequestedBy string `bson:"requested_by,omitempty" json:"requested_by,omitempty"`

	// WorkerID names the process that claimed the job — surfaced in logs
	// for debugging which run owns a stuck job.
	WorkerID string `bson:"worker_id,omitempty" json:"worker_id,omitempty"`

	EnqueuedAt time.Time `bson:"enqueued_at" json:"enqueued_at"`
	// ClaimedAt is when the job was atomically claimed (pending →
	// running).
	ClaimedAt *time.Time `bson:"claimed_at,omitempty" json:"claimed_at,omitempty"`
	// StartedAt is when the agent process actually began work. Written by
	// the agent on its first state-change.
	StartedAt *time.Time `bson:"started_at,omitempty" json:"started_at,omitempty"`
	// HeartbeatAt is the agent's most-recent liveness write. Updated
	// every ~60s while the batch runs.
	HeartbeatAt *time.Time `bson:"heartbeat_at,omitempty" json:"heartbeat_at,omitempty"`
	// CompletedAt is set when Status flips to completed | failed |
	// cancelled. Drives the partial TTL.
	CompletedAt *time.Time `bson:"completed_at,omitempty" json:"completed_at,omitempty"`
	// CancelledAt is the caller-initiated cancel timestamp.
	CancelledAt *time.Time `bson:"cancelled_at,omitempty" json:"cancelled_at,omitempty"`

	// RunID is the agent process / K8s job name the dispatcher returned —
	// debug aid for correlating with agent logs.
	RunID string `bson:"run_id,omitempty" json:"run_id,omitempty"`
}

// SQLValidationResult is the per-statement verdict the agent records.
// OK is true iff the warehouse accepted the statement through its native
// compile-only path. On failure, Error carries the warehouse's own error
// text so the caller can echo it directly.
type SQLValidationResult struct {
	SQL   string `bson:"sql" json:"sql"`
	OK    bool   `bson:"ok" json:"ok"`
	Error string `bson:"error,omitempty" json:"error,omitempty"`
}

// IsTerminal returns true when the job has reached a final state and will
// not transition further without a new Enqueue. Used by the dashboard's /
// caller's polling stop condition.
func (j *SQLValidationJob) IsTerminal() bool {
	switch j.Status {
	case ValidationJobStatusCompleted,
		ValidationJobStatusFailed,
		ValidationJobStatusCancelled:
		return true
	default:
		return false
	}
}
