package discovery

import (
	"testing"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func validated(area string, state valmodels.Status) models.Insight {
	return models.Insight{
		AnalysisArea: area,
		Validation:   &models.InsightValidation{Combined: state},
	}
}

// Only the budget-cap state counts as starved. A finding the consolidation
// pass folded was answered, not starved, and an area whose findings were all
// folded must not jump the queue on the strength of that.
func TestStarvedAreasOf_ReadsOnlyTheBudgetCap(t *testing.T) {
	disc := &models.DiscoveryResult{Insights: []models.Insight{
		validated("stock", valmodels.StatusSkippedBudgetCap),
		validated("stock", valmodels.StatusSupported),
		validated("margin", valmodels.StatusSkippedDuplicate),
		validated("traffic", valmodels.StatusConfirmed),
		{AnalysisArea: "", Validation: &models.InsightValidation{Combined: valmodels.StatusSkippedBudgetCap}},
		{AnalysisArea: "no-verdict-at-all"},
	}}

	got := starvedAreasOf(disc)
	if len(got) != 1 || !got["stock"] {
		t.Errorf("starved = %v, want just stock", got)
	}
	if starvedAreasOf(nil) != nil {
		t.Error("a nil discovery must not report a starved area")
	}
}

// The starved areas go first and everything else keeps the id order #560
// fixed, so two runs of one project are still comparable.
func TestOrderAreasStarvedFirst_StablePartition(t *testing.T) {
	areas := []AnalysisArea{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}

	got := areaIDs(orderAreasStarvedFirst(areas, map[string]bool{"c": true, "b": true}))
	want := []string{"b", "c", "a", "d"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	// And the caller's slice is left alone: the restore bookkeeping reads it.
	if areaIDs(areas)[0] != "a" {
		t.Errorf("the input slice was reordered in place: %v", areaIDs(areas))
	}
}

// Nothing starved — the first run of a project, or a run whose budget covered
// everything — leaves the order exactly as it arrived.
func TestOrderAreasStarvedFirst_NothingStarvedChangesNothing(t *testing.T) {
	areas := []AnalysisArea{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	for name, starved := range map[string]map[string]bool{
		"nil":                  nil,
		"empty":                {},
		"naming unknown areas": {"not-in-this-pack": true},
	} {
		t.Run(name, func(t *testing.T) {
			got := areaIDs(orderAreasStarvedFirst(areas, starved))
			for i, id := range []string{"a", "b", "c"} {
				if got[i] != id {
					t.Fatalf("order = %v, want the arrival order", got)
				}
			}
		})
	}
}

// Every area starved is the same as none: the partition has nothing to move,
// and the id order has to survive it.
func TestOrderAreasStarvedFirst_AllStarvedKeepsIDOrder(t *testing.T) {
	areas := []AnalysisArea{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	got := areaIDs(orderAreasStarvedFirst(areas, map[string]bool{"a": true, "b": true, "c": true}))
	for i, id := range []string{"a", "b", "c"} {
		if got[i] != id {
			t.Fatalf("order = %v, want the arrival order", got)
		}
	}
}

// A resumed run must not read its own crashed attempt: that attempt's
// starvation says how far it got, not what the project's last complete run
// managed. Historical documents carry no run id and are never matched.
func TestLastCompleteDiscovery_SkipsThisRunsOwnResult(t *testing.T) {
	mine := &models.DiscoveryResult{ID: "d-mine", RunID: "run-1"}
	theirs := &models.DiscoveryResult{ID: "d-previous", RunID: "run-0"}
	historical := &models.DiscoveryResult{ID: "d-old"}

	if got := lastCompleteDiscovery([]*models.DiscoveryResult{mine, theirs, historical}, "run-1"); got != theirs {
		t.Errorf("got %v, want the previous run's discovery", got)
	}
	if got := lastCompleteDiscovery([]*models.DiscoveryResult{nil, mine}, "run-1"); got != nil {
		t.Errorf("got %v, want nil when only this run's own result is there", got)
	}
	if got := lastCompleteDiscovery([]*models.DiscoveryResult{historical, theirs}, ""); got != historical {
		t.Errorf("got %v, want the newest when this run has no id yet", got)
	}
	if got := lastCompleteDiscovery(nil, "run-1"); got != nil {
		t.Errorf("got %v, want nil for an empty list", got)
	}
}
