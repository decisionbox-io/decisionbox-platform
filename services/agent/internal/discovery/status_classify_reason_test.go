package discovery

import (
	"strings"
	"testing"
)

// TestClassifyExplorationStep_NamesTheRuleThatRefused covers what an operator
// reads when a run stops early. A cube-reaching run ignores --min-steps
// entirely, so a log line blaming the floor sends them to a setting that had
// no part in the decision — and, worse, invites them to change it.
func TestClassifyExplorationStep_NamesTheRuleThatRefused(t *testing.T) {
	_, msg := classifyExplorationStep("complete_rejected", 7, "",
		"rejected premature completion (recent steps were still finding new ground; 1 of the last steps repeated earlier work)")
	if strings.Contains(msg, "min-steps") {
		t.Errorf("a novelty refusal was reported as a min-steps rejection: %s", msg)
	}
	if !strings.Contains(msg, "still finding new ground") {
		t.Errorf("the engine's reason did not reach the run log: %s", msg)
	}
	if !strings.Contains(msg, "Step 7") {
		t.Errorf("the step number was lost: %s", msg)
	}
}

// TestClassifyExplorationStep_FloorRejectionsKeepTheirLegacyMessage pins the
// fallback for a step recorded without a reason, so an older run still renders
// the way it always did.
func TestClassifyExplorationStep_FloorRejectionsKeepTheirLegacyMessage(t *testing.T) {
	stepType, msg := classifyExplorationStep("complete_rejected", 3, "", "")
	if stepType != "complete_rejected" {
		t.Errorf("step type changed to %q", stepType)
	}
	if !strings.Contains(msg, "min-steps floor") {
		t.Errorf("a reasonless rejection lost its message: %s", msg)
	}
}

// TestClassifyExplorationStep_GetCorrelationsIsNotAQuery: the default arm
// renders anything it does not recognise as a query step, so a new action that
// never reaches the switch is mislabelled on the live dashboard — a step that
// ran no SQL, shown as one that did.
func TestClassifyExplorationStep_GetCorrelationsIsNotAQuery(t *testing.T) {
	stepType, msg := classifyExplorationStep("get_correlations", 4, "check what was reviewed first", "")
	if stepType != "get_correlations" {
		t.Errorf("stepType = %q, want get_correlations", stepType)
	}
	if !strings.Contains(msg, "(get_correlations)") {
		t.Errorf("msg = %q, want it to name the action", msg)
	}
	if !strings.Contains(msg, "check what was reviewed first") {
		t.Errorf("msg = %q, want the model's thinking kept", msg)
	}
}
