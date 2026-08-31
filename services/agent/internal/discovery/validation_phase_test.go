package discovery

import (
	"context"
	"testing"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation/verifier"
)

// When the validation agent is nil (project toggle off, no aiClient,
// or no schema provider), validateInsights must stamp every insight
// with combined=validation_disabled AND backfill the legacy Status
// field — the latter so consumers reading the legacy shape (dashboard
// list rendering, etc.) still see the verdict instead of an empty
// pill.
func TestValidateInsights_NilAgentStampsValidationDisabledWithLegacyBackfill(t *testing.T) {
	p := &validationPhase{agent: nil}
	insights := []models.Insight{
		{ID: "i1", AffectedCount: 100},
		{ID: "i2", AffectedCount: 50},
	}

	results, validated := p.validateInsights(context.Background(), insights, nil, "area-1", 0)

	if len(results) != 0 {
		t.Errorf("nil-agent path should emit zero ValidationResults, got %d", len(results))
	}
	if validated != 0 {
		t.Errorf("nil-agent path should not advance runValidated, got %d", validated)
	}
	for i, ins := range insights {
		if ins.Validation == nil {
			t.Fatalf("insight[%d] Validation == nil; nil-agent path must stamp every insight", i)
			return
		}
		if ins.Validation.Combined != valmodels.StatusValidationDisabled {
			t.Errorf("insight[%d] Combined = %q, want %q", i, ins.Validation.Combined, valmodels.StatusValidationDisabled)
		}
		if ins.Validation.Status != string(valmodels.StatusValidationDisabled) {
			t.Errorf("insight[%d] Status = %q, want %q (legacy backfill)", i, ins.Validation.Status, valmodels.StatusValidationDisabled)
		}
		if ins.Validation.ValidatedAt.IsZero() {
			t.Errorf("insight[%d] ValidatedAt is zero — should be set even on disabled path", i)
		}
	}
}

// Mirror of the insight test for recommendations — same nil-agent
// contract applies to Phase 5.5.
func TestValidateRecommendations_NilAgentStampsValidationDisabledWithLegacyBackfill(t *testing.T) {
	p := &validationPhase{agent: nil}
	recs := []models.Recommendation{
		{ID: "r1"},
		{ID: "r2"},
	}

	results := p.validateRecommendations(context.Background(), recs, nil, nil)

	if len(results) != 0 {
		t.Errorf("nil-agent path should emit zero ValidationResults, got %d", len(results))
	}
	for i, rec := range recs {
		if rec.Validation == nil {
			t.Fatalf("rec[%d] Validation == nil", i)
			return
		}
		if rec.Validation.Combined != valmodels.StatusValidationDisabled {
			t.Errorf("rec[%d] Combined = %q, want %q", i, rec.Validation.Combined, valmodels.StatusValidationDisabled)
		}
		if rec.Validation.Status != string(valmodels.StatusValidationDisabled) {
			t.Errorf("rec[%d] Status = %q, want %q (legacy backfill)", i, rec.Validation.Status, valmodels.StatusValidationDisabled)
		}
	}
}

// validateRelatedInsightIDs is the server-side defense against LLMs
// that hallucinate slug-style ids instead of copying the input UUIDs
// (see issue #237). These tests pin the keep/drop contract and the
// per-reason counters that drive the per-run dropped-rec telemetry.
func TestValidateRelatedInsightIDs_KeepsRecsWithFullyMatchingUUIDs(t *testing.T) {
	insights := []models.Insight{
		{ID: "6e9261f5-c4ec-404b-bdf0-760a4644f384"},
		{ID: "02665b9e-468f-41eb-b50e-28702b95e999"},
	}
	recs := []models.Recommendation{
		{ID: "r1", Title: "ok-single", RelatedInsightIDs: []string{"6e9261f5-c4ec-404b-bdf0-760a4644f384"}},
		{ID: "r2", Title: "ok-multi", RelatedInsightIDs: []string{
			"6e9261f5-c4ec-404b-bdf0-760a4644f384",
			"02665b9e-468f-41eb-b50e-28702b95e999",
		}},
	}

	kept, stats := validateRelatedInsightIDs(recs, insights)
	if len(kept) != 2 {
		t.Errorf("kept %d recs, want 2", len(kept))
	}
	if stats.Total != 0 || stats.MissingIDs != 0 || stats.UnknownOrIneligibleID != 0 {
		t.Errorf("expected zero drops, got %+v", stats)
	}
}

func TestValidateRelatedInsightIDs_DropsHallucinatedSlugFormat(t *testing.T) {
	// Real-world failure mode from issue #237: Vertex Gemini emits
	// category:severity:theme slugs in place of UUIDs. None match the
	// input insight set so every such rec must be dropped, and the
	// drop reason must be UnknownOrIneligibleID (NOT MissingIDs).
	insights := []models.Insight{
		{ID: "6e9261f5-c4ec-404b-bdf0-760a4644f384"},
	}
	recs := []models.Recommendation{
		{ID: "r1", Title: "slug-1", RelatedInsightIDs: []string{
			"ecommerce-conversion-funnel:critical:inconsistent-product-category",
		}},
		{ID: "r2", Title: "slug-2", RelatedInsightIDs: []string{
			"product-merchandising-performance:critical:100-cart-abandonment",
			"ecommerce-conversion-funnel:high:high-friction-in-checkout-funnel",
		}},
	}

	kept, stats := validateRelatedInsightIDs(recs, insights)
	if len(kept) != 0 {
		t.Errorf("kept %d recs, want 0 (all slugs are unknown)", len(kept))
	}
	if stats.Total != 2 {
		t.Errorf("stats.Total = %d, want 2", stats.Total)
	}
	if stats.UnknownOrIneligibleID != 2 {
		t.Errorf("stats.UnknownOrIneligibleID = %d, want 2", stats.UnknownOrIneligibleID)
	}
	if stats.MissingIDs != 0 {
		t.Errorf("stats.MissingIDs = %d, want 0 (rec cited ids, they were just bogus)", stats.MissingIDs)
	}
}

func TestValidateRelatedInsightIDs_DropsEmptyRelatedIDsAsMissing(t *testing.T) {
	insights := []models.Insight{
		{ID: "6e9261f5-c4ec-404b-bdf0-760a4644f384"},
	}
	recs := []models.Recommendation{
		{ID: "r1", Title: "no-ids", RelatedInsightIDs: nil},
		{ID: "r2", Title: "empty-ids", RelatedInsightIDs: []string{}},
		{ID: "r3", Title: "ok", RelatedInsightIDs: []string{"6e9261f5-c4ec-404b-bdf0-760a4644f384"}},
	}

	kept, stats := validateRelatedInsightIDs(recs, insights)
	if len(kept) != 1 || kept[0].ID != "r3" {
		t.Errorf("kept = %+v, want only r3", kept)
	}
	if stats.Total != 2 || stats.MissingIDs != 2 || stats.UnknownOrIneligibleID != 0 {
		t.Errorf("stats = %+v, want Total=2 MissingIDs=2 UnknownID=0", stats)
	}
}

func TestValidateRelatedInsightIDs_PartialBadIDsDropsWholeRec(t *testing.T) {
	// A rec that cites two ids — one valid, one slug — must be dropped
	// entirely: keeping it while silently filtering the bad id would
	// mask the underlying hallucination from operators.
	insights := []models.Insight{
		{ID: "6e9261f5-c4ec-404b-bdf0-760a4644f384"},
	}
	recs := []models.Recommendation{
		{ID: "r1", Title: "mixed", RelatedInsightIDs: []string{
			"6e9261f5-c4ec-404b-bdf0-760a4644f384",
			"some-slug-thing",
		}},
	}

	kept, stats := validateRelatedInsightIDs(recs, insights)
	if len(kept) != 0 {
		t.Errorf("kept %d recs, want 0 (any bad id drops the whole rec)", len(kept))
	}
	if stats.UnknownOrIneligibleID != 1 || stats.Total != 1 {
		t.Errorf("stats = %+v, want UnknownID=1 Total=1", stats)
	}
}

func TestValidateRelatedInsightIDs_EmptyInputsReturnEmptyAndZeroStats(t *testing.T) {
	// Defensive: empty inputs must not panic, and stats must be zero.
	kept, stats := validateRelatedInsightIDs(nil, nil)
	if len(kept) != 0 {
		t.Errorf("kept = %d, want 0", len(kept))
	}
	if stats.Total != 0 || stats.MissingIDs != 0 || stats.UnknownOrIneligibleID != 0 {
		t.Errorf("stats on empty input = %+v, want zero", stats)
	}
}

// --- recoverRelatedInsightIDs (#347): salvage + fail-open backfill ---

// A model that cites correctly must be returned byte-identical with zero
// recovery stats — the big-model non-regression guarantee.
func TestRecoverRelatedInsightIDs_ValidCitationsUntouched(t *testing.T) {
	insights := []models.Insight{
		{ID: "6e9261f5-c4ec-404b-bdf0-760a4644f384"},
		{ID: "02665b9e-468f-41eb-b50e-28702b95e999"},
	}
	recs := []models.Recommendation{
		{ID: "r1", RelatedInsightIDs: []string{"6e9261f5-c4ec-404b-bdf0-760a4644f384"}},
		{ID: "r2", RelatedInsightIDs: []string{
			"6e9261f5-c4ec-404b-bdf0-760a4644f384", "02665b9e-468f-41eb-b50e-28702b95e999",
		}},
	}
	out, stats := recoverRelatedInsightIDs(recs, insights)
	if len(out) != 2 {
		t.Fatalf("kept %d recs, want 2", len(out))
	}
	if stats.Salvaged != 0 || stats.Backfilled != 0 {
		t.Errorf("expected zero recovery, got %+v", stats)
	}
	if len(out[1].RelatedInsightIDs) != 2 {
		t.Errorf("valid rec citations changed: %v", out[1].RelatedInsightIDs)
	}
}

// A rec citing one real + one bogus id keeps the real id and is salvaged
// (kept), NOT dropped — the key behavior change from the old validator.
func TestRecoverRelatedInsightIDs_SalvagesPartialBadIDs(t *testing.T) {
	insights := []models.Insight{{ID: "6e9261f5-c4ec-404b-bdf0-760a4644f384"}}
	recs := []models.Recommendation{
		{ID: "r1", RelatedInsightIDs: []string{"6e9261f5-c4ec-404b-bdf0-760a4644f384", "some-slug-thing"}},
	}
	out, stats := recoverRelatedInsightIDs(recs, insights)
	if len(out) != 1 {
		t.Fatalf("kept %d recs, want 1 (salvage, not drop)", len(out))
	}
	if got := out[0].RelatedInsightIDs; len(got) != 1 || got[0] != "6e9261f5-c4ec-404b-bdf0-760a4644f384" {
		t.Errorf("salvaged ids = %v, want the single valid uuid", got)
	}
	if stats.Salvaged != 1 || stats.Backfilled != 0 {
		t.Errorf("stats = %+v, want Salvaged=1 Backfilled=0", stats)
	}
}

// Recs with missing / all-hallucinated citations are fail-open backfilled to
// the eligible insight set and KEPT — the Run C case (DeepSeek-V3.1 recs with
// no citations were dropped to zero under the old validator).
func TestRecoverRelatedInsightIDs_BackfillsMissingAndAllBad(t *testing.T) {
	insights := []models.Insight{
		{ID: "6e9261f5-c4ec-404b-bdf0-760a4644f384"},
		{ID: "02665b9e-468f-41eb-b50e-28702b95e999"},
	}
	recs := []models.Recommendation{
		{ID: "r1", RelatedInsightIDs: nil},                                        // missing
		{ID: "r2", RelatedInsightIDs: []string{}},                                 // empty
		{ID: "r3", RelatedInsightIDs: []string{"cat:crit:slug", "another-slug"}},  // all bad
	}
	out, stats := recoverRelatedInsightIDs(recs, insights)
	if len(out) != 3 {
		t.Fatalf("kept %d recs, want 3 (backfill, never drop)", len(out))
	}
	for _, r := range out {
		if len(r.RelatedInsightIDs) != 2 {
			t.Errorf("rec %s backfilled ids = %v, want the 2 eligible ids", r.ID, r.RelatedInsightIDs)
		}
	}
	if stats.Backfilled != 3 || stats.Salvaged != 0 {
		t.Errorf("stats = %+v, want Backfilled=3 Salvaged=0", stats)
	}
	// After recovery, the defensive validator must drop nothing.
	kept, drop := validateRelatedInsightIDs(out, insights)
	if len(kept) != 3 || drop.Total != 0 {
		t.Errorf("post-recovery validator dropped recs: kept=%d stats=%+v", len(kept), drop)
	}
}

func TestRecoverRelatedInsightIDs_EmptyEligibleLeavesRecsUnchanged(t *testing.T) {
	// Unreachable in Phase 5 (it skips when eligible is empty) but must not
	// panic or fabricate ids from nothing.
	recs := []models.Recommendation{{ID: "r1", RelatedInsightIDs: []string{"x"}}}
	out, stats := recoverRelatedInsightIDs(recs, nil)
	if len(out) != 1 || len(out[0].RelatedInsightIDs) != 0 {
		t.Errorf("out = %+v, want the rec kept with its bad id trimmed to empty", out)
	}
	if stats.Salvaged != 0 || stats.Backfilled != 0 {
		t.Errorf("stats = %+v, want zero", stats)
	}
}

func TestApplyRecommendationDropStats_SyncsStructuredRecsAndCounters(t *testing.T) {
	// The orchestrator's persistence path writes recStep verbatim into
	// discovery_recommendation_log. generateRecommendations seeds
	// recStep.Recommendations with the unfiltered LLM output BEFORE
	// validateRelatedInsightIDs runs; without this helper the persisted
	// log would carry the bogus rows the drop counters claim were
	// discarded. Pin both halves of the reconcile: the structured slice
	// is replaced AND the three counters are stamped.
	unfiltered := []models.Recommendation{
		{ID: "r1", Title: "kept"},
		{ID: "r2", Title: "dropped"},
	}
	kept := []models.Recommendation{unfiltered[0]}
	stats := RecommendationDropStats{
		Total:                 1,
		MissingIDs:            0,
		UnknownOrIneligibleID: 1,
	}
	step := &models.RecommendationStep{
		Recommendations: unfiltered,
		Response:        "raw LLM output preserved for diagnosis",
	}

	applyRecommendationDropStats(step, kept, stats)

	if len(step.Recommendations) != 1 || step.Recommendations[0].ID != "r1" {
		t.Errorf("Recommendations not synced to kept slice: %+v", step.Recommendations)
	}
	if step.RecommendationsDropped != 1 {
		t.Errorf("RecommendationsDropped = %d, want 1", step.RecommendationsDropped)
	}
	if step.RecommendationsDroppedUnknownID != 1 {
		t.Errorf("RecommendationsDroppedUnknownID = %d, want 1", step.RecommendationsDroppedUnknownID)
	}
	if step.RecommendationsDroppedMissingIDs != 0 {
		t.Errorf("RecommendationsDroppedMissingIDs = %d, want 0", step.RecommendationsDroppedMissingIDs)
	}
	if step.Response == "" {
		t.Errorf("Response was clobbered; raw LLM text must survive for diagnostics")
	}
}

func TestApplyRecommendationDropStats_NilStepIsNoOp(t *testing.T) {
	// Defensive: helper must tolerate a nil step rather than panicking,
	// so the orchestrator does not need a nil-guard at the call site.
	applyRecommendationDropStats(nil, nil, RecommendationDropStats{Total: 5})
}

func TestApplyRecommendationDropStats_CleanRunZerosCountersAndKeepsSlice(t *testing.T) {
	// On the happy path the helper still runs, and must NOT pollute the
	// step with non-zero counters or strip the kept recs.
	kept := []models.Recommendation{{ID: "r1"}, {ID: "r2"}}
	step := &models.RecommendationStep{Recommendations: kept}

	applyRecommendationDropStats(step, kept, RecommendationDropStats{})

	if len(step.Recommendations) != 2 {
		t.Errorf("clean run lost recs: %+v", step.Recommendations)
	}
	if step.RecommendationsDropped != 0 || step.RecommendationsDroppedMissingIDs != 0 || step.RecommendationsDroppedUnknownID != 0 {
		t.Errorf("clean run set non-zero counter: %+v", step)
	}
}

// #279: the run-log validation step renders `Validated "<label>": …`,
// where <label> comes from ValidationResult.ClaimedMetric. That field was
// declared but never assigned, so every step showed an empty label. These
// tests pin that the label is now populated from the source doc's display
// name on the budget-cap-skipped path — the exact status the bug report
// captured (skipped_budget_cap). ClaimedMetric is set in the struct literal
// before the cap branch, so the validated path carries the same value.

func TestValidateInsights_PopulatesClaimedMetricLabel(t *testing.T) {
	// MaxInsightsPerRun=0 forces the budget-cap skip on the first insight
	// without ever invoking the (zero-value) agent; a non-nil agent is
	// needed only to get past the nil-agent early return.
	p := &validationPhase{
		agent: &verifier.Agent{},
		caps:  verifier.RunCaps{MaxInsightsPerRun: 0},
	}
	insights := []models.Insight{
		{ID: "i1", Name: "Office Furniture Category: Single Dominant Seller", AffectedCount: 609},
	}

	results, _ := p.validateInsights(context.Background(), insights, nil, "area-1", 0)

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if got := results[0].ClaimedMetric; got != "Office Furniture Category: Single Dominant Seller" {
		t.Errorf("ClaimedMetric = %q, want the insight Name (empty label is the #279 bug)", got)
	}
	if results[0].Status != string(valmodels.StatusSkippedBudgetCap) {
		t.Errorf("Status = %q, want %q", results[0].Status, valmodels.StatusSkippedBudgetCap)
	}
}

func TestValidateRecommendations_PopulatesClaimedMetricLabel(t *testing.T) {
	// Same setup for Phase 5.5: cap of 0 skips the rec on the budget path,
	// and the label must still be the recommendation Title.
	p := &validationPhase{
		agent: &verifier.Agent{},
		caps:  verifier.RunCaps{MaxRecommendationsPerRun: 0},
	}
	recs := []models.Recommendation{
		{ID: "r1", Title: "Reduce checkout funnel friction"},
	}

	results := p.validateRecommendations(context.Background(), recs, nil, nil)

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if got := results[0].ClaimedMetric; got != "Reduce checkout funnel friction" {
		t.Errorf("ClaimedMetric = %q, want the recommendation Title (empty label is the #279 bug)", got)
	}
	if results[0].Status != string(valmodels.StatusSkippedBudgetCap) {
		t.Errorf("Status = %q, want %q", results[0].Status, valmodels.StatusSkippedBudgetCap)
	}
}
