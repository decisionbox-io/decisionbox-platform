package agentserver

import (
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func ids(whs []models.WarehouseConfig) []string {
	out := make([]string, len(whs))
	for i, w := range whs {
		out[i] = w.ID
	}
	return out
}

// warehousesToIndex drives which datasources index-schema builds a catalog for.
// It must list the primary first, then every other datasource once, so
// ask-serve's search_tables / lookup_schema work across all of them — while a
// legacy single-warehouse project still yields exactly one warehouse.
func TestWarehousesToIndex(t *testing.T) {
	t.Run("legacy single warehouse yields the synthesised default", func(t *testing.T) {
		p := &models.Project{Warehouse: models.WarehouseConfig{Provider: "postgres", Datasets: []string{"public"}}}
		got := ids(warehousesToIndex(p))
		if len(got) != 1 || got[0] != models.DefaultWarehouseID {
			t.Fatalf("got %v, want [%s]", got, models.DefaultWarehouseID)
		}
	})

	t.Run("multi-warehouse: primary first, others deduped in order", func(t *testing.T) {
		p := &models.Project{
			PrimaryWarehouseID: "wh_b",
			Warehouses: []models.WarehouseConfig{
				{ID: "wh_a", Provider: "snowflake"},
				{ID: "wh_b", Provider: "redshift"},
				{ID: "wh_c", Provider: "bigquery"},
			},
		}
		got := ids(warehousesToIndex(p))
		want := []string{"wh_b", "wh_a", "wh_c"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	})

	t.Run("id-less default entry is not double-indexed", func(t *testing.T) {
		// Primary resolves (via normalization) to the id-less entry; it must not
		// then reappear as a second, separate warehouse.
		p := &models.Project{
			PrimaryWarehouseID: models.DefaultWarehouseID,
			Warehouses: []models.WarehouseConfig{
				{ID: "wh_a", Provider: "snowflake"},
				{ID: "", Provider: "postgres"}, // the default
			},
		}
		got := warehousesToIndex(p)
		if len(got) != 2 {
			t.Fatalf("got %d warehouses, want 2 (no double-index): %v", len(got), ids(got))
		}
		if got[0].Provider != "postgres" {
			t.Fatalf("primary should be the id-less default (postgres), got %q", got[0].Provider)
		}
	})

	t.Run("no warehouse configured yields a single zero entry (fails downstream, as before)", func(t *testing.T) {
		got := warehousesToIndex(&models.Project{})
		if len(got) != 1 || got[0].Provider != "" {
			t.Fatalf("empty project should yield one zero warehouse, got %v", ids(got))
		}
	})
}

// warehouseIDOrDefault maps an id-less (default) warehouse to the reserved
// "default" id so warehouse-scoped lookups (discovery search_tables, the schema
// cache) filter on a concrete id instead of reading empty as "all warehouses".
func TestWarehouseIDOrDefault(t *testing.T) {
	if got := warehouseIDOrDefault(models.WarehouseConfig{ID: ""}); got != models.DefaultWarehouseID {
		t.Errorf("empty id = %q, want %q", got, models.DefaultWarehouseID)
	}
	if got := warehouseIDOrDefault(models.WarehouseConfig{ID: "wh_b"}); got != "wh_b" {
		t.Errorf("explicit id = %q, want wh_b", got)
	}
}
