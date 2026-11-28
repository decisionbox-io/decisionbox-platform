package discovery

import (
	"testing"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func judged(area string, state valmodels.Status) models.Insight {
	return models.Insight{
		AnalysisArea: area,
		Validation:   &models.InsightValidation{Combined: state},
	}
}

// What counts as "this area had its turn". A finding the consolidation pass
// folded was answered without a check, so its area does not need the budget
// moved towards it; the budget cap is the one state that says the findings
// were never looked at.
func TestJudgedAreasOf_TheBudgetCapIsTheOnlyUnjudgedState(t *testing.T) {
	disc := &models.DiscoveryResult{Insights: []models.Insight{
		judged("stock", valmodels.StatusSkippedBudgetCap),
		judged("margin", valmodels.StatusSkippedDuplicate),
		judged("traffic", valmodels.StatusConfirmed),
		judged("vendors", valmodels.StatusRejected),
		judged("", valmodels.StatusConfirmed),
		{AnalysisArea: "no-validation-block"},
	}}

	got := judgedAreasOf(disc)
	for _, area := range []string{"margin", "traffic", "vendors"} {
		if !got[area] {
			t.Errorf("%s was judged and is not reported as such: %v", area, got)
		}
	}
	for _, area := range []string{"stock", "no-validation-block", ""} {
		if got[area] {
			t.Errorf("%s was never judged and is reported as judged: %v", area, got)
		}
	}
	if judgedAreasOf(nil) != nil {
		t.Error("a nil discovery must report nothing")
	}
}

// The age of each area's last check, counted in discoveries, and which
// discoveries are worth counting: not this run's own partial result, and not a
// run whose every area failed — counting that one would age every area by one
// for a run that never ran.
func TestValidationStaleness_CountsUsableDiscoveriesOnly(t *testing.T) {
	disc := func(id, runID, runType string, ins ...models.Insight) *models.DiscoveryResult {
		return &models.DiscoveryResult{ID: id, RunID: runID, RunType: runType, Insights: ins}
	}
	// Newest first, the order the repository returns.
	recent := []*models.DiscoveryResult{
		disc("d-mine", "run-now", "partial", judged("stock", valmodels.StatusConfirmed)),
		disc("d-failed", "run-x", runTypeFailed),
		disc("d-1", "run-1", "full", judged("stock", valmodels.StatusSkippedBudgetCap), judged("margin", valmodels.StatusSupported)),
		disc("d-2", "run-2", "full", judged("stock", valmodels.StatusConfirmed), judged("margin", valmodels.StatusConfirmed)),
	}

	got := validationStaleness(recent, "run-now")
	if got["margin"] != 0 {
		t.Errorf("margin age = %d, want 0 — judged in the newest usable discovery", got["margin"])
	}
	if got["stock"] != 1 {
		t.Errorf("stock age = %d, want 1 — capped in the newest usable discovery, judged in the one before", got["stock"])
	}
	if got[unjudgedArea] != 2 {
		t.Errorf("window = %d, want 2 usable discoveries (this run's own and the all-failed one do not count)", got[unjudgedArea])
	}
	if validationStaleness(nil, "run-now") != nil {
		t.Error("no history must rank nothing")
	}
	if validationStaleness([]*models.DiscoveryResult{disc("d-mine", "run-now", "full")}, "run-now") != nil {
		t.Error("only this run's own result is no history")
	}
}

// The stalest areas go first and everything else keeps the id order #560
// fixed, so two runs of one project are still comparable.
func TestOrderAreasStalestFirst_StalestFirstThenIDOrder(t *testing.T) {
	areas := []AnalysisArea{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
	staleness := map[string]int{"a": 0, "b": 2, "c": 2, "d": 1, unjudgedArea: 3}

	got := areaIDs(orderAreasStalestFirst(areas, staleness))
	for i, want := range []string{"b", "c", "d", "a"} {
		if got[i] != want {
			t.Fatalf("order = %v, want b,c,d,a — oldest check first, id breaking the b/c tie", got)
		}
	}
	// An area the window never judged is as stale as the window is long.
	withNew := []AnalysisArea{{ID: "a"}, {ID: "new"}}
	if first := areaIDs(orderAreasStalestFirst(withNew, staleness))[0]; first != "new" {
		t.Errorf("first = %q, want the area no discovery in the window judged", first)
	}
	// And the caller's slice is left alone: the restore bookkeeping reads it.
	if areaIDs(areas)[0] != "a" {
		t.Errorf("the input slice was reordered in place: %v", areaIDs(areas))
	}
}

// No history, or a window in which every area was judged equally, leaves the
// order exactly as it arrived.
func TestOrderAreasStalestFirst_NothingToSeparateLeavesIDOrder(t *testing.T) {
	areas := []AnalysisArea{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	for name, staleness := range map[string]map[string]int{
		"nil":             nil,
		"empty":           {},
		"all judged at 0": {"a": 0, "b": 0, "c": 0, unjudgedArea: 1},
		"all unjudged":    {"elsewhere": 0, unjudgedArea: 1},
	} {
		t.Run(name, func(t *testing.T) {
			got := areaIDs(orderAreasStalestFirst(areas, staleness))
			for i, id := range []string{"a", "b", "c"} {
				if got[i] != id {
					t.Fatalf("order = %v, want the arrival order", got)
				}
			}
		})
	}
}

// The rule has to reach every area, which is what the first version of it did
// NOT do: "starved last run first, then by id" re-sorted the starved group by
// id every run, so with three areas and a budget of one it walked a, b, a, b
// and never c. Simulated rather than argued, over several pack sizes and
// budgets, with the ranking recomputed from each run's outcome the way a real
// run recomputes it from the stored history.
func TestOrderAreasStalestFirst_TheBudgetReachesEveryArea(t *testing.T) {
	for _, tc := range []struct{ areas, budget int }{{3, 1}, {3, 2}, {6, 2}, {8, 3}, {5, 5}, {7, 1}} {
		areas := make([]AnalysisArea, 0, tc.areas)
		for i := 0; i < tc.areas; i++ {
			areas = append(areas, AnalysisArea{ID: string(rune('a' + i))})
		}
		// judgedAt[area] = how many runs ago it last had a check.
		judgedAt := map[string]int{}
		reached := map[string]bool{}
		runs := 3 * ((tc.areas + tc.budget - 1) / tc.budget)
		for r := 0; r < runs; r++ {
			staleness := map[string]int{unjudgedArea: r + 1}
			for area, at := range judgedAt {
				staleness[area] = r - at
			}
			for i, a := range orderAreasStalestFirst(areas, staleness) {
				if i >= tc.budget {
					break
				}
				judgedAt[a.ID] = r
				reached[a.ID] = true
			}
		}
		for _, a := range areas {
			if !reached[a.ID] {
				t.Errorf("%d areas, budget %d: %s never reached the budget in %d runs",
					tc.areas, tc.budget, a.ID, runs)
			}
		}
	}
}
