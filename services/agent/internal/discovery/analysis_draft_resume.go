package discovery

// Per-insight analysis resume: the draft written as an area finishes analysing
// and before it starts validating, the per-insight completion markers written
// as each verdict lands, and the restore that lets a resumed run finish an
// area instead of re-analysing it.
//
// A sibling of analysis_resume.go rather than part of it: that file is the
// story of an area that COMPLETED, and this is the story of one that did not.
//
// The two are read together and the order matters. A completed area's
// checkpoint describes the same work as its draft with the validation done, so
// a loader holding both must prefer the checkpoint — see loadAreaDrafts, which
// is the only place that rule lives.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// analysisDraftStore is the slice of
// *database.DiscoveryAnalysisDraftRepository the orchestrator calls. An
// interface so a unit test can inject a fake without MongoDB, and so a nil
// store cleanly disables per-insight resume.
type analysisDraftStore interface {
	SaveDraft(ctx context.Context, in database.AnalysisAreaDraftInput) error
	SaveInsightVerdict(ctx context.Context, in database.InsightVerdictInput) (bool, error)
	LoadDrafts(ctx context.Context, runID string, explorationAttempt int) (*database.AnalysisDraftSet, error)
	DeleteByArea(ctx context.Context, runID, areaID string) error
	DeleteByRun(ctx context.Context, runID string) (int64, error)
	TouchByRun(ctx context.Context, runID string) (int64, error)
}

// checkpointAreaDraft records an area that has been analysed and repaired but
// not yet validated, so a resumed run re-enters at its first unvalidated
// insight instead of paying for the whole area again.
//
// Written at the one point in an area's life where a crash costs the most for
// the least reason: the analysis call and the repair rounds are done, and the
// per-insight verifier/refuter calls — the largest number of LLM calls the
// area will make — are about to start.
//
// validationEnabled is the caller's answer to "can the phase this protects
// actually cost anything". With no validation agent the area goes from here to
// its completed checkpoint through two in-memory passes, so a draft would
// protect nothing and be superseded seconds later. This is the one gate that
// differs from the recommendation checkpoint's, which has a persistence tail
// behind it either way.
//
// Ownership is asked BEFORE writing, for the reason checkpointStep gives at
// length: the row-level attempt fence in SaveDraft only refuses a write where
// a higher-attempt row ALREADY exists, so a superseded attempt running ahead
// of the live one would otherwise plant a draft the live attempt has not
// reached and have it restored as though the live attempt had produced it.
//
// Returns ai.ErrAttemptSuperseded when this attempt has lost the run. Every
// other failure is logged and swallowed, like every checkpoint write: the cost
// is that the area is analysed again on a later resume, and a checkpoint must
// never be able to abort a working run.
//
// Also a PAUSE seam, under the uniform rule. A pause is free exactly where a
// crash would have left a resumable run, and this write is the clearest case
// of that in the whole area: once the draft is down, a stop costs nothing
// and saves them the area's entire per-insight validation bill — the largest
// number of LLM calls the area will make. Found before the write, taken after,
// so the pause never discards the analysis call that just paid for this row.
//
// The bool says whether a draft for this area is actually on disk, and it
// GATES the per-insight marker writes rather than merely informing them. A
// marker write is filtered on `attempt <= this attempt`, so with no draft of
// its own it can land on an EARLIER attempt's row for the same area — and
// since an insight id can be model-supplied rather than minted, a reused id
// would then overwrite a marker describing a different finding, which a later
// resume would restore as validated without ever verifying it. No draft, no
// markers.
func (o *Orchestrator) checkpointAreaDraft(ctx context.Context, step models.AnalysisStep, validationEnabled bool) (bool, error) {
	if o.analysisDraftRepo == nil || !validationEnabled {
		return false, nil
	}
	if o.explorationSummaryAttempt <= 0 {
		// No durable exploration summary, so this run re-explores on resume
		// and nothing analysed over the current exploration could be restored
		// against the next one. Writing a row would be writing one LoadDrafts
		// is obliged to ignore.
		return false, nil
	}
	if len(step.Insights) == 0 {
		// Nothing to validate, so nothing to resume. The area is moments from
		// its completed checkpoint.
		return false, nil
	}
	if step.AreaID == "" {
		applog.WithField("run_id", o.runID).Warn("analysis area drafted with no area id; not checkpointing it")
		return false, nil
	}
	// Standing rather than OwnsRun: one read answers both "is this still my
	// run" and "has an operator asked me to stop", and the pause half is
	// remembered rather than acted on so the write below still happens.
	pauseRequested := false
	if o.statusReporter != nil {
		switch o.statusReporter.Standing(ctx) {
		case standingLost:
			return false, ai.ErrAttemptSuperseded
		case standingPauseRequested:
			pauseRequested = true
		}
	}

	err := o.analysisDraftRepo.SaveDraft(ctx, database.AnalysisAreaDraftInput{
		ProjectID:          o.projectID,
		RunID:              o.runID,
		Attempt:            o.resume.attemptNumber(),
		ExplorationAttempt: o.explorationSummaryAttempt,
		Step:               step,
	})
	if err != nil {
		if errors.Is(err, database.ErrSupersededAttempt) {
			applog.WithFields(applog.Fields{
				"run_id": o.runID,
				"area":   step.AreaID,
			}).Warn("a newer attempt of this run owns this analysis area; stopping rather than spending its per-insight validation on a result that will be discarded")
			return false, ai.ErrAttemptSuperseded
		}
		applog.WithFields(applog.Fields{
			"run_id": o.runID,
			"area":   step.AreaID,
			"error":  err.Error(),
		}).Warn("failed to draft an analysed area; a resumed run would analyse and re-validate it in full")
		// False keeps the pause unhonoured too: with no draft on disk there is
		// nothing for a resume to re-enter, so stopping here would throw the
		// analysis call away. The request stands and the next seam with
		// something durable behind it takes it.
		return false, nil
	}
	// The draft is durable, so this is a free stop. SIGNALLED, not performed:
	// the caller is the only thing that knows this area has no feed rows out
	// yet, and it is the caller that must skip validation rather than start
	// per-insight calls it is about to abandon.
	if pauseRequested {
		return true, o.pauseSignalIfResumable("analysis area draft " + step.AreaID)
	}
	return true, nil
}

// checkpointInsightVerdict records that one insight has been validated.
//
// Called from validationPhase's bounded workers as each verdict lands, so it
// must stay safe to call concurrently — which is why the repository writes one
// field rather than replacing the document.
//
// insightID is the id the DRAFT holds, which is the id live in memory until
// the area is merged into the run's result. Every failure is swallowed: a lost
// marker costs one re-validated insight on a later resume.
//
// It is also the finest PAUSE seam in the run. Returns ai.ErrPauseRequested
// when this verdict landed and an operator's pause is standing — a signal to
// the validation phase, never a failure, and never a reason not to have
// written the row.
//
// So this seam DOES read standing once per verdict, and it is the only one
// that pays for a read of its own rather than reusing a probe it was making
// anyway. An earlier version of this comment declined exactly that, on the
// grounds that it would "add a Mongo read next to every verifier call to
// narrow a window the area-level gate already bounds". That reasoning was
// about SUPERSESSION, where the gate really does bound the window. It does not
// transfer to a pause: the window a pause cares about is the whole area, and
// the measured cost of not reading here was thirteen and a half minutes of
// already-durable work. One projected read on `_id` against a verifier and a
// refuter call is the right side of that trade.
//
// Supersession is still deliberately NOT asked. This runs inside a bounded
// worker, where an ErrAttemptSuperseded has nothing to abort, and the
// row-level attempt fence in SaveInsightVerdict already refuses a write a
// newer attempt has overtaken.
func (o *Orchestrator) checkpointInsightVerdict(ctx context.Context, areaID string, vr models.ValidationResult) error {
	if o.analysisDraftRepo == nil || areaID == "" || vr.InsightID == "" {
		return nil
	}
	if o.explorationSummaryAttempt <= 0 {
		return nil
	}
	// Read BEFORE the write, like every other seam, so the answer cannot be
	// influenced by how long the write took.
	pauseRequested := o.statusReporter != nil && o.statusReporter.Standing(ctx) == standingPauseRequested
	landed, err := o.analysisDraftRepo.SaveInsightVerdict(ctx, database.InsightVerdictInput{
		RunID:     o.runID,
		AreaID:    areaID,
		Attempt:   o.resume.attemptNumber(),
		InsightID: vr.InsightID,
		Verdict:   vr,
	})
	if err != nil {
		applog.WithFields(applog.Fields{
			"run_id":  o.runID,
			"area":    areaID,
			"insight": vr.InsightID,
			"error":   err.Error(),
		}).Warn("failed to record a validated insight; it would be validated again on a later resume")
		return nil
	}
	if !landed {
		// The write SUCCEEDED and matched nothing: the draft was swept by the
		// retention TTL, or deleted when the area completed, or the attempt
		// fence excluded it because a newer attempt owns the row. No error,
		// and no marker either — which is why the bool exists and why reading
		// `err == nil` as "the verdict is durable" was wrong. Pausing here
		// would stop the run on work it cannot restore, stranding exactly what
		// the pause was meant to preserve.
		applog.WithFields(applog.Fields{
			"run_id":  o.runID,
			"area":    areaID,
			"insight": vr.InsightID,
		}).Warn("no draft row took this insight verdict, so it is not durable; not pausing on it")
		return nil
	}
	if pauseRequested {
		return o.pauseSignalIfResumable("insight verdict in area " + areaID)
	}
	return nil
}

// loadAreaDrafts reads back the in-flight analysis areas this run may restore
// and finish.
//
// The gates are loadCompletedAreas', for the same three reasons, plus the one
// this function exists to apply: an area with a COMPLETED checkpoint is not a
// draft any more. The completed row describes the same analysis with its
// validation done, so restoring the draft instead would re-validate work
// already paid for — and, worse, hand Phase 5 an area whose insights carry no
// verdict.
//
// The draft is deleted when its area completes, so this usually finds nothing
// to drop. It is still the guarantee rather than the delete: that delete is
// best-effort, and a draft also outlives its area whenever the delete is lost
// to a crash between the two writes.
//
// A read failure costs money (the areas are analysed again), never
// correctness, so it is logged and swallowed.
func (o *Orchestrator) loadAreaDrafts(ctx context.Context, completed map[string]models.AnalysisAreaCheckpoint) map[string]models.AnalysisAreaDraft {
	if o.analysisDraftRepo == nil || !o.resume.active() || !o.resume.explorationComplete() {
		return nil
	}
	set, err := o.analysisDraftRepo.LoadDrafts(ctx, o.runID, o.explorationSummaryAttempt)
	if err != nil {
		applog.WithFields(applog.Fields{
			"run_id": o.runID,
			"error":  err.Error(),
		}).Warn("could not load this run's in-flight analysis areas; any area a previous attempt left unvalidated will be analysed again in full")
		return nil
	}
	if set.Len() == 0 {
		return nil
	}
	drafts := make(map[string]models.AnalysisAreaDraft, set.Len())
	superseded := 0
	for areaID, draft := range set.Drafts {
		if _, done := completed[areaID]; done {
			superseded++
			continue
		}
		drafts[areaID] = draft
	}
	if superseded > 0 {
		applog.WithFields(applog.Fields{
			"run_id": o.runID,
			"areas":  superseded,
		}).Debug("ignored analysis drafts whose areas have since completed")
	}
	if len(drafts) == 0 {
		return nil
	}
	// Reading them without rewriting them leaves their retention clock running
	// from the attempt that wrote them, exactly as the exploration prefix and
	// the area checkpoints do.
	o.refreshAreaDraftTTL(ctx)
	markers := 0
	for _, draft := range drafts {
		markers += len(draft.InsightVerdicts)
	}
	applog.WithFields(applog.Fields{
		"run_id":              o.runID,
		"areas":               len(drafts),
		"validated_insights":  markers,
		"from_attempt":        set.Attempt,
		"exploration_attempt": o.explorationSummaryAttempt,
	}).Info("resume: finishing analysis areas a previous attempt analysed but did not validate")
	return drafts
}

// restorableDrafts narrows the draft set to the areas THIS run is going to
// run, for the reason restorableAreas does it one rung up: the two sets can
// legitimately differ when the project's domain pack was edited between
// attempts or the resume used a narrower area selection, and a row the loop
// never reads must not reach anything else either.
func restorableDrafts(runAreas []AnalysisArea, drafts map[string]models.AnalysisAreaDraft) map[string]models.AnalysisAreaDraft {
	if len(drafts) == 0 {
		return nil
	}
	out := make(map[string]models.AnalysisAreaDraft, len(drafts))
	for _, area := range runAreas {
		if d, ok := drafts[area.ID]; ok {
			out[area.ID] = d
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// restoreAreaDraft hands the Phase 4 loop one area's draft: the step and
// insights as the previous attempt left them, and the verdicts it already
// paid for.
//
// The step is returned as stored, ids and all. A draft's insight ids are
// deliberately NOT reminted here — they stay live for the whole of the area's
// in-flight life, so this attempt's own verdict markers key on the same ids a
// third attempt would read back. The remint happens where the area is merged
// into the run's result, which is the point at which those ids would otherwise
// collide with what a previous attempt already persisted.
//
// A pure function, reporting whether there was a draft at all, so the caller
// reads as one decision rather than a branch over a map lookup.
func restoreAreaDraft(drafts map[string]models.AnalysisAreaDraft, areaID string) (models.AnalysisStep, []models.Insight, map[string]models.ValidationResult, bool) {
	draft, ok := drafts[areaID]
	if !ok {
		return models.AnalysisStep{}, nil, nil, false
	}
	applog.WithFields(applog.Fields{
		"area":              areaID,
		"insights":          len(draft.Step.Insights),
		"already_validated": len(draft.InsightVerdicts),
		"still_to_validate": len(draft.Step.Insights) - len(draft.InsightVerdicts),
	}).Info("Analysis area restored mid-validation from a previous attempt — not re-analysed")
	return draft.Step, draft.Step.Insights, draft.InsightVerdicts, true
}

// refreshAreaDraftTTL re-anchors the retention clock on a run's analysis
// drafts. Best-effort: losing it costs resumability on a later attempt, not
// this run.
func (o *Orchestrator) refreshAreaDraftTTL(ctx context.Context) {
	if o.analysisDraftRepo == nil || o.runID == "" {
		return
	}
	touched, err := o.analysisDraftRepo.TouchByRun(ctx, o.runID)
	if err != nil {
		applog.WithFields(applog.Fields{
			"run_id": o.runID,
			"error":  err.Error(),
		}).Warn("could not re-anchor the analysis draft retention clock; these rows may expire while this attempt is still running")
		return
	}
	applog.WithFields(applog.Fields{
		"run_id":  o.runID,
		"touched": touched,
	}).Debug("re-anchored the analysis draft retention clock for the resumed attempt")
}

// discardAreaDraft drops one area's draft because the area has finished and
// its completed checkpoint now describes the same work with its validation
// done.
//
// Best-effort by design, and safe to lose: loadAreaDrafts prefers the
// completed row over a draft for the same area, so a surviving draft is
// ignored rather than restored, and the TTL reclaims it.
func (o *Orchestrator) discardAreaDraft(ctx context.Context, areaID string) {
	if o.analysisDraftRepo == nil || o.runID == "" || areaID == "" {
		return
	}
	if err := o.analysisDraftRepo.DeleteByArea(ctx, o.runID, areaID); err != nil {
		applog.WithFields(applog.Fields{
			"run_id": o.runID,
			"area":   areaID,
			"error":  err.Error(),
		}).Debug("could not delete a finished area's draft; it is superseded by the area's checkpoint either way")
	}
}

// discardAreaDrafts deletes the run's analysis drafts, because the run has
// reached an outcome it cannot be resumed from. Best-effort with the TTL as
// the backstop, like its two counterparts.
func (o *Orchestrator) discardAreaDrafts(ctx context.Context, why string) {
	if o.analysisDraftRepo == nil || o.runID == "" {
		return
	}
	deleted, err := o.analysisDraftRepo.DeleteByRun(ctx, o.runID)
	if err != nil {
		applog.WithFields(applog.Fields{
			"run_id": o.runID,
			"reason": why,
			"error":  err.Error(),
		}).Warn("failed to delete analysis drafts; the retention TTL will reclaim them")
		return
	}
	applog.WithFields(applog.Fields{
		"run_id":  o.runID,
		"reason":  why,
		"deleted": deleted,
	}).Debug("deleted this run's analysis drafts")
}

// restoredDraftBudget totals what the restored drafts have already spent of
// the run's per-insight validation cap.
//
// Summed BEFORE the area loop, for the reason restoredValidationBudget is:
// the orchestrator builds its area list by ranging over a map of prompts, so
// the order is nondeterministic. An area this attempt still has to analyse
// from scratch can come first, be validated against a budget that has not yet
// absorbed a draft's prior spend, and push the run past the cap the project
// set — after which restoring the draft would top the counter up to a number
// that was already exceeded.
//
// Every marker counts exactly once, because a marker exists only for an
// insight actually submitted to the verifier.
func restoredDraftBudget(drafts map[string]models.AnalysisAreaDraft) int {
	total := 0
	for _, draft := range drafts {
		total += len(draft.InsightVerdicts)
	}
	return total
}

// storedInsightIDs maps a set of this attempt's insights onto the ids the
// run's analysis checkpoints hold for them.
//
// The whole of the recommendation restore gate rests on one invariant, which
// this function is: an insight's STORED id is the id carried by the checkpoint
// row that currently holds its area.
//
//   - An area this attempt analysed itself — freshly, or by finishing a draft
//     — is written to its completed checkpoint with the ids this attempt has,
//     so stored and live are the same id and the insight is absent from
//     storedByLive.
//   - An area restored from a completed checkpoint is never rewritten, so that
//     row still holds the previous attempt's ids, and storedByLive maps this
//     attempt's reminted id back to it.
//
// Because of that, the list is the same on every attempt of a run whose area
// checkpoints have not changed — which is what makes comparing it to a stored
// list a meaningful question rather than a coincidence.
//
// The second return is the inverse — stored id to live id — and it is
// deliberately NOT the whole of the first. It carries an entry only for an
// insight whose identity a CHECKPOINT vouches for, which is what makes it safe
// to use as a restore gate.
//
// The reason is that an insight id is not always server-minted. The analysis
// parser keeps a model-supplied id, so an area this attempt analysed itself
// can produce an id a previous attempt also used for a DIFFERENT finding —
// reachable whenever an area is re-analysed because its checkpoint expired or
// could not be read. Treating a matching id as proof of matching content would
// let a stored recommendation set be restored over findings it was never
// reasoned from, carrying validation verdicts another finding earned.
//
// So the first return names every eligible insight, because that list is what
// the next attempt will recognise once this attempt's own checkpoints hold
// them; the second admits only the ones a checkpoint already holds. An attempt
// that re-analysed an area therefore cannot satisfy the gate on it, and loses
// a saving rather than correctness.
func storedInsightIDs(insights []models.Insight, storedByLive map[string]string) ([]string, map[string]string) {
	ids := make([]string, 0, len(insights))
	liveByStored := make(map[string]string, len(insights))
	ambiguous := make(map[string]bool)
	for i := range insights {
		live := insights[i].ID
		if live == "" {
			continue
		}
		stored, checkpointed := storedByLive[live]
		if !checkpointed || stored == "" {
			// Analysed by this attempt: its live id is what its own
			// checkpoint will hold, so it belongs in the list — but nothing
			// yet vouches for it, so it stays out of the gate's mapping.
			ids = append(ids, live)
			continue
		}
		ids = append(ids, stored)
		if _, seen := liveByStored[stored]; seen || ambiguous[stored] {
			// Two eligible insights under one stored id. Separate areas can
			// return the same model-supplied id, and restoring them mints two
			// distinct live ids — so a single mapping entry would translate a
			// citation to whichever of them happened to be written last.
			// Vouching for neither is the only answer that cannot mistranslate.
			delete(liveByStored, stored)
			ambiguous[stored] = true
			continue
		}
		liveByStored[stored] = live
	}
	return ids, liveByStored
}

// analysisGenerationFingerprint identifies the analysis a set of eligible
// insights came from, independently of the ids those insights carry.
//
// It exists because an insight id cannot carry that meaning. The ids are the
// only handle a stored recommendation has on its insights, but a model-supplied
// id survives re-analysis, so "the same ids are eligible" does not imply "the
// same findings are eligible" — and the gap is reachable in both directions:
// an area re-analysed on THIS attempt (whose checkpoint expired or could not
// be read) may hand back ids a previous attempt used for different content,
// and once that re-analysis has replaced the area's checkpoint, a LATER
// attempt restoring it sees a checkpoint-backed id that matches a
// recommendation set reasoned over the content it replaced.
//
// Each contributing area's analysis RunAt is what distinguishes one generation
// from another: it is stamped when the analysis step is built, carried verbatim
// through a restore, and freshly set by a re-analysis. Rendered as a sorted
// list of "area@millis" rather than a map so the stored shape needs no
// assumption about area ids being legal field names, and truncated to
// milliseconds because that is the precision BSON keeps — without it an
// attempt's in-memory value would never match the value the next attempt reads
// back.
func analysisGenerationFingerprint(eligible []models.Insight, analysisLog []models.AnalysisStep) []string {
	runAt := make(map[string]time.Time, len(analysisLog))
	for i := range analysisLog {
		runAt[analysisLog[i].AreaID] = analysisLog[i].RunAt
	}
	seen := make(map[string]bool, len(eligible))
	out := make([]string, 0, len(eligible))
	for i := range eligible {
		area := eligible[i].AnalysisArea
		if seen[area] {
			continue
		}
		seen[area] = true
		out = append(out, fmt.Sprintf("%s@%d", area, runAt[area].Truncate(time.Millisecond).UnixMilli()))
	}
	sort.Strings(out)
	return out
}

// sameAnalysisGeneration compares two fingerprints. Both are sorted, so this
// is an element-wise comparison.
func sameAnalysisGeneration(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
