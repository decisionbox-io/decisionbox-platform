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

	// ScoringCfg is the scoring engine's calibration to re-derive this
	// document's confidence against. The zero value is the current default,
	// which is what a caller with nothing to say should pass -- the engine
	// falls back rather than scoring against an empty table.
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
		// The confidence axis and the priority rollup over it. Severity and
		// risk_score stay FROZEN: severity is magnitude x weight, and a
		// validation run changes neither -- so a stored severity is an
		// immutable fact about the run that produced it. That is also why no
		// vector-store method is needed here: the Qdrant payload's `severity`
		// filter can never drift, and its other scoring field is write-only.
		//
		// Priority is the exception and must move, because one of its two
		// inputs just did.
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
// document's confidence axis in place and leave its severity axis exactly as
// the run that produced it left it.
//
// Through the engine's own RescoreConfidence rather than a merge written here,
// because deciding which fields survive a re-validation is a property of the
// scoring model and not of this call site. The engine states the asymmetry and
// a test holds it field by field.
//
// A document that was never scored at all -- a legacy row an operator clicked
// Run validation on -- gets its CONFIDENCE axis derived and keeps its legacy
// severity word. It is tempting to score it in full instead, on the grounds
// that the one action whose purpose is to re-derive a grade should produce
// one. Two things rule that out:
//
//   - The severity it would get carries no information. This path has one
//     stored document in hand and no datasource context, so the home currency
//     cannot be resolved and no amount qualifies -- the derivation would land
//     on the area baseline and replace the model's word with a band that says
//     only "not quantified".
//   - It would make the severity filter lie. `severity` is an indexed vector
//     payload field and the search handler filters on it BEFORE hydrating
//     rows, so a row whose Mongo severity moved while its indexed copy did
//     not is a row that answers the wrong query. Severity not moving on this
//     path is precisely what makes a payload update unnecessary, and spending
//     a vector-store method on every provider to buy a baseline band would be
//     paying a lot for nothing.
//
// A new discovery run grades such a finding on the current engine, which is
// what the legacy badge in the dashboard says.
func rescoreTargetInsight(target *models.Insight, cfg scoring.Config) {
	// Only the calibration: this path holds one stored document, has no
	// datasource context, and does not recompute the severity half at all.
	in := insightScoringInput(*target, scoringContext{cfg: cfg})
	prior := models.Scoring{}
	if target.Scoring != nil {
		prior = *target.Scoring
	}
	derived := scoring.RescoreConfidence(prior, in)
	target.Scoring = &derived
	target.Confidence = derived.ConfidenceScore
	// Severity and risk_score are deliberately NOT written. They are frozen
	// on this path, and writing an unchanged value would still be this layer
	// claiming authorship of a field it did not derive.
}

func rescoreTargetRecommendation(target *models.Recommendation, cfg scoring.Config) {
	in := recommendationScoringInput(*target, cfg)
	prior := models.Scoring{}
	if target.Scoring != nil {
		prior = *target.Scoring
	}
	derived := scoring.RescoreConfidence(prior, in)
	target.Scoring = &derived
	target.Confidence = derived.ConfidenceScore
	// Priority is the one mirrored field that DOES move here, because the
	// tier is the matrix over this recommendation's verdict and the severity
	// it inherited -- and the verdict is what just changed. Left alone when
	// there is no tier: a legacy row's integer is the analysis model's own
	// ranking, and zeroing it would destroy the only value it has.
	if derived.PriorityTier != "" {
		target.Priority = scoring.RecommendationPriority(derived.PriorityTier)
	}
}
