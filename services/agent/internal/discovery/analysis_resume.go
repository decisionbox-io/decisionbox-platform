package discovery

// Per-area analysis resume: the checkpoint written as each analysis area
// finishes, the restore that lets a resumed run skip those areas, and the
// live-feed replay they need because the feed is attempt-scoped.
//
// A sibling of orchestrator_resume.go rather than part of it: that file is
// exploration's story — the replayable prefix, the skip-exploration branch,
// the idempotent tail — and this is the phase after it.

import (
	"context"
	"errors"
	"sort"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// analysisCheckpointStore is the slice of
// *database.DiscoveryAnalysisCheckpointRepository the orchestrator calls. An
// interface so a unit test can inject a fake without MongoDB, and so a nil
// store cleanly disables per-area resume.
type analysisCheckpointStore interface {
	SaveArea(ctx context.Context, in database.AnalysisAreaCheckpointInput) error
	LoadAreas(ctx context.Context, runID string, explorationAttempt int) (*database.AnalysisCheckpointSet, error)
	DeleteByRun(ctx context.Context, runID string) (int64, error)
	TouchByRun(ctx context.Context, runID string) (int64, error)
}

// checkpointAnalysisArea records that one analysis area is finished, so a
// resumed run restores it instead of paying for it again.
//
// Only a COMPLETED area is recorded, and that is the most consequential
// decision in this file rather than an implementation detail.
//
// An area whose analysis call failed — a provider error, a context deadline,
// a response that could not be parsed at all — gets an AnalysisStep with
// Error set, and is deliberately left unmarked so a resumed run retries it.
// Marking it would break the case per-area resume is most useful for: when
// DISCOVERY_MAX_DURATION expires mid-analysis the loop does not stop, it
// error-marks every area it has left and the run fails with a thin partial
// result. If those areas counted as done, the resume would restore all of
// them, re-run nothing, and reproduce the same near-empty discovery. A failed
// analysis call also banks no work worth protecting, so there is nothing on
// the other side of the trade.
//
// The marker's existence IS its status, which is why no status field is
// stored: a row only ever describes an area that produced a parsed result,
// finished its validation and had its evidence-quality labels attached.
//
// Ownership is asked BEFORE writing, for the reason checkpointStep gives at
// length: the row-level attempt fence in SaveArea only refuses a write where
// a higher-attempt row ALREADY exists, so a superseded attempt running ahead
// of the live one would otherwise plant an area the live attempt has not
// reached and have it restored as though the live attempt had done it.
//
// Returns ai.ErrAttemptSuperseded when this attempt has lost the run — the
// caller must stop rather than carry the rest of the pipeline on another
// attempt's behalf. Every other failure is logged and swallowed, like every
// checkpoint write: the cost is that the area is analysed again on a later
// resume, and a checkpoint must never be able to abort a working run.
func (o *Orchestrator) checkpointAnalysisArea(ctx context.Context, step models.AnalysisStep, insightsValidated int) error {
	if o.analysisCheckpointRepo == nil {
		return nil
	}
	if o.explorationSummaryAttempt <= 0 {
		// No durable exploration summary, so this run re-explores on resume
		// and nothing analysed over the current exploration could be
		// restored against the next one. Writing a row would be writing one
		// that LoadAreas is obliged to ignore.
		return nil
	}
	if step.Error != "" {
		return nil
	}
	if step.AreaID == "" {
		// The loop always has an area id here; a row without one is
		// unrestorable, so refuse it rather than storing a key nothing can
		// match.
		applog.WithField("run_id", o.runID).Warn("analysis area finished with no area id; not checkpointing it")
		return nil
	}
	if o.statusReporter != nil && !o.statusReporter.OwnsRun(ctx) {
		return ai.ErrAttemptSuperseded
	}

	err := o.analysisCheckpointRepo.SaveArea(ctx, database.AnalysisAreaCheckpointInput{
		ProjectID:          o.projectID,
		RunID:              o.runID,
		Attempt:            o.resume.attemptNumber(),
		ExplorationAttempt: o.explorationSummaryAttempt,
		Step:               step,
		InsightsValidated:  insightsValidated,
	})
	if err != nil {
		if errors.Is(err, database.ErrSupersededAttempt) {
			applog.WithFields(applog.Fields{
				"run_id": o.runID,
				"area":   step.AreaID,
			}).Warn("a newer attempt of this run owns this analysis area; stopping rather than spending the rest of the pipeline on a result that will be discarded")
			return ai.ErrAttemptSuperseded
		}
		applog.WithFields(applog.Fields{
			"run_id": o.runID,
			"area":   step.AreaID,
			"error":  err.Error(),
		}).Warn("failed to checkpoint a completed analysis area; a resumed run would analyse it again")
		return nil
	}
	// The run is now resumable past this area, so its per-run vector index
	// must survive a failure rather than being dropped on the way out. By
	// this point exploration already set the flag — an area can only be
	// checkpointed once a summary exists — so this is belt and braces
	// rather than the first setter.
	o.keepStepIndex = true
	return nil
}

// loadCompletedAreas reads back the analysis areas this run may restore.
//
// Three gates, each of which makes the map empty for a reason:
//
//   - no store: per-area resume is disabled for this deployment;
//   - not a resume: a first attempt has nothing to restore, and asking would
//     be a query per run for an answer that is always empty;
//   - this attempt is NOT skipping exploration: it is about to re-explore,
//     so the step numbers a stored area's insights cite are about to stop
//     meaning what they meant. loadAreas would refuse these rows anyway on
//     the exploration-attempt fence, and this says so at the call site too.
//
// A read failure costs money (the areas are analysed again), never
// correctness, so it is logged and swallowed.
func (o *Orchestrator) loadCompletedAreas(ctx context.Context) map[string]models.AnalysisAreaCheckpoint {
	if o.analysisCheckpointRepo == nil || !o.resume.active() || !o.resume.explorationComplete() {
		return nil
	}
	set, err := o.analysisCheckpointRepo.LoadAreas(ctx, o.runID, o.explorationSummaryAttempt)
	if err != nil {
		applog.WithFields(applog.Fields{
			"run_id": o.runID,
			"error":  err.Error(),
		}).Warn("could not load this run's completed analysis areas; every area will be analysed again")
		return nil
	}
	if set.Len() == 0 {
		return nil
	}
	// Reading them without rewriting them leaves their retention clock
	// running from the attempt that wrote them, exactly as the exploration
	// prefix does — so an operator resuming near the retention horizon
	// would watch them expire mid-run and lose them for a second resume.
	o.refreshAnalysisCheckpointTTL(ctx)
	applog.WithFields(applog.Fields{
		"run_id":              o.runID,
		"areas":               set.Len(),
		"from_attempt":        set.Attempt,
		"exploration_attempt": o.explorationSummaryAttempt,
	}).Info("resume: restoring analysis areas a previous attempt already completed")
	return set.Areas
}

// refreshAnalysisCheckpointTTL re-anchors the retention clock on a run's
// analysis checkpoints. Best-effort: losing it costs resumability on a later
// attempt, not this run. See refreshCheckpointTTL for the exploration half.
func (o *Orchestrator) refreshAnalysisCheckpointTTL(ctx context.Context) {
	if o.analysisCheckpointRepo == nil || o.runID == "" {
		return
	}
	touched, err := o.analysisCheckpointRepo.TouchByRun(ctx, o.runID)
	if err != nil {
		applog.WithFields(applog.Fields{
			"run_id": o.runID,
			"error":  err.Error(),
		}).Warn("could not re-anchor the analysis checkpoint retention clock; these rows may expire while this attempt is still running")
		return
	}
	applog.WithFields(applog.Fields{
		"run_id":  o.runID,
		"touched": touched,
	}).Debug("re-anchored the analysis checkpoint retention clock for the resumed attempt")
}

// discardAnalysisCheckpoints deletes the run's analysis checkpoints, because
// the run has reached an outcome it cannot be resumed from. Best-effort with
// the TTL as the backstop, like its exploration counterpart.
func (o *Orchestrator) discardAnalysisCheckpoints(ctx context.Context, why string) {
	if o.analysisCheckpointRepo == nil || o.runID == "" {
		return
	}
	deleted, err := o.analysisCheckpointRepo.DeleteByRun(ctx, o.runID)
	if err != nil {
		applog.WithFields(applog.Fields{
			"run_id": o.runID,
			"reason": why,
			"error":  err.Error(),
		}).Warn("failed to delete analysis checkpoints; the retention TTL will reclaim them")
		return
	}
	applog.WithFields(applog.Fields{
		"run_id":  o.runID,
		"reason":  why,
		"deleted": deleted,
	}).Debug("deleted this run's analysis area checkpoints")
}

// restoreArea merges one completed area's checkpointed output back into the
// run's accumulators, and is the whole of what restoring an area means.
//
// Three values because the Phase 4 loop body produces exactly three things
// that outlive the iteration, and everything downstream is derived from them:
//
//   - the analysis step, which Phase 7 turns into the run's analysis log and
//     its split-log rows, and from which the failed-area count and so the
//     run's partial/full/failed classification are computed. A restored area
//     always carries an empty Error — only completed areas are checkpointed
//     — so it contributes to the denominator without ever being counted as a
//     failure;
//   - its insights, which flow into the result, into the recommendation
//     phase's eligibility filter, and into Phase 8's embedding, carrying the
//     ids that Phase 5's related_insight_ids resolve against;
//   - its slice of the run's validation budget, so the areas this attempt
//     still has to run are not handed a fresh one.
//
// A pure function so the arithmetic is testable on its own: RunDiscovery
// needs a warehouse, an LLM and a schema cache, so the loop it lives in
// cannot be driven from a unit test.
func restoreArea(
	cp models.AnalysisAreaCheckpoint,
	analysisLog []models.AnalysisStep,
	allInsights []models.Insight,
	insightsValidated int,
) ([]models.AnalysisStep, []models.Insight, int) {
	return append(analysisLog, cp.Step),
		append(allInsights, cp.Step.Insights...),
		insightsValidated + cp.InsightsValidated
}

// countRestorableAreas reports how many of THIS run's areas the checkpoint
// set actually covers.
//
// Not len(completed): the two sets can legitimately differ. A checkpoint can
// name an area this run is not running — the project's domain pack was edited
// between attempts, or the operator resumed with a narrower area selection —
// and such a row is simply never read. Reporting the raw row count in the
// live panel would then claim progress on areas the run was never going to
// touch, and could even exceed its own total.
func countRestorableAreas(runAreas []AnalysisArea, completed map[string]models.AnalysisAreaCheckpoint) int {
	n := 0
	for _, area := range runAreas {
		if _, ok := completed[area.ID]; ok {
			n++
		}
	}
	return n
}

// replayAnalysisFeedForResume re-emits the restored areas' rows into this
// attempt's live feed.
//
// The same reason replayLiveFeedForResume exists one phase earlier: the feed
// is attempt-scoped, because the API's reader filters discovery_run_steps on
// the run's current attempt. The restored areas' analysis, insight and
// validation rows belong to the attempt that produced them, so without this
// a resumed run's log would show recommendations drawn from insights the
// operator cannot see.
//
// Emitted once, here, rather than inside the loop as each area is restored.
// The orchestrator's area list comes from ranging over a map of prompts, so
// its order is nondeterministic, and emitting per iteration would interleave
// the restored rows in that arbitrary order while their timestamps said
// something else. Ordering by the area's own RunAt keeps the feed monotonic.
//
// Rows only, no counters: the per-area analysis counters live on the run
// document and already carry across attempts, so bumping them again would
// double-count every restored area's step-index searches.
func (o *Orchestrator) replayAnalysisFeedForResume(ctx context.Context, completed map[string]models.AnalysisAreaCheckpoint) {
	if o.statusReporter == nil || len(completed) == 0 {
		return
	}
	steps := make([]models.AnalysisStep, 0, len(completed))
	for _, cp := range completed {
		steps = append(steps, cp.Step)
	}
	sort.Slice(steps, func(i, j int) bool {
		if steps[i].RunAt.Equal(steps[j].RunAt) {
			// A deterministic tiebreak so two areas recorded in the same
			// millisecond do not reintroduce the map's arbitrary order.
			return steps[i].AreaID < steps[j].AreaID
		}
		return steps[i].RunAt.Before(steps[j].RunAt)
	})
	for _, step := range steps {
		o.statusReporter.ReplayAnalysisArea(ctx, step)
	}
	applog.WithFields(applog.Fields{
		"run_id":  o.runID,
		"attempt": o.resume.attemptNumber(),
		"areas":   len(steps),
	}).Info("resume: re-emitted the restored analysis areas into this attempt's live feed")
}
