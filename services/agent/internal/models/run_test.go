package models

import "testing"

func TestRunStatusConstants(t *testing.T) {
	// Verify constants exist and have expected values
	if RunStatusPending != "pending" {
		t.Error("RunStatusPending should be 'pending'")
	}
	if RunStatusRunning != "running" {
		t.Error("RunStatusRunning should be 'running'")
	}
	if RunStatusCompleted != "completed" {
		t.Error("RunStatusCompleted should be 'completed'")
	}
	if RunStatusFailed != "failed" {
		t.Error("RunStatusFailed should be 'failed'")
	}
}

func TestPhaseConstants(t *testing.T) {
	phases := []string{PhaseInit, PhaseSchemaDiscovery, PhaseExploration, PhaseAnalysis,
		PhaseValidation, PhaseRecommendations, PhaseSaving, PhaseComplete}
	for _, p := range phases {
		if p == "" {
			t.Error("phase constant should not be empty")
		}
	}
}

func TestRunStepTypes(t *testing.T) {
	step := RunStep{
		Phase:       PhaseExploration,
		Type:        "query",
		Message:     "Step 1: checking retention",
		LLMThinking: "I need to check retention rates",
		Query:       "SELECT * FROM sessions",
		RowCount:    100,
	}

	if step.Phase != PhaseExploration {
		t.Error("Phase not set")
	}
	if step.RowCount != 100 {
		t.Error("RowCount not set")
	}
}
