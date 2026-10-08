package discovery

// Resume support for the orchestrator: checkpoint wiring, the
// skip-exploration branch, and the idempotent tail.
//
// Kept out of orchestrator.go, which is already 3 000 lines, so the feature
// reads as one thing rather than as a scatter of conditionals.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation/verifier"
)

// explorationCheckpointStore is the slice of
// *database.DiscoveryCheckpointRepository the orchestrator actually calls.
// An interface so a unit test can inject a fake without MongoDB, and so a
// nil store cleanly disables checkpointing.
type explorationCheckpointStore interface {
	SaveStep(ctx context.Context, in database.CheckpointStepInput) error
	SaveExplorationSummary(ctx context.Context, in database.CheckpointSummaryInput) error
	DeleteByRun(ctx context.Context, runID string) (int64, error)
	TouchByRun(ctx context.Context, runID string) (int64, error)
}

// ResumeState is what a resumed run is handed at construction: how far the
// previous attempts got, and how much compute they already spent.
type ResumeState struct {
	// Attempt is this attempt's 1-based number (2 for the first resume).
	Attempt int
	// PriorActiveMs is the cumulative active compute time of the attempts
	// before this one, so the result reports total work rather than this
	// process's slice of it.
	PriorActiveMs int64
	// Checkpoints is the replayable prefix plus, when exploration had
	// already finished, its summary.
	Checkpoints *database.CheckpointSet
}

// active reports whether this run is a resume at all. Safe on nil — an
// ordinary run is handed no ResumeState, which is what keeps every
// resume-only behaviour off the path it always took.
func (r *ResumeState) active() bool { return r != nil }

// prefixLen reports how many steps will be replayed. Safe on nil.
func (r *ResumeState) prefixLen() int {
	if r == nil {
		return 0
	}
	return r.Checkpoints.Len()
}

// explorationComplete reports whether a previous attempt finished exploration,
// in which case this run skips Phase 3 entirely. Safe on nil.
func (r *ResumeState) explorationComplete() bool {
	return r != nil && r.Checkpoints.ExplorationComplete()
}

// attemptNumber is the attempt this run is, 1 when it is not a resume.
func (r *ResumeState) attemptNumber() int {
	if r == nil || r.Attempt <= 0 {
		return 1
	}
	return r.Attempt
}

// engineResume converts the loaded checkpoints into what the exploration
// engine replays. Nil when there is nothing to replay, which is what makes a
// non-resumed run take exactly the path it always did.
func (r *ResumeState) engineResume() *ai.ResumeState {
	if r.prefixLen() == 0 {
		return nil
	}
	return &ai.ResumeState{Steps: r.Checkpoints.Steps}
}

// checkpointStep is the engine's PersistStep hook.
//
// It owns the one transformation the checkpoint applies to a step: reducing
// the full result set to the bounded row sample the verifier's evidence
// bundle is rebuilt from. The sample is cut by verifier.CheckpointSample —
// the same function the live bundle path uses — so the two cannot drift, and
// the caps come from this run's own validation config rather than from a
// constant that would silently disagree with it.
//
// Also stamps last_checkpoint_step on the run document, which is what the
// dashboard reads to offer Resume. Stamped from HERE rather than from the
// live-status step hook on purpose: the field must mean "a checkpoint exists
// for this step", and the two seams disagree exactly when a checkpoint write
// fails — the case where offering Resume would be a lie.
func (o *Orchestrator) checkpointStep(ctx context.Context, step models.ExplorationStep, args models.CheckpointArgs) error {
	if o.checkpointRepo == nil {
		return nil
	}
	// Ask whether this attempt still owns the run BEFORE writing anything.
	//
	// That order is what stops a superseded agent inserting a row for a step
	// the live attempt has not reached yet: the row-level attempt fence in
	// SaveStep only refuses a write where a higher-attempt row ALREADY
	// exists, so a dead attempt running AHEAD of the live one would
	// otherwise splice its own work into the prefix, and a later resume would
	// replay a transcript assembled from two different runs.
	//
	// A dedicated read rather than reusing the run-document marker below,
	// even though that write is attempt-fenced and would answer for free.
	// The marker means "a checkpoint exists for this step", and writing it
	// first breaks that: a SaveStep failure is logged and swallowed so the
	// run can continue, which would leave the marker advertising a
	// checkpoint that was never written — and the dashboard offering a
	// Resume that then refuses with 409 because there is no prefix to
	// replay. One extra indexed read per step, next to an LLM call and a
	// warehouse query, buys back the field's meaning.
	//
	// Not airtight, and worth being precise about: a resume landing between
	// this read and the write still lets one row through. That window is two
	// consecutive operations wide rather than the whole remaining run, and
	// the row-level fence catches it whenever the live attempt has already
	// written that step.
	if o.statusReporter != nil && !o.statusReporter.OwnsRun(ctx) {
		return ai.ErrAttemptSuperseded
	}

	err := o.checkpointRepo.SaveStep(ctx, database.CheckpointStepInput{
		ProjectID: o.projectID,
		RunID:     o.runID,
		Attempt:   o.resume.attemptNumber(),
		Step:      step,
		RowSample: verifier.CheckpointSample(step.QueryResult, o.validationCfg.Bundle),
		Args:      args,
	})
	if err != nil {
		// The store reports a lost race as its own fact; translate it into
		// the pipeline's control signal, which is what stops the run.
		if errors.Is(err, database.ErrSupersededAttempt) {
			return ai.ErrAttemptSuperseded
		}
		return err
	}
	// The run is now resumable, so its per-run vector index must survive a
	// failure rather than being dropped on the way out.
	o.keepStepIndex = true

	// Only now: the row is durable, so the marker is true. This is what the
	// dashboard reads to offer Resume, and it must never advertise a
	// checkpoint that does not exist.
	//
	// Its answer is also a second fence, and the one that closes most of the
	// window left by the probe above. If a resume landed in between, the
	// marker write does not apply — and stopping on that keeps this attempt
	// out of the SHARED writes that follow in the engine (the per-run vector
	// index and the live step feed), which is what the
	// checkpoint-before-shared-writes ordering exists for. The checkpoint row
	// itself is already written by then; that is the documented residual.
	if o.statusReporter != nil && !o.statusReporter.MarkExplorationCheckpoint(ctx, step.Step) {
		return ai.ErrAttemptSuperseded
	}
	return nil
}

// checkpointExplorationSummary records that exploration finished. Its
// presence is what lets a later attempt go straight to analysis — zero
// exploration LLM calls, zero warehouse queries.
// Returns ErrAttemptSuperseded when this attempt has lost the run, which the
// caller must propagate rather than carry on: everything after exploration —
// analysis, recommendations, validation — is expensive, and a superseded
// attempt would spend all of it on a result it then deletes at the tail.
func (o *Orchestrator) checkpointExplorationSummary(ctx context.Context, res *ai.ExplorationResult) error {
	if o.checkpointRepo == nil || res == nil {
		return nil
	}
	// A dedicated ownership read, because there is no write to piggyback the
	// question on here — and because this row is the worst one to lose to a
	// superseded attempt: a later resume reads it, believes exploration
	// finished, and skips Phase 3 over work another attempt did. Once per
	// run, against an indexed _id.
	if o.statusReporter != nil && !o.statusReporter.OwnsRun(ctx) {
		applog.WithField("run_id", o.runID).Warn("another attempt of this run has taken over; stopping before analysis rather than spending it on a result that will be discarded")
		return ai.ErrAttemptSuperseded
	}
	err := o.checkpointRepo.SaveExplorationSummary(ctx, database.CheckpointSummaryInput{
		ProjectID: o.projectID,
		RunID:     o.runID,
		Attempt:   o.resume.attemptNumber(),
		Summary: models.ExplorationCheckpointSummary{
			Completed:     res.Completed,
			CompletionMsg: res.CompletionMsg,
			TotalSteps:    res.TotalSteps,
			Duration:      res.Duration,
		},
	})
	if err != nil {
		if errors.Is(err, database.ErrSupersededAttempt) {
			// Not a write failure — a newer attempt wrote the summary first,
			// which means this one lost the run between the ownership check
			// above and here. Stop, rather than carrying the whole analysis
			// phase on its behalf.
			applog.WithField("run_id", o.runID).Warn("a newer attempt recorded the exploration summary first; stopping before analysis")
			return ai.ErrAttemptSuperseded
		}
		applog.WithFields(applog.Fields{
			"run_id": o.runID,
			"error":  err.Error(),
		}).Warn("failed to checkpoint the exploration summary; a resumed run would re-explore instead of going straight to analysis")
		return nil
	}
	o.keepStepIndex = true
	return nil
}

// refreshCheckpointTTL re-anchors the retention clock on a run's checkpoints.
//
// For the skip-exploration path, which rebuilds from the rows without
// rewriting them: their created_at — the TTL anchor — would stay at whatever
// the previous attempt stamped. An operator resuming near
// DISCOVERY_CHECKPOINT_RETENTION would then watch the rows expire while the
// resumed attempt was still working, and a second resume would be impossible.
// The replay path has no such problem: it rewrites every row it replays.
//
// One bulk update rather than N rewrites — the content is unchanged and only
// the anchor needs moving. Best-effort: losing it costs resumability on a
// later attempt, not this run.
func (o *Orchestrator) refreshCheckpointTTL(ctx context.Context) {
	if o.checkpointRepo == nil || o.runID == "" {
		return
	}
	touched, err := o.checkpointRepo.TouchByRun(ctx, o.runID)
	if err != nil {
		applog.WithFields(applog.Fields{
			"run_id": o.runID,
			"error":  err.Error(),
		}).Warn("could not re-anchor the checkpoint retention clock; these rows may expire while this attempt is still running")
		return
	}
	applog.WithFields(applog.Fields{
		"run_id":  o.runID,
		"touched": touched,
	}).Debug("re-anchored the checkpoint retention clock for the resumed attempt")
}

// explorationFromCheckpoints rebuilds the ExplorationResult of a run whose
// exploration already finished, without calling the model or the warehouse
// even once.
//
// This is the second acceptance criterion of resume, and the cheapest path
// through the feature: everything the analysis phase reads off an
// ExplorationResult — the steps with their digests, quality caveats and token
// counts, the total, the completion message — was checkpointed.
func (o *Orchestrator) explorationFromCheckpoints() *ai.ExplorationResult {
	set := o.resume.Checkpoints
	steps := make([]models.ExplorationStep, 0, len(set.Steps))
	for _, cp := range set.Steps {
		steps = append(steps, cp.Step)
	}
	total := set.Summary.TotalSteps
	if total < len(steps) {
		// The summary and the rows disagree only if a step's checkpoint
		// write failed. Report what we can actually show the analysis.
		total = len(steps)
	}
	return &ai.ExplorationResult{
		Steps:         steps,
		TotalSteps:    total,
		Duration:      set.Summary.Duration,
		Completed:     set.Summary.Completed,
		CompletionMsg: set.Summary.CompletionMsg,
	}
}

// reindexReplayedSteps pushes an already-executed prefix back into the
// per-run vector index.
//
// Needed on the skip-exploration path, where the engine never runs and so
// never indexes anything: without it the analysis picker would rank against
// an empty index and silently fall back to keyword-only selection for the
// whole run. On the replay path the engine does this itself as it rebuilds
// the conversation.
//
// Exact and idempotent either way: the embedded text is the step's purpose
// plus its query, both checkpointed, and the point id is derived from
// (runID, step). So it is a no-op upsert when the collection survived and a
// full rebuild when it was swept — which is what makes resume robust to a
// hard kill, where no deferred cleanup ran.
func (o *Orchestrator) reindexReplayedSteps(ctx context.Context, steps []models.ExplorationStep) {
	if o.runStepIndex == nil || len(steps) == 0 {
		return
	}
	indexed := 0
	for _, step := range steps {
		if err := o.runStepIndex.Upsert(ctx, step); err != nil {
			applog.WithFields(applog.Fields{
				"step":  step.Step,
				"error": err.Error(),
			}).Warn("resume: re-indexing a checkpointed step failed; analysis ranking will degrade for it")
			continue
		}
		indexed++
	}
	applog.WithFields(applog.Fields{
		"run_id":  o.runID,
		"steps":   len(steps),
		"indexed": indexed,
	}).Info("resume: re-indexed the checkpointed prefix into the per-run step index")
}

// resumePhaseDetail is the live-panel text for a resumed run, so the
// dashboard explains itself instead of showing a run that appears to start
// from nothing.
func (o *Orchestrator) resumePhaseDetail() string {
	switch {
	case o.resume.explorationComplete():
		return "Exploration already complete — resuming at analysis"
	case o.resume.prefixLen() > 0:
		return fmt.Sprintf("Resuming exploration at step %d", o.resume.prefixLen()+1)
	default:
		return ""
	}
}

// cumulativeDuration is the run's total active compute time: the attempts
// before this one, plus this one.
//
// Without it a resumed run reports time.Since(startTime) for the current
// process, so the dashboard shows a resumed run as though it had been
// running since whenever the operator happened to click Resume.
func (o *Orchestrator) cumulativeDuration(thisAttempt time.Duration) time.Duration {
	if o.resume == nil {
		return thisAttempt
	}
	return time.Duration(o.resume.PriorActiveMs)*time.Millisecond + thisAttempt
}

// recordAttemptOutcome books this attempt's compute time against the run's
// cumulative total and appends the terminal lifecycle event.
//
// Both are per-attempt facts a single mutable run document cannot hold: the
// status field shows only the latest attempt, and elapsed time computed from
// timestamps counts the hours a failed run sat waiting to be noticed.
//
// Called just before the status flip, which is also the limit of what it can
// promise: an attempt hard-killed before reaching here records nothing. See
// models.DiscoveryRun.ActiveMs.
func (o *Orchestrator) recordAttemptOutcome(ctx context.Context, elapsed time.Duration, computeErr error) {
	if o.statusReporter == nil {
		return
	}
	o.statusReporter.AddActiveTime(ctx, elapsed)

	ev := models.RunLifecycleEvent{
		Status:      models.RunStatusCompleted,
		At:          time.Now(),
		Attempt:     o.resume.attemptNumber(),
		LLMProvider: o.llmProvider,
		LLMModel:    o.llmModel,
	}
	if computeErr != nil {
		ev.Status = models.RunStatusFailed
		ev.Reason = computeErr.Error()
	}
	o.statusReporter.AppendLifecycle(ctx, ev)
}

// retireSupersededAttempts deletes the results of this run's EARLIER
// attempts, now that the current attempt's result is fully written.
//
// The order matters and is the opposite of the obvious one. Upserting the
// discoveries document in place would keep discovery_id stable, but every
// derived row — insights, recommendations, the four split logs, the Qdrant
// points — carries a freshly minted id on the new attempt, so an in-place
// upsert still has to delete the old rows BEFORE writing the new ones. That
// leaves a window in which the project has no visible result at all.
//
// Writing the new attempt first and retiring afterwards has no such window,
// makes the duplicate-key crash on discovery_recommendation_log's unique
// index structurally impossible rather than ordering-dependent (the new rows
// are a clean insert under a new discovery_id), and is a no-op on the common
// resume case, where the previous attempt died before it saved anything.
//
// Every failure is logged and swallowed: the run's result is already durable,
// and a leftover superseded document is a tidiness problem, not a correctness
// one. Retrying the retire step is harmless.
// discoveryRetirer is the slice of *database.DiscoveryRepository the retire
// step calls. An interface so the retire logic — the one part of resume that
// DELETES things — can be exercised against fakes rather than only against a
// live MongoDB.
type discoveryRetirer interface {
	ListIDsByRun(ctx context.Context, runID string) ([]string, error)
	DeleteByID(ctx context.Context, idHex string) error
}

// retireDeps is everything retireSuperseded touches. Passed in rather than
// read off the Orchestrator so the whole deletion path is unit-testable; the
// method below is the one-line adapter that supplies the run's real stores.
type retireDeps struct {
	discoveries discoveryRetirer
	logs        discoveryLogPersister
	embed       EmbedIndexStore
	vectors     vectorDeleter
}

// vectorDeleter is the one method of vectorstore.Provider the retire step
// uses. Narrowed rather than taking the whole provider so a test does not
// have to stub a dozen search methods to assert which points were deleted.
type vectorDeleter interface {
	Delete(ctx context.Context, ids []string) error
}

// retireOwnResult deletes the result THIS attempt just saved, along with
// everything derived from it.
//
// For the attempt that lost the run. It has already written its discovery,
// split logs, standalone docs and vectors by the time it finds out — those
// happen before the terminal write that establishes ownership — and leaving
// them is worse than the problem skipping the cleanup avoided: the orphan's
// discovery_date is typically LATER than the live attempt's, so its result
// becomes the project's latest and the dead attempt wins the display.
//
// It deletes only its own, never the owner's. Idempotent: the live attempt's
// own retire may have removed it already.
func (o *Orchestrator) retireOwnResult(ctx context.Context, discoveryID string) {
	if discoveryID == "" {
		// Save never got far enough to produce one; nothing to clean up.
		return
	}
	applog.WithFields(applog.Fields{
		"run_id":       o.runID,
		"discovery_id": discoveryID,
	}).Warn("this attempt lost the run; removing the result it produced so the owning attempt's stays the project's latest")
	retireDiscovery(ctx, o.runID, discoveryID, retireDeps{
		discoveries: o.discoveryRepo,
		logs:        o.discoveryLogRepo,
		embed:       o.embedIndexStore,
		vectors:     o.vectorStore,
	})
}

func (o *Orchestrator) retireSupersededAttempts(ctx context.Context, keepDiscoveryID string) {
	retireSuperseded(ctx, o.runID, keepDiscoveryID, retireDeps{
		discoveries: o.discoveryRepo,
		logs:        o.discoveryLogRepo,
		embed:       o.embedIndexStore,
		vectors:     o.vectorStore,
	})
}

func retireSuperseded(ctx context.Context, runID, keepDiscoveryID string, deps retireDeps) {
	if runID == "" || keepDiscoveryID == "" || deps.discoveries == nil {
		return
	}
	ids, err := deps.discoveries.ListIDsByRun(ctx, runID)
	if err != nil {
		applog.WithFields(applog.Fields{
			"run_id": runID,
			"error":  err.Error(),
		}).Warn("could not list this run's discoveries; a superseded partial result may remain visible")
		return
	}

	for _, id := range ids {
		if id == keepDiscoveryID {
			continue
		}
		retireDiscovery(ctx, runID, id, deps)
	}
}

// retireDiscovery removes one superseded result and everything derived from
// it: the standalone insight / recommendation rows, their Qdrant points, the
// split-log rows, and the parent document.
//
// Vectors go first among the derived data, because a point whose Mongo row is
// gone is an orphan that search can still return — while a row whose point is
// gone merely ranks lower.
func retireDiscovery(ctx context.Context, runID, discoveryID string, deps retireDeps) {
	logf := applog.WithFields(applog.Fields{"run_id": runID, "discovery_id": discoveryID})

	if deps.embed != nil {
		insightIDs, recIDs, err := deps.embed.DeleteByDiscovery(ctx, discoveryID)
		if err != nil {
			logf.WithError(err).Warn("failed to delete a superseded attempt's standalone insight / recommendation rows")
		}
		pointIDs := append(append(make([]string, 0, len(insightIDs)+len(recIDs)), insightIDs...), recIDs...)
		if len(pointIDs) > 0 && deps.vectors != nil {
			if err := deps.vectors.Delete(ctx, pointIDs); err != nil {
				logf.WithError(err).Warn("failed to delete a superseded attempt's vectors; project search may return orphaned points until the next run")
			}
		}
	}

	if deps.logs != nil {
		if _, err := deps.logs.DeleteByDiscovery(ctx, discoveryID); err != nil {
			logf.WithError(err).Warn("failed to delete a superseded attempt's split-log rows")
		}
	}

	if err := deps.discoveries.DeleteByID(ctx, discoveryID); err != nil {
		logf.WithError(err).Warn("failed to delete a superseded attempt's discovery document; the project may show two results for one run")
		return
	}
	logf.Info("retired a superseded attempt's result")
}

// discardCheckpoints deletes the run's checkpoints, because the run has
// reached an outcome it cannot be resumed from.
//
// Best-effort with the TTL as the backstop. Clearing keepStepIndex is the
// load-bearing half: it is what lets the deferred Drop take the per-run
// vector collection with it on a successful exit, instead of leaving it for
// the boot sweep.
func (o *Orchestrator) discardCheckpoints(ctx context.Context, why string) {
	if o.checkpointRepo == nil || o.runID == "" {
		return
	}
	deleted, err := o.checkpointRepo.DeleteByRun(ctx, o.runID)
	if err != nil {
		applog.WithFields(applog.Fields{
			"run_id": o.runID,
			"reason": why,
			"error":  err.Error(),
		}).Warn("failed to delete checkpoints; the retention TTL will reclaim them")
		return
	}
	o.keepStepIndex = false
	applog.WithFields(applog.Fields{
		"run_id":  o.runID,
		"reason":  why,
		"deleted": deleted,
	}).Debug("deleted this run's exploration checkpoints")
}

// rebuildStepIndexForResume drops the per-run step index so a resumed run
// rebuilds it from the prefix it actually replays.
//
// See the call site in RunDiscovery for why reuse is unsafe: the surviving
// collection can hold points for steps the replayable prefix no longer
// includes. No-op on an ordinary run, and on a resume with nothing to replay
// — there would be nothing to rebuild from, and an empty collection is what
// an ordinary run starts with anyway.
//
// A failed drop is logged and swallowed, like every other index operation
// here: the run works without the index, and refusing to resume over a
// Qdrant hiccup would be the wrong trade. It does mean the stale points can
// survive a failed drop, which noveltyMeasurable and the ranking degrade
// around rather than break on.
func (o *Orchestrator) rebuildStepIndexForResume(ctx context.Context) {
	if o.runStepIndex == nil || !o.resume.active() {
		return
	}
	if err := o.runStepIndex.Drop(ctx); err != nil {
		applog.WithFields(applog.Fields{
			"run_id": o.runID,
			"error":  err.Error(),
		}).Warn("resume: dropping the per-run step index before replay failed; it may still hold steps the replay discarded")
		return
	}
	applog.WithField("run_id", o.runID).Info("resume: dropped the per-run step index; replay will rebuild it from the replayable prefix")
}

// ownershipLost gates an expensive phase on this attempt still owning the run.
//
// The exploration loop finds out it has been superseded for free, on the
// attempt-fenced marker it writes every step, and checkpointExplorationSummary
// is the gate between exploration and everything after it. Past that point
// nothing asked again until the terminal write — so a resume landing during
// analysis left the old attempt to spend the whole post-exploration pipeline
// (analysis per area, validation per insight, recommendations) on a result
// its own Complete would then miss and delete. Correct in the end, and
// entirely wasted.
//
// The writes it makes along the way are all attempt-fenced, so they no-op
// silently; none of them reports back. Hence an explicit read, at the phase
// boundaries where the money is about to be spent: once per phase and once
// per analysis area, each an indexed read on _id next to multi-second LLM
// calls.
//
// OwnsRun fails OPEN — a read it could not complete answers "still ours" —
// so a Mongo blip degrades to the old behaviour of spending the phase rather
// than aborting a run that is working. That asymmetry is deliberate: the
// cost of a wasted phase is money, and the cost of a wrong abort is a
// discovery the operator has to pay for twice.
func (o *Orchestrator) ownershipLost(ctx context.Context, what string) bool {
	if o.statusReporter == nil || o.statusReporter.OwnsRun(ctx) {
		return false
	}
	applog.WithFields(applog.Fields{
		"run_id": o.runID,
		"before": what,
	}).Warn("another attempt of this run has taken over; stopping rather than spending this phase on a result that will be discarded")
	return true
}
