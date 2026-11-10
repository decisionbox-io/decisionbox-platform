package discovery

// Per-recommendation resume: the checkpoint written when Phase 5 finishes, the
// per-recommendation completion markers written as Phase 5.5's verdicts land,
// and the gate that decides whether a stored recommendation set still
// describes the findings this attempt actually produced.
//
// The last of the four checkpoint seams. Exploration, areas and area drafts
// all protect work that produces insights; this protects the work done ON
// them, which is the only spend a run can still lose in full.

import (
	"context"
	"errors"

	"github.com/google/uuid"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// recommendationCheckpointStore is the slice of
// *database.DiscoveryRecommendationCheckpointRepository the orchestrator
// calls. An interface so a unit test can inject a fake without MongoDB, and so
// a nil store cleanly disables recommendation resume.
type recommendationCheckpointStore interface {
	SaveDraft(ctx context.Context, in database.RecommendationCheckpointInput) error
	SaveRecVerdict(ctx context.Context, in database.RecommendationVerdictInput) (bool, error)
	Load(ctx context.Context, runID string, explorationAttempt int) (*models.RecommendationCheckpoint, error)
	DeleteByRun(ctx context.Context, runID string) (int64, error)
	TouchByRun(ctx context.Context, runID string) (int64, error)
}

// checkpointRecommendations records a finished Phase 5, so a resumed run goes
// straight to validating the recommendations instead of generating them again.
//
// Only a phase that banked something is recorded. A generation call that
// failed, or one whose response could not be parsed at all, is deliberately
// left unmarked so a resumed run retries it — the same distinction
// checkpointAnalysisArea draws for a failed area, and for the same reason:
// restoring a failure would reproduce it instead of recovering from it.
//
// A clean zero-recommendation result IS recorded. Re-running it costs a real
// LLM call, the honest answer is still "none", and recStep.Status carries the
// reason the dashboard renders.
//
// eligibleIDs is the insight set the recommender was given, in the ids the
// run's analysis checkpoints hold for those insights. It is the restore gate;
// see restorableRecommendations.
//
// Ownership is asked BEFORE writing, for the reason every other checkpoint
// asks: the row-level fence only refuses a write where a higher-attempt row
// already exists, so a superseded attempt running ahead of the live one would
// otherwise plant recommendations the live attempt has not generated.
// The bool says whether the row is actually on disk, and it GATES the
// per-recommendation marker writes for the reason checkpointAreaDraft's does:
// a marker write is filtered on `attempt <= this attempt`, so with no row of
// its own it can land on an earlier attempt's, where a reused model-supplied
// id would overwrite a marker describing a different recommendation.
//
// Also a PAUSE seam, under the uniform rule: once this row is down, Phase 5 is
// paid for, so a stop costs nothing and saves the operator every
// per-recommendation verifier call in Phase 5.5. Found before the write, taken
// after.
func (o *Orchestrator) checkpointRecommendations(ctx context.Context, step *models.RecommendationStep, eligibleIDs, generation []string) (bool, error) {
	if o.recommendationCheckpointRepo == nil || step == nil {
		return false, nil
	}
	if o.explorationSummaryAttempt <= 0 {
		return false, nil
	}
	if step.Error != "" || step.Status == statusRecommendationParseError {
		return false, nil
	}
	if step.Status == statusRecommendationSkippedNoEligible {
		// The phase made no LLM call, so there is nothing to protect — and
		// the eligible set was empty, which a later attempt could satisfy
		// trivially without the recommendations being right for it.
		return false, nil
	}
	// Standing rather than OwnsRun: one read, both questions, and the pause
	// half remembered rather than acted on so the write below still happens.
	pauseRequested := false
	if o.statusReporter != nil {
		switch o.statusReporter.Standing(ctx) {
		case standingLost:
			return false, ai.ErrAttemptSuperseded
		case standingPauseRequested:
			pauseRequested = true
		}
	}

	err := o.recommendationCheckpointRepo.SaveDraft(ctx, database.RecommendationCheckpointInput{
		ProjectID:          o.projectID,
		RunID:              o.runID,
		Attempt:            o.resume.attemptNumber(),
		ExplorationAttempt: o.explorationSummaryAttempt,
		Step:               *step,
		EligibleInsightIDs: eligibleIDs,
		AnalysisGeneration: generation,
	})
	if err != nil {
		if errors.Is(err, database.ErrSupersededAttempt) {
			applog.WithField("run_id", o.runID).Warn("a newer attempt of this run owns its recommendations; stopping rather than spending the persistence tail on a result that will be discarded")
			return false, ai.ErrAttemptSuperseded
		}
		applog.WithFields(applog.Fields{
			"run_id": o.runID,
			"error":  err.Error(),
		}).Warn("failed to checkpoint this run's recommendations; a resumed run would generate and re-validate them")
		// No row, so no free stop: pausing here would discard the generation
		// call. The request stands for the next seam that has something
		// durable behind it.
		return false, nil
	}
	// Phase 5 is durable, so a stop here is free and saves the operator the
	// whole of Phase 5.5. Signalled, not performed — the caller still has this
	// phase's live row to emit.
	if pauseRequested {
		return true, o.pauseSignalIfResumable("recommendation checkpoint")
	}
	return true, nil
}

// translateRecommendationInsightIDs rewrites every insight id a slice of
// recommendations names, through `to`, onto a deep copy — and reports a ref it
// could not translate rather than leaving it pointing at the wrong document.
//
// A recommendation names insights in TWO places, and missing the second one is
// subtle because it is still persisted and still carries a verdict.
// RelatedInsightIDs is the citation list everything downstream resolves; but a
// recommendation's figures are the one grammar it has for numbers, and each
// ref names the insight whose figure it restates (models.FigureRef.Insight —
// "the same id related_insight_ids carries"). Left in the previous attempt's
// id space, the result ships figure evidence naming insights its own discovery
// does not contain, with the original `holds` verdicts still attached, so the
// provenance reads as checked when nothing in the result can be checked
// against it.
//
// Deep copies down to each figure's own Refs slice: a shallow copy of the
// recommendation shares the Figures array, and a copy of a Figure shares its
// Refs array, so rewriting either in place would reach back into whatever the
// caller handed over — this attempt's live recommendations in one direction,
// the loaded checkpoint in the other.
//
// `missing` is left empty when `to` is nil or empty, which is the identity
// case: a run that restored nothing names the ids its own checkpoints hold.
func translateRecommendationInsightIDs(recs []models.Recommendation, to map[string]string) (out []models.Recommendation, missing []string) {
	out = make([]models.Recommendation, len(recs))
	copy(out, recs)
	identity := len(to) == 0

	translate := func(id string) (string, bool) {
		if identity {
			return id, true
		}
		if mapped, ok := to[id]; ok && mapped != "" {
			return mapped, true
		}
		return id, false
	}

	for i := range out {
		if len(out[i].RelatedInsightIDs) > 0 {
			ids := make([]string, len(out[i].RelatedInsightIDs))
			for j, id := range out[i].RelatedInsightIDs {
				mapped, ok := translate(id)
				if !ok {
					missing = append(missing, id)
				}
				ids[j] = mapped
			}
			out[i].RelatedInsightIDs = ids
		}
		if len(out[i].Figures) == 0 {
			continue
		}
		figures := make([]commonmodels.Figure, len(out[i].Figures))
		copy(figures, out[i].Figures)
		for j := range figures {
			if len(figures[j].Refs) == 0 {
				continue
			}
			refs := make([]commonmodels.FigureRef, len(figures[j].Refs))
			copy(refs, figures[j].Refs)
			for k := range refs {
				if refs[k].Insight == "" {
					continue
				}
				mapped, ok := translate(refs[k].Insight)
				if !ok {
					missing = append(missing, refs[k].Insight)
				}
				refs[k].Insight = mapped
			}
			figures[j].Refs = refs
		}
		out[i].Figures = figures
	}
	return out, missing
}

// recommendationStepInStoredIDs returns a copy of a finished Phase 5 whose
// insight citations are expressed in the ids the run's analysis checkpoints
// hold, rather than in this attempt's live ids.
//
// Without it the stored row mixes two id spaces and defeats its own gate. An
// attempt that restored a completed area reasons over REMINTED insight ids, so
// the recommender cites those — while EligibleInsightIDs is recorded in the
// checkpoints' ids, because that is the only id space stable across attempts.
// The next resume then passes the eligible-set check and fails to translate a
// single citation, refusing a row that describes exactly the insights it holds.
//
// A copy, because the live recommendations must keep their live ids: Phase 5.5
// validates them, the result persists them, and only the stored row speaks the
// checkpoints' language. Identity, and so a no-op, on an attempt that restored
// nothing.
func recommendationStepInStoredIDs(step models.RecommendationStep, storedByLive map[string]string) models.RecommendationStep {
	if len(storedByLive) == 0 || len(step.Recommendations) == 0 {
		return step
	}
	// An id this attempt analysed itself is absent from storedByLive, and its
	// live id IS the id its checkpoint holds — so an untranslated id is
	// correct here and the missing list is deliberately ignored. It only
	// means something in the other direction, where a stored id with no live
	// counterpart is an insight this attempt does not have.
	recs, _ := translateRecommendationInsightIDs(step.Recommendations, storedByLive)
	step.Recommendations = recs
	return step
}

// checkpointRecommendationVerdict records that one recommendation has been
// validated. Called from validationPhase's bounded workers, so it must stay
// safe to call concurrently. Failures are swallowed: a lost marker costs one
// re-validated recommendation.
//
// Also a per-item PAUSE seam, on the same contract as
// checkpointInsightVerdict: ai.ErrPauseRequested means this verdict is durable
// and an operator is waiting, and supersession is left to the row-level
// attempt fence in SaveRecVerdict for the same reason.
func (o *Orchestrator) checkpointRecommendationVerdict(ctx context.Context, vr models.ValidationResult) error {
	if o.recommendationCheckpointRepo == nil || vr.InsightID == "" {
		return nil
	}
	if o.explorationSummaryAttempt <= 0 {
		return nil
	}
	pauseRequested := o.statusReporter != nil && o.statusReporter.Standing(ctx) == standingPauseRequested
	landed, err := o.recommendationCheckpointRepo.SaveRecVerdict(ctx, database.RecommendationVerdictInput{
		RunID:            o.runID,
		Attempt:          o.resume.attemptNumber(),
		RecommendationID: vr.InsightID,
		Verdict:          vr,
	})
	if err != nil {
		applog.WithFields(applog.Fields{
			"run_id":         o.runID,
			"recommendation": vr.InsightID,
			"error":          err.Error(),
		}).Warn("failed to record a validated recommendation; it would be validated again on a later resume")
		return nil
	}
	if !landed {
		// Nothing took the marker — same three causes as the insight seam's,
		// same conclusion: no row, no free stop.
		applog.WithFields(applog.Fields{
			"run_id":         o.runID,
			"recommendation": vr.InsightID,
		}).Warn("no recommendation checkpoint took this verdict, so it is not durable; not pausing on it")
		return nil
	}
	if pauseRequested {
		return o.pauseSignalIfResumable("recommendation verdict")
	}
	return nil
}

// loadRecommendationCheckpoint reads back this run's stored Phase 5, if any.
//
// The gates are loadCompletedAreas': no store, not a resume, or this attempt
// is about to RE-explore — in which case the step numbers the insights behind
// those recommendations cite are about to stop meaning what they meant.
//
// A read failure costs money, never correctness, so it is logged and
// swallowed. Nil is the ordinary answer.
func (o *Orchestrator) loadRecommendationCheckpoint(ctx context.Context) *models.RecommendationCheckpoint {
	if o.recommendationCheckpointRepo == nil || !o.resume.active() || !o.resume.explorationComplete() {
		return nil
	}
	cp, err := o.recommendationCheckpointRepo.Load(ctx, o.runID, o.explorationSummaryAttempt)
	if err != nil {
		applog.WithFields(applog.Fields{
			"run_id": o.runID,
			"error":  err.Error(),
		}).Warn("could not load this run's stored recommendations; they will be generated again")
		return nil
	}
	if cp == nil {
		return nil
	}
	o.refreshRecommendationCheckpointTTL(ctx)
	return cp
}

// refreshRecommendationCheckpointTTL re-anchors the retention clock.
// Best-effort, like its three counterparts.
func (o *Orchestrator) refreshRecommendationCheckpointTTL(ctx context.Context) {
	if o.recommendationCheckpointRepo == nil || o.runID == "" {
		return
	}
	touched, err := o.recommendationCheckpointRepo.TouchByRun(ctx, o.runID)
	if err != nil {
		applog.WithFields(applog.Fields{
			"run_id": o.runID,
			"error":  err.Error(),
		}).Warn("could not re-anchor the recommendation checkpoint retention clock; the row may expire while this attempt is still running")
		return
	}
	applog.WithFields(applog.Fields{
		"run_id":  o.runID,
		"touched": touched,
	}).Debug("re-anchored the recommendation checkpoint retention clock for the resumed attempt")
}

// discardRecommendationCheckpoint deletes the run's recommendation
// checkpoint, because the run has reached an outcome it cannot be resumed
// from. Best-effort with the TTL as the backstop.
func (o *Orchestrator) discardRecommendationCheckpoint(ctx context.Context, why string) {
	if o.recommendationCheckpointRepo == nil || o.runID == "" {
		return
	}
	deleted, err := o.recommendationCheckpointRepo.DeleteByRun(ctx, o.runID)
	if err != nil {
		applog.WithFields(applog.Fields{
			"run_id": o.runID,
			"reason": why,
			"error":  err.Error(),
		}).Warn("failed to delete the recommendation checkpoint; the retention TTL will reclaim it")
		return
	}
	applog.WithFields(applog.Fields{
		"run_id":  o.runID,
		"reason":  why,
		"deleted": deleted,
	}).Debug("deleted this run's recommendation checkpoint")
}

// restorableRecommendations decides whether a stored Phase 5 can be reused by
// this attempt, and if so returns it with its insight citations re-pointed at
// this attempt's insights.
//
// Two things have to hold, and one mechanism answers both.
//
// A recommendation cites insights by id through RelatedInsightIDs. Those ids
// belong to the attempt that generated it, and a resumed run mints fresh ids
// for every insight it restores — so the citations have to be translated. And
// the recommendations were reasoned FROM that insight set, so if this attempt
// produced a different one (it re-analysed an area, or the project's
// eligibility verdicts changed between attempts) the stored recommendations
// describe findings the run no longer holds and must not be reused.
//
// liveByStored maps the ids the checkpoints hold onto this attempt's live ids
// — see storedInsightIDs for the invariant that makes the stored half stable
// across attempts. The gate is then a set comparison:
//
//   - the stored eligible set is the same SIZE as this attempt's;
//   - every id in it maps to an insight this attempt is actually giving the
//     recommender;
//   - every citation of every stored recommendation maps through it too.
//
// On a miss the checkpoint is IGNORED, not deleted, and Phase 5 runs normally
// — v2's rule, so a transient misread cannot lose paid-for work. The log line
// names the clause that failed, because "the recommendations were regenerated"
// is otherwise indistinguishable from "there were none".
//
// What this deliberately does NOT do is re-run recoverRelatedInsightIDs over
// the restored set. That function fail-open backfills a recommendation with no
// valid citations by attaching the whole eligible set, which here would paper
// over a broken translation and ship recommendations grounded in nothing. A
// refused checkpoint is the correct failure.
func restorableRecommendations(
	cp *models.RecommendationCheckpoint,
	eligible []models.Insight,
	liveByStored map[string]string,
	generation []string,
) (*models.RecommendationCheckpoint, bool) {
	if cp == nil {
		return nil, false
	}
	// First, and independently of every id: were these recommendations
	// reasoned over the analysis this attempt is holding? An id cannot answer
	// that — a model-supplied one survives re-analysis — so the generation
	// fingerprint is checked before anything that trusts an id.
	if !sameAnalysisGeneration(cp.AnalysisGeneration, generation) {
		applog.WithFields(applog.Fields{
			"stored": cp.AnalysisGeneration,
			"now":    generation,
		}).Info("not restoring this run's stored recommendations: they were reasoned over a different analysis of at least one area")
		return nil, false
	}
	if len(cp.EligibleInsightIDs) != len(eligible) {
		applog.WithFields(applog.Fields{
			"stored": len(cp.EligibleInsightIDs),
			"now":    len(eligible),
		}).Info("not restoring this run's stored recommendations: they were generated over a different number of insights, so they would describe findings this attempt does not have")
		return nil, false
	}
	live := make(map[string]bool, len(eligible))
	for i := range eligible {
		live[eligible[i].ID] = true
	}
	for _, stored := range cp.EligibleInsightIDs {
		fresh, ok := liveByStored[stored]
		if !ok || !live[fresh] {
			applog.WithField("insight", stored).Info("not restoring this run's stored recommendations: one of the insights they were generated over is not among this attempt's eligible insights")
			return nil, false
		}
	}

	// Translate every insight a recommendation names — its citations AND its
	// figure refs — before accepting the set, so a checkpoint is either wholly
	// usable or wholly refused. A deep copy, because the loaded document must
	// keep its own ids: a later attempt translates from the same baseline.
	recs, missing := translateRecommendationInsightIDs(cp.Step.Recommendations, liveByStored)
	if len(missing) > 0 {
		applog.WithFields(applog.Fields{
			"insight": missing[0],
			"count":   len(missing),
		}).Info("not restoring this run's stored recommendations: one names an insight this attempt does not have")
		return nil, false
	}
	// And every translated id has to land on an insight that is still
	// eligible, not merely on one this run produced.
	for i := range recs {
		for _, id := range recs[i].RelatedInsightIDs {
			if !live[id] {
				applog.WithFields(applog.Fields{
					"recommendation": recs[i].ID,
					"insight":        id,
				}).Info("not restoring this run's stored recommendations: one cites an insight that is no longer eligible")
				return nil, false
			}
		}
		for _, f := range recs[i].Figures {
			for _, ref := range f.Refs {
				if ref.Insight != "" && !live[ref.Insight] {
					applog.WithFields(applog.Fields{
						"recommendation": recs[i].ID,
						"insight":        ref.Insight,
					}).Info("not restoring this run's stored recommendations: one restates a figure from an insight that is no longer eligible")
					return nil, false
				}
			}
		}
	}

	out := *cp
	out.Step = cp.Step
	out.Step.Recommendations = recs
	applog.WithFields(applog.Fields{
		"recommendations":   len(recs),
		"already_validated": len(cp.RecVerdicts),
	}).Info("resume: restoring the recommendations a previous attempt generated — not re-generating them")
	return &out, true
}

// reidentifyRestoredRecommendations gives restored recommendations fresh ids
// and re-points everything that names them.
//
// The same hazard the analysis side has, with the same three consequences. A
// recommendation id keys the embedded recommendation, the standalone row in
// the `recommendations` collection and its Qdrant point, all written by the
// tail under a freshly minted discovery_id — so an id carried over from the
// attempt that generated it is an id that may ALREADY be in those
// collections, written by that attempt before it died.
//
// What makes that reachable is a death in or after Phase 8, which is the only
// thing that writes those rows: an attempt that finished its compute, saved,
// denormalised and inserted, and then died before its terminal claim — the
// window the API's startup sweep reaches routinely, since it marks in-flight
// runs failed without reaping their workloads.
//
// NOT the DISCOVERY_MAX_DURATION path, which an earlier version of this
// comment claimed. That path leaves computeErr non-nil, and Phase 8 is gated
// on computeErr == nil, so a deadline-killed attempt never reaches the
// standalone writes at all — it saves its partial result and stops. The
// remint is still required; the trigger is the post-Phase-8 death, not the
// deadline.
//
// Called AFTER Phase 5.5 returns, not at restore: the stored ids stay live for
// the whole of that phase, so this attempt's verdict markers key on the ids
// the checkpoint holds and a third attempt reads back one consistent set.
//
// Rewrites in place. The slice is this attempt's own copy, made by
// restorableRecommendations, and recStep.Recommendations shares its backing
// array — which is what keeps discovery_recommendation_log consistent with the
// result.
func reidentifyRestoredRecommendations(recs []models.Recommendation, results []models.ValidationResult) {
	freshByOld := make(map[string]string, len(recs))
	for i := range recs {
		old := recs[i].ID
		fresh := uuid.New().String()
		recs[i].ID = fresh
		if old != "" {
			freshByOld[old] = fresh
		}
		// Validation is a POINTER, so a restored recommendation shares the
		// struct with whatever produced it, and the verifier and refuter
		// verdicts inside each carry their own doc_id. Left alone, this
		// attempt would persist evidence naming a recommendation that is not
		// in its result.
		recs[i].Validation = reidentifyInsightValidation(recs[i].Validation, fresh)
	}
	for i := range results {
		fresh, ok := freshByOld[results[i].InsightID]
		if !ok {
			continue
		}
		results[i].InsightID = fresh
		results[i].Verifier = reidentifyVerdict(results[i].Verifier, fresh)
		results[i].Refuter = reidentifyVerdict(results[i].Refuter, fresh)
	}
}

// restoredRecommendationBudget totals what a restored checkpoint already spent
// of the run's per-recommendation validation cap. Only markers for
// recommendations this attempt still carries count: a marker for one the
// translation dropped would shrink a budget nothing is spending.
func restoredRecommendationBudget(cp *models.RecommendationCheckpoint) map[string]models.ValidationResult {
	if cp == nil || len(cp.RecVerdicts) == 0 {
		return nil
	}
	have := make(map[string]bool, len(cp.Step.Recommendations))
	for i := range cp.Step.Recommendations {
		have[cp.Step.Recommendations[i].ID] = true
	}
	out := make(map[string]models.ValidationResult, len(cp.RecVerdicts))
	for id, vr := range cp.RecVerdicts {
		if have[id] {
			out[id] = vr
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// recommendationsPendingValidation reports how many of a restored
// checkpoint's recommendations still need a verdict — the number the live
// panel shows, and the only thing this attempt will pay Phase 5.5 for.
func recommendationsPendingValidation(recs []models.Recommendation, done map[string]models.ValidationResult) int {
	pending := 0
	for i := range recs {
		if _, ok := done[recs[i].ID]; !ok {
			pending++
		}
	}
	return pending
}
