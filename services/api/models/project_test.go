package models

import "testing"

// Default-on contract for the per-project validation toggle: legacy
// projects (validation_enabled field missing from Mongo) resolve to
// true so the verifier + refuter pipeline keeps running for them.
func TestProject_EffectiveValidationEnabled_NilIsTrue(t *testing.T) {
	p := &Project{}
	if !p.EffectiveValidationEnabled() {
		t.Errorf("EffectiveValidationEnabled() with nil pointer = false, want true")
	}
}

func TestProject_EffectiveValidationEnabled_PassThrough(t *testing.T) {
	yes, no := true, false
	if !(&Project{ValidationEnabled: &yes}).EffectiveValidationEnabled() {
		t.Errorf("EffectiveValidationEnabled() with *true = false")
	}
	if (&Project{ValidationEnabled: &no}).EffectiveValidationEnabled() {
		t.Errorf("EffectiveValidationEnabled() with *false = true")
	}
}

// Reasoning is opt-in: nil resolves to false (= today), and the stored value
// passes through. Mirrors the agent-side helper.
func TestProject_EffectiveReasoningEnabled(t *testing.T) {
	if (&Project{}).EffectiveReasoningEnabled() {
		t.Errorf("EffectiveReasoningEnabled() with nil pointer = true, want false")
	}
	yes, no := true, false
	if !(&Project{ReasoningEnabled: &yes}).EffectiveReasoningEnabled() {
		t.Errorf("EffectiveReasoningEnabled() with *true = false")
	}
	if (&Project{ReasoningEnabled: &no}).EffectiveReasoningEnabled() {
		t.Errorf("EffectiveReasoningEnabled() with *false = true")
	}
}
