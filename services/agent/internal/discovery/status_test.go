package discovery

import (
	"context"
	"testing"
)

func TestNewStatusReporter_Defaults(t *testing.T) {
	sr := NewStatusReporter(nil, "", 0)
	if sr.maxSteps != 100 {
		t.Errorf("maxSteps = %d, want 100 (default)", sr.maxSteps)
	}
}

func TestNewStatusReporter_CustomMaxSteps(t *testing.T) {
	sr := NewStatusReporter(nil, "", 50)
	if sr.maxSteps != 50 {
		t.Errorf("maxSteps = %d, want 50", sr.maxSteps)
	}
}

func TestNewStatusReporter_NegativeMaxSteps(t *testing.T) {
	sr := NewStatusReporter(nil, "", -10)
	if sr.maxSteps != 100 {
		t.Errorf("maxSteps = %d, want 100 (default for negative)", sr.maxSteps)
	}
}

func TestStatusReporter_EnabledWithRunID(t *testing.T) {
	sr := NewStatusReporter(nil, "run-123", 10)
	// enabled() requires both repo and runID — no repo means disabled
	if sr.enabled() {
		t.Error("should be disabled when repo is nil")
	}
}

func TestStatusReporter_DisabledWithEmptyRunID(t *testing.T) {
	sr := NewStatusReporter(nil, "", 10)
	if sr.enabled() {
		t.Error("should be disabled when runID is empty")
	}
}

func TestStatusReporter_SetPhase_NoOp_WhenDisabled(t *testing.T) {
	sr := NewStatusReporter(nil, "", 10)
	// Should not panic when disabled
	sr.SetPhase(context.TODO(), "exploration", "testing", 50)
}

func TestStatusReporter_AddExplorationStep_NoOp_WhenDisabled(t *testing.T) {
	sr := NewStatusReporter(nil, "", 10)
	// Should not panic when disabled
	sr.AddExplorationStep(context.TODO(), 1, "thinking", "SELECT 1", 10, 100, false, "")
}

func TestStatusReporter_AddAnalysisStep_NoOp_WhenDisabled(t *testing.T) {
	sr := NewStatusReporter(nil, "", 10)
	// Should not panic when disabled
	sr.AddAnalysisStep(context.TODO(), "churn", "Churn Risks", 3, "")
}
