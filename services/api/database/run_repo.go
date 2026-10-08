package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ErrNoResumableRun means the run was not in a resumable state when the
// atomic flip was attempted: it is gone, or something else already moved it
// out of `failed` — which is exactly what a second, racing Resume looks like.
var ErrNoResumableRun = errors.New("run is not resumable")

// RunRepository manages DiscoveryRun documents.
type RunRepository struct {
	col *mongo.Collection
}

func NewRunRepository(db *DB) *RunRepository {
	return &RunRepository{col: db.Collection("discovery_runs")}
}

// Create creates a new discovery run record. Per-step rows live in the
// discovery_run_steps collection (RunStepRepository) — no embedded
// `steps` slice initialisation here.
//
// params records the run's own shape (step budget, areas, effort). It is
// persisted because nothing recorded it before: a resumed run spawned from
// the run document alone would get the agent's defaults instead of the
// budget the operator chose, silently changing the run's shape halfway
// through.
func (r *RunRepository) Create(ctx context.Context, projectID string, params models.RunParams) (string, error) {
	now := time.Now()
	run := models.DiscoveryRun{
		ProjectID: projectID,
		Status:    "pending",
		Phase:     "init",
		Progress:  0,
		StartedAt: now,
		UpdatedAt: now,

		Attempt:  1,
		MaxSteps: params.MaxSteps,
		MinSteps: params.MinSteps,
		Areas:    params.Areas,
		Effort:   params.Effort,
		Lifecycle: []models.RunLifecycleEvent{{
			Status:  "pending",
			At:      now,
			Reason:  params.Source,
			Attempt: 1,
		}},
	}

	result, err := r.col.InsertOne(ctx, run)
	if err != nil {
		return "", fmt.Errorf("create run: %w", err)
	}

	if oid, ok := result.InsertedID.(primitive.ObjectID); ok {
		return oid.Hex(), nil
	}
	return "", nil
}

// BeginResume flips a failed run back to running for a fresh attempt and
// returns the updated document.
//
// The filter is the race guard, and it is the reason this is one
// FindOneAndUpdate rather than a read followed by a write: a double-clicked
// Resume (or two operators at once) would otherwise both see `failed`, both
// pass, and both spawn an agent onto the same runID — two processes writing
// one run's checkpoints and results. Here the second caller matches nothing
// and gets ErrNoResumableRun, which the handler renders as a 409.
//
// Status, not a lock: `failed` is the only status a run may be resumed from.
// `cancelled` is a deliberate hard kill and stays terminal; `completed` has
// nothing to resume; `running` and `pending` already have an agent.
func (r *RunRepository) BeginResume(ctx context.Context, runID string) (*models.DiscoveryRun, error) {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return nil, fmt.Errorf("invalid run ID: %w", err)
	}
	now := time.Now()

	// Read the attempt we are about to become, so the counter, the returned
	// document and the pushed lifecycle event all carry the SAME number.
	// $inc and $push cannot reference each other inside one update, and the
	// event is read by humans reconstructing what happened — an off-by-one
	// between the two is worse than an extra read.
	var current models.DiscoveryRun
	if err := r.col.FindOne(ctx, bson.M{"_id": oid}).Decode(&current); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNoResumableRun
		}
		return nil, fmt.Errorf("read run before resume: %w", err)
	}
	nextAttempt := current.Attempt + 1
	if nextAttempt < 2 {
		// A run created before the attempt counter existed reads as 0, so
		// $inc would leave it at 1 — claiming a resumed run is on its first
		// attempt, and disagreeing with the event we are about to push. Its
		// first resume is attempt 2.
		nextAttempt = 2
	}

	update := bson.M{
		"$set": bson.M{
			"status":          "running",
			"phase":           "init",
			"phase_detail":    "Resuming from the last exploration checkpoint",
			"last_resumed_at": now,
			"updated_at":      now,
			// Set, not $inc. The filter below already serialises the flip —
			// only one caller can move a run out of `failed` — so there is
			// no counter to race, and writing the computed value is the only
			// way the stored attempt, the returned document and the pushed
			// event agree on a run whose counter predates this field.
			"attempt": nextAttempt,
		},
		"$unset": bson.M{
			// The previous attempt's failure is history now; the lifecycle
			// log keeps it. Leaving them set would show a running run with
			// an error and a completion time.
			"error":        "",
			"completed_at": "",
			// A failed run is terminal, so the completion-hook dispatcher
			// will already have fired its hooks and stamped this. Leaving it
			// set means ListTerminalWithoutCompletionHook filters the run
			// out forever — so when the RESUMED attempt finishes, no plugin
			// ever sees the result it produced. Clearing it makes the run
			// dispatch-pending again, which is exactly what it is: it has a
			// terminal outcome still to come.
			"completion_hooks_fired_at": "",
			// NOT cleared here: policy_reservation_id. Resume deliberately
			// opens no reservation of its own, so the previous attempt's has
			// to be ended — but clearing the id before that confirm succeeds
			// loses the only handle anyone has on it. A crash in between
			// would leak the concurrent-run reservation with nothing left to
			// reconcile from. ResumeRun confirms it and clears it after, in
			// that order, the way StartRun already does for Release.
			//
			// The back-reference to the PREVIOUS attempt's partial result.
			// Clearing the hook marker above re-arms dispatch, so leaving
			// this would point the re-fired hooks at a result this attempt
			// did not produce — and if the resumed attempt fails before
			// saving anything (an init failure, a checkpoint TTL race), that
			// stale result is all a consumer would ever see for it.
			//
			// The agent re-stamps it from its own Complete / Fail, so a run
			// that produces a result has the right one; one that produces
			// nothing now reads as having produced nothing, which is both
			// honest and what a fresh run that failed early looks like.
			"discovery_id": "",
		},
		"$push": bson.M{"lifecycle": models.RunLifecycleEvent{
			Status:  "running",
			At:      now,
			Reason:  "resumed from the last exploration checkpoint",
			Attempt: nextAttempt,
		}},
	}
	// Fenced on the attempt we READ, not just on the status. Without it a
	// stale resume request could land after another resume had advanced the
	// run to attempt 2 and that attempt had failed back to `failed`: the
	// filter would match, and it would write `attempt: 2` a second time. Two
	// different attempts both numbered 2 means the same suffixed Kubernetes
	// Job name — which may still exist for the first of them — and
	// attempt-fenced callbacks that can no longer tell them apart, which is
	// the whole mechanism every other guard in this feature rests on.
	//
	// A run that predates the counter reads as 0, and a missing field has to
	// match that, or its first resume could never be fenced at all.
	filter := bson.M{"_id": oid, "status": "failed"}
	if current.Attempt == 0 {
		filter["attempt"] = bson.M{"$in": []any{0, nil}}
	} else {
		filter["attempt"] = current.Attempt
	}
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)

	var run models.DiscoveryRun
	if err := r.col.FindOneAndUpdate(ctx, filter, update, opts).Decode(&run); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNoResumableRun
		}
		return nil, fmt.Errorf("begin resume of run %s: %w", runID, err)
	}
	return &run, nil
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

// GetLatestByProject returns the run that represents the project's current
// state: its ACTIVE run if it has one, and otherwise the most recently
// started.
//
// "Most recently started" alone stopped being right the moment a run could
// be resumed. A resume re-enters the run it resumes, and started_at keeps
// the original start — that is what started_at means, and active_ms is what
// carries compute across attempts — so resuming a failed run after a NEWER
// run has since finished leaves the live run holding the OLDER started_at.
// Sorting on started_at then answers with the finished run, and the one
// that is actually spending budget becomes invisible: no progress, and no
// way to cancel it.
//
// Two queries rather than a computed sort key, so each still runs off the
// existing (project_id, started_at) index.
func (r *RunRepository) GetLatestByProject(ctx context.Context, projectID string) (*models.DiscoveryRun, error) {
	opts := options.FindOne().SetSort(bson.D{{Key: "started_at", Value: -1}})

	// An active run wins outright. There is normally at most one — both the
	// trigger path and the resume path refuse to start a second — so the
	// sort here only ever breaks a tie this code should not see.
	var run models.DiscoveryRun
	err := r.col.FindOne(ctx, bson.M{
		"project_id": projectID,
		"status":     bson.M{"$in": []string{"pending", "running"}},
	}, opts).Decode(&run)
	if err == nil {
		return &run, nil
	}
	if err != mongo.ErrNoDocuments {
		return nil, err
	}

	// Nothing active. The newest by started_at is the answer for every run
	// that was never resumed — but a resumed run keeps its ORIGINAL
	// started_at, so one that has failed again would hide behind any newer
	// run, and the panel would show an unrelated completed run with no
	// Resume for the attempt that just failed. The resume affordance is the
	// whole feature, so it has to survive the second failure as well as the
	// first.
	//
	// Two queries and pick the later: newest by started_at, and newest among
	// the runs that have ever been resumed. The second set is sparse — the
	// field exists only on resumed runs — and both sorts stay index-friendly,
	// which a computed sort key over the whole history would not.
	//
	// `$ne: nil` is the predicate that means "has actually been resumed",
	// and it is not interchangeable with `$exists`. Mongo treats a missing
	// field as null, so $ne:nil excludes BOTH the missing field and an
	// explicit null — while `$exists: true` alone would let an explicit null
	// through and widen this pass. Verified against Mongo 7, because the
	// semantics read the wrong way round.
	byStart, err := r.newestMatching(ctx, bson.M{"project_id": projectID}, "started_at")
	if err != nil {
		return nil, err
	}
	byResume, err := r.newestMatching(ctx, bson.M{
		"project_id":      projectID,
		"last_resumed_at": bson.M{"$ne": nil},
	}, "last_resumed_at")
	if err != nil {
		return nil, err
	}
	switch {
	case byStart == nil:
		return byResume, nil
	case byResume == nil:
		return byStart, nil
	case byResume.LatestAttemptAt().After(byStart.LatestAttemptAt()):
		return byResume, nil
	default:
		return byStart, nil
	}
}

// newestMatching returns the newest run matching filter by the given field,
// or nil when nothing matches.
func (r *RunRepository) newestMatching(ctx context.Context, filter bson.M, sortField string) (*models.DiscoveryRun, error) {
	var run models.DiscoveryRun
	err := r.col.FindOne(ctx, filter,
		options.FindOne().SetSort(bson.D{{Key: sortField, Value: -1}}),
	).Decode(&run)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &run, nil
}

// LatestByProjects returns the run that represents each project's current
// state, keyed by project ID — the rule GetLatestByProject applies, for a
// page of projects at once. Projects with no runs are simply absent.
//
// Each pass is one aggregation (match → sort → group $first) so enriching a
// page of N projects costs a fixed number of Mongo round-trips rather than
// N. The sort key is `{project_id: 1, started_at: -1}` — exactly the
// existing compound index on discovery_runs — so Mongo streams the sort
// off the index instead of doing a blocking in-memory sort (which would
// risk the aggregation sort-memory limit on projects with long run
// histories). Within each project_id group the docs arrive newest-first,
// so `$first` yields that group's newest run. An empty input returns
// an empty map without touching Mongo.
//
// Two passes, for the reason GetLatestByProject uses two queries: an active
// run has to win even when a newer run has since finished, and folding that
// into the sort key would cost the index-backed sort the paragraph above
// depends on. The active pass matches at most one row per project, so it is
// the cheap half.
func (r *RunRepository) LatestByProjects(ctx context.Context, projectIDs []string) (map[string]*models.DiscoveryRun, error) {
	if len(projectIDs) == 0 {
		return map[string]*models.DiscoveryRun{}, nil
	}

	out, err := r.newestPerProject(ctx, bson.M{"project_id": bson.M{"$in": projectIDs}}, "started_at")
	if err != nil {
		return nil, err
	}
	// A run that has been resumed keeps its original started_at, so the pass
	// above hides it behind any newer run once it is no longer active. Same
	// rule as GetLatestByProject, because the two readers giving different
	// answers is the only thing that could justify having both.
	resumed, err := r.newestPerProject(ctx, bson.M{
		"project_id":      bson.M{"$in": projectIDs},
		"last_resumed_at": bson.M{"$ne": nil},
	}, "last_resumed_at")
	if err != nil {
		return nil, err
	}
	for projectID, run := range resumed {
		if cur, ok := out[projectID]; !ok || run.LatestAttemptAt().After(cur.LatestAttemptAt()) {
			out[projectID] = run
		}
	}
	active, err := r.newestPerProject(ctx, bson.M{
		"project_id": bson.M{"$in": projectIDs},
		"status":     bson.M{"$in": []string{"pending", "running"}},
	}, "started_at")
	if err != nil {
		return nil, err
	}
	// Active still wins outright, whatever the timestamps say.
	for projectID, run := range active {
		out[projectID] = run
	}
	return out, nil
}

// newestPerProject groups the matching runs by project and returns the newest
// in each group by sortField.
//
// sortField matters and is not cosmetic. The resumed pass must order by
// last_resumed_at: a project can hold two resumed runs where the one that
// STARTED earlier was resumed more recently, and ordering that pass by
// started_at would return the wrong one — which the caller's LatestAttemptAt
// comparison cannot repair, because it only ever sees one row per pass.
//
// Only the started_at sort rides the existing (project_id, started_at)
// index. The resumed pass sorts in memory, which is affordable precisely
// because it is sparse: last_resumed_at exists only on runs that have
// actually been resumed.
func (r *RunRepository) newestPerProject(ctx context.Context, match bson.M, sortField string) (map[string]*models.DiscoveryRun, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: match}},
		{{Key: "$sort", Value: bson.D{
			{Key: "project_id", Value: 1},
			{Key: sortField, Value: -1},
		}}},
		{{Key: "$group", Value: bson.M{
			"_id": "$project_id",
			"run": bson.M{"$first": "$$ROOT"},
		}}},
	}

	cursor, err := r.col.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate latest runs by project: %w", err)
	}
	defer cursor.Close(ctx) //nolint:errcheck

	var rows []struct {
		ProjectID string              `bson:"_id"`
		Run       models.DiscoveryRun `bson:"run"`
	}
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("decode latest runs by project: %w", err)
	}

	out := make(map[string]*models.DiscoveryRun, len(rows))
	for i := range rows {
		run := rows[i].Run
		out[rows[i].ProjectID] = &run
	}
	return out, nil
}

// ListTerminalWithoutCompletionHook returns runs that have terminated
// (status in {completed, failed, cancelled}) and have not had their
// completion hooks fired yet. The run-completion dispatcher consumes
// this list and invokes every registered hook for each row — see
// plugin-hooks.md Hook 5.
//
// limit caps the per-tick batch. A small value (default 50) keeps each
// tick bounded and lets the dispatcher catch up gradually after a long
// API outage without monopolising a Mongo connection.
func (r *RunRepository) ListTerminalWithoutCompletionHook(ctx context.Context, limit int) ([]*models.DiscoveryRun, error) {
	if limit <= 0 {
		limit = 50
	}
	filter := bson.M{
		"status": bson.M{"$in": []string{"completed", "failed", "cancelled"}},
		// A run is dispatch-pending when the field is missing OR explicitly
		// null. The MongoDB equality `{field: null}` predicate matches both
		// shapes natively, so a single key suffices — keeping the index
		// `(status, completion_hooks_fired_at, started_at)` usable rather
		// than forcing the planner to merge two index scans behind an $or.
		"completion_hooks_fired_at": nil,
	}
	// Sort by started_at ascending so the oldest pending run is dispatched
	// first. FIFO order bounds tail latency when many runs land in the
	// same window (e.g. after an API restart drains the backlog).
	opts := options.Find().
		SetLimit(int64(limit)).
		SetSort(bson.D{{Key: "started_at", Value: 1}})
	cursor, err := r.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list terminal runs without completion hook: %w", err)
	}
	defer cursor.Close(ctx) //nolint:errcheck
	var runs []*models.DiscoveryRun
	if err := cursor.All(ctx, &runs); err != nil {
		return nil, fmt.Errorf("decode terminal runs: %w", err)
	}
	return runs, nil
}

// MarkCompletionHooksFired stamps completion_hooks_fired_at on the run
// so the dispatcher's next scan skips it. Called by the dispatcher
// after every registered hook returned without error.
//
// attempt is the attempt the dispatcher read off the run it selected; the
// mark lands only if the run is still terminal AND still on that attempt.
func (r *RunRepository) MarkCompletionHooksFired(ctx context.Context, runID string, attempt int) error {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}
	now := time.Now()
	// Fenced on BOTH the terminal status and the attempt the dispatcher
	// actually selected. The dispatcher reads a batch and marks each row
	// afterwards, so a resume can land in between — and the status alone is
	// not enough to catch it.
	//
	// Status catches the easy half: the resume has flipped the run back to
	// `running`, so a stale mark matches nothing. But if the RESUMED attempt
	// then reaches a terminal state before the stale mark arrives, a
	// status-only filter matches again and stamps the marker for an attempt
	// whose hooks were never dispatched — so
	// ListTerminalWithoutCompletionHook skips it for ever. That is the exact
	// bug clearing the field on resume exists to prevent, reintroduced one
	// step further along.
	//
	// The attempt pins it to the row that was selected. A no-op is correct in
	// both cases: the run has an outcome the dispatcher has not seen yet, and
	// a later scan picks it up.
	//
	// Unlike FailAttempt there is no "unknown attempt" caller to accommodate
	// here: the only caller reads the run document first, so a zero attempt
	// means the field was absent, which means the run is on its first — not
	// that the attempt is unknown. Leaving the filter off for a zero would
	// reopen the hole above for every run created before the counter existed.
	filter := bson.M{
		"_id":    oid,
		"status": bson.M{"$in": []string{"completed", "failed", "cancelled"}},
	}
	if attempt <= 1 {
		// A run created before the counter existed carries no attempt.
		filter["attempt"] = bson.M{"$in": []any{1, nil}}
	} else {
		// Resumed attempts always have the field — BeginResume writes it.
		filter["attempt"] = attempt
	}
	_, err = r.col.UpdateOne(ctx, filter, bson.M{
		"$set": bson.M{
			"completion_hooks_fired_at": now,
			"updated_at":                now,
		},
	})
	if err != nil {
		return fmt.Errorf("mark completion hooks fired: %w", err)
	}
	return nil
}

// ListTerminalWithReservation returns runs that have ended (succeeded,
// failed, or cancelled) AND still carry a non-empty policy reservation.
// Used by the post-completion confirmer goroutine so discovery runs
// that the agent wrote as "completed" directly to Mongo still trigger
// a Confirm on the policy Checker. The caller typically limits the
// scan to a handful at a time.
func (r *RunRepository) ListTerminalWithReservation(ctx context.Context, limit int) ([]*models.DiscoveryRun, error) {
	if limit <= 0 {
		limit = 50
	}
	filter := bson.M{
		"status":                bson.M{"$in": []string{"completed", "failed", "cancelled"}},
		"policy_reservation_id": bson.M{"$nin": []any{"", nil}},
	}
	opts := options.Find().SetLimit(int64(limit))
	cursor, err := r.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list terminal runs with reservation: %w", err)
	}
	defer cursor.Close(ctx) //nolint:errcheck
	var runs []*models.DiscoveryRun
	if err := cursor.All(ctx, &runs); err != nil {
		return nil, fmt.Errorf("decode terminal runs: %w", err)
	}
	return runs, nil
}

// ClearPolicyReservationID unsets the reservation handle on a run so
// the post-completion confirmer does not re-process it on the next
// scan. Called after a successful Confirm.
func (r *RunRepository) ClearPolicyReservationID(ctx context.Context, runID string) error {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}
	_, err = r.col.UpdateByID(ctx, oid, bson.M{
		"$unset": bson.M{"policy_reservation_id": ""},
		"$set":   bson.M{"updated_at": time.Now()},
	})
	return err
}

// SetPolicyReservationID stores the opaque reservation handle returned
// by the policy Checker when the run was triggered. Persisted so exit
// paths outside the trigger request (cancel, crash sweeper, agent
// completion callback) can resolve the reservation back to the control
// plane without keeping request-scoped state.
func (r *RunRepository) SetPolicyReservationID(ctx context.Context, runID, reservationID string) error {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}
	_, err = r.col.UpdateByID(ctx, oid, bson.M{
		"$set": bson.M{"policy_reservation_id": reservationID, "updated_at": time.Now()},
	})
	return err
}

// Fail marks a run as failed.
//
// Terminal-status invariant: only writes when the run is currently
// in a non-terminal status (`pending` or `running`). Mirrors the
// guard on the agent-side RunRepository.Fail (see
// services/agent/internal/database/run_repo.go). The K8s watcher's
// exhaustion fallback in runner.go can fire OnFailure → Fail AFTER
// the agent has already stamped `completed` or after the cancel
// handler has stamped `cancelled`; without this guard those
// terminal runs would silently flip to `failed`.
//
// Mongo's UpdateOne returns no error when zero documents match, so
// callers that don't care about no-ops (the watcher OnFailure
// callback) need no change.
func (r *RunRepository) Fail(ctx context.Context, runID string, errMsg string) error {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return err
	}

	now := time.Now()
	update := bson.M{
		"$set": bson.M{
			"status":       "failed",
			"error":        errMsg,
			"phase_detail": "Failed: " + errMsg,
			"completed_at": now,
			"updated_at":   now,
		},
	}

	filter := bson.M{
		"_id":    oid,
		"status": bson.M{"$in": []string{"pending", "running"}},
	}
	_, err = r.col.UpdateOne(ctx, filter, update)
	return err
}

// FailAttempt marks a run failed only if it is still on the attempt the
// caller is reporting about, and says whether it applied.
//
// Fail() guards on status alone, which is enough while a run has one attempt.
// Once a run can be resumed it is not: a previous attempt's background watcher
// outlives the attempt itself (the K8s Job watcher polls, the Docker watcher
// waits on the container), and the agent writes its own `failed` status before
// the watcher observes the dead Job. So the sequence
//
//	agent writes failed → operator resumes → run is `running` again
//	→ old watcher finally fires OnFailure
//
// would have the dead attempt's callback mark the LIVE one failed, killing a
// run that is working. The window is small but it is the window an operator
// clicking Resume on a just-failed run sits in, and the same race repeats on
// every subsequent attempt.
//
// attempt <= 0 means "unknown", and matches any attempt — the behaviour of a
// caller that cannot say which attempt it belongs to. Attempt 1 also matches a
// document with NO attempt field, which is how every run created before the
// counter existed reads.
func (r *RunRepository) FailAttempt(ctx context.Context, runID string, attempt int, errMsg string) (bool, error) {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return false, err
	}

	now := time.Now()
	update := bson.M{
		"$set": bson.M{
			"status":       "failed",
			"error":        errMsg,
			"phase_detail": "Failed: " + errMsg,
			"completed_at": now,
			"updated_at":   now,
		},
	}
	filter := bson.M{
		"_id":    oid,
		"status": bson.M{"$in": []string{"pending", "running"}},
	}
	switch {
	case attempt == 1:
		// A legacy run carries no attempt field; it can only be its first.
		filter["attempt"] = bson.M{"$in": []any{1, nil}}
	case attempt > 1:
		// Resumed attempts always have the field — BeginResume writes it.
		filter["attempt"] = attempt
	}

	res, err := r.col.UpdateOne(ctx, filter, update)
	if err != nil {
		return false, err
	}
	return res.MatchedCount > 0, nil
}

// Cancel marks a run as cancelled.
func (r *RunRepository) Cancel(ctx context.Context, runID string) error {
	oid, err := primitive.ObjectIDFromHex(runID)
	if err != nil {
		return err
	}

	now := time.Now()
	update := bson.M{
		"$set": bson.M{
			"status":       "cancelled",
			"phase_detail": "Cancelled by user",
			"completed_at": now,
			"updated_at":   now,
		},
	}

	_, err = r.col.UpdateByID(ctx, oid, update)
	return err
}

// CleanupStaleRuns marks any pending/running runs as failed.
// Called on API startup to clean up runs from previous container lifecycle.
func (r *RunRepository) CleanupStaleRuns(ctx context.Context) (int, error) {
	filter := bson.M{
		"status": bson.M{"$in": []string{"pending", "running"}},
	}

	now := time.Now()
	update := bson.M{
		"$set": bson.M{
			"status":       "failed",
			"error":        "stale: API restarted while run was in progress",
			"phase_detail": "Failed: API restarted during discovery",
			"completed_at": now,
			"updated_at":   now,
		},
	}

	result, err := r.col.UpdateMany(ctx, filter, update)
	if err != nil {
		return 0, err
	}
	return int(result.ModifiedCount), nil
}

// GetOtherRunningByProject returns an active run for the project that is NOT
// the given one, or nil.
//
// Needed because GetRunningByProject returns ONE active run with no ordering,
// and the caller that matters here — the resume path re-checking concurrency
// after it has already flipped its own run to `running` — would otherwise get
// its own run back and conclude there was no competitor. Excluding at the
// query level is the only way to ask the question it is actually asking.
func (r *RunRepository) GetOtherRunningByProject(ctx context.Context, projectID, excludeRunID string) (*models.DiscoveryRun, error) {
	filter := bson.M{
		"project_id": projectID,
		"status":     bson.M{"$in": []string{"pending", "running"}},
	}
	if excludeRunID != "" {
		oid, err := primitive.ObjectIDFromHex(excludeRunID)
		if err != nil {
			return nil, fmt.Errorf("invalid run ID: %w", err)
		}
		filter["_id"] = bson.M{"$ne": oid}
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

// GetRunningByProject checks if there's an active run for a project.
func (r *RunRepository) GetRunningByProject(ctx context.Context, projectID string) (*models.DiscoveryRun, error) {
	filter := bson.M{
		"project_id": projectID,
		"status":     bson.M{"$in": []string{"pending", "running"}},
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
