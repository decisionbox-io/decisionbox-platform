package discovery

import (
	"errors"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
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
	// wired names the datasources the engine actually got an executor for.
	wired := func(ids ...string) map[string]*queryexec.QueryExecutor {
		m := map[string]*queryexec.QueryExecutor{}
		for _, id := range ids {
			m[id] = nil
		}
		return m
	}

	cases := map[string]struct {
		routable    map[string]*queryexec.QueryExecutor
		warehouses  []models.WarehouseConfig
		primarySlug string
		want        bool
	}{
		"a SQL-only project": {
			routable:    wired("a", "b"),
			warehouses:  []models.WarehouseConfig{{ID: "a", Provider: testTablesSlug}, {ID: "b", Provider: testTablesSlug}},
			primarySlug: testTablesSlug,
			want:        false,
		},
		"a cube alongside the warehouse": {
			routable:    wired("a", "b"),
			warehouses:  []models.WarehouseConfig{{ID: "a", Provider: testTablesSlug}, {ID: "b", Provider: testCubeSlug}},
			primarySlug: testTablesSlug,
			want:        true,
		},
		"a cube configured but never wired into the run": {
			// Its provider failed to open, or its schema was never indexed,
			// so the engine has no executor for it. The run can only query
			// tables and must keep the step floor.
			routable:    wired("a"),
			warehouses:  []models.WarehouseConfig{{ID: "a", Provider: testTablesSlug}, {ID: "b", Provider: testCubeSlug}},
			primarySlug: testTablesSlug,
			want:        false,
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
			routable:    wired("a"),
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
		if got := runReachesCube(tc.routable, tc.warehouses, tc.primarySlug); got != tc.want {
			t.Errorf("%s: runReachesCube = %v, want %v", name, got, tc.want)
		}
	}
}

// TestRunReachesCube_ReadsTheWarehouseListNotOnlyThePrimary pins the case a
// shortcut would break: a cube is never the primary — it cannot anchor a
// project — so a check that only consulted the primary slug would report
// every real mixed run as table-shaped.
func TestRunReachesCube_ReadsTheWarehouseListNotOnlyThePrimary(t *testing.T) {
	routable := map[string]*queryexec.QueryExecutor{"secondary": nil}
	if !runReachesCube(routable, []models.WarehouseConfig{{ID: "secondary", Provider: testCubeSlug}}, testTablesSlug) {
		t.Error("a cube configured as a secondary datasource was not seen")
	}
}
