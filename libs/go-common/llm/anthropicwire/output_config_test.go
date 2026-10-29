package anthropicwire

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// A model that does not take an effort setting must get no output_config at
// all. Sending the field to a model that predates it is rejected outright, so
// an unconditional emit would break every older deployment in order to
// configure the newer ones.
func TestOutputConfigOmittedForAModelThatDoesNotTakeEffort(t *testing.T) {
	for _, effort := range []string{"", "on", "off", "low", "medium", "high"} {
		if got := OutputConfig(effort, false); got != nil {
			t.Errorf("effort %q on a non-effort model produced %v, want nil", effort, got)
		}
	}
}

// The levels a caller names travel through untouched.
func TestOutputConfigHonoursAnExplicitLevel(t *testing.T) {
	for _, level := range []string{gollm.ReasoningEffortLow, gollm.ReasoningEffortMedium, gollm.ReasoningEffortHigh} {
		got := OutputConfig(level, true)
		if got == nil {
			t.Fatalf("effort %q produced no output_config", level)
		}
		if got["effort"] != level {
			t.Errorf("effort = %v, want %q", got["effort"], level)
		}
	}
}

// No preference, and the "reason, I don't mind how much" sentinel, both take
// the deployment default. Leaving the field off would otherwise mean "whatever
// this model's default happens to be" — which differs between models in one
// family and shifts between releases.
func TestOutputConfigAppliesTheDeploymentDefault(t *testing.T) {
	for _, effort := range []string{gollm.ReasoningEffortDefault, gollm.ReasoningEffortOn} {
		got := OutputConfig(effort, true)
		if got == nil {
			t.Fatalf("effort %q produced no output_config", effort)
		}
		if got["effort"] != gollm.DefaultReasoningEffort {
			t.Errorf("effort %q resolved to %v, want the deployment default %q",
				effort, got["effort"], gollm.DefaultReasoningEffort)
		}
	}
}

// "off" is honoured as the lowest level we can express, not as a disable:
// current reasoning models reject an explicit disable and the whole call would
// fail, where the caller only wanted as little reasoning as possible.
func TestOutputConfigTranslatesOffToTheLowestLevel(t *testing.T) {
	got := OutputConfig(gollm.ReasoningEffortOff, true)
	if got == nil {
		t.Fatal("effort off produced no output_config")
	}
	if got["effort"] != gollm.ReasoningEffortLow {
		t.Errorf("effort off resolved to %v, want %q", got["effort"], gollm.ReasoningEffortLow)
	}
}

// The operator override replaces the default for every layer at once.
func TestOutputConfigRespectsTheOperatorOverride(t *testing.T) {
	t.Setenv(gollm.ReasoningEffortEnvVar, "low")
	got := OutputConfig("", true)
	if got == nil || got["effort"] != gollm.ReasoningEffortLow {
		t.Errorf("override ignored: got %v", got)
	}
}

// A typo in one env var must not fail every LLM call in the deployment.
func TestOutputConfigIgnoresAnUnrecognisedOverride(t *testing.T) {
	t.Setenv(gollm.ReasoningEffortEnvVar, "maximum-effort")
	got := OutputConfig("", true)
	if got == nil || got["effort"] != gollm.DefaultReasoningEffort {
		t.Errorf("bad override not ignored: got %v, want the default %q", got, gollm.DefaultReasoningEffort)
	}
}

// The default is high, deliberately — effort is the only control left on
// models whose thinking cannot be switched off.
func TestDeploymentDefaultIsHigh(t *testing.T) {
	if gollm.DefaultReasoningEffort != gollm.ReasoningEffortHigh {
		t.Errorf("default effort = %q, want high", gollm.DefaultReasoningEffort)
	}
}
