package models

import (
	"reflect"
	"testing"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

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

// Clarifying-questions is default-on: nil resolves to true, and the stored
// value (including an explicit opt-out false) passes through. Mirrors the
// agent-side helper.
func TestProject_EffectiveClarifyingQuestionsEnabled(t *testing.T) {
	if !(&Project{}).EffectiveClarifyingQuestionsEnabled() {
		t.Errorf("EffectiveClarifyingQuestionsEnabled() with nil pointer = false, want true")
	}
	yes, no := true, false
	if !(&Project{ClarifyingQuestionsEnabled: &yes}).EffectiveClarifyingQuestionsEnabled() {
		t.Errorf("EffectiveClarifyingQuestionsEnabled() with *true = false")
	}
	if (&Project{ClarifyingQuestionsEnabled: &no}).EffectiveClarifyingQuestionsEnabled() {
		t.Errorf("EffectiveClarifyingQuestionsEnabled() with *false = true")
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

// Recommendation-eligibility verdicts: unset/empty → default
// {confirmed, supported} (today's filter); a set passes through sanitised.
// Mirrors the agent-side helper.
func TestProject_EffectiveRecommendationVerdicts(t *testing.T) {
	def := []valmodels.Status{valmodels.StatusConfirmed, valmodels.StatusSupported}
	if got := (&Project{}).EffectiveRecommendationVerdicts(); !reflect.DeepEqual(got, def) {
		t.Errorf("nil verdicts = %v, want default %v", got, def)
	}
	got := (&Project{RecommendationVerdicts: []string{"supported", "partial"}}).EffectiveRecommendationVerdicts()
	want := []valmodels.Status{valmodels.StatusSupported, valmodels.StatusPartial}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("set verdicts = %v, want %v", got, want)
	}
}

// Prompt caching resolves from the project field when set, else the legacy
// LLM.Config entry, else on. The legacy fallback matters: the setting existed
// as a provider config key before it was a project field, and a project that
// predates the field must not be silently switched off — nor silently
// switched ON if its config says "false".
func TestProject_EffectivePromptCacheEnabled(t *testing.T) {
	yes, no := true, false
	cfg := func(v string) map[string]string {
		if v == "" {
			return nil
		}
		return map[string]string{"prompt_cache": v}
	}
	cases := []struct {
		name  string
		field *bool
		conf  string
		want  bool
	}{
		{"nothing set — caching is opt-out", nil, "", true},
		{"legacy config false, no field", nil, "false", false},
		{"legacy config FALSE is case-insensitive", nil, "FALSE", false},
		{"legacy config padded false still counts", nil, "  false  ", false},
		{"legacy config true", nil, "true", true},
		{"legacy config junk is not false, so on", nil, "banana", true},
		{"field true wins over legacy false", &yes, "false", true},
		{"field false wins over legacy true", &no, "true", false},
		{"field false with no legacy value", &no, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &Project{PromptCacheEnabled: c.field}
			p.LLM.Config = cfg(c.conf)
			if got := p.EffectivePromptCacheEnabled(); got != c.want {
				t.Errorf("EffectivePromptCacheEnabled() = %v, want %v", got, c.want)
			}
		})
	}
	// A nil project must not panic — callers resolve before knowing whether
	// the project loaded.
	if !(*Project)(nil).EffectivePromptCacheEnabled() {
		t.Error("nil project should resolve to caching on")
	}
}
