package models

import (
	"testing"
	"time"
)

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
	// Everything up to and including the recommendation phases has a
	// checkpoint behind it: exploration checkpoints every step; analysis
	// checkpoints every analysed area, every single insight verdict and every
	// completed area; recommendations checkpoints the generated set and every
	// single recommendation verdict.
	for _, phase := range []string{"", "init", "schema_discovery", "exploration", "analysis", "recommendations"} {
		if !RunPhaseAcceptsPause(phase) {
			t.Errorf("RunPhaseAcceptsPause(%q) = false, want true", phase)
		}
	}
	// Past that is the persist tail, which owes durability for work already
	// done and is outside the pause window permanently.
	//
	// `validation` is in this list for a different reason, and it is a trap
	// worth naming: no run document ever reports it. Phase 4.5 and 5.5 stamp
	// it through setLLMPhase, which is layer attribution for cost accounting
	// and does not touch the run's `phase`. If it ever started appearing here,
	// the honest fix is to make it pausable — both validation loops checkpoint
	// every verdict — not to leave a phase the API silently refuses.
	for _, phase := range []string{"validation", "saving", "embed_index", "questions", "reflection", "complete"} {
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

// TestReservationOwnerEndedAt_KnowsAPausedAttemptStopped is a Codex finding.
//
// The chain answers "when did the attempt holding this reservation stop
// running". A paused first attempt sets neither completed_at (it has not
// completed) nor last_resumed_at (it has not been resumed yet), so before
// #508 the chain fell through to the zero time — and the resume then sent a
// year-one EndedAt to the policy checker for the reservation it was closing,
// and preserved that zero so every later retry inherited it.
func TestReservationOwnerEndedAt_KnowsAPausedAttemptStopped(t *testing.T) {
	paused := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
	resumed := time.Date(2026, 3, 14, 14, 0, 0, 0, time.UTC)
	completed := time.Date(2026, 3, 14, 9, 0, 0, 0, time.UTC)
	stamped := time.Date(2026, 3, 14, 8, 0, 0, 0, time.UTC)

	t.Run("a paused attempt stopped when it paused", func(t *testing.T) {
		run := &DiscoveryRun{LastPausedAt: &paused}
		if got := run.ReservationOwnerEndedAt(); !got.Equal(paused) {
			t.Errorf("= %v, want the pause time %v (a zero time bills the reservation from year one)", got, paused)
		}
	})

	t.Run("the pause beats the resume that replaced it", func(t *testing.T) {
		// The attempt stopped when it paused; the resume came afterwards, by
		// however long the operator took to come back — which is the whole
		// point of pausing, so the gap can be hours.
		run := &DiscoveryRun{LastPausedAt: &paused, LastResumedAt: &resumed}
		if got := run.ReservationOwnerEndedAt(); !got.Equal(paused) {
			t.Errorf("= %v, want the pause time %v", got, paused)
		}
	})

	t.Run("the preserved stamp still wins", func(t *testing.T) {
		// Write-once, and the only field that still means what it meant when
		// it was written.
		run := &DiscoveryRun{
			PolicyReservationOwnerEndedAt: &stamped,
			LastPausedAt:                  &paused,
			LastResumedAt:                 &resumed,
		}
		if got := run.ReservationOwnerEndedAt(); !got.Equal(stamped) {
			t.Errorf("= %v, want the preserved stamp %v", got, stamped)
		}
	})

	t.Run("a run that was never paused is unchanged", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			run  *DiscoveryRun
			want time.Time
		}{
			{"resumed", &DiscoveryRun{LastResumedAt: &resumed}, resumed},
			{"completed", &DiscoveryRun{CompletedAt: &completed}, completed},
			{"nothing known", &DiscoveryRun{}, time.Time{}},
		} {
			if got := tc.run.ReservationOwnerEndedAt(); !got.Equal(tc.want) {
				t.Errorf("%s: = %v, want %v", tc.name, got, tc.want)
			}
		}
	})
}
