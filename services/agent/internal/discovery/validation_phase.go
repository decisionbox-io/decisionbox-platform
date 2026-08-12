package discovery

import (
	"context"
	"sort"
	"time"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
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
// `runValidated` tracks how many insights have been validated so far
// across all areas; the caller must thread it through area loops.
//
// Returns the new runValidated count (so callers can keep their
// running total in sync).
func (p *validationPhase) validateInsights(ctx context.Context, insights []models.Insight, stepByID map[int]*models.ExplorationStep, areaID string, runValidated int) ([]models.ValidationResult, int) {
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
		return results, runValidated
	}

	// Order by affected_count desc so the run-cap hits the least-
	// important docs last.
	order := indicesByAffectedDesc(insights)
	for _, idx := range order {
		ins := &insights[idx]
		vr := models.ValidationResult{
			InsightID:     ins.ID,
			AnalysisArea:  areaID,
			ClaimedCount:  ins.AffectedCount,
			ClaimedMetric: ins.Name, // display label for the run-log step
			ValidatedAt:   time.Now(),
			DocKind:       valmodels.DocInsight,
		}
		if runValidated >= p.caps.MaxInsightsPerRun {
			ins.Validation = &valmodels.InsightValidation{
				Combined:    valmodels.StatusSkippedBudgetCap,
				ValidatedAt: vr.ValidatedAt,
			}
			vr.Combined = valmodels.StatusSkippedBudgetCap
			vr.Status = string(valmodels.StatusSkippedBudgetCap)
			results = append(results, vr)
			continue
		}

		// Verify against the datasource this insight is about (multi-
		// warehouse); single-warehouse resolves to the primary fallback.
		dsID := p.datasourceForSteps(ins.SourceSteps, stepByID)
		wh, ex := p.forDatasource(dsID)
		vr.WarehouseID = p.stampDatasource(dsID)
		bundle := verifier.BuildInsightBundle(ins, stepByID, wh, p.disc, p.cfg.Bundle)
		v, _ := p.agent.Verify(ctx, bundle, ex)
		vr.Verifier = &v
		vr.InputTokens += v.LLMTokensIn
		vr.OutputTokens += v.LLMTokensOut
		var rPtr *valmodels.StructuredVerdict
		if p.cfg.RefuterEnabled {
			refuterBundle := bundle
			if len(v.ClaimsConsidered) > 0 {
				refuterBundle.PriorClaims = append([]string(nil), v.ClaimsConsidered...)
			}
			r, _ := p.agent.Refute(ctx, refuterBundle, ex)
			rPtr = &r
			vr.Refuter = &r
			vr.InputTokens += r.LLMTokensIn
			vr.OutputTokens += r.LLMTokensOut
		}
		combined, rd := valmodels.Combine(&v, rPtr, !p.cfg.RefuterEnabled)
		vr.Combined = combined
		vr.RefuterDisabled = rd
		vr.Status = string(combined)

		ins.Validation = &valmodels.InsightValidation{
			WarehouseID: vr.WarehouseID,
			// Backfill legacy Status so dashboards / consumers that
			// still read the old field see the new verdict.
			Status:          string(combined),
			Reasoning:       v.OverallReason,
			Verifier:        &v,
			Refuter:         rPtr,
			Combined:        combined,
			RefuterDisabled: rd,
			ValidatedAt:     vr.ValidatedAt,
			InputTokens:     vr.InputTokens,
			OutputTokens:    vr.OutputTokens,
		}
		results = append(results, vr)
		runValidated++
	}
	applog.WithFields(applog.Fields{
		"area":     areaID,
		"insights": len(insights),
		"verdicts": len(results),
	}).Info("Insight validation complete")
	return results, runValidated
}

// validateRecommendations runs Phase 5.5 — same shape as
// validateInsights but for recommendations, which build their bundles
// from the union of their related insights' source steps.
func (p *validationPhase) validateRecommendations(ctx context.Context, recommendations []models.Recommendation, allInsights []models.Insight, stepByID map[int]*models.ExplorationStep) []models.ValidationResult {
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
		return results
	}
	insightByID := make(map[string]*models.Insight, len(allInsights))
	for i := range allInsights {
		insightByID[allInsights[i].ID] = &allInsights[i]
	}

	validated := 0
	for i := range recommendations {
		rec := &recommendations[i]
		vr := models.ValidationResult{
			InsightID:     rec.ID,
			ClaimedMetric: rec.Title, // display label for the run-log step
			ValidatedAt:   time.Now(),
			DocKind:       valmodels.DocRecommendation,
		}
		if validated >= p.caps.MaxRecommendationsPerRun {
			rec.Validation = &valmodels.InsightValidation{
				Combined:    valmodels.StatusSkippedBudgetCap,
				ValidatedAt: vr.ValidatedAt,
			}
			vr.Combined = valmodels.StatusSkippedBudgetCap
			vr.Status = string(valmodels.StatusSkippedBudgetCap)
			results = append(results, vr)
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
		bundle := verifier.BuildRecommendationBundle(rec, insightByID, stepByID, wh, p.disc, p.cfg.Bundle)
		v, _ := p.agent.Verify(ctx, bundle, ex)
		vr.Verifier = &v
		vr.InputTokens += v.LLMTokensIn
		vr.OutputTokens += v.LLMTokensOut
		var rPtr *valmodels.StructuredVerdict
		if p.cfg.RefuterEnabled {
			rb := bundle
			if len(v.ClaimsConsidered) > 0 {
				rb.PriorClaims = append([]string(nil), v.ClaimsConsidered...)
			}
			r, _ := p.agent.Refute(ctx, rb, ex)
			rPtr = &r
			vr.Refuter = &r
			vr.InputTokens += r.LLMTokensIn
			vr.OutputTokens += r.LLMTokensOut
		}
		combined, rd := valmodels.Combine(&v, rPtr, !p.cfg.RefuterEnabled)
		vr.Combined = combined
		vr.RefuterDisabled = rd
		vr.Status = string(combined)
		rec.Validation = &valmodels.InsightValidation{
			WarehouseID:     vr.WarehouseID,
			Verifier:        &v,
			Refuter:         rPtr,
			Combined:        combined,
			RefuterDisabled: rd,
			ValidatedAt:     vr.ValidatedAt,
			InputTokens:     vr.InputTokens,
			OutputTokens:    vr.OutputTokens,
		}
		results = append(results, vr)
		validated++
	}
	applog.WithField("recommendations", len(recommendations)).Info("Recommendation validation complete")
	return results
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

// filterEligibleInsights returns the subset of insights whose
// Combined verdict is in {confirmed, supported} — the recommender
// input filter from plan §"Recommendation policy". When an insight
// has no Validation (the orchestrator's validationPhase ran but
// emitted nothing for this insight, or no agent was wired), the
// insight is treated as eligible — failing-open keeps the
// recommender from starving when validation is disabled.
func filterEligibleInsights(all []models.Insight) []models.Insight {
	out := make([]models.Insight, 0, len(all))
	for i := range all {
		v := all[i].Validation
		if v == nil {
			out = append(out, all[i])
			continue
		}
		if v.Combined.IsTerminalPositive() || v.Combined == valmodels.StatusValidationDisabled {
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

// validateRelatedInsightIDs drops recommendations whose
// related_insight_ids reference unknown / ineligible insights, or
// whose list is empty (the discipline rule requires every rec to
// cite at least one insight). Returns the kept recommendations and a
// RecommendationDropStats summarizing how many were dropped and why.
// A warning is logged per dropped recommendation so the agent log
// retains the offending ids for post-mortem analysis.
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
	step.RecommendationsDropped = stats.Total
	step.RecommendationsDroppedMissingIDs = stats.MissingIDs
	step.RecommendationsDroppedUnknownID = stats.UnknownOrIneligibleID
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
