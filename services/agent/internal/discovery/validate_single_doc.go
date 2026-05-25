package discovery

import (
	"context"
	"errors"
	"fmt"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation/verifier"
)

// ValidateSingleDocInput carries everything the single-doc validation
// path needs. The caller (services/agent/agentserver/validate_doc.go)
// loads the discovery, target insight/rec, and exploration-step rows
// from Mongo; this function only orchestrates the verifier + refuter
// pair against an already-hydrated bundle.
//
// Target is keyed by (DocKind, DocID). Insights is the FULL insight
// slice from the discovery — for recommendations the validator walks
// it to fan out related_insight_ids; for insights only the matching
// row is used. Steps is the cited exploration log keyed by step
// number on the way in; the helper rebuilds the stepByID map.
type ValidateSingleDocInput struct {
	AIClient *ai.Client
	Cfg      verifier.Config
	Caps     verifier.RunCaps
	WH       verifier.WarehouseInfo
	Disc     verifier.DiscoveryContext
	Executor verifier.Executor

	DocKind  valmodels.DocKind
	DocID    string
	Insights []models.Insight        // full slice from the discovery doc
	Recs     []models.Recommendation // full slice from the discovery doc
	Steps    []models.ExplorationStep
}

// ValidateSingleDoc runs Phase 4.5 (insight) or Phase 5.5
// (recommendation) for exactly one document and returns the resulting
// ValidationResult. The matching slice entry's .Validation field is
// stamped in place so the caller can persist both the embedded-array
// update (positional array-filter on the discovery doc) and the
// ValidationResult row with a single set of values.
//
// The function deliberately requires AIClient to be non-nil — the
// nil-agent skip path (which stamps validation_disabled on every doc)
// is for the full-run orchestrator; a single manual click that
// landed here implies the user expects the verifier to actually run.
func ValidateSingleDoc(ctx context.Context, in ValidateSingleDocInput) (models.ValidationResult, error) {
	if in.AIClient == nil {
		return models.ValidationResult{}, errors.New("aiClient is required for single-doc validation")
	}

	agent, err := verifier.NewAgent(in.AIClient, in.Cfg)
	if err != nil {
		return models.ValidationResult{}, fmt.Errorf("build verifier agent: %w", err)
	}

	stepByID := buildStepIndex(in.Steps)

	phase := &validationPhase{
		agent:    agent,
		cfg:      in.Cfg,
		caps:     in.Caps,
		wh:       in.WH,
		disc:     in.Disc,
		executor: in.Executor,
	}

	switch in.DocKind {
	case valmodels.DocInsight:
		var target *models.Insight
		for i := range in.Insights {
			if in.Insights[i].ID == in.DocID {
				target = &in.Insights[i]
				break
			}
		}
		if target == nil {
			return models.ValidationResult{}, fmt.Errorf("insight %q not found in discovery", in.DocID)
		}
		// Wrap in a one-element slice so the existing validateInsights
		// helper does the work without divergent code paths.
		one := []models.Insight{*target}
		results, _ := phase.validateInsights(ctx, one, stepByID, target.AnalysisArea, 0)
		if len(results) != 1 {
			return models.ValidationResult{}, fmt.Errorf("validateInsights returned %d results, expected 1", len(results))
		}
		target.Validation = one[0].Validation
		return results[0], nil

	case valmodels.DocRecommendation:
		var target *models.Recommendation
		for i := range in.Recs {
			if in.Recs[i].ID == in.DocID {
				target = &in.Recs[i]
				break
			}
		}
		if target == nil {
			return models.ValidationResult{}, fmt.Errorf("recommendation %q not found in discovery", in.DocID)
		}
		one := []models.Recommendation{*target}
		// validateRecommendations needs the FULL insight set (for the
		// related_insight_ids fan-out) so we pass in.Insights unchanged.
		results := phase.validateRecommendations(ctx, one, in.Insights, stepByID)
		if len(results) != 1 {
			return models.ValidationResult{}, fmt.Errorf("validateRecommendations returned %d results, expected 1", len(results))
		}
		target.Validation = one[0].Validation
		return results[0], nil

	default:
		return models.ValidationResult{}, fmt.Errorf("unsupported doc_kind %q", in.DocKind)
	}
}
