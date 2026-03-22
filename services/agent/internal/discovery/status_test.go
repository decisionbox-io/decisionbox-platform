package discovery

import (
	"context"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
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

func TestStatusReporter_AddInsightStep_NoOp_WhenDisabled(t *testing.T) {
	sr := NewStatusReporter(nil, "", 10)
	// Should not panic when disabled
	sr.AddInsightStep(context.TODO(), "High Churn", "critical", "churn")
}

func TestStatusReporter_AddValidationStep_NoOp_WhenDisabled(t *testing.T) {
	sr := NewStatusReporter(nil, "", 10)
	// Should not panic when disabled
	sr.AddValidationStep(context.TODO(), "affected_count", "confirmed", 2847, 2900)
}

func TestStatusReporter_Complete_NoOp_WhenDisabled(t *testing.T) {
	sr := NewStatusReporter(nil, "", 10)
	// Should not panic when disabled
	sr.Complete(context.TODO(), 5)
}

func TestStatusReporter_Fail_NoOp_WhenDisabled(t *testing.T) {
	sr := NewStatusReporter(nil, "", 10)
	// Should not panic when disabled
	sr.Fail(context.TODO(), "something went wrong")
}

func TestStatusReporter_AddStep_NoOp_WhenDisabled(t *testing.T) {
	sr := NewStatusReporter(nil, "", 10)
	// Should not panic when disabled
	sr.AddStep(context.TODO(), models.RunStep{
		Phase:   models.PhaseExploration,
		Type:    "query",
		Message: "Test step",
	})
}

func TestStatusReporter_Enabled_RequiresBothRepoAndRunID(t *testing.T) {
	// Neither set
	sr1 := NewStatusReporter(nil, "", 10)
	if sr1.enabled() {
		t.Error("should be disabled when both repo and runID are missing")
	}

	// Only runID set (no repo)
	sr2 := NewStatusReporter(nil, "run-123", 10)
	if sr2.enabled() {
		t.Error("should be disabled when repo is nil")
	}

	// Only repo nil, but runID empty
	sr3 := NewStatusReporter(nil, "", 10)
	if sr3.enabled() {
		t.Error("should be disabled when runID is empty")
	}
}

func TestStatusReporter_MaxStepsValues(t *testing.T) {
	tests := []struct {
		name     string
		input    int
		expected int
	}{
		{"zero defaults to 100", 0, 100},
		{"negative defaults to 100", -5, 100},
		{"positive value kept", 50, 50},
		{"one kept", 1, 1},
		{"large value kept", 500, 500},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sr := NewStatusReporter(nil, "", tt.input)
			if sr.maxSteps != tt.expected {
				t.Errorf("maxSteps = %d, want %d", sr.maxSteps, tt.expected)
			}
		})
	}
}

func TestStatusReporter_AllMethods_NoOp_WhenEmptyRunID(t *testing.T) {
	sr := NewStatusReporter(nil, "", 10)
	ctx := context.Background()

	// All of these should be no-ops and not panic
	sr.SetPhase(ctx, models.PhaseSchemaDiscovery, "discovering schemas...", 10)
	sr.AddStep(ctx, models.RunStep{Phase: models.PhaseInit, Type: "info", Message: "starting"})
	sr.AddExplorationStep(ctx, 1, "thinking about retention", "SELECT 1", 5, 100, false, "")
	sr.AddExplorationStep(ctx, 2, "thinking", "SELECT 2", 0, 50, false, "query failed")
	sr.AddAnalysisStep(ctx, "churn", "Churn Risks", 0, "timeout")
	sr.AddInsightStep(ctx, "Revenue Drop", "high", "monetization")
	sr.AddValidationStep(ctx, "affected_count", "adjusted", 500, 350)
	sr.AddValidationStep(ctx, "user_count", "confirmed", 0, 0)
	sr.Complete(ctx, 3)
	sr.Fail(ctx, "catastrophic failure")
}
