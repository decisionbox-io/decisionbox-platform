package discovery

import (
	"context"
	"errors"
	"fmt"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/libs/go-common/scoring"
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
	// VerifierClient / RefuterClient are the two validation layers' own
	// clients (#407) — this is the layer pair where the model choice matters
	// most, because verdicts depend heavily on it. Nil falls back to
	// AIClient, which is what a caller with one model passes.
	VerifierClient verifier.LLMClient
	RefuterClient  verifier.LLMClient
	Cfg            verifier.Config
	Caps           verifier.RunCaps
	WH             verifier.WarehouseInfo
	Disc           verifier.DiscoveryContext
	Executor       verifier.Executor

	// ScoringCfg is the confidence engine's calibration to re-derive this
	// document's score against. The zero value is the current default, which
	// is what a caller with nothing to say should pass -- Score stamps the
	// build's own engine version rather than an unexplainable 0.
	ScoringCfg scoring.Config

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

	byMode := map[valmodels.AgentMode]verifier.LLMClient{}
	if in.VerifierClient != nil {
		byMode[valmodels.ModeVerifier] = in.VerifierClient
	}
	if in.RefuterClient != nil && in.Cfg.RefuterEnabled {
		byMode[valmodels.ModeRefuter] = in.RefuterClient
	}
	agent, err := verifier.NewAgentPerMode(byMode, in.AIClient, in.Cfg)
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
		// No per-item checkpoint: this is an operator re-validating one
		// document of a FINISHED discovery, not a run that can be resumed.
		results, _, _ := phase.validateInsights(ctx, one, stepByID, target.AnalysisArea, 0, nil)
		if len(results) != 1 {
			return models.ValidationResult{}, fmt.Errorf("validateInsights returned %d results, expected 1", len(results))
		}
		target.Validation = one[0].Validation
		// The verdict just changed, so the confidence derived from it is
		// stale. Re-derived here rather than left for the caller, so the one
		// document can never be persisted with a confidence from verdict X
		// beside a validation that says Y -- an internally contradictory
		// document, which is worse than either staleness alone.
		//
		// The confidence axis only. Severity and risk_score are still the
		// analysis model's own words and this path has no business touching
		// them; it is also why no vector-store method is needed here, since
		// the Qdrant payload's only re-derivable field is write-only.
		rescoreTargetInsight(target, in.ScoringCfg)
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
		// related_insight_ids fan-out) so we pass in.Insights unchanged. No
		// per-item checkpoint, for the reason the insight branch gives.
		// The pause flag is discarded on purpose: this path validates ONE
		// document on request from the API, outside any run, so there is no
		// attempt to pause and nil cp can never report one.
		results, _ := phase.validateRecommendations(ctx, one, in.Insights, stepByID, nil)
		if len(results) != 1 {
			return models.ValidationResult{}, fmt.Errorf("validateRecommendations returned %d results, expected 1", len(results))
		}
		target.Validation = one[0].Validation
		rescoreTargetRecommendation(target, in.ScoringCfg)
		return results[0], nil

	default:
		return models.ValidationResult{}, fmt.Errorf("unsupported doc_kind %q", in.DocKind)
	}
}

// rescoreTargetInsight and rescoreTargetRecommendation re-derive one
// document's confidence in place, through the same scorer the run uses.
//
// The same path deliberately: a second derivation here would be a second
// answer to "what is this worth", and the two would drift the first time
// either changed. The single-element slice is how the shared scorer is
// reached without a divergent code path, which is the same shape
// ValidateSingleDoc already uses to reach validateInsights.
func rescoreTargetInsight(target *models.Insight, cfg scoring.Config) {
	one := []models.Insight{*target}
	scoreInsights(one, cfg)
	target.Confidence = one[0].Confidence
	target.Scoring = one[0].Scoring
}

func rescoreTargetRecommendation(target *models.Recommendation, cfg scoring.Config) {
	one := []models.Recommendation{*target}
	scoreRecommendations(one, cfg)
	target.Confidence = one[0].Confidence
	target.Scoring = one[0].Scoring
}
