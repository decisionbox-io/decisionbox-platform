package backfill

import (
	"testing"
	"time"
)

// defaultReflectionBudget mirrors defaultDiscoveryReflectionTimeout in
// services/agent/internal/discovery. It is duplicated rather than imported
// because it is unexported there and this is the only place that needs to know
// it — the two are coupled, and the point of this test is to notice when they
// stop agreeing.
const defaultReflectionBudget = 3 * time.Hour

// TestReflectionStaleAfterExceedsPhaseBudget pins the one relationship that
// makes the in-flight marker safe to ignore.
//
// The backfill skips a project whose ledger is marked as being written, and
// believes that marker for reflectionStaleAfter — a ceiling that exists because
// an agent killed mid-phase never clears its own marker. The ceiling therefore
// has to sit strictly above the longest a legitimate reflection can run, which
// is DISCOVERY_REFLECTION_TIMEOUT. Below it, the backfill would declare a
// running reflection stale and race the finding writes the marker exists to
// protect. When that budget was three minutes an hour was ample slack; it is
// now three hours, so this is worth holding down.
func TestReflectionStaleAfterExceedsPhaseBudget(t *testing.T) {
	if reflectionStaleAfter <= defaultReflectionBudget {
		t.Errorf("reflectionStaleAfter = %v, must exceed the reflection phase's default budget of %v — "+
			"otherwise the backfill treats an in-flight reflection as stale and races it",
			reflectionStaleAfter, defaultReflectionBudget)
	}
}
