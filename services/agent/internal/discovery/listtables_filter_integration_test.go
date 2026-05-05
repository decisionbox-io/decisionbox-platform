package discovery

import (
	"context"
	"errors"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// TestSchemaDiscovery_AppliesListTablesFilter is the Phase 0 integration
// guard: a registered ListTables filter must observe the warehouse's full
// table list and shrink the per-table discovery loop accordingly.
func TestSchemaDiscovery_AppliesListTablesFilter(t *testing.T) {
	resetListTablesFiltersForTest()
	defer resetListTablesFiltersForTest()

	wh := testutil.NewMockWarehouseProvider("dbo")
	wh.Tables = []string{"orders", "customers", "events_v1", "events_v2"}

	// Register a deny-list filter that drops the deprecated table.
	var seenInput []string
	RegisterListTablesFilter("test-deny-events-v1", func(_ context.Context, projectID, dataset string, in []string) ([]string, error) {
		seenInput = append([]string(nil), in...)
		out := in[:0:0]
		for _, t := range in {
			if t != "events_v1" {
				out = append(out, t)
			}
		}
		return out, nil
	})

	sd := newSchemaDiscoveryForTest(t, wh)
	got, err := sd.DiscoverSchemas(context.Background())
	if err != nil {
		t.Fatalf("DiscoverSchemas: %v", err)
	}

	// Filter saw the full list; deny target is gone from the discovered set.
	wantSeen := []string{"orders", "customers", "events_v1", "events_v2"}
	if len(seenInput) != len(wantSeen) {
		t.Fatalf("filter saw %v, want %v", seenInput, wantSeen)
	}
	for i, want := range wantSeen {
		if seenInput[i] != want {
			t.Fatalf("filter input[%d] = %q, want %q", i, seenInput[i], want)
		}
	}
	if _, present := got["dbo.events_v1"]; present {
		t.Fatalf("filtered-out table appeared in discovered set: %v", got)
	}
	if _, present := got["dbo.orders"]; !present {
		t.Fatalf("expected dbo.orders to remain; got %v", got)
	}
}

// TestSchemaDiscovery_FilterErrorSkipsDataset asserts the documented
// fail-closed semantic: a filter error fails the dataset, identical to a
// list-tables error. Other datasets still proceed.
func TestSchemaDiscovery_FilterErrorSkipsDataset(t *testing.T) {
	resetListTablesFiltersForTest()
	defer resetListTablesFiltersForTest()

	wh := testutil.NewMockWarehouseProvider("dbo")
	wh.Tables = []string{"orders"}

	RegisterListTablesFilter("test-boom", func(_ context.Context, _, _ string, _ []string) ([]string, error) {
		return nil, errors.New("scope misconfigured")
	})

	sd := newSchemaDiscoveryForTest(t, wh)
	got, err := sd.DiscoverSchemas(context.Background())
	if err != nil {
		t.Fatalf("DiscoverSchemas should not bubble filter error; got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("filter error should skip dataset; got %v", got)
	}
}
