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
		for i := range insights {
			insights[i].Validation = &valmodels.InsightValidation{
				Combined:    valmodels.StatusValidationDisabled,
				ValidatedAt: time.Now(),
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
			InsightID:    ins.ID,
			AnalysisArea: areaID,
			ClaimedCount: ins.AffectedCount,
			ValidatedAt:  time.Now(),
			DocKind:      valmodels.DocInsight,
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

		bundle := verifier.BuildInsightBundle(ins, stepByID, p.wh, p.disc, p.cfg.Bundle)
		v, _ := p.agent.Verify(ctx, bundle, p.executor)
		vr.Verifier = &v
		vr.InputTokens += v.LLMTokensIn
		vr.OutputTokens += v.LLMTokensOut
		var rPtr *valmodels.StructuredVerdict
		if p.cfg.RefuterEnabled {
			refuterBundle := bundle
			if len(v.ClaimsConsidered) > 0 {
				refuterBundle.PriorClaims = append([]string(nil), v.ClaimsConsidered...)
			}
			r, _ := p.agent.Refute(ctx, refuterBundle, p.executor)
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
			// Backfill legacy Status so dashboards / consumers that
			// still read the old field see the new verdict (Codex
			// prod-r1 MEDIUM).
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
		for i := range recommendations {
			recommendations[i].Validation = &valmodels.InsightValidation{
				Combined:    valmodels.StatusValidationDisabled,
				ValidatedAt: time.Now(),
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
			InsightID:   rec.ID,
			ValidatedAt: time.Now(),
			DocKind:     valmodels.DocRecommendation,
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
		bundle := verifier.BuildRecommendationBundle(rec, insightByID, stepByID, p.wh, p.disc, p.cfg.Bundle)
		v, _ := p.agent.Verify(ctx, bundle, p.executor)
		vr.Verifier = &v
		vr.InputTokens += v.LLMTokensIn
		vr.OutputTokens += v.LLMTokensOut
		var rPtr *valmodels.StructuredVerdict
		if p.cfg.RefuterEnabled {
			rb := bundle
			if len(v.ClaimsConsidered) > 0 {
				rb.PriorClaims = append([]string(nil), v.ClaimsConsidered...)
			}
			r, _ := p.agent.Refute(ctx, rb, p.executor)
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

// validateRelatedInsightIDs drops recommendations whose
// related_insight_ids reference unknown / ineligible insights, or
// whose list is empty (the discipline rule requires every rec to
// cite at least one insight). Returns the kept recommendations and
// logs a warning per dropped one.
func validateRelatedInsightIDs(recs []models.Recommendation, eligible []models.Insight) []models.Recommendation {
	eligibleSet := make(map[string]struct{}, len(eligible))
	for _, ins := range eligible {
		eligibleSet[ins.ID] = struct{}{}
	}
	out := make([]models.Recommendation, 0, len(recs))
	for i := range recs {
		ids := recs[i].RelatedInsightIDs
		if len(ids) == 0 {
			applog.WithFields(applog.Fields{
				"recommendation_id": recs[i].ID,
				"title":             recs[i].Title,
				"reason":            "missing_related_insight_ids",
			}).Warn("Dropping recommendation: no related insight IDs cited")
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
				"recommendation_id": recs[i].ID,
				"title":             recs[i].Title,
				"bad_ids":           bad,
				"reason":            "ineligible_or_unknown_insight_id",
			}).Warn("Dropping recommendation: cites insights that are not eligible")
			continue
		}
		out = append(out, recs[i])
	}
	return out
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
