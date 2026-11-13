package agentserver

import (
	"context"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/libs/go-common/scoring"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Pinning a run's severity weighting, so a resume cannot be scored under two
// sets of weights.
//
// Severity is magnitude x the analysis area's weight, and the weight is
// configuration: a project's category weights, the pack's taxonomy, and each
// area's own override. A resume can happen days after the attempt it
// continues, and a weight changed in between would score the second half of a
// run against different numbers from the first -- leaving a result with no
// meaningful ranking across it. The run's step budget, area list and effort
// are already pinned on the document for exactly this reason; this is the
// same decision one field over.

// pinRunScoringConfig returns the severity weighting this run is scored under,
// writing it on the run document if this is the attempt that starts it.
//
// Resolved from the project here, at the one place that knows both the project
// and the run, and then never again: the repository's write is conditional on
// the field being absent, so attempt 1 stores this answer and every later
// attempt gets attempt 1's back.
//
// The INPUTS are stored rather than the resolved per-area weights. A later
// attempt therefore re-derives the weight and the risk flags through the same
// engine code as the first, and cannot disagree with it -- where storing the
// output would put a second copy of the "largest of its categories" rule in
// the database and leave the flags unpinned.
func pinRunScoringConfig(
	ctx context.Context,
	runRepo *database.RunRepository,
	runID string,
	project *models.Project,
) (*gomodels.RunScoringConfig, error) {
	return runRepo.PinScoringConfig(ctx, runID, resolveRunScoringConfig(project))
}

// resolveRunScoringConfig is the project's severity configuration, resolved:
// the pack's taxonomy and the project's category weights folded into one
// weight map, plus each area's mapping and override.
//
// Pure, and separate from the write, so the resolution is testable without a
// database and so the rule it follows is one function rather than a sequence
// of assignments at a call site.
//
// A project with no taxonomy, no category weights and no mapped areas resolves
// to an empty configuration. That is still PINNED, deliberately: it records
// that this run was started with nothing configured, which is what a resume
// after someone adds a taxonomy has to honour.
func resolveRunScoringConfig(project *models.Project) gomodels.RunScoringConfig {
	out := gomodels.RunScoringConfig{}
	if project == nil {
		return out
	}

	var taxonomy []gomodels.RiskCategory
	if project.Prompts != nil {
		taxonomy = project.Prompts.RiskTaxonomy

		areas := make(map[string]gomodels.RunAreaRisk, len(project.Prompts.AnalysisAreas))
		for id, cfg := range project.Prompts.AnalysisAreas {
			if len(cfg.RiskCategories) == 0 && cfg.SeverityWeight == nil {
				// Nothing configured for this area. Omitted rather than
				// stored empty, so the pinned document says what was
				// configured rather than listing every area a project has.
				continue
			}
			areas[id] = gomodels.RunAreaRisk{
				RiskCategories: cfg.RiskCategories,
				SeverityWeight: cfg.SeverityWeight,
			}
		}
		if len(areas) > 0 {
			out.Areas = areas
		}
	}

	// Through the engine's own resolution, not a fold written here: the same
	// function the precedence rules are tested against, so a project weight
	// cannot win here and lose there.
	resolved := scoring.ResolveSeverity(scoring.SeverityConfig{}, taxonomy, project.CategoryWeights)
	out.CategoryWeights = resolved.CategoryWeights

	return out
}
