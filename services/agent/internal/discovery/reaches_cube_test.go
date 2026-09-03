package discovery

import (
	"errors"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Providers registered only for these tests, so shape resolution runs against
// the real registry — the thing the running process reads.
const (
	testCubeSlug   = "discovery-test-cube"
	testTablesSlug = "discovery-test-tables"
)

func init() {
	factory := func(gowarehouse.ProviderConfig) (gowarehouse.Provider, error) {
		return nil, errors.New("registered for its metadata only")
	}
	gowarehouse.RegisterWithMeta(testCubeSlug, factory, gowarehouse.ProviderMeta{
		Name:       "Test cube",
		Capability: gowarehouse.Capability{Shape: gowarehouse.ShapeCube},
	})
	// Declares no shape, like every provider that predates the descriptor.
	gowarehouse.RegisterWithMeta(testTablesSlug, factory, gowarehouse.ProviderMeta{Name: "Test tables"})
}

// TestRunReachesCube_DecidesWhichRuleARunGets is the switch protecting every
// existing deployment: a run that can only reach tables keeps the step floor
// exactly as it is, and only a run that can reach a cube changes behaviour.
func TestRunReachesCube_DecidesWhichRuleARunGets(t *testing.T) {
	cases := map[string]struct {
		warehouses  []models.WarehouseConfig
		primarySlug string
		want        bool
	}{
		"a SQL-only project": {
			warehouses:  []models.WarehouseConfig{{ID: "a", Provider: testTablesSlug}, {ID: "b", Provider: testTablesSlug}},
			primarySlug: testTablesSlug,
			want:        false,
		},
		"a cube alongside the warehouse": {
			warehouses:  []models.WarehouseConfig{{ID: "a", Provider: testTablesSlug}, {ID: "b", Provider: testCubeSlug}},
			primarySlug: testTablesSlug,
			want:        true,
		},
		"a legacy single-warehouse run, which carries no warehouses[] at all": {
			warehouses:  nil,
			primarySlug: testCubeSlug,
			want:        true,
		},
		"a legacy single-warehouse SQL run": {
			warehouses:  nil,
			primarySlug: testTablesSlug,
			want:        false,
		},
		"a provider this binary does not link": {
			warehouses:  []models.WarehouseConfig{{ID: "a", Provider: "not-registered"}},
			primarySlug: "not-registered",
			want:        false,
		},
		"nothing configured": {
			warehouses:  nil,
			primarySlug: "",
			want:        false,
		},
	}
	for name, tc := range cases {
		if got := runReachesCube(tc.warehouses, tc.primarySlug); got != tc.want {
			t.Errorf("%s: runReachesCube = %v, want %v", name, got, tc.want)
		}
	}
}

// TestRunReachesCube_ReadsTheWarehouseListNotOnlyThePrimary pins the case a
// shortcut would break: a cube is never the primary — it cannot anchor a
// project — so a check that only consulted the primary slug would report
// every real mixed run as table-shaped.
func TestRunReachesCube_ReadsTheWarehouseListNotOnlyThePrimary(t *testing.T) {
	if !runReachesCube([]models.WarehouseConfig{{ID: "secondary", Provider: testCubeSlug}}, testTablesSlug) {
		t.Error("a cube configured as a secondary datasource was not seen")
	}
}
