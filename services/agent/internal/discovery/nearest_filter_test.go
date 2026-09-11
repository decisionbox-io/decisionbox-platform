package discovery

import (
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	pb "github.com/qdrant/go-client/qdrant"
)

// The filter deciding which earlier steps a step may be judged against.
//
// Its Qdrant SEMANTICS are proved against a real server in
// run_step_nearest_integration_test.go — whether a condition actually excludes
// what it claims to is a question only the store can answer. What is pinned
// here is the intent, because that lane is not one CI runs and a filter is an
// easy place to quietly drop a clause.

// conditionFields returns the payload keys a filter's Must conditions match
// on, so a test can assert which clauses are present without depending on the
// protobuf's nesting.
func conditionFields(f *pb.Filter) []string {
	var out []string
	for _, c := range f.GetMust() {
		if fc := c.GetField(); fc != nil {
			out = append(out, fc.GetKey())
		}
		if c.GetIsEmpty() != nil {
			out = append(out, c.GetIsEmpty().GetKey())
		}
	}
	return out
}

func hasField(f *pb.Filter, key string) bool {
	for _, k := range conditionFields(f) {
		if k == key {
			return true
		}
	}
	return false
}

// TestNearestFilter_ExcludesFailedSteps pins the clause without which a retry
// of a broken query is scored against the failure it retries — reporting a run
// as covered on the strength of queries that never returned anything.
func TestNearestFilter_ExcludesFailedSteps(t *testing.T) {
	f := nearestFilter(models.ExplorationStep{Step: 2, WarehouseID: "wh-a"})
	if !hasField(f, "has_error") {
		t.Errorf("the filter does not exclude failed steps: fields = %v", conditionFields(f))
	}
}

// TestNearestFilter_ScopesToTheDatasourceQueried pins the clause without which
// the same question asked of a second source counts as a repeat. A
// multi-datasource run asks parallel questions across its sources on purpose,
// so a run doing exactly what it should would read as repeating itself.
func TestNearestFilter_ScopesToTheDatasourceQueried(t *testing.T) {
	f := nearestFilter(models.ExplorationStep{Step: 2, WarehouseID: "wh-a"})
	if !hasField(f, "warehouse_id") {
		t.Errorf("the filter is not scoped to a datasource: fields = %v", conditionFields(f))
	}
}

// TestNearestFilter_ADatasourcelessStepMatchesOnlyOthersWithNone is the strict
// direction, chosen deliberately. The case is unreachable for a judged step —
// the engine stamps the id on every executed query — but a missing neighbour
// costs a longer run, which MaxSteps bounds, while a spurious one ends a run
// early and silently.
func TestNearestFilter_ADatasourcelessStepMatchesOnlyOthersWithNone(t *testing.T) {
	f := nearestFilter(models.ExplorationStep{Step: 2})
	if !hasField(f, "warehouse_id") {
		t.Fatalf("a step with no datasource was compared against every datasource: fields = %v", conditionFields(f))
	}
	var sawIsEmpty bool
	for _, c := range f.GetMust() {
		if e := c.GetIsEmpty(); e != nil && e.GetKey() == "warehouse_id" {
			sawIsEmpty = true
		}
	}
	if !sawIsEmpty {
		t.Error("a step with no datasource should require the neighbour to have none either")
	}
}

// TestNearestFilter_KeepsBothClausesTogether guards the likeliest regression:
// one clause surviving a later edit while the other is dropped. Each alone
// leaves a failure mode the other does not cover.
func TestNearestFilter_KeepsBothClausesTogether(t *testing.T) {
	for _, step := range []models.ExplorationStep{
		{Step: 1, WarehouseID: "wh-a"},
		{Step: 1},
	} {
		f := nearestFilter(step)
		if got := len(f.GetMust()); got != 2 {
			t.Errorf("warehouse %q: filter has %d conditions, want the error and datasource clauses both: %v",
				step.WarehouseID, got, conditionFields(f))
		}
	}
}
