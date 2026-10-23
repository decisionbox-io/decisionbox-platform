package discovery

import (
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// dropRecommendationsWithUnresolvedFigures discards every recommendation whose
// prose still carries a figure reference, and returns the survivors.
//
// Called after renderRecommendationFigures, which is what makes the test
// simple: the renderer replaces every reference it can resolve, and leaves the
// ones it cannot exactly as written. So after that pass, a surviving reference
// is by definition one that resolved to nothing.
//
// Detection uses reAnyRef rather than the resolving pattern. The schema accepts
// any string as a figure id, so a model can declare `id: "revenue-total"`, have
// it refused by the id grammar, and ship "{{revenue-total}}" into the published
// sentence -- and the resolving pattern does not match a hyphen, which is how
// that placeholder escaped the counter built to report it. A gate that reads
// what a reader will see has to match what a reader will see.
//
// Why drop rather than repair. The reference names a number the model promised
// and never supplied; there is nothing to substitute. Blanking it leaves a
// sentence with a hole, which tells a reader nothing, and substituting a zero
// ships a fabricated figure -- the first live replay of this layer produced
// "$11.48B — 0.00% of gross $229.58B" exactly that way. The recommendation is
// not publishable in any of those shapes, so it does not get published.
func dropRecommendationsWithUnresolvedFigures(recs []models.Recommendation) ([]models.Recommendation, int) {
	kept := make([]models.Recommendation, 0, len(recs))
	dropped := 0
	for _, rec := range recs {
		if refs := unresolvedRefsInRecommendation(rec); len(refs) > 0 {
			applog.WithFields(applog.Fields{
				"recommendation_id": rec.ID,
				"title":             rec.Title,
				"refs":              refs,
				"reason":            "unresolved_figure_reference",
			}).Warn("Dropping recommendation: its prose references figures that resolved to nothing")
			dropped++
			continue
		}
		kept = append(kept, rec)
	}
	return kept, dropped
}

// unresolvedRefsInRecommendation returns every reference-shaped placeholder
// left in the fields the renderer writes to.
//
// DescriptionMd is included and hasRecommendationFigureRef omits it: that
// helper decides whether a recommendation is worth rendering at all, and the
// markdown rendition is derived from Description, so it cannot be the only
// field carrying one. Here the question is the opposite -- whether anything a
// reader opens still shows a placeholder -- and the dashboard renders
// DescriptionMd in preference to Description.
func unresolvedRefsInRecommendation(rec models.Recommendation) []string {
	fields := []string{
		rec.Title, rec.Description, rec.DescriptionMd,
		rec.ExpectedImpact.Metric, rec.ExpectedImpact.EstimatedImprovement, rec.ExpectedImpact.Reasoning,
	}
	fields = append(fields, rec.Actions...)

	var refs []string
	for _, s := range fields {
		refs = append(refs, reAnyRef.FindAllString(s, -1)...)
	}
	return refs
}

// applyUnresolvedFigureDropStats reconciles a RecommendationStep after the
// figure gate has dropped recommendations.
//
// The same two jobs applyRecommendationDropStats does, for the same reasons:
// stamp the per-reason counter so the persisted log row and the live RunStep
// both say why the result shrank, and sync the step's structured list to the
// kept slice so the log does not keep rows the counters claim were discarded.
// The raw LLM text stays on RecommendationStep.Response, which is where the
// undeclared references are still readable afterwards.
func applyUnresolvedFigureDropStats(step *models.RecommendationStep, kept []models.Recommendation, dropped int) {
	if step == nil {
		return
	}
	step.Recommendations = kept
	step.RecommendationsDroppedUnresolvedFigures = dropped
	// Compose, don't clobber: a parse drop, a bad citation and an unresolved
	// figure are distinct reasons for the same run, so the grand total is
	// their sum.
	step.RecommendationsDropped = step.RecommendationsDroppedParse +
		step.RecommendationsDroppedMissingIDs +
		step.RecommendationsDroppedUnknownID +
		dropped

	// A gate that can empty a result has to say so. Without this the run
	// renders as "none found", which is what a model with nothing to say also
	// renders as -- the silent-empty failure this pipeline has spent several
	// issues eliminating, reintroduced by the fix for a different one.
	//
	// Only when nothing survived, and only over an unset status: a parse error
	// happened earlier in the phase and is the more specific reason.
	if len(kept) == 0 && dropped > 0 && step.Status == "" {
		step.Status = statusRecommendationFiguresUnresolved
	}
}
