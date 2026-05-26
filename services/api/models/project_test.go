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
