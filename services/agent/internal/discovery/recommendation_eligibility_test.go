package discovery

import (
	"testing"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func ins(c valmodels.Status) models.Insight {
	return models.Insight{Validation: &valmodels.InsightValidation{Combined: c}}
}

// setOf mirrors how RunDiscovery builds o.recommendationVerdicts.
func setOf(ss ...valmodels.Status) map[valmodels.Status]bool {
	m := make(map[valmodels.Status]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}

// TestFilterEligibleInsights_HonorsConfiguredSet proves the per-project
// recommendation_verdicts setting actually widens/narrows the recommender
// input, using the exact verdict distribution observed in the live Kimi run
// (supported:3, partial:7, unverifiable:11, rejected:1 = 22 insights).
func TestFilterEligibleInsights_HonorsConfiguredSet(t *testing.T) {
	var all []models.Insight
	add := func(c valmodels.Status, n int) {
		for i := 0; i < n; i++ {
			all = append(all, ins(c))
		}
	}
	add(valmodels.StatusSupported, 3)
	add(valmodels.StatusPartial, 7)
	add(valmodels.StatusUnverifiable, 11)
	add(valmodels.StatusRejected, 1)

	cases := []struct {
		name     string
		eligible map[valmodels.Status]bool
		want     int
	}{
		{"default confirmed+supported = today", setOf(valmodels.StatusConfirmed, valmodels.StatusSupported), 3},
		{"include partial+unverifiable", setOf(valmodels.StatusSupported, valmodels.StatusPartial, valmodels.StatusUnverifiable), 21},
		{"everything incl rejected", setOf(valmodels.StatusConfirmed, valmodels.StatusSupported, valmodels.StatusPartial, valmodels.StatusUnverifiable, valmodels.StatusRejected), 22},
		{"partial only", setOf(valmodels.StatusPartial), 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(filterEligibleInsights(all, tc.eligible)); got != tc.want {
				t.Errorf("eligible = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestFilterEligibleInsights_FailOpen covers the two states that must stay
// eligible regardless of the configured set: a nil Validation (validation
// emitted nothing) and Combined == validation_disabled (validation off).
// Both mean "no operator verdict", not "excluded".
func TestFilterEligibleInsights_FailOpen(t *testing.T) {
	all := []models.Insight{
		{Validation: nil},
		ins(valmodels.StatusValidationDisabled),
		ins(valmodels.StatusRejected), // not in the set → excluded
	}
	// A set that excludes everything selectable still keeps the two fail-open ones.
	got := filterEligibleInsights(all, setOf(valmodels.StatusConfirmed))
	if len(got) != 2 {
		t.Fatalf("fail-open eligible = %d, want 2 (nil + validation_disabled)", len(got))
	}
}
