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

	"github.com/google/uuid"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"

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
	// keepStepIndex is deliberately NOT set here. An area can only be
	// checkpointed once a durable exploration summary exists, and both paths
	// to one have already set it — the explore path in
	// checkpointExplorationSummary, the resume path at the top of
	// RunDiscovery — so a setter here could never change the value.
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
// run's accumulators, and with restoredValidationBudget below is the whole of
// what restoring an area means.
//
// Two values because the Phase 4 loop body produces exactly three things that
// outlive the iteration, and the third — the validation budget — is restored
// up front rather than here. See restoredValidationBudget for why it cannot
// be done per area.
//
//   - The analysis step, which Phase 7 turns into the run's analysis log and
//     its split-log rows, and from which the failed-area count and so the
//     run's partial/full/failed classification are computed. A restored area
//     always carries an empty Error — only completed areas are checkpointed
//     — so it contributes to the denominator without ever being counted as a
//     failure.
//   - Its insights, which flow into the result, into the recommendation
//     phase's eligibility filter, and into Phase 8's embedding, carrying the
//     ids that Phase 5's related_insight_ids resolve against.
//
// A pure function so the arithmetic is testable on its own: RunDiscovery
// needs a warehouse, an LLM and a schema cache, so the loop it lives in
// cannot be driven from a unit test.
func restoreArea(
	cp models.AnalysisAreaCheckpoint,
	analysisLog []models.AnalysisStep,
	allInsights []models.Insight,
) ([]models.AnalysisStep, []models.Insight) {
	step := reidentifyRestoredArea(cp.Step)
	return append(analysisLog, step), append(allInsights, step.Insights...)
}

// reidentifyRestoredArea gives a restored area's insights fresh ids, and
// re-points its validation results at them.
//
// An insight id is the key of three things: the embedded insight in the
// discovery document, the standalone row in the `insights` collection, and
// the Qdrant point. All three are written by the tail as this attempt's own,
// under a freshly minted discovery_id — so an id carried over from the
// attempt that produced the area is an id that may ALREADY be in those
// collections, written by that attempt before it died. Three separate
// failures follow from that, and they are why the ids are reminted rather
// than preserved:
//
//   - The standalone write is an insert, so a surviving row is an E11000 on
//     `_id`. That phase is non-fatal, so it logs and returns — dropping the
//     standalone recommendation rows and the embedding with them — and the
//     retire step then removes the previous attempt's rows. A run that
//     finished successfully ends up with no findings in search at all.
//   - The duplicate check runs BEFORE the vectors are upserted, and excludes
//     only this discovery's own points. A surviving point carrying the same
//     id is therefore returned as a ~1.0 match, and the insight is marked a
//     duplicate of itself.
//   - A superseded attempt that is still persisting could write those shared
//     ids back under its own discovery_id; when its terminal claim is then
//     refused, its own cleanup deletes what are by then the live attempt's
//     findings.
//
// Reminting removes the shared identity those three need. It is also not a
// change in observable behaviour: before per-area resume a resumed run
// re-ran analysis from scratch and minted new ids for every insight anyway,
// so no consumer can have been relying on an id surviving a resume.
//
// Copies rather than rewriting in place: the slices belong to the loaded
// checkpoint, which the live-feed replay has already read, and the stored row
// must keep its own ids so a later attempt remints from the same baseline.
func reidentifyRestoredArea(step models.AnalysisStep) models.AnalysisStep {
	if len(step.Insights) == 0 {
		return step
	}
	insights := make([]models.Insight, len(step.Insights))
	copy(insights, step.Insights)
	freshByOld := make(map[string]string, len(insights))
	for i := range insights {
		old := insights[i].ID
		fresh := uuid.New().String()
		insights[i].ID = fresh
		if old != "" {
			freshByOld[old] = fresh
		}
		// The verdict names the document it judged, twice over and
		// separately from the outer id: Validation is a POINTER, so the
		// shallow copy above still shares the checkpoint's struct, and the
		// verifier and refuter verdicts inside it each carry their own
		// doc_id. Left alone, this attempt would persist evidence — in the
		// discovery document, the standalone row and the split validation
		// log — identifying an insight that does not exist in its result,
		// and naming one the retire step is about to delete.
		insights[i].Validation = reidentifyInsightValidation(insights[i].Validation, fresh)
	}
	step.Insights = insights

	if len(step.ValidationResults) > 0 {
		results := make([]models.ValidationResult, len(step.ValidationResults))
		copy(results, step.ValidationResults)
		for i := range results {
			fresh, ok := freshByOld[results[i].InsightID]
			if !ok {
				// A result whose insight this area no longer carries. Left
				// exactly as it was rather than re-pointed at some other
				// finding.
				continue
			}
			results[i].InsightID = fresh
			results[i].Verifier = reidentifyVerdict(results[i].Verifier, fresh)
			results[i].Refuter = reidentifyVerdict(results[i].Refuter, fresh)
		}
		step.ValidationResults = results
	}
	return step
}

// reidentifyInsightValidation deep-copies an insight's validation and points
// it, and the verdicts inside it, at the insight's new id. Nil in, nil out.
func reidentifyInsightValidation(v *valmodels.InsightValidation, docID string) *valmodels.InsightValidation {
	if v == nil {
		return nil
	}
	out := *v
	out.Verifier = reidentifyVerdict(v.Verifier, docID)
	out.Refuter = reidentifyVerdict(v.Refuter, docID)
	return &out
}

// reidentifyVerdict deep-copies one structured verdict and restamps the
// document it judged. Nil in, nil out — a refuter is legitimately absent.
//
// A copy rather than a mutation because the verdict belongs to the loaded
// checkpoint, which must keep its own ids: a later attempt remints from that
// same baseline, and the live-feed replay has already read it.
func reidentifyVerdict(v *valmodels.StructuredVerdict, docID string) *valmodels.StructuredVerdict {
	if v == nil {
		return nil
	}
	out := *v
	out.DocID = docID
	return &out
}

// restoredValidationBudget totals what the restored areas already spent of
// the run's per-insight validation cap.
//
// Summed BEFORE the area loop rather than added as each area is restored,
// and that ordering is the whole correctness of it. The orchestrator builds
// its area list by ranging over a map of prompts, so the order is
// nondeterministic: an area this attempt still has to run can come first, be
// validated against a budget that has not yet absorbed what the previous
// attempt spent, and push the run past the cap the project set — after which
// the restore would quietly top the counter up to a number that was already
// exceeded.
//
// Only this run's own areas count. A checkpoint can name an area the run is
// not running, and that area's insights are not in its result, so charging
// the run for validating them would shrink a budget nothing is spending.
func restoredValidationBudget(completed map[string]models.AnalysisAreaCheckpoint) int {
	total := 0
	for _, cp := range completed {
		total += cp.InsightsValidated
	}
	return total
}

// restorableAreas narrows the checkpoint set to the areas THIS run is
// actually going to restore.
//
// The two sets can legitimately differ: a checkpoint can name an area the run
// is not running, because the project's domain pack was edited between
// attempts or the resume used a narrower area selection. Such a row is never
// read by the loop, so it must not reach anything else either — counted in
// the live panel it would claim progress on work the run was never going to
// do, and re-emitted into the feed it would show the operator analysis rows
// for an area the finished discovery does not contain.
//
// Narrowing once, here, is what keeps those three readers — the loop, the
// panel and the feed — looking at the same set.
func restorableAreas(runAreas []AnalysisArea, completed map[string]models.AnalysisAreaCheckpoint) map[string]models.AnalysisAreaCheckpoint {
	if len(completed) == 0 {
		return nil
	}
	out := make(map[string]models.AnalysisAreaCheckpoint, len(completed))
	for _, area := range runAreas {
		if cp, ok := completed[area.ID]; ok {
			out[area.ID] = cp
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
