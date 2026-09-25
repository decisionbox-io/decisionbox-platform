package discovery

import (
	"encoding/json"
	"testing"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func step4Insight(claims ...models.QuantifierClaim) models.Insight {
	return models.Insight{Name: "Tables", SourceSteps: []int{4}, QuantifierClaims: claims}
}

func step4ByID() map[int]*models.ExplorationStep {
	return map[int]*models.ExplorationStep{
		4: {Step: 4, QueryResult: superstoreStep4().Rows},
	}
}

func TestAttachQuantifierVerdicts_RecordsTheRefutation(t *testing.T) {
	ins := []models.Insight{step4Insight(models.QuantifierClaim{
		Claim: "the only top-10 revenue line running a loss", Kind: QuantifierOnly,
		Step: 4, Filter: "profit < 0", TopN: 10, TopNColumn: "sales",
	})}
	attachQuantifierVerdicts(ins, step4ByID())
	if len(ins[0].QuantifierVerdicts) != 1 {
		t.Fatalf("got %d verdicts, want 1", len(ins[0].QuantifierVerdicts))
	}
	if ins[0].QuantifierVerdicts[0].Status != QuantifierFails {
		t.Errorf("status = %q, want fails", ins[0].QuantifierVerdicts[0].Status)
	}
}

// A claim citing a step the insight does not list as evidence cannot be
// settled from that insight's evidence, and must not be reported as false.
func TestAttachQuantifierVerdicts_OnlyUsesCitedSteps(t *testing.T) {
	ins := []models.Insight{{
		Name: "x", SourceSteps: []int{9},
		QuantifierClaims: []models.QuantifierClaim{{Kind: QuantifierOnly, Step: 4, Filter: "profit < 0"}},
	}}
	attachQuantifierVerdicts(ins, step4ByID())
	// Fatalf rather than indexing straight in: a panic here aborts the test
	// binary and hides every case after it, which is exactly what it did the
	// first time this suite was proven against a stub.
	if len(ins[0].QuantifierVerdicts) != 1 {
		t.Fatalf("got %d verdicts, want 1", len(ins[0].QuantifierVerdicts))
	}
	if ins[0].QuantifierVerdicts[0].Status != QuantifierUndecidable {
		t.Errorf("status = %q, want undecidable", ins[0].QuantifierVerdicts[0].Status)
	}
}

// The model has just been told to emit quantifier_claims; emitting verdicts
// beside them is an obvious next step, and they must not survive.
func TestAttachQuantifierVerdicts_DiscardsModelAuthoredVerdicts(t *testing.T) {
	ins := []models.Insight{{
		Name: "x",
		QuantifierVerdicts: []models.QuantifierVerdict{
			{Claim: "I checked this myself", Status: QuantifierHolds},
		},
	}}
	attachQuantifierVerdicts(ins, step4ByID())
	if len(ins[0].QuantifierVerdicts) != 0 {
		t.Errorf("model-authored verdicts survived: %+v", ins[0].QuantifierVerdicts)
	}
}

// A refuted quantifier claim must not make an insight ineligible for
// recommendations. The moment it does, the verdict is a rejection whatever it
// is called, and the two advisory layers stop being advisory.
func TestQuantifierVerdictsDoNotGateRecommendationEligibility(t *testing.T) {
	refuted := models.Insight{
		Name: "refuted but eligible",
		QuantifierVerdicts: []models.QuantifierVerdict{
			{Claim: "the only loss-making line", Status: QuantifierFails, Reason: "2 of 10 rows"},
		},
	}
	// Eligibility is decided from Validation alone; this insight has none, so
	// it passes the filter exactly as it did before quantifier claims existed.
	eligible := map[valmodels.Status]bool{valmodels.StatusSupported: true, valmodels.StatusConfirmed: true}
	got := filterEligibleInsights([]models.Insight{refuted}, eligible)
	if len(got) != 1 {
		t.Fatalf("a refuted quantifier claim removed the insight from the eligible set: got %d, want 1", len(got))
	}

	// And a failing verdict must not flip an otherwise-eligible validated
	// insight out either.
	validated := refuted
	validated.Validation = &models.InsightValidation{Combined: valmodels.StatusSupported}
	if got := filterEligibleInsights([]models.Insight{validated}, eligible); len(got) != 1 {
		t.Errorf("a refuted claim dropped a supported insight: got %d, want 1", len(got))
	}
}

// The model must not be able to author its own pass through the JSON tag.
func TestEvidenceChecksTagIsNotAKeyTheModelIsToldToEmit(t *testing.T) {
	var ins models.Insight
	// A model that has been asked for `quantifier_claims` volunteering the
	// sibling key it would naturally guess.
	raw := []byte(`{"name":"x","quantifier_verdicts":[{"claim":"self-certified","status":"holds"}]}`)
	if err := json.Unmarshal(raw, &ins); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(ins.QuantifierVerdicts) != 0 {
		t.Errorf("a model-emitted quantifier_verdicts key decoded into the derived field: %+v", ins.QuantifierVerdicts)
	}
}
