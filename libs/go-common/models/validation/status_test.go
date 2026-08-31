package validation

import (
	"reflect"
	"testing"
)

// TestParseStatuses covers the sanitiser for the per-project
// recommendation-eligibility setting: case-insensitive, trims space,
// keeps only the five user-selectable per-claim verdicts, drops unknowns
// and the internal "agent never ran" states, and de-dups while
// preserving first-seen order.
func TestParseStatuses(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []Status
	}{
		{"nil", nil, []Status{}},
		{"empty", []string{}, []Status{}},
		{
			"canonical five",
			[]string{"confirmed", "supported", "partial", "unverifiable", "rejected"},
			[]Status{StatusConfirmed, StatusSupported, StatusPartial, StatusUnverifiable, StatusRejected},
		},
		{
			"case + whitespace",
			[]string{" Supported ", "PARTIAL"},
			[]Status{StatusSupported, StatusPartial},
		},
		{
			"drops unknowns and internal states",
			[]string{"supported", "validation_disabled", "skipped_budget_cap", "bogus", ""},
			[]Status{StatusSupported},
		},
		{
			"de-dups preserving order",
			[]string{"partial", "partial", "supported"},
			[]Status{StatusPartial, StatusSupported},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseStatuses(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseStatuses(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestDefaultRecommendationVerdicts pins the unset-project default to the
// historical hardcoded filter (IsTerminalPositive) so legacy projects are
// byte-identical: only confirmed + supported flow to the recommender.
func TestDefaultRecommendationVerdicts(t *testing.T) {
	got := DefaultRecommendationVerdicts()
	want := []Status{StatusConfirmed, StatusSupported}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultRecommendationVerdicts() = %v, want %v", got, want)
	}
	for _, s := range got {
		if !s.IsTerminalPositive() {
			t.Errorf("default verdict %q is not terminal-positive — diverges from historical filter", s)
		}
	}
}
