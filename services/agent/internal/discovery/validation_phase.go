package discovery

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation/verifier"
)

// validationPhase encapsulates the orchestrator's Phase 4.5 (insight
// validation) and Phase 5.5 (recommendation validation). Lives in its
// own file so orchestrator.go stays scoped to the discovery state
// machine; tests under verifier_phase_test.go exercise the helpers
// here with stub agents.
type validationPhase struct {
	agent    *verifier.Agent
	cfg      verifier.Config
	caps     verifier.RunCaps
	wh       verifier.WarehouseInfo
	disc     verifier.DiscoveryContext
	executor verifier.Executor

	// Multi-warehouse validation routing. On a multi-warehouse run these
	// hold one WarehouseInfo + Executor per datasource id, so each insight
	// / recommendation is verified against the datasource it is ABOUT
	// (resolved from the warehouse_ids of the exploration steps it cites)
	// — not always the primary. Empty on a single-warehouse run, where
	// every doc falls back to wh / executor above. primaryDS is the
	// fallback datasource id.
	whByDS       map[string]verifier.WarehouseInfo
	executorByDS map[string]verifier.Executor
	primaryDS    string

	// onRequest marks the phase as serving ONE document an operator asked
	// for, outside any run (see ValidateSingleDoc). It switches off the
	// duplicate skip below: the skip exists so a run's budget is not spent on
	// restatements, and an operator clicking Validate on a finding the
	// dashboard shows as restated has already decided to spend it. Without
	// this the request was a permanent no-op that overwrote whatever verdict
	// the document had with the skip stamp — and the dashboard offers the
	// button for exactly those documents.
	onRequest bool
}

// perItemCheckpoint is how a run makes one document's validation verdict
// durable as it lands, and skips the ones a previous attempt already paid for.
//
// Nil on a deployment with no checkpoint store, which is what keeps the
// validation phase on exactly the path it always took.
type perItemCheckpoint struct {
	// done holds the verdicts a previous attempt produced, by document id as
	// the checkpoint holds it. A document found here is not re-checked: its
	// verdict is restored verbatim, because it describes the same document
	// (the draft it came from is immutable) and re-deriving it would be two
	// LLM calls for an answer already paid for.
	done map[string]models.ValidationResult

	// save persists one freshly produced verdict. Called from the bounded
	// worker that produced it, so it must be safe to call concurrently.
	//
	// Returns ai.ErrPauseRequested when the verdict landed AND an operator's
	// pause is standing — the finest-grained application of the uniform pause rule. It is a signal, not
	// a failure: this verdict is durable either way, and the phase decides
	// what to do about it.
	save func(vr models.ValidationResult) error

	// pauseSeen is set when a save reported a standing pause. Atomic because
	// the workers that set it run concurrently, and read by the dispatch loop
	// to stop queueing documents this attempt is not going to pay for.
	pauseSeen atomic.Bool
}

// restored returns the verdict a previous attempt produced for a document, if
// any. Safe on nil.
func (c *perItemCheckpoint) restored(docID string) (models.ValidationResult, bool) {
	if c == nil || docID == "" {
		return models.ValidationResult{}, false
	}
	vr, ok := c.done[docID]
	return vr, ok
}

// record persists a verdict this attempt produced. Safe on nil.
//
// A pause reported by the seam is remembered rather than returned: this runs
// inside a bounded worker, where there is nothing to return an error TO. The
// dispatch loop reads it and stops queueing; the workers already in flight
// finish and bank their own verdicts, because a verdict half-paid-for is the
// one thing a pause must never create.
func (c *perItemCheckpoint) record(vr models.ValidationResult) {
	if c == nil || c.save == nil {
		return
	}
	if errors.Is(c.save(vr), ai.ErrPauseRequested) {
		c.pauseSeen.Store(true)
	}
}

// pauseRequested reports whether any worker has seen a standing pause behind a
// landed verdict. Safe on nil.
func (c *perItemCheckpoint) pauseRequested() bool {
	return c != nil && c.pauseSeen.Load()
}

// insightValidationFrom renders the Validation an insight or recommendation
// carries from the ValidationResult the phase produced for it.
//
// Extracted so the live path and the restore path cannot drift: the
// ValidationResult is what gets checkpointed, so a second construction of this
// struct somewhere else would be a second definition of what a stored verdict
// means. Every field is derivable from vr, including Reasoning, which lives on
// the verifier verdict inside it.
//
// The legacy Status / Reasoning pair is backfilled for insights only, which is
// not a style choice: insight validations have always carried it for consumers
// still reading the old shape, and recommendation validations never have.
// Deciding it from vr.DocKind rather than from a parameter keeps that
// difference a property of the verdict, so a restored verdict renders exactly
// as the live one did.
func insightValidationFrom(vr models.ValidationResult) *valmodels.InsightValidation {
	out := &valmodels.InsightValidation{
		WarehouseID:     vr.WarehouseID,
		Verifier:        vr.Verifier,
		Refuter:         vr.Refuter,
		Combined:        vr.Combined,
		RefuterDisabled: vr.RefuterDisabled,
		ValidatedAt:     vr.ValidatedAt,
		InputTokens:     vr.InputTokens,
		OutputTokens:    vr.OutputTokens,
	}
	if vr.DocKind == valmodels.DocInsight {
		out.Status = string(vr.Combined)
		if vr.Verifier != nil {
			out.Reasoning = vr.Verifier.OverallReason
		}
	}
	return out
}

// forDatasource returns the WarehouseInfo + Executor for a datasource id,
// falling back to the primary wh / executor when the run is single-warehouse
// or the id has no per-datasource wiring.
func (p *validationPhase) forDatasource(dsID string) (verifier.WarehouseInfo, verifier.Executor) {
	if ex, ok := p.executorByDS[dsID]; ok {
		if wh, ok2 := p.whByDS[dsID]; ok2 {
			return wh, ex
		}
	}
	return p.wh, p.executor
}

// stampDatasource returns the datasource id to persist on a validation
// result — but only on a multi-warehouse run, so single-warehouse results
// stay unlabeled (warehouse_id omitted).
func (p *validationPhase) stampDatasource(dsID string) string {
	if len(p.executorByDS) == 0 {
		return ""
	}
	return dsID
}

// datasourceForSteps resolves the datasource a doc is primarily about from
// the warehouse_ids of the exploration steps it cites: the most-cited
// datasource that has per-datasource wiring (ties broken lexicographically),
// or primaryDS when the steps carry none. Single-warehouse runs (no
// executorByDS) always resolve to primaryDS and route through the fallback.
func (p *validationPhase) datasourceForSteps(steps []int, stepByID map[int]*models.ExplorationStep) string {
	if len(p.executorByDS) == 0 {
		return p.primaryDS
	}
	counts := make(map[string]int, len(p.executorByDS))
	for _, s := range steps {
		st := stepByID[s]
		if st == nil || st.WarehouseID == "" {
			continue
		}
		if _, ok := p.executorByDS[st.WarehouseID]; ok {
			counts[st.WarehouseID]++
		}
	}
	ids := make([]string, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	best, bestN := p.primaryDS, 0
	for _, id := range ids {
		if counts[id] > bestN {
			best, bestN = id, counts[id]
		}
	}
	return best
}

// validateInsights walks the area's insights in `affected_count`-desc
// order. For each, it builds a bundle from `stepByID`, runs verifier
// + (optionally) refuter, combines, stamps Validation on the insight
// in place, and appends a ValidationResult to `out`. Insights past
// the per-run cap get Combined=skipped_budget_cap; if the agent is
// nil (no aiClient or no schemaProvider), Combined=validation_disabled.
//
// An insight the mid-run consolidation pass marked as a restatement gets
// Combined=skipped_duplicate and is NOT charged against `runValidated` — the
// whole point of consolidating before validation is that the cap is spent on
// the run's distinct findings rather than on its restatements. See
// consolidate_phase.go.
//
// `runValidated` tracks how many insights have been validated so far
// across all areas; the caller must thread it through area loops, and must
// have already added every RESTORED marker of every area it is going to
// restore — see restoredDraftBudget. A restored marker found in cp.done is
// therefore stamped without being charged again.
//
// Returns the new runValidated count (so callers can keep their
// running total in sync).
func (p *validationPhase) validateInsights(ctx context.Context, insights []models.Insight, stepByID map[int]*models.ExplorationStep, areaID string, runValidated int, cp *perItemCheckpoint) ([]models.ValidationResult, int, bool) {
	results := make([]models.ValidationResult, 0, len(insights))
	if p.agent == nil {
		// Validation is disabled (project setting off, or no aiClient
		// wired). Stamp every insight with combined=validation_disabled
		// AND backfill the legacy Status field so consumers reading the
		// legacy shape still see the verdict.
		now := time.Now()
		for i := range insights {
			insights[i].Validation = &valmodels.InsightValidation{
				Status:      string(valmodels.StatusValidationDisabled),
				Combined:    valmodels.StatusValidationDisabled,
				ValidatedAt: now,
			}
		}
		// No agent, so no verdicts and no seam to pause at.
		return results, runValidated, false
	}

	// Order by affected_count desc so the run-cap hits the least-
	// important docs last. The cap is decided here, in order, before any
	// document is checked; the checks then run with bounded concurrency
	// (RunCaps.Concurrency) and write into their own result slot, so the
	// result order and which documents are skipped never depend on timing.
	order := indicesByAffectedDesc(insights)
	results = make([]models.ValidationResult, len(order))
	var jobs []docJob
	for pos, idx := range order {
		ins := &insights[idx]
		vr := models.ValidationResult{
			InsightID:     ins.ID,
			AnalysisArea:  areaID,
			ClaimedCount:  ins.AffectedCount,
			ClaimedMetric: ins.Name, // display label for the run-log step
			ValidatedAt:   time.Now(),
			DocKind:       valmodels.DocInsight,
		}
		// Already validated on a previous attempt of this run. Restored
		// rather than re-derived, and NOT charged here: runValidated arrives
		// already carrying every restored marker of every area this run is
		// going to restore. See restoredDraftBudget for why that has to
		// happen before the area loop rather than per area.
		if prior, ok := cp.restored(ins.ID); ok {
			results[pos] = prior
			ins.Validation = insightValidationFrom(prior)
			continue
		}
		// Consolidated into another of this run's findings before validation
		// started. Stamped and skipped, and deliberately NOT charged against
		// runValidated: the whole point of consolidating first is that the cap
		// is spent on the run's distinct findings rather than on its
		// restatements. Measured on six runs, nearly a quarter of the checks a
		// run paid for landed on insights the same run then marked duplicate.
		//
		// Its own status rather than skipped_budget_cap, so the stored verdict
		// says which of the two happened — "there was nothing here the
		// canonical does not already carry" is not "the run ran out of budget".
		if ins.DuplicateOf != "" && !p.onRequest {
			ins.Validation = &valmodels.InsightValidation{
				Combined:    valmodels.StatusSkippedDuplicate,
				ValidatedAt: vr.ValidatedAt,
			}
			vr.Combined = valmodels.StatusSkippedDuplicate
			vr.Status = string(valmodels.StatusSkippedDuplicate)
			results[pos] = vr
			continue
		}
		if runValidated >= p.caps.MaxInsightsPerRun {
			ins.Validation = &valmodels.InsightValidation{
				Combined:    valmodels.StatusSkippedBudgetCap,
				ValidatedAt: vr.ValidatedAt,
			}
			vr.Combined = valmodels.StatusSkippedBudgetCap
			vr.Status = string(valmodels.StatusSkippedBudgetCap)
			results[pos] = vr
			continue
		}

		// Verify against the datasource this insight is about (multi-
		// warehouse); single-warehouse resolves to the primary fallback.
		dsID := p.datasourceForSteps(ins.SourceSteps, stepByID)
		wh, ex := p.forDatasource(dsID)
		vr.WarehouseID = p.stampDatasource(dsID)
		results[pos] = vr
		jobs = append(jobs, docJob{pos: pos, idx: idx, wh: wh, ex: ex})
		runValidated++
	}

	dispatched := p.runBounded(len(jobs), func(j int) {
		job := jobs[j]
		ins := &insights[job.idx]
		vr := &results[job.pos]
		bundle := verifier.BuildInsightBundle(ins, stepByID, job.wh, p.disc, p.cfg.Bundle)
		d := p.checkDoc(ctx, bundle, job.ex)
		d.stamp(vr)
		ins.Validation = insightValidationFrom(*vr)
		// Durable before the next insight starts, so a crash costs this one
		// verdict rather than the whole area's. Called from the worker, which
		// is why the store writes one field instead of the document.
		cp.record(*vr)
	}, cp.pauseRequested)

	// Stopped short of the queue, so the operator's pause is honoured here.
	// Every document that ran banked its verdict and every one that did not
	// has no marker at all, which is exactly the state a crash at this point
	// would leave — so the resumed attempt re-enters at the first insight
	// without a verdict and re-analyses nothing.
	// Two separate questions, and conflating them dropped the pause whenever
	// the queue happened to drain: `stoppedShort` is "is there anything to
	// trim", `paused` is "must the caller stop". A pause reported by the LAST
	// verdict — or seen only after every job had been dispatched — leaves
	// nothing to trim and still has to be honoured, or the run sails on into
	// the save with the operator's request silently dropped.
	stoppedShort := dispatched < len(jobs)
	paused := stoppedShort || cp.pauseRequested()
	if stoppedShort {
		// Drop the slots whose check never ran. They hold a pre-filled
		// ValidationResult with no verdict stamped, and persisting one would
		// read downstream as a document that WAS checked and came back empty.
		// Only queued-but-undispatched slots are dropped: a slot filled by a
		// restored verdict or by the budget cap is already final.
		undispatched := make(map[int]bool, len(jobs)-dispatched)
		for j := dispatched; j < len(jobs); j++ {
			undispatched[jobs[j].pos] = true
		}
		kept := make([]models.ValidationResult, 0, dispatched)
		for pos := range results {
			if !undispatched[pos] {
				kept = append(kept, results[pos])
			}
		}
		results = kept
		// Give back the budget the undispatched jobs reserved. runValidated is
		// charged when a document is QUEUED, not when its check returns —
		// which is right while the queue always drains, and wrong the moment a
		// pause can stop it. Left inflated, the count would claim this attempt
		// spent validations it never made, and a caller that carried on rather
		// than pausing would hand the remaining areas a budget that is too
		// small.
		runValidated -= len(jobs) - dispatched
		applog.WithFields(applog.Fields{
			"area": areaID, "validated": dispatched, "queued": len(jobs),
			"dropped": len(undispatched), "budget_returned": len(jobs) - dispatched,
		}).Info("validation: an operator's pause was honoured mid-area; the verdicts already paid for are on disk")
	}
	applog.WithFields(applog.Fields{
		"area":     areaID,
		"insights": len(insights),
		"verdicts": len(results),
	}).Info("Insight validation complete")
	return results, runValidated, paused
}

// validateRecommendations runs Phase 5.5 — same shape as
// validateInsights but for recommendations, which build their bundles
// from the union of their related insights' source steps.
func (p *validationPhase) validateRecommendations(ctx context.Context, recommendations []models.Recommendation, allInsights []models.Insight, stepByID map[int]*models.ExplorationStep, cp *perItemCheckpoint) ([]models.ValidationResult, bool) {
	results := make([]models.ValidationResult, 0, len(recommendations))
	if p.agent == nil {
		// Same legacy-Status backfill as the insight nil-agent branch.
		now := time.Now()
		for i := range recommendations {
			recommendations[i].Validation = &valmodels.InsightValidation{
				Status:      string(valmodels.StatusValidationDisabled),
				Combined:    valmodels.StatusValidationDisabled,
				ValidatedAt: now,
			}
		}
		return results, false
	}
	insightByID := make(map[string]*models.Insight, len(allInsights))
	for i := range allInsights {
		insightByID[allInsights[i].ID] = &allInsights[i]
	}

	// Same shape as validateInsights: the cap is decided in order first,
	// then the checks run with bounded concurrency into their own slots.
	results = make([]models.ValidationResult, len(recommendations))
	var jobs []docJob

	// Charge every restored verdict BEFORE the cap is applied to anything,
	// which is the whole correctness of the cap under a resume. Counting a
	// restored verdict only when the loop reaches it lets the documents ahead
	// of it consume cap slots first: with one verdict restored for the third
	// recommendation and the cap lowered to two between attempts, the first
	// two would be queued and the third then charged, validating three
	// documents against a cap of two.
	validated := 0
	for i := range recommendations {
		if _, ok := cp.restored(recommendations[i].ID); ok {
			validated++
		}
	}

	for i := range recommendations {
		rec := &recommendations[i]
		vr := models.ValidationResult{
			InsightID:     rec.ID,
			ClaimedMetric: rec.Title, // display label for the run-log step
			ValidatedAt:   time.Now(),
			DocKind:       valmodels.DocRecommendation,
		}
		// Already validated on a previous attempt. Restored rather than
		// re-derived, and NOT charged here: its slice of the cap was counted
		// above, before any document was selected.
		if prior, ok := cp.restored(rec.ID); ok {
			results[i] = prior
			rec.Validation = insightValidationFrom(prior)
			continue
		}
		if validated >= p.caps.MaxRecommendationsPerRun {
			rec.Validation = &valmodels.InsightValidation{
				Combined:    valmodels.StatusSkippedBudgetCap,
				ValidatedAt: vr.ValidatedAt,
			}
			vr.Combined = valmodels.StatusSkippedBudgetCap
			vr.Status = string(valmodels.StatusSkippedBudgetCap)
			results[i] = vr
			continue
		}
		// Verify against the datasource the recommendation is about — the
		// dominant datasource across its related insights' cited steps.
		recSteps := make([]int, 0)
		for _, iid := range rec.RelatedInsightIDs {
			if ins, ok := insightByID[iid]; ok {
				recSteps = append(recSteps, ins.SourceSteps...)
			}
		}
		recDS := p.datasourceForSteps(recSteps, stepByID)
		wh, ex := p.forDatasource(recDS)
		vr.WarehouseID = p.stampDatasource(recDS)
		results[i] = vr
		jobs = append(jobs, docJob{pos: i, idx: i, wh: wh, ex: ex})
		validated++
	}

	dispatched := p.runBounded(len(jobs), func(j int) {
		job := jobs[j]
		rec := &recommendations[job.idx]
		vr := &results[job.pos]
		bundle := verifier.BuildRecommendationBundle(rec, insightByID, stepByID, job.wh, p.disc, p.cfg.Bundle)
		d := p.checkDoc(ctx, bundle, job.ex)
		d.stamp(vr)
		rec.Validation = insightValidationFrom(*vr)
		cp.record(*vr)
	}, cp.pauseRequested)

	// Same per-item pause seam as the insight half: the recommendations
	// and the verdicts already produced are both on disk, so a resumed attempt
	// regenerates nothing and re-validates only what is left.
	stoppedShort := dispatched < len(jobs)
	paused := stoppedShort || cp.pauseRequested()
	if stoppedShort {
		undispatched := make(map[int]bool, len(jobs)-dispatched)
		for j := dispatched; j < len(jobs); j++ {
			undispatched[jobs[j].pos] = true
		}
		kept := make([]models.ValidationResult, 0, dispatched)
		for pos := range results {
			if !undispatched[pos] {
				kept = append(kept, results[pos])
			}
		}
		results = kept
		applog.WithFields(applog.Fields{
			"validated": dispatched, "queued": len(jobs), "dropped": len(undispatched),
		}).Info("validation: an operator's pause was honoured mid-Phase-5.5; the verdicts already paid for are on disk")
	}
	applog.WithField("recommendations", len(recommendations)).Info("Recommendation validation complete")
	return results, paused
}

// docJob is one document queued for checking: its result slot, its index
// in the caller's slice, and the datasource it is verified against.
type docJob struct {
	pos, idx int
	wh       verifier.WarehouseInfo
	ex       verifier.Executor
}

// docVerdict is the outcome of checking one document.
type docVerdict struct {
	verifier        valmodels.StructuredVerdict
	refuter         *valmodels.StructuredVerdict
	combined        valmodels.Status
	refuterDisabled bool
}

// checkDoc runs the verifier and, when enabled, the refuter on one bundle
// and combines their verdicts. Safe to call concurrently: the agent, the
// executors and the step snapshot are read-only for the whole phase.
func (p *validationPhase) checkDoc(ctx context.Context, bundle verifier.Bundle, ex verifier.Executor) docVerdict {
	var d docVerdict
	d.verifier, _ = p.agent.Verify(ctx, bundle, ex)
	if p.cfg.RefuterEnabled {
		rb := bundle
		if len(d.verifier.ClaimsConsidered) > 0 {
			rb.PriorClaims = append([]string(nil), d.verifier.ClaimsConsidered...)
		}
		r, _ := p.agent.Refute(ctx, rb, ex)
		d.refuter = &r
	}
	d.combined, d.refuterDisabled = valmodels.Combine(&d.verifier, d.refuter, !p.cfg.RefuterEnabled)
	return d
}

// stamp writes the verdicts, token counts and combined status into vr.
func (d *docVerdict) stamp(vr *models.ValidationResult) {
	v := d.verifier
	vr.Verifier = &v
	vr.InputTokens += v.LLMTokensIn
	vr.OutputTokens += v.LLMTokensOut
	if d.refuter != nil {
		vr.Refuter = d.refuter
		vr.InputTokens += d.refuter.LLMTokensIn
		vr.OutputTokens += d.refuter.LLMTokensOut
	}
	vr.Combined = d.combined
	vr.RefuterDisabled = d.refuterDisabled
	vr.Status = string(d.combined)
}

// runBounded calls fn(0..n-1) with at most RunCaps.Concurrency calls in
// flight (sequentially when it is 1 or less — the default), and reports how
// many calls it actually made.
//
// `stop` is consulted BEFORE each dispatch and nowhere else. That placement is
// the whole contract: a document is either never started or runs to completion
// and banks its verdict, so a cooperative stop can never leave a half-paid
// check. Work already in flight is always waited for.
func (p *validationPhase) runBounded(n int, fn func(i int), stop func() bool) int {
	dispatched := 0
	limit := p.caps.Concurrency
	if limit <= 1 || n <= 1 {
		for i := 0; i < n; i++ {
			if stop != nil && stop() {
				return dispatched
			}
			fn(i)
			dispatched++
		}
		return dispatched
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		if stop != nil && stop() {
			break
		}
		sem <- struct{}{}
		// Asked AGAIN, after the wait for a slot. That wait is precisely when
		// a worker is most likely to report the pause — it blocks until one
		// finishes, and finishing is what banks a verdict and reads standing.
		// Checking only before the wait would spend one more whole
		// verifier+refuter pair on a stop that had already been signalled.
		if stop != nil && stop() {
			<-sem
			break
		}
		wg.Add(1)
		dispatched++
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}(i)
	}
	wg.Wait()
	return dispatched
}

// indicesByAffectedDesc returns indices into the insights slice
// sorted by AffectedCount descending. Used so the run-cap drops the
// least-prominent insights first.
func indicesByAffectedDesc(insights []models.Insight) []int {
	idx := make([]int, len(insights))
	for i := range insights {
		idx[i] = i
	}
	sort.Slice(idx, func(i, j int) bool {
		return insights[idx[i]].AffectedCount > insights[idx[j]].AffectedCount
	})
	return idx
}

// filterEligibleInsights returns the subset of insights whose Combined
// verdict is in the per-project eligibility set — the recommender input
// filter from plan §"Recommendation policy", made configurable via the
// per-project recommendation_verdicts setting (Settings → Advanced). The
// caller passes o.recommendationVerdicts, which defaults to
// {confirmed, supported} (today's IsTerminalPositive) when the project has
// not configured its own, so unset projects are byte-identical.
//
// Two states always fail-open regardless of the set: an insight with no
// Validation (validationPhase ran but emitted nothing for it, or no agent
// was wired) and Combined == validation_disabled (validation turned off).
// Both mean "validation didn't produce a verdict", not "operator excluded
// this verdict" — failing-open keeps the recommender from starving.
//
// A consolidated duplicate is excluded on the MARK, before either fail-open
// applies, and not on its verdict. The two facts are independent, and that
// independence is load-bearing in both directions. A marked duplicate whose
// verdict was suppressed would otherwise reach Phase 5 MORE easily than an
// unmarked insight, because a nil verdict fails open — so filtering on the
// verdict alone would make consolidating a finding promote it. And a
// draft-restored area's insight can arrive carrying a `confirmed` verdict from
// the previous attempt and be marked a duplicate on this one, which the verdict
// says nothing about.
func filterEligibleInsights(all []models.Insight, eligible map[valmodels.Status]bool) []models.Insight {
	out := make([]models.Insight, 0, len(all))
	for i := range all {
		if all[i].DuplicateOf != "" {
			continue
		}
		v := all[i].Validation
		if v == nil {
			out = append(out, all[i])
			continue
		}
		if v.Combined == valmodels.StatusValidationDisabled || eligible[v.Combined] {
			out = append(out, all[i])
		}
	}
	return out
}

// RecommendationDropStats counts the recommendations dropped by
// validateRelatedInsightIDs, broken down by reason. The orchestrator
// stamps this onto the per-run RecommendationStep so the dashboard /
// API can surface how many recs were silently discarded and which
// failure mode dominated — important for measuring LLM regression
// rates (e.g. some providers emit category:severity:theme slugs
// instead of UUIDs, which all fall into UnknownOrIneligibleID).
//
// MissingIDs counts recs whose related_insight_ids array was empty
// or absent. UnknownOrIneligibleID counts recs whose array cited at
// least one id not present in the eligible insight set (after the
// {supported, confirmed} filter). The two reasons are mutually
// exclusive per recommendation; Total equals their sum.
type RecommendationDropStats struct {
	Total                 int
	MissingIDs            int
	UnknownOrIneligibleID int
}

// RecommendationCitationStats summarizes how recoverRelatedInsightIDs
// repaired recommendations whose related_insight_ids were missing or
// hallucinated — instead of dropping them (the old server-side discipline,
// which zeroed the run's recommendations on small/open models that omit the
// field). Both counters are zero for a model that cites correctly, so a big
// model's output is untouched.
type RecommendationCitationStats struct {
	Salvaged   int // recs that cited ≥1 bad id (trimmed) but kept ≥1 valid id
	Backfilled int // recs with zero valid citations, fail-open linked to eligible insights
}

// recoverRelatedInsightIDs repairs each recommendation's related_insight_ids
// against the eligible insight set instead of discarding recs. For each rec:
//
//   - valid cited ids are kept and hallucinated / ineligible ones are dropped
//     from the list (salvage) — a rec that cites 2 real + 1 bogus id survives
//     with the 2 real ids;
//   - a rec left with zero valid citations is fail-open linked to every
//     eligible insight id (backfill), so a grounded recommendation is never
//     lost just because a small model omitted or slug-ified the field.
//
// It never drops a recommendation. The follow-on validateRelatedInsightIDs
// stays as a defensive guard (it now finds every rec already valid, so it
// drops nothing). A big model that cites correctly is returned byte-identical
// with zero stats. Backfill attaches the full eligible set because the true
// per-rec provenance is unknown once the model failed to cite it; Phase 5.5
// recommendation validation still budgets the resulting source steps.
func recoverRelatedInsightIDs(recs []models.Recommendation, eligible []models.Insight) ([]models.Recommendation, RecommendationCitationStats) {
	eligibleSet := make(map[string]struct{}, len(eligible))
	allIDs := make([]string, 0, len(eligible))
	for _, ins := range eligible {
		if ins.ID == "" {
			continue
		}
		if _, dup := eligibleSet[ins.ID]; dup {
			continue
		}
		eligibleSet[ins.ID] = struct{}{}
		allIDs = append(allIDs, ins.ID)
	}

	var stats RecommendationCitationStats
	out := make([]models.Recommendation, 0, len(recs))
	for _, rec := range recs {
		valid := make([]string, 0, len(rec.RelatedInsightIDs))
		bad := make([]string, 0)
		for _, id := range rec.RelatedInsightIDs {
			if _, ok := eligibleSet[id]; ok {
				valid = append(valid, id)
			} else {
				bad = append(bad, id)
			}
		}
		switch {
		case len(valid) > 0:
			if len(bad) > 0 {
				applog.WithFields(applog.Fields{
					"recommendation_id": rec.ID,
					"title":             rec.Title,
					"dropped_ids":       bad,
					"kept":              len(valid),
				}).Info("Salvaged recommendation citations: trimmed ineligible ids, kept valid ones")
				stats.Salvaged++
			}
			rec.RelatedInsightIDs = valid
		case len(allIDs) > 0:
			// Zero valid citations → fail-open backfill (never drop). The rec
			// content is usable; ground it in the run's eligible insight set.
			applog.WithFields(applog.Fields{
				"recommendation_id": rec.ID,
				"title":             rec.Title,
				"cited":             rec.RelatedInsightIDs,
				"backfilled":        len(allIDs),
			}).Warn("Backfilling recommendation citations: model cited no eligible insight")
			rec.RelatedInsightIDs = append([]string(nil), allIDs...)
			stats.Backfilled++
		default:
			// No eligible insights at all (unreachable — Phase 5 skips when the
			// eligible set is empty). Leave the rec's ids as-is.
			rec.RelatedInsightIDs = valid
		}
		out = append(out, rec)
	}
	return out, stats
}

// validateRelatedInsightIDs drops recommendations whose
// related_insight_ids reference unknown / ineligible insights, or
// whose list is empty (the discipline rule requires every rec to
// cite at least one insight). Returns the kept recommendations and a
// RecommendationDropStats summarizing how many were dropped and why.
// A warning is logged per dropped recommendation so the agent log
// retains the offending ids for post-mortem analysis.
//
// As of #347 this runs AFTER recoverRelatedInsightIDs, which salvages /
// backfills citations, so in the normal path every rec is already valid and
// nothing is dropped here — it remains as defense-in-depth (a bug in recovery,
// or a future caller that skips it, still cannot persist an ungrounded rec).
func validateRelatedInsightIDs(recs []models.Recommendation, eligible []models.Insight) ([]models.Recommendation, RecommendationDropStats) {
	eligibleSet := make(map[string]struct{}, len(eligible))
	for _, ins := range eligible {
		eligibleSet[ins.ID] = struct{}{}
	}
	stats := RecommendationDropStats{}
	out := make([]models.Recommendation, 0, len(recs))
	for _, rec := range recs {
		ids := rec.RelatedInsightIDs
		if len(ids) == 0 {
			applog.WithFields(applog.Fields{
				"recommendation_id": rec.ID,
				"title":             rec.Title,
				"reason":            "missing_related_insight_ids",
			}).Warn("Dropping recommendation: no related insight IDs cited")
			stats.MissingIDs++
			stats.Total++
			continue
		}
		bad := make([]string, 0)
		for _, id := range ids {
			if _, ok := eligibleSet[id]; !ok {
				bad = append(bad, id)
			}
		}
		if len(bad) > 0 {
			applog.WithFields(applog.Fields{
				"recommendation_id": rec.ID,
				"title":             rec.Title,
				"bad_ids":           bad,
				"reason":            "ineligible_or_unknown_insight_id",
			}).Warn("Dropping recommendation: cites insights that are not eligible")
			stats.UnknownOrIneligibleID++
			stats.Total++
			continue
		}
		out = append(out, rec)
	}
	return out, stats
}

// applyRecommendationDropStats reconciles a RecommendationStep after
// validateRelatedInsightIDs has dropped recommendations:
//
//  1. stamps the three per-reason counters so the persisted
//     discovery_recommendation_log row + the live RunStep message both
//     surface how many recs were discarded; and
//  2. syncs RecommendationStep.Recommendations to the kept slice so
//     the persisted structured rec list stays consistent with the
//     counters — without this sync, the log would still contain the
//     unfiltered LLM output (generateRecommendations sets it before
//     the validation gate runs) and operators querying the new
//     telemetry would see the bogus rows the drop counters claim were
//     discarded. The raw LLM text remains available on
//     RecommendationStep.Response for diagnosing why an id was
//     hallucinated in the first place.
func applyRecommendationDropStats(step *models.RecommendationStep, kept []models.Recommendation, stats RecommendationDropStats) {
	if step == nil {
		return
	}
	step.Recommendations = kept
	step.RecommendationsDroppedMissingIDs = stats.MissingIDs
	step.RecommendationsDroppedUnknownID = stats.UnknownOrIneligibleID
	// Compose, don't clobber: per-item parse drops (recorded by
	// generateRecommendations before this validation gate runs, issue #342)
	// and related-insight-id drops are distinct reasons for the same run, so
	// the grand total is their sum.
	step.RecommendationsDropped = step.RecommendationsDroppedParse + stats.Total
}

// buildStepIndex turns the exploration step slice into a map keyed by
// step number. The verifier indexes by step number internally; this
// function lets the orchestrator pass either form depending on the
// call site.
func buildStepIndex(steps []models.ExplorationStep) map[int]*models.ExplorationStep {
	m := make(map[int]*models.ExplorationStep, len(steps))
	for i := range steps {
		m[steps[i].Step] = &steps[i]
	}
	return m
}
