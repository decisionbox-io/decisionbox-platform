package discovery

import (
	"strings"
	"testing"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// anyRecCitesEligible gates the citation self-heal re-prompt: it must be true
// when any rec cites a real eligible id (big-model path → no re-prompt) and
// false only when the whole batch is uncited (the small/open-model trigger).
func TestAnyRecCitesEligible(t *testing.T) {
	elig := []models.Insight{{ID: "a-id"}, {ID: "b-id"}}
	cited := []models.Recommendation{{RelatedInsightIDs: []string{"slug:x"}}, {RelatedInsightIDs: []string{"b-id"}}}
	if !anyRecCitesEligible(cited, elig) {
		t.Errorf("expected true when one rec cites b-id")
	}
	uncited := []models.Recommendation{{RelatedInsightIDs: []string{"slug:x"}}, {RelatedInsightIDs: nil}}
	if anyRecCitesEligible(uncited, elig) {
		t.Errorf("expected false when no rec cites an eligible id")
	}
	if anyRecCitesEligible(nil, elig) {
		t.Errorf("expected false for no recs")
	}
}

// The citation repair suffix must list the exact eligible UUIDs so the model
// can copy them verbatim.
func TestRecommendationCitationRepairSuffix_ListsEligibleIDs(t *testing.T) {
	insights := []models.Insight{{ID: "6e9261f5-c4ec-404b-bdf0-760a4644f384"}, {ID: ""}, {ID: "02665b9e-468f-41eb-b50e-28702b95e999"}}
	s := recommendationCitationRepairSuffix(insights)
	if !strings.Contains(s, "6e9261f5-c4ec-404b-bdf0-760a4644f384") || !strings.Contains(s, "02665b9e-468f-41eb-b50e-28702b95e999") {
		t.Errorf("suffix missing eligible ids: %q", s)
	}
	if !strings.Contains(s, "related_insight_ids") {
		t.Errorf("suffix should name the field: %q", s)
	}
}

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
