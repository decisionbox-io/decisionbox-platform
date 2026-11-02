package server

import (
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// TestReservationOutcomeFor_DoesNotCreditAResumeToTheAttemptItSuperseded is
// the misattribution the resume path's own comment says must not happen.
//
// The resume confirms the superseded attempt's reservation itself, as a
// failure, and only leaves the id behind when that confirm failed — so this
// background retry is the recovery path. Deriving the outcome from the run
// document there would close the DEAD attempt's reservation with the LIVE
// attempt's result: a failed attempt recorded as a success, against a
// reservation it did not make.
//
// What makes it knowable is that a resume opens no reservation of its own, so
// a reservation still on a run past its first attempt can only belong to an
// earlier one.
func TestReservationOutcomeFor_DoesNotCreditAResumeToTheAttemptItSuperseded(t *testing.T) {
	resumedAt := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	completedAt := resumedAt.Add(2 * time.Hour)

	got := reservationOutcomeFor(&models.DiscoveryRun{
		ID:     "run-1",
		Status: "completed", // the RESUMED attempt succeeded
		// The reservation the FIRST attempt opened, still on the run
		// because the resume's confirm of it failed. Always present here:
		// ListTerminalWithReservation is what selects these runs.
		PolicyReservationID: "res-attempt-1",
		Attempt:             2,
		LastResumedAt:       &resumedAt,
		CompletedAt:         &completedAt,
	})

	if got.Status == "success" {
		t.Error("the superseded attempt's reservation was closed as a success the resumed attempt earned")
	}
	if got.Status != "failure" {
		t.Errorf("status = %q, want failure", got.Status)
	}
	if got.Error == "" {
		t.Error("the outcome says nothing about why the attempt ended")
	}
	// The moment it was superseded is the closest thing to its end time; the
	// resumed attempt's completion time belongs to the resumed attempt.
	if !got.EndedAt.Equal(resumedAt) {
		t.Errorf("EndedAt = %v, want the resume time %v", got.EndedAt, resumedAt)
	}
}

// TestReservationOutcomeFor_FirstAttemptKeepsItsOwnOutcome is the ordinary
// case, which must be untouched: the reservation belongs to the only attempt
// there has ever been, so the run document IS its outcome.
func TestReservationOutcomeFor_FirstAttemptKeepsItsOwnOutcome(t *testing.T) {
	completedAt := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name       string
		status     string
		attempt    int
		runErr     string
		wantStatus string
		wantErr    string
	}{
		{"completed", "completed", 1, "", "success", ""},
		{"failed", "failed", 1, "exploration died", "failure", "exploration died"},
		{"cancelled", "cancelled", 1, "", "cancelled", ""},
		// A run created before the attempt counter existed reads as 0 and is
		// on its first attempt by definition.
		{"legacy run with no attempt", "completed", 0, "", "success", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reservationOutcomeFor(&models.DiscoveryRun{
				ID: "run-1", Status: tc.status, Attempt: tc.attempt,
				PolicyReservationID: "res-run-1",
				Error:               tc.runErr, CompletedAt: &completedAt,
			})
			if got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.Error != tc.wantErr {
				t.Errorf("error = %q, want %q", got.Error, tc.wantErr)
			}
			if !got.EndedAt.Equal(completedAt) {
				t.Errorf("EndedAt = %v, want %v", got.EndedAt, completedAt)
			}
		})
	}
}

// TestReservationOutcomeFor_SupersededWithNoResumeTimeStillEnds covers the
// run whose last_resumed_at predates the field: the outcome must still carry
// SOME end time rather than a zero one the control plane cannot interpret.
func TestReservationOutcomeFor_SupersededWithNoResumeTimeStillEnds(t *testing.T) {
	completedAt := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	got := reservationOutcomeFor(&models.DiscoveryRun{
		ID: "run-1", Status: "failed", Attempt: 3,
		PolicyReservationID: "res-attempt-1", CompletedAt: &completedAt,
	})
	if got.Status != "failure" {
		t.Errorf("status = %q, want failure", got.Status)
	}
	if got.EndedAt.IsZero() {
		t.Error("EndedAt is zero; the control plane has no end time to record")
	}
}
