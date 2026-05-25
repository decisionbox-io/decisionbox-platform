package validation

import (
	"fmt"
	"testing"
)

// allStatuses enumerates every Status the matrix considers. The test
// iterates all 7×7 verifier-vs-refuter combinations × 2 refuterDisabled
// values = 98 cells, plus the nil edge cases.
var allStatuses = []Status{
	StatusConfirmed,
	StatusSupported,
	StatusRejected,
	StatusPartial,
	StatusUnverifiable,
	StatusValidationDisabled,
	StatusSkippedBudgetCap,
}

// TestCombineMatrix exhausts every cell of the 7×7×2 = 98-cell truth
// table. The expected outcome is derived from the rules in plan
// §"Combine() — full 7-status matrix":
//
//  1. Verifier trip-wires (validation_disabled, skipped_budget_cap)
//     short-circuit to themselves.
//  2. Either side's `rejected` short-circuits the combined to
//     `rejected`.
//  3. Verifier `unverifiable` → combined unverifiable.
//  4. Verifier `partial` → combined partial.
//  5. Verifier confirmed/supported is upgraded/downgraded based on
//     the refuter's "ok" vs "incomplete" state. Disabled-by-config
//     refuter counts as OK; nil-when-enabled counts as incomplete.
func TestCombineMatrix(t *testing.T) {
	for _, vs := range allStatuses {
		for _, rs := range allStatuses {
			for _, refDisabled := range []bool{false, true} {
				v := &StructuredVerdict{Overall: vs}
				r := &StructuredVerdict{Overall: rs}
				want := expectedCombined(vs, rs, refDisabled)
				got, gotDisabled := Combine(v, r, refDisabled)
				if got != want {
					t.Errorf("Combine(v=%s, r=%s, refDis=%v) = %s, want %s",
						vs, rs, refDisabled, got, want)
				}
				if gotDisabled != refDisabled {
					t.Errorf("Combine(v=%s, r=%s, refDis=%v) returned refDisabled=%v, want %v",
						vs, rs, refDisabled, gotDisabled, refDisabled)
				}
			}
		}
	}
}

// expectedCombined is the same 7-status logic Combine encodes,
// re-expressed in test code so the matrix has an independent oracle.
// If the implementation drifts from the spec one will diverge from the
// other.
func expectedCombined(vs, rs Status, refDisabled bool) Status {
	if vs == StatusValidationDisabled {
		return StatusValidationDisabled
	}
	if vs == StatusSkippedBudgetCap {
		return StatusSkippedBudgetCap
	}
	if vs == StatusRejected {
		return StatusRejected
	}
	if rs == StatusRejected {
		return StatusRejected
	}

	rOK := refDisabled || rs == StatusConfirmed || rs == StatusSupported

	switch vs {
	case StatusUnverifiable:
		return StatusUnverifiable
	case StatusPartial:
		return StatusPartial
	case StatusConfirmed:
		if rOK {
			return StatusConfirmed
		}
		return StatusSupported
	case StatusSupported:
		if rOK {
			return StatusSupported
		}
		return StatusPartial
	}
	return StatusUnverifiable
}

// TestCombineNilVerifier — when the verifier produces no verdict
// (transport failure before any chat), Combine collapses to
// unverifiable. The refuter side, regardless of state, cannot rescue
// an absent verifier.
func TestCombineNilVerifier(t *testing.T) {
	for _, rs := range allStatuses {
		for _, refDisabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("rs=%s/refDisabled=%v", rs, refDisabled), func(t *testing.T) {
				r := &StructuredVerdict{Overall: rs}
				got, gotDisabled := Combine(nil, r, refDisabled)
				if got != StatusUnverifiable {
					t.Errorf("Combine(nil, r=%s, %v) = %s, want unverifiable", rs, refDisabled, got)
				}
				if gotDisabled != refDisabled {
					t.Errorf("refDisabled not preserved: got %v want %v", gotDisabled, refDisabled)
				}
			})
		}
	}
}

// TestCombineNilRefuterEnabled — refuter expected but missing (e.g.
// chat transport error). The verifier's confirmed/supported state is
// downgraded one notch; unverifiable/partial/rejected stay as-is.
func TestCombineNilRefuterEnabled(t *testing.T) {
	cases := []struct {
		v    Status
		want Status
	}{
		{StatusConfirmed, StatusSupported},      // refuter incomplete penalises confirmed → supported
		{StatusSupported, StatusPartial},         // refuter incomplete penalises supported → partial
		{StatusUnverifiable, StatusUnverifiable}, // verifier already weak
		{StatusPartial, StatusPartial},           // verifier already weak
		{StatusRejected, StatusRejected},         // counter-evidence stands
		{StatusValidationDisabled, StatusValidationDisabled},
		{StatusSkippedBudgetCap, StatusSkippedBudgetCap},
	}
	for _, c := range cases {
		t.Run(string(c.v), func(t *testing.T) {
			v := &StructuredVerdict{Overall: c.v}
			got, _ := Combine(v, nil, false /* refuterDisabled */)
			if got != c.want {
				t.Errorf("Combine(%s, nil, false) = %s, want %s", c.v, got, c.want)
			}
		})
	}
}

// TestCombineNilRefuterDisabled — refuter intentionally off, so
// missing-refuter is NOT a penalty. confirmed stays confirmed,
// supported stays supported.
func TestCombineNilRefuterDisabled(t *testing.T) {
	cases := []struct {
		v    Status
		want Status
	}{
		{StatusConfirmed, StatusConfirmed},
		{StatusSupported, StatusSupported},
		{StatusUnverifiable, StatusUnverifiable},
		{StatusPartial, StatusPartial},
		{StatusRejected, StatusRejected},
		{StatusValidationDisabled, StatusValidationDisabled},
		{StatusSkippedBudgetCap, StatusSkippedBudgetCap},
	}
	for _, c := range cases {
		t.Run(string(c.v), func(t *testing.T) {
			v := &StructuredVerdict{Overall: c.v}
			got, gotDisabled := Combine(v, nil, true /* refuterDisabled */)
			if got != c.want {
				t.Errorf("Combine(%s, nil, true) = %s, want %s", c.v, got, c.want)
			}
			if !gotDisabled {
				t.Errorf("refuterDisabled should round-trip true; got false")
			}
		})
	}
}

// TestCombineMatrixCount sanity-checks that we walk exactly the
// matrix size the plan documents (98 cells), so a future Status added
// without updating allStatuses won't silently shrink the test.
func TestCombineMatrixCount(t *testing.T) {
	if got := len(allStatuses); got != 7 {
		t.Fatalf("allStatuses has %d entries; expected 7 — every new Status must be added to keep matrix coverage", got)
	}
	if got := len(allStatuses) * len(allStatuses) * 2; got != 98 {
		t.Fatalf("matrix size = %d cells; expected 98", got)
	}
}

// TestStatusIsKnown — finaliser uses Status.IsKnown() to reject
// typoed per-claim statuses. Every defined Status must report known;
// unknown strings must report unknown.
func TestStatusIsKnown(t *testing.T) {
	for _, s := range allStatuses {
		if !s.IsKnown() {
			t.Errorf("Status(%q).IsKnown() = false, want true", s)
		}
	}
	for _, s := range []Status{"", "spported", "ok", "yes", "unkown"} {
		if s.IsKnown() {
			t.Errorf("Status(%q).IsKnown() = true, want false (typo)", s)
		}
	}
}

// TestStatusIsTerminalPositive — only confirmed and supported are
// terminal-positive (eligible to flow to the recommender).
func TestStatusIsTerminalPositive(t *testing.T) {
	yes := map[Status]bool{StatusConfirmed: true, StatusSupported: true}
	for _, s := range allStatuses {
		want := yes[s]
		if got := s.IsTerminalPositive(); got != want {
			t.Errorf("Status(%q).IsTerminalPositive() = %v, want %v", s, got, want)
		}
	}
}
