package discovery

import (
	"context"
	"testing"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// When the validation agent is nil (project toggle off, no aiClient, or
// no schema provider), validateInsights must stamp every insight with
// combined=validation_disabled AND backfill the legacy Status field —
// the latter so consumers reading the pre-plan shape (dashboard list
// rendering, etc.) still see the verdict instead of an empty pill.
// Codex prod-r1 flagged the missing Status backfill as a MEDIUM.
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
