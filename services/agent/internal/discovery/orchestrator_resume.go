package discovery

// Resume support for the orchestrator: checkpoint wiring, the
// skip-exploration branch, and the idempotent tail.
//
// Kept out of orchestrator.go, which is already 3 000 lines, so the feature
// reads as one thing rather than as a scatter of conditionals.

import (
	"context"
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
	err := o.checkpointRepo.SaveStep(ctx, database.CheckpointStepInput{
		ProjectID: o.projectID,
		RunID:     o.runID,
		Attempt:   o.resume.attemptNumber(),
		Step:      step,
		RowSample: verifier.CheckpointSample(step.QueryResult, o.validationCfg.Bundle),
		Args:      args,
	})
	if err != nil {
		return err
	}
	// The run is now resumable, so its per-run vector index must survive a
	// failure rather than being dropped on the way out.
	o.keepStepIndex = true

	if o.statusReporter != nil {
		o.statusReporter.MarkExplorationCheckpoint(ctx, step.Step)
	}
	return nil
}

// checkpointExplorationSummary records that exploration finished. Its
// presence is what lets a later attempt go straight to analysis — zero
// exploration LLM calls, zero warehouse queries.
func (o *Orchestrator) checkpointExplorationSummary(ctx context.Context, res *ai.ExplorationResult) {
	if o.checkpointRepo == nil || res == nil {
		return
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
		applog.WithFields(applog.Fields{
			"run_id": o.runID,
			"error":  err.Error(),
		}).Warn("failed to checkpoint the exploration summary; a resumed run would re-explore instead of going straight to analysis")
		return
	}
	o.keepStepIndex = true
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
func (o *Orchestrator) retireSupersededAttempts(ctx context.Context, keepDiscoveryID string) {
	if o.runID == "" || keepDiscoveryID == "" {
		return
	}
	ids, err := o.discoveryRepo.ListIDsByRun(ctx, o.runID)
	if err != nil {
		applog.WithFields(applog.Fields{
			"run_id": o.runID,
			"error":  err.Error(),
		}).Warn("could not list this run's discoveries; a superseded partial result may remain visible")
		return
	}

	for _, id := range ids {
		if id == keepDiscoveryID {
			continue
		}
		o.retireDiscovery(ctx, id)
	}
}

// retireDiscovery removes one superseded result and everything derived from
// it: the standalone insight / recommendation rows, their Qdrant points, the
// split-log rows, and the parent document.
//
// Vectors go first among the derived data, because a point whose Mongo row is
// gone is an orphan that search can still return — while a row whose point is
// gone merely ranks lower.
func (o *Orchestrator) retireDiscovery(ctx context.Context, discoveryID string) {
	logf := applog.WithFields(applog.Fields{"run_id": o.runID, "discovery_id": discoveryID})

	if o.embedIndexStore != nil {
		insightIDs, recIDs, err := o.embedIndexStore.DeleteByDiscovery(ctx, discoveryID)
		if err != nil {
			logf.WithError(err).Warn("failed to delete a superseded attempt's standalone insight / recommendation rows")
		}
		pointIDs := append(append(make([]string, 0, len(insightIDs)+len(recIDs)), insightIDs...), recIDs...)
		if len(pointIDs) > 0 && o.vectorStore != nil {
			if err := o.vectorStore.Delete(ctx, pointIDs); err != nil {
				logf.WithError(err).Warn("failed to delete a superseded attempt's vectors; project search may return orphaned points until the next run")
			}
		}
	}

	if o.discoveryLogRepo != nil {
		if _, err := o.discoveryLogRepo.DeleteByDiscovery(ctx, discoveryID); err != nil {
			logf.WithError(err).Warn("failed to delete a superseded attempt's split-log rows")
		}
	}

	if err := o.discoveryRepo.DeleteByID(ctx, discoveryID); err != nil {
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
