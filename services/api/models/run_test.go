package models

import "testing"

// TestRunPhaseAcceptsPause pins the phase names, because this package cannot
// import the agent's models.Phase* constants and a rename there would
// otherwise silently narrow or widen where a run can be paused (issue #508).
//
// The two halves fail for different reasons. A phase wrongly in the set means
// a pause is accepted where nothing is checkpointed, so the agent never
// honours it and the operator watches a "Pausing…" that cannot resolve. A
// phase wrongly out of it means Pause is refused during work that IS
// checkpointed — the feature not working, with a plausible-looking 409.
func TestRunPhaseAcceptsPause(t *testing.T) {
	// Everything up to and including analysis has a checkpoint behind it:
	// exploration checkpoints every step, and analysis every completed area.
	for _, phase := range []string{"", "init", "schema_discovery", "exploration", "analysis"} {
		if !RunPhaseAcceptsPause(phase) {
			t.Errorf("RunPhaseAcceptsPause(%q) = false, want true", phase)
		}
	}
	// Past that nothing is checkpointed. The recommendation phases would be
	// discarded wholesale, and the persist tail owes durability for work
	// already done — it is outside the pause window permanently.
	for _, phase := range []string{"validation", "recommendations", "saving", "embed_index", "questions", "reflection", "complete"} {
		if RunPhaseAcceptsPause(phase) {
			t.Errorf("RunPhaseAcceptsPause(%q) = true, want false", phase)
		}
	}
	// An unknown phase is refused rather than accepted. A phase this build
	// has never heard of is one whose checkpointing it cannot vouch for, and
	// the fail-safe direction is to decline the pause and say so.
	if RunPhaseAcceptsPause("some_future_phase") {
		t.Error("an unrecognised phase was accepted for pausing; the safe default is to refuse")
	}
}

// TestRunStatusPaused pins the literal, which the database layer writes into
// Mongo filters and the dashboard compares against.
func TestRunStatusPaused(t *testing.T) {
	if RunStatusPaused != "paused" {
		t.Errorf("RunStatusPaused = %q, want \"paused\"", RunStatusPaused)
	}
}
