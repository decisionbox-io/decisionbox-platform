package database

import (
	"context"
	"fmt"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// RunRepository manages DiscoveryRun status documents. Collection name
// lives in mongodb.go (sourced from libs/go-common/mongodb).
type RunRepository struct {
	col *mongo.Collection
}

func NewRunRepository(db *DB) *RunRepository {
	return &RunRepository{col: db.Collection(CollectionDiscoveryRuns)}
}

// Create creates a new discovery run and returns its ID. Per-step rows
// are now persisted in the discovery_run_steps collection via
// RunStepRepository — no embedded `steps` slice initialisation here.
func (r *RunRepository) Create(ctx context.Context, run *models.DiscoveryRun) (string, error) {
	run.StartedAt = time.Now()
	run.UpdatedAt = time.Now()
	run.Status = models.RunStatusPending

	result, err := r.col.InsertOne(ctx, run)
	if err != nil {
		return "", fmt.Errorf("create run: %w", err)
	}

	if oid, ok := result.InsertedID.(primitive.ObjectID); ok {
		return oid.Hex(), nil
	}
	return "", nil
}

// UpdateStatus updates the run's status, phase, and detail.
//
// Attempt-fenced, and additionally barred from overriding a cancellation —
// for the same reasons as Complete below, including why `failed` has to stay
// matchable. The hazard here is sharper than it looks: this write sets
// `status` to `running`, so one late phase update after a cancel does not
// merely mislabel the run, it clears the `cancelled` that Complete's own
// guard tests for. Cancel would then be fully undone by the pair of them,
// and the resulting Complete reports a CLAIM, which is what licenses
// retireSupersededAttempts and discardCheckpoints to start deleting.
//
// Reachable because a cancel does not change the attempt and does not stop
// the agent instantly: the API writes `cancelled` and kills the workload,
// and the process can emit one more SetPhase on its way out.
func (r *RunRepository) UpdateStatus(ctx context.Context, runID string, status, phase, detail string, progress int, attempt int) error {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}

	update := bson.M{
		"$set": bson.M{
			"status":       status,
			"phase":        phase,
			"phase_detail": detail,
			"progress":     progress,
			"updated_at":   time.Now(),
		},
	}

	filter := attemptFilter(oid, attempt)
	filter["status"] = bson.M{"$ne": models.RunStatusCancelled}
	filter["discarded_at"] = nil

	_, err = r.col.UpdateOne(ctx, filter, update)
	return err
}

// RecordSchemaContextTelemetry stamps the one-shot counters that describe
// the schema context the run used. Called once, immediately after the
// schema renderer builds the catalog. The on-demand action counters
// (lookup_schema, search_tables) are updated separately via
// IncrementSchemaActionCalls as the engine services each action.
func (r *RunRepository) RecordSchemaContextTelemetry(ctx context.Context, runID string, tokens, tableCount int, attempt int) error {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}
	update := bson.M{
		"$set": bson.M{
			"schema_tokens":      tokens,
			"schema_table_count": tableCount,
			"updated_at":         time.Now(),
		},
	}
	_, err = r.col.UpdateOne(ctx, attemptFilter(oid, attempt), update)
	return err
}

// IncrementSchemaActionCalls atomically bumps the per-action counters
// on a run. action is one of "lookup_schema", "search_tables" or
// "get_correlations"; any other value is a no-op so a future action type
// doesn't accidentally roll into the wrong counter. Safe to call
// concurrently.
func (r *RunRepository) IncrementSchemaActionCalls(ctx context.Context, runID, action string, delta int, attempt int) error {
	if delta <= 0 {
		return nil
	}
	var field string
	switch action {
	case "lookup_schema":
		field = "schema_lookup_calls"
	case "search_tables":
		field = "schema_search_calls"
	case "get_correlations":
		field = "correlation_lookup_calls"
	default:
		return nil
	}
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}
	update := bson.M{
		"$inc": bson.M{field: delta},
		"$set": bson.M{"updated_at": time.Now()},
	}
	_, err = r.col.UpdateOne(ctx, attemptFilter(oid, attempt), update)
	return err
}

// IncrementAnalysisCounter atomically bumps one of the analysis-
// phase compaction counters on a run. metric is one of:
//
//   - "step_index_upserts"      → analysis_step_index_upserts
//   - "step_index_search_calls" → analysis_step_index_search_calls
//   - "steps_dropped"           → analysis_steps_dropped
//
// Any other value is a no-op so a future metric name doesn't roll
// into the wrong field.
func (r *RunRepository) IncrementAnalysisCounter(ctx context.Context, runID, metric string, delta int, attempt int) error {
	if delta <= 0 {
		return nil
	}
	var field string
	switch metric {
	case "step_index_upserts":
		field = "analysis_step_index_upserts"
	case "step_index_search_calls":
		field = "analysis_step_index_search_calls"
	case "steps_dropped":
		field = "analysis_steps_dropped"
	default:
		return nil
	}
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}
	update := bson.M{
		"$inc": bson.M{field: delta},
		"$set": bson.M{"updated_at": time.Now()},
	}
	_, err = r.col.UpdateOne(ctx, attemptFilter(oid, attempt), update)
	return err
}

// AddStep was removed — per-step rows now go to the discovery_run_steps
// collection via RunStepRepository. The previous $push into the embedded
// steps array hit the 16MB BSON limit on long runs.

// Complete marks a run as completed and stamps the discovery_id
// the run produced. The link is critical for run-completion hook
// consumers (plugin-hooks.md Hook 5) — without it they can't query
// insights / recommendations (both keyed on discovery_id), and the
// implicit "run and discovery created around the same time" linkage
// is fragile when concurrent runs land in the same project.
//
// discoveryID is required: a run that completes without producing a
// discovery is a contract violation the caller must surface. An
// empty string returns an error rather than silently writing a
// half-state.
//
// Returns whether the write landed. That is this attempt's CLAIM on the run,
// and the caller needs it: the attempt that did not claim the run must not go
// on to delete the other attempt's results.
func (r *RunRepository) Complete(ctx context.Context, runID, discoveryID string, insightsFound int, attempt int) (bool, error) {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return false, fmt.Errorf("invalid run ID: %w", err)
	}
	if discoveryID == "" {
		return false, fmt.Errorf("run %s: complete requires a discovery_id", runID)
	}

	now := time.Now()
	update := bson.M{
		"$set": bson.M{
			"status":         models.RunStatusCompleted,
			"phase":          models.PhaseComplete,
			"phase_detail":   "Discovery completed successfully",
			"progress":       100,
			"completed_at":   now,
			"updated_at":     now,
			"insights_found": insightsFound,
			"discovery_id":   discoveryID,
			// A completed run has nothing left to resume, so the
			// dashboard's Resume affordance must not survive it. Zeroed
			// here rather than relying on the checkpoint rows being gone:
			// their deletion is best-effort, and an offered-but-impossible
			// Resume is worse than none.
			"last_checkpoint_step": 0,
		},
	}

	// Attempt-fenced, and additionally barred from overriding a
	// cancellation.
	//
	// Cancel is a deliberate hard kill that stays terminal: the API writes
	// `cancelled` and deletes the checkpoints. An agent still finishing its
	// save would otherwise match on (_id, attempt) — the cancel does not
	// change the attempt — and flip the run to `completed`, erasing the
	// cancellation. Worse since this write began reporting whether it
	// claimed the run: a claim licenses retireSupersededAttempts and
	// discardCheckpoints, so a late Complete after a cancel would not just
	// mislabel the run but start deleting on the strength of it.
	//
	// Deliberately NOT the pending/running predicate Fail uses. `failed` has
	// to stay matchable here, because the API's startup sweep marks
	// in-flight runs failed WITHOUT reaping their agents: an agent that then
	// finishes genuinely has a discovery to record, and refusing it would
	// leave a complete result saved but unreachable behind a `failed` run,
	// inviting a resume that re-runs analysis for nothing. A resumed run is
	// already excluded by the attempt fence, not by the status.
	//
	// A DISCARD is the exception to that, which is why discarded_at is
	// fenced here even though `failed` is not: the sweep's `failed` is the
	// API guessing that a run died, and an agent that proves otherwise
	// should be allowed to record what it found. A discard is the operator
	// saying they do not want this run. Completing it would resurrect a run
	// they threw away — with a discovery attached, and visible again as the
	// project's state, since a completed run is terminal-but-present.
	filter := attemptFilter(oid, attempt)
	filter["status"] = bson.M{"$ne": models.RunStatusCancelled}
	filter["discarded_at"] = nil

	res, err := r.col.UpdateOne(ctx, filter, update)
	if err != nil {
		return false, err
	}
	return res.MatchedCount > 0, nil
}

// attemptFilter builds a run filter that matches only while the run is still
// on the given attempt.
//
// It fences a previous attempt's agent that is somehow still alive. That is
// reachable: the API's startup sweep marks in-flight runs `failed` after a
// restart WITHOUT reaping their workloads, so an operator resuming such a run
// can have two agents on one run id — and the terminal status the agent writes
// is the authoritative one. Without this, the orphan's eventual Complete or
// Fail would overwrite the live attempt's outcome.
//
// attempt <= 0 means "unknown" and matches anything, which is what a caller
// that cannot say its attempt gets — the behaviour before attempts existed.
// Attempt 1 also matches a document with NO attempt field, which is how every
// run created before the counter existed reads.
func attemptFilter(oid primitive.ObjectID, attempt int) bson.M {
	filter := bson.M{"_id": oid}
	switch {
	case attempt == 1:
		filter["attempt"] = bson.M{"$in": []any{1, nil}}
	case attempt > 1:
		filter["attempt"] = attempt
	}
	return filter
}

// Fail marks a run as failed. discoveryID is the _id of the partial
// DiscoveryResult that was persisted before the failure, when one
// exists — this is the case for cancellation mid-compute where
// exploration finished, the orchestrator saved a partial result,
// and then the outer cap fired. Empty discoveryID is allowed for
// failures that produced no persisted discovery (factory errors,
// warehouse health-check failure, etc.); in that case the
// discovery_id field is not stamped and the run record stays
// without a back-reference.
//
// Terminal-status invariant: Fail only writes when the run is
// currently in a non-terminal status (`pending` or `running`). A
// run that already reached `completed`, `failed`, or `cancelled`
// is left alone — see e.g. the K8s watcher's exhaustion fallback,
// which fires the OnFailure callback even when the agent has
// already stamped Complete or the user has already cancelled the
// run. Without this guard a successful discovery could be flipped
// to failed hours later by an inconclusive watcher timeout. The
// caller can detect the no-op via NoTerminalStatusChange.
//
// The plugin-hooks Hook 5 and the discovery-log APIs key off
// DiscoveryRun.DiscoveryID; stamping it on the failed run lets
// consumers navigate to the partial result the same way they would
// for a completed run.
func (r *RunRepository) Fail(ctx context.Context, runID, discoveryID, errMsg string, attempt int) (bool, error) {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return false, fmt.Errorf("invalid run ID: %w", err)
	}

	now := time.Now()
	set := bson.M{
		"status":       models.RunStatusFailed,
		"phase_detail": "Discovery failed: " + errMsg,
		"error":        errMsg,
		"completed_at": now,
		"updated_at":   now,
	}
	if discoveryID != "" {
		set["discovery_id"] = discoveryID
	}

	filter := attemptFilter(oid, attempt)
	filter["status"] = bson.M{"$in": []string{
		models.RunStatusPending,
		models.RunStatusRunning,
	}}
	res, err := r.col.UpdateOne(ctx, filter, bson.M{"$set": set})
	if err != nil {
		return false, err
	}
	return res.MatchedCount > 0, nil
}

// MarkExplorationCheckpoint records that a checkpoint now exists for this
// step, which is what the dashboard reads to offer Resume on a failed run.
//
// $max rather than $set: a resumed run re-checkpoints the prefix it replayed,
// so a plain write would walk the value back down to 1 and climb again,
// making the field briefly claim less progress than the run actually has.
// Returns whether the write landed. Because the filter is attempt-fenced,
// that doubles as a free ownership probe: a `false` means this attempt may no
// longer write to the run, on a write the agent was making anyway. The
// checkpoint path uses it to stop before it writes anything further.
//
// Barred from a cancelled run too, and not only to keep the field honest.
// Cancel is a hard kill that purges the checkpoints, but it leaves the
// attempt unchanged and the runners can return before the workload is
// actually gone — so a dying agent passes an attempt-only probe and
// re-creates a checkpoint row for a run that can never be resumed. That row
// then reads as "resumable" to the boot-time orphan sweep, which holds the
// run's whole per-run vector collection open until the checkpoint TTL
// reclaims it. Refusing here bounds the damage to the single row already
// written before this marker, which loadActiveRunIDs ignores on status.
func (r *RunRepository) MarkExplorationCheckpoint(ctx context.Context, runID string, step int, attempt int) (bool, error) {
	if step <= 0 {
		return true, nil
	}
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return false, fmt.Errorf("invalid run ID: %w", err)
	}
	filter := attemptFilter(oid, attempt)
	filter["status"] = bson.M{"$ne": models.RunStatusCancelled}
	filter["discarded_at"] = nil
	res, err := r.col.UpdateOne(ctx, filter, bson.M{
		"$max": bson.M{"last_checkpoint_step": step},
		"$set": bson.M{"updated_at": time.Now()},
	})
	if err != nil {
		return false, err
	}
	return res.MatchedCount > 0, nil
}

// RunOwnership is what a run document says about one attempt's standing.
//
// Three answers rather than a bool, because the two ways of NOT owning a run
// need different words even where they take the same action. Callers that
// only gate work treat anything but RunOwnedByThisAttempt as "stop"; the
// caller deciding whether to delete a result has to be able to say WHY it is
// deleting one.
type RunOwnership int

const (
	// RunOwnedByThisAttempt: the run is still on this attempt and is not
	// cancelled. Carry on.
	RunOwnedByThisAttempt RunOwnership = iota

	// RunTakenOverByAnotherAttempt: the run has moved to a newer attempt.
	// It is alive, and someone else owns it.
	RunTakenOverByAnotherAttempt

	// RunCancelled: the run was cancelled. The attempt number is unchanged
	// — a cancel does not bump it — so this is invisible to an
	// attempt-only comparison, which is exactly how a cancelled run's agent
	// used to walk through every ownership gate and keep its result.
	RunCancelled

	// RunDiscarded: the operator threw the run away after it had ended. Like
	// a cancel, this leaves the attempt number alone, so it is invisible to
	// an attempt-only comparison — and unlike a cancel it can land while an
	// agent is still alive, because the API's startup sweep marks in-flight
	// runs failed without reaping them. Discard is the operator saying they
	// do not want the run; an agent that survived it must not write the run
	// back to life.
	//
	// Distinct from RunCancelled rather than folded into it because the two
	// need different words for the same action, the reason this type exists:
	// telling an operator their run was cancelled when they discarded it
	// sends them looking for a cancel nobody performed.
	RunDiscarded

	// RunPauseRequested: the run is STILL THIS ATTEMPT'S, and an operator has
	// asked for a cooperative stop (issue #508).
	//
	// Read that first clause carefully, because getting it wrong aborts
	// healthy runs: a pause request is not a loss of ownership. Gates that
	// only ask "may I spend the next phase" must treat this as yes —
	// StatusReporter.OwnsRun does — and only the gates that can stop at a
	// clean checkpoint act on it. Folding it in with the three above would
	// make every gate past the pause window abort the run as superseded.
	//
	// Only returned for a request that is still honourable: within its
	// deadline, and aimed at this attempt. A lapsed or stale one reads as
	// RunOwnedByThisAttempt, so the run simply carries on.
	RunPauseRequested
)

// Ownership reports where this attempt stands with the run.
//
// A dedicated read, used where there is no write to piggyback the question on
// — the end-of-exploration summary, written once per run, whose loss to a
// superseded attempt is the worst case of all: a later resume would read it,
// believe exploration finished, and skip Phase 3 over another attempt's work.
//
// Cancellation is checked FIRST and regardless of the attempt. A cancel is a
// hard kill that leaves the attempt untouched, so comparing attempts alone
// answers "still yours" for a run the operator has already killed — and the
// workload can still be alive, because the runners can return before it is
// actually gone.
//
// attempt <= 0 means "unknown", which owns everything that is not cancelled —
// the behaviour before attempts existed.
func (r *RunRepository) Ownership(ctx context.Context, runID string, attempt int) (RunOwnership, error) {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return RunOwnedByThisAttempt, fmt.Errorf("invalid run ID: %w", err)
	}

	var doc struct {
		Status      string     `bson:"status"`
		Attempt     int        `bson:"attempt"`
		DiscardedAt *time.Time `bson:"discarded_at"`
		// Pause request (#508). Projected alongside the rest so the
		// cooperative stop costs no extra read: this probe already runs at
		// every gate, which is why it is the seam the pause travels through.
		PauseRequestedAt      *time.Time `bson:"pause_requested_at"`
		PauseRequestedAttempt int        `bson:"pause_requested_attempt"`
		PauseDeadlineAt       *time.Time `bson:"pause_deadline_at"`
	}
	err = r.col.FindOne(ctx, bson.M{"_id": oid}, options.FindOne().SetProjection(bson.M{
		"status":                  1,
		"attempt":                 1,
		"discarded_at":            1,
		"pause_requested_at":      1,
		"pause_requested_attempt": 1,
		"pause_deadline_at":       1,
	})).Decode(&doc)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			// No run to own. Treated as taken over rather than as an error:
			// there is positively nothing here for this attempt to write to.
			return RunTakenOverByAnotherAttempt, nil
		}
		return RunOwnedByThisAttempt, err
	}

	if doc.Status == models.RunStatusCancelled {
		return RunCancelled, nil
	}
	// Checked alongside the cancel, and before the attempt comparison, for
	// the same reason: a discard does not bump the attempt, so an
	// attempt-only answer would call a discarded run "still yours".
	if doc.DiscardedAt != nil {
		return RunDiscarded, nil
	}
	// A run created before the counter existed reads as 0 and can only be
	// its first attempt — the same equivalence attemptFilter encodes.
	current := doc.Attempt
	if current == 0 {
		current = 1
	}
	// attempt <= 0 is "the spawner did not say", which owns everything that is
	// not cancelled — the behaviour before attempts existed.
	if attempt > 0 && current != attempt {
		return RunTakenOverByAnotherAttempt, nil
	}
	// Ownership is settled by here, so the only question left is whether the
	// operator has asked this attempt to stop. Checked LAST deliberately:
	// supersession, cancellation and discard all outrank a pause, because each
	// of them means the run is not this attempt's to pause.
	if pauseIsHonourable(doc.PauseRequestedAt, doc.PauseDeadlineAt, doc.PauseRequestedAttempt, current) {
		return RunPauseRequested, nil
	}
	return RunOwnedByThisAttempt, nil
}

// pauseIsHonourable reports whether a stamped pause request still binds.
//
// Three ways it does not. There is no request. It has LAPSED — the deadline is
// what stops an un-honoured request pausing a run hours later, unattended,
// long after the operator gave up; a nil deadline means the operator disabled
// that, so the request stands until something acts on it. Or it names a
// different attempt, which a resume landing concurrently with the request can
// produce.
//
// Compared against the RUN's current attempt rather than against the agent's
// own, because by this point the two are equal whenever the agent was told
// which attempt it is, and when it was not, the run document's is the only
// answer available.
//
// A request with no attempt recorded matches, so a stamp written by an older
// API build is still honoured rather than silently ignored.
func pauseIsHonourable(requestedAt, deadlineAt *time.Time, requestedAttempt, currentAttempt int) bool {
	if requestedAt == nil {
		return false
	}
	if deadlineAt != nil && !time.Now().Before(*deadlineAt) {
		return false
	}
	return requestedAttempt == 0 || requestedAttempt == currentAttempt
}

// Pause records a cooperative stop: the run leaves `running` for `paused`,
// which is resumable (issue #508).
//
// Written by the AGENT, not the API, because only the agent knows it reached a
// clean stop with its checkpoint already durable. The API's job ends at
// stamping the request.
//
// Fenced like every other terminal-ish write: this attempt, and a run that is
// still `running`. So a pause cannot resurrect a cancelled run, cannot
// overwrite a `completed` one that won the race, and cannot be written by a
// superseded attempt. Reports whether it applied, so the caller can exit
// quietly rather than announcing a pause that did not happen.
//
// Deliberately does NOT set completed_at: a paused run has not completed, and
// both GetLatestByProject and the dashboard's elapsed-time calculation read
// that field. The lifecycle event goes in the SAME update as the status, for
// the reason the API's FailAttempt gives for doing the same — BeginResume
// clears the scalar fields on the next resume, so the appended event is the
// only record of this pause that survives it.
func (r *RunRepository) Pause(ctx context.Context, runID string, attempt int, reason string, activeMs int64, lastCheckpointStep int, llmProvider, llmModel string) (bool, error) {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return false, fmt.Errorf("invalid run ID: %w", err)
	}
	now := time.Now()
	filter := attemptFilter(oid, attempt)
	filter["status"] = models.RunStatusRunning
	filter["discarded_at"] = nil

	detail := "Paused by an operator — resume to continue"
	if reason != "" {
		detail = reason
	}
	update := bson.M{
		"$set": bson.M{
			"status":         models.RunStatusPaused,
			"phase_detail":   detail,
			"last_paused_at": now,
			"updated_at":     now,
		},
		// The request has been served, so it stops being pending. Left in
		// place it would describe a stop that already happened, and the
		// dashboard reads its presence as "pausing".
		"$unset": bson.M{
			"pause_requested_at":      "",
			"pause_requested_attempt": "",
			"pause_deadline_at":       "",
		},
		// Provenance travels with the event, as it does on the terminal ones
		// recordAttemptOutcome writes. A paused attempt never reaches that
		// function — it returns before the persistence tail — so without this
		// the one attempt whose model is MOST likely to differ from its
		// neighbours' records none: an operator who pauses a run, changes its
		// model and resumes is exactly the case "which model ran which
		// attempt" exists to answer.
		"$push": bson.M{"lifecycle": models.RunLifecycleEvent{
			Status:      models.RunStatusPaused,
			At:          now,
			Reason:      detail,
			Attempt:     attempt,
			LLMProvider: llmProvider,
			LLMModel:    llmModel,
		}},
	}
	// This attempt's compute, booked in the SAME fenced update as the status.
	//
	// Not a separate AddActiveTime call afterwards, and the reason is a race
	// with no safe side to stand on. Booked BEFORE the status write, a pause
	// that failed to apply leaves the run going and the next seam books the
	// attempt's whole duration again — AddActiveTime adds
	// time.Since(attemptStartedAt), not a delta — so active_ms inflates once
	// per attempted seam. Booked AFTER, the run is already visibly `paused`,
	// so an operator can resume instantly, bump the attempt, and the
	// attempt-fenced increment then matches nothing and the whole attempt's
	// compute is lost from the run's total. One atomic update has neither
	// window: the pause lands with its time, or neither happens.
	if activeMs > 0 {
		update["$inc"] = bson.M{"active_ms": activeMs}
	}
	// The resume marker, stamped by the pause itself rather than relied upon
	// from the per-step write.
	//
	// That write is best-effort and, by design, reports success even when it
	// errors — losing the marker is documented as costing the dashboard's
	// Resume affordance rather than the run. For a FAILED run that trade is
	// right. For a paused one it is not: the dashboard gates Resume on this
	// field, so a Mongo blip on the step that the operator paused at would
	// leave a perfectly resumable run showing only Cancel, and the only way
	// back in would be the API. A pause knows exactly how far its replayable
	// prefix reaches, so it stamps the value itself.
	//
	// $max, so it can only ever move forward — it must not walk back a marker
	// a later step already advanced.
	if lastCheckpointStep > 0 {
		update["$max"] = bson.M{"last_checkpoint_step": lastCheckpointStep}
	}

	res, err := r.col.UpdateOne(ctx, filter, update)
	if err != nil {
		return false, err
	}
	return res.MatchedCount > 0, nil
}

// AddActiveTime adds this attempt's elapsed compute time to the run's
// cumulative total.
//
// Called once at the terminal write, which is also its limitation: an
// attempt hard-killed before it gets here contributes nothing. See
// DiscoveryRun.ActiveMs.
func (r *RunRepository) AddActiveTime(ctx context.Context, runID string, d time.Duration, attempt int) error {
	if d <= 0 {
		return nil
	}
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}
	_, err = r.col.UpdateOne(ctx, attemptFilter(oid, attempt), bson.M{
		"$inc": bson.M{"active_ms": d.Milliseconds()},
		"$set": bson.M{"updated_at": time.Now()},
	})
	return err
}

// AppendLifecycle pushes one transition onto the run's append-only lifecycle
// log. See models.RunLifecycleEvent for why a single mutable status stops
// being enough once a run can be resumed.
func (r *RunRepository) AppendLifecycle(ctx context.Context, runID string, ev models.RunLifecycleEvent, attempt int) error {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	// Barred from a cancelled run, like the status writes.
	//
	// This one asserts an OUTCOME — recordAttemptOutcome appends
	// `completed` or `failed` just before the terminal write — and the
	// terminal write is already refused on a cancelled run. Without the
	// same guard here the pair disagree: the run reads `cancelled` while
	// its lifecycle log, which is the human-readable record of what
	// happened, claims it completed.
	//
	// Note what is deliberately NOT guarded this way: AddActiveTime and the
	// query / schema / analysis counters. Those tally work that really
	// happened, and a cancel does not refund it, so losing them would
	// understate what the operator was charged for. The line is whether a
	// write asserts an outcome or records effort spent.
	filter := attemptFilter(oid, attempt)
	filter["status"] = bson.M{"$ne": models.RunStatusCancelled}
	filter["discarded_at"] = nil

	_, err = r.col.UpdateOne(ctx, filter, bson.M{
		"$push": bson.M{"lifecycle": ev},
		"$set":  bson.M{"updated_at": time.Now()},
	})
	return err
}

// IncrementQueryCount increments query counters.
func (r *RunRepository) IncrementQueryCount(ctx context.Context, runID string, success bool, attempt int) error {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return err
	}

	inc := bson.M{"total_queries": 1}
	if success {
		inc["successful_queries"] = 1
	} else {
		inc["failed_queries"] = 1
	}

	update := bson.M{
		"$inc": inc,
		"$set": bson.M{"updated_at": time.Now()},
	}

	_, err = r.col.UpdateOne(ctx, attemptFilter(oid, attempt), update)
	return err
}

// GetByID returns a discovery run by ID.
func (r *RunRepository) GetByID(ctx context.Context, runID string) (*models.DiscoveryRun, error) {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return nil, fmt.Errorf("invalid run ID: %w", err)
	}

	var run models.DiscoveryRun
	if err := r.col.FindOne(ctx, bson.M{"_id": oid}).Decode(&run); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &run, nil
}

// GetLatestByProject returns the most recent run for a project.
func (r *RunRepository) GetLatestByProject(ctx context.Context, projectID string) (*models.DiscoveryRun, error) {
	opts := options.FindOne().SetSort(bson.D{{Key: "started_at", Value: -1}})

	var run models.DiscoveryRun
	err := r.col.FindOne(ctx, bson.M{"project_id": projectID}, opts).Decode(&run)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &run, nil
}

// ResumableOrActiveIDs narrows a set of run ids to those whose per-run vector
// collection is still worth something: a run a resume could pick up, or one
// that is live right now.
//
// The orphan sweep needs the narrowing because it keeps the collection of
// every run that still has checkpoint rows, and a row is not by itself proof
// of anything. A cancelled run's dying agent can re-create one after the API
// purged them — cancel leaves the attempt unchanged and the runners can
// return before the workload is gone — and keeping a collection open on the
// strength of that row costs Qdrant storage until the checkpoint TTL reclaims
// it, for a run nothing can ever resume.
//
// Stated as what to EXCLUDE rather than what to include, and that polarity is
// the point. The first version of this asked for `failed`, the only resumable
// status, and so dropped the collection of a resumed attempt that was RUNNING
// — those have checkpoint rows too, and ListActiveRecent can miss them
// because a resume keeps the run's original started_at, which may be older
// than the sweep's lookback. Another agent booting mid-run would then delete
// a live run's index under it. The decision here is destructive, so the
// certain-to-exclude set is the one to enumerate: `cancelled` and `completed`
// are the two states that can never want an index again, and anything the
// sweep does not recognise keeps its index rather than losing it.
//
// Unknown ids are simply absent from the result, which is the answer the
// sweep wants: no run, nothing to keep.
func (r *RunRepository) ResumableOrActiveIDs(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	oids := make([]primitive.ObjectID, 0, len(ids))
	for _, id := range ids {
		oid, err := primitive.ObjectIDFromHex(id)
		if err != nil {
			// A malformed id cannot name a run, so it cannot be resumable.
			continue
		}
		oids = append(oids, oid)
	}
	if len(oids) == 0 {
		return nil, nil
	}

	cur, err := r.col.Find(ctx, bson.M{
		"_id": bson.M{"$in": oids},
		"status": bson.M{"$nin": []string{
			models.RunStatusCancelled,
			models.RunStatusCompleted,
		}},
		// A discarded run meets the same certain-to-exclude test as those two
		// — it can never want an index again, because it can never be
		// resumed. It needs saying separately because its STATUS is `failed`,
		// the one resumable state. Discard purges the checkpoints, but a
		// surviving agent's SaveStep can land a row afterwards: the marker is
		// fenced, the row is not, and one residual row was enough to keep a
		// discarded run's collection alive until the checkpoint TTL.
		"discarded_at": nil,
	}, options.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return nil, fmt.Errorf("list resumable or active runs: %w", err)
	}
	defer cur.Close(ctx)

	out := make([]string, 0, len(oids))
	for cur.Next(ctx) {
		var doc struct {
			ID primitive.ObjectID `bson:"_id"`
		}
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode resumable or active run: %w", err)
		}
		out = append(out, doc.ID.Hex())
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("iterate resumable or active runs: %w", err)
	}
	return out, nil
}

// ListActiveRecent returns ids of runs that are currently in a non-
// terminal status and were started within the given lookback window.
// The boot-time per-run-collection orphan sweep treats these as
// "live" (don't drop their Qdrant collections).
//
// Returns DiscoveryRun structs (id + status + started_at — the rest
// of the fields are zero-value) so the caller can also log freshness.
func (r *RunRepository) ListActiveRecent(ctx context.Context, lookback time.Duration) ([]models.DiscoveryRun, error) {
	cutoff := time.Now().Add(-lookback)
	filter := bson.M{
		"started_at": bson.M{"$gte": cutoff},
		"status": bson.M{"$in": []string{
			models.RunStatusPending,
			models.RunStatusRunning,
		}},
		// A discarded run is not active, whatever its status says. An agent
		// that survived the startup sweep can write `running` back onto one —
		// including an agent on a build older than the discard fences — and
		// this list feeds the orphan sweep's keep set. Excluding it only from
		// the checkpoint-derived half of that set was not enough: an id added
		// here stays in, so the discarded run's per-run vector collection was
		// held open for the whole lookback window.
		"discarded_at": nil,
	}
	cur, err := r.col.Find(ctx, filter, options.Find().SetProjection(bson.M{
		"_id":        1,
		"status":     1,
		"started_at": 1,
	}))
	if err != nil {
		return nil, fmt.Errorf("list active recent runs: %w", err)
	}
	defer cur.Close(ctx)

	out := make([]models.DiscoveryRun, 0)
	for cur.Next(ctx) {
		var doc struct {
			ID        primitive.ObjectID `bson:"_id"`
			Status    string             `bson:"status"`
			StartedAt time.Time          `bson:"started_at"`
		}
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode active run: %w", err)
		}
		out = append(out, models.DiscoveryRun{
			ID:        doc.ID.Hex(),
			Status:    doc.Status,
			StartedAt: doc.StartedAt,
		})
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("cursor active runs: %w", err)
	}
	return out, nil
}

// GetRunningByProject returns any currently running run for a project.
func (r *RunRepository) GetRunningByProject(ctx context.Context, projectID string) (*models.DiscoveryRun, error) {
	filter := bson.M{
		"project_id": projectID,
		"status":     bson.M{"$in": []string{models.RunStatusPending, models.RunStatusRunning}},
	}

	var run models.DiscoveryRun
	err := r.col.FindOne(ctx, filter).Decode(&run)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &run, nil
}
