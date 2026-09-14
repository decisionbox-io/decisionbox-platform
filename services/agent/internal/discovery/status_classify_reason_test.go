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
