package discovery

import (
	"testing"

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
