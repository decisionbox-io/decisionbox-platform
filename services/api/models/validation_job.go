package models

import "time"

// Validation-job lifecycle states. Each job moves through:
//
//	pending  ─> running ─┬─> completed   (agent finished, verdict persisted)
//	                     ├─> failed      (agent error; verdict may or may not be persisted)
//	                     └─> cancelled   (user-initiated abort)
//
// Workers atomically claim pending → running via FindOneAndUpdate; on
// stale-recovery the worker may flip running → pending again (bumping
// Attempt) when HeartbeatAt is older than the stale threshold.
const (
	ValidationJobStatusPending   = "pending"
	ValidationJobStatusRunning   = "running"
	ValidationJobStatusCompleted = "completed"
	ValidationJobStatusFailed    = "failed"
	ValidationJobStatusCancelled = "cancelled"
)

// Validation-job step labels reported by the agent during execution.
// The dashboard's progress card uses these to render "Running verifier"
// vs "Running refuter" vs "Combining". Empty step means "not yet
// started" (job is still queued or the agent has not started writing
// progress).
const (
	ValidationJobStepVerifier  = "verifier"
	ValidationJobStepRefuter   = "refuter"
	ValidationJobStepCombining = "combining"
)

// Validation-job document kind — discriminates insight vs recommendation
// targets. Mirrors valmodels.DocKind on the agent side but lives here
// so the API model package doesn't need to import the validation
// package just to refer to the constants.
const (
	ValidationJobDocKindInsight        = "insight"
	ValidationJobDocKindRecommendation = "recommendation"
)

// ValidationJob is one queued / running / completed manual-validation
// request. Persisted in the `validation_jobs` collection. Workers
// atomically claim a `pending` row via FindOneAndUpdate; the agent
// writes back to HeartbeatAt + Step as it progresses so stale recovery
// can tell "stuck" from "in flight".
//
// Schema notes:
//   - ID is a UUID, NOT a Mongo ObjectID — handlers generate one on
//     enqueue so the response body can name a stable id immediately.
//   - DocKind is in the unique index key so a future insight/rec UUID
//     collision can't cross-match.
//   - The active-job uniqueness invariant is enforced by a partial
//     unique index registered in init.go (handler-side 409 is racy on
//     its own).
//   - TTL on terminal rows is enforced by a partial TTL index over
//     CompletedAt for status ∈ {completed, failed, cancelled}.
type ValidationJob struct {
	ID          string `bson:"_id" json:"id"`
	ProjectID   string `bson:"project_id" json:"project_id"`
	DiscoveryID string `bson:"discovery_id" json:"discovery_id"`
	// DocKind is one of ValidationJobDocKind* — "insight" or
	// "recommendation".
	DocKind string `bson:"doc_kind" json:"doc_kind"`
	DocID   string `bson:"doc_id" json:"doc_id"`

	// Status is one of ValidationJobStatus* — pending, running,
	// completed, failed, cancelled.
	Status string `bson:"status" json:"status"`

	// Step is the agent's current execution step (verifier / refuter /
	// combining). Empty while pending or before the agent's first
	// progress write.
	Step string `bson:"step,omitempty" json:"step,omitempty"`

	// Error is populated on Status=failed.
	Error string `bson:"error,omitempty" json:"error,omitempty"`

	// Attempt counts how many times this job has been claimed. Starts
	// at 1 on first claim; incremented when stale recovery requeues.
	// Used to bound retries — see ValidationJobMaxAttempts (env var).
	Attempt int `bson:"attempt" json:"attempt"`

	// RequestedBy carries the user identifier from the auth provider
	// (when wired). Empty in unauthenticated dev mode.
	RequestedBy string `bson:"requested_by,omitempty" json:"requested_by,omitempty"`

	// WorkerID names the API instance / pod that claimed the job —
	// surfaced in logs for debugging which worker owns a stuck job.
	WorkerID string `bson:"worker_id,omitempty" json:"worker_id,omitempty"`

	EnqueuedAt time.Time `bson:"enqueued_at" json:"enqueued_at"`
	// ClaimedAt is when the worker FindOneAndUpdate'd it. Equals
	// StartedAt on the common path; only diverges if the agent hangs
	// in startup before writing its first heartbeat.
	ClaimedAt *time.Time `bson:"claimed_at,omitempty" json:"claimed_at,omitempty"`
	// StartedAt is when the agent process actually began work. Written
	// by the agent on its first state-change.
	StartedAt *time.Time `bson:"started_at,omitempty" json:"started_at,omitempty"`
	// HeartbeatAt is the agent's most-recent liveness write. Updated
	// every ~60s and on every step change. Stale recovery compares
	// (now - HeartbeatAt) against ValidationJobStaleAfter.
	HeartbeatAt *time.Time `bson:"heartbeat_at,omitempty" json:"heartbeat_at,omitempty"`
	// CompletedAt is set when Status flips to completed | failed |
	// cancelled. Drives the partial TTL.
	CompletedAt *time.Time `bson:"completed_at,omitempty" json:"completed_at,omitempty"`
	// CancelledAt is the user-initiated cancel timestamp (separate
	// from CompletedAt because we want to know the click time even if
	// the agent took a few seconds to honour the signal).
	CancelledAt *time.Time `bson:"cancelled_at,omitempty" json:"cancelled_at,omitempty"`

	// RunID is the agent process / K8s job name the runner returned —
	// debug aid for correlating with agent logs.
	RunID string `bson:"run_id,omitempty" json:"run_id,omitempty"`
}

// IsTerminal returns true when the job has reached a final state and
// will not transition further without a new Enqueue. Used by the
// active-job conflict check + the dashboard's polling stop condition.
func (j *ValidationJob) IsTerminal() bool {
	switch j.Status {
	case ValidationJobStatusCompleted,
		ValidationJobStatusFailed,
		ValidationJobStatusCancelled:
		return true
	default:
		return false
	}
}
