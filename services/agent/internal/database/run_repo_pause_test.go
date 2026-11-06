package database

import (
	"testing"
	"time"
)

// TestPauseIsHonourable covers the three ways a stamped pause request stops
// binding (issue #508). Pure, so it is unit-tested here rather than through a
// Mongo container — the integration tests cover Ownership returning it.
//
// Each case is a way an operator's request must NOT stop a run:
//
//   - the deadline, which is the whole reason a request expires at all. An
//     un-expiring one would let a run pause hours later, unattended, long after
//     the operator gave up watching — a nil deadline means that was switched
//     off deliberately.
//   - the attempt, so a request aimed at the attempt that was running when the
//     operator clicked cannot stop the one a resume started since.
func TestPauseIsHonourable(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)

	for _, tc := range []struct {
		name             string
		requestedAt      *time.Time
		deadlineAt       *time.Time
		requestedAttempt int
		currentAttempt   int
		want             bool
	}{
		{"no request at all", nil, &future, 0, 1, false},
		{"live request on the matching attempt", &past, &future, 1, 1, true},
		{"live request with no attempt recorded", &past, &future, 0, 3, true},
		{"live request aimed at another attempt", &past, &future, 1, 2, false},
		{"lapsed request", &past, &past, 1, 1, false},
		{"no deadline — stands until something acts on it", &past, nil, 1, 1, true},
		// A request stamped with no deadline AND no attempt is what an older
		// API build writes. It still binds: silently ignoring it would make
		// Pause do nothing during a rolling upgrade, with no error anywhere.
		{"legacy stamp — neither deadline nor attempt", &past, nil, 0, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pauseIsHonourable(tc.requestedAt, tc.deadlineAt, tc.requestedAttempt, tc.currentAttempt)
			if got != tc.want {
				t.Errorf("pauseIsHonourable = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPauseIsHonourable_DeadlineBoundaryIsExclusive pins that a request is
// dead ON its deadline rather than one tick after it.
//
// Worth pinning because the comparison reads backwards: the code asks whether
// now is BEFORE the deadline, so `!now.Before(deadline)` is the lapse. An
// accidental `After` would keep a request alive for the instant it expires,
// which is harmless — and the same slip in the other direction would expire
// every request immediately, which is not.
func TestPauseIsHonourable_DeadlineBoundaryIsExclusive(t *testing.T) {
	requested := time.Now().Add(-time.Minute)

	deadline := time.Now().Add(-time.Nanosecond)
	if pauseIsHonourable(&requested, &deadline, 1, 1) {
		t.Error("a request whose deadline has just passed still bound")
	}

	deadline = time.Now().Add(time.Minute)
	if !pauseIsHonourable(&requested, &deadline, 1, 1) {
		t.Error("a request well inside its deadline did not bind")
	}
}
