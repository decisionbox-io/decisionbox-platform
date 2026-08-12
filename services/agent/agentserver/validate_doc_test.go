package agentserver

import (
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Manual doc validation must run against the datasource the discovery actually
// used. An explicit warehouse id is honoured; a legacy discovery (no
// warehouse_id) ran on the default warehouse and must stay there even after the
// project grows to multi-warehouse and moves its primary — else validation would
// query unrelated data.
func TestDocValidationWarehouseID(t *testing.T) {
	t.Run("explicit discovery warehouse honoured", func(t *testing.T) {
		p := &models.Project{
			PrimaryWarehouseID: "wh_a",
			Warehouses:         []models.WarehouseConfig{{ID: "wh_a", Provider: "postgres"}, {ID: "wh_b", Provider: "redshift"}},
		}
		if got := docValidationWarehouseID("wh_b", p); got != "wh_b" {
			t.Errorf("explicit = %q, want wh_b", got)
		}
	})

	t.Run("legacy on single-warehouse resolves to default", func(t *testing.T) {
		p := &models.Project{Warehouse: models.WarehouseConfig{Provider: "postgres", Datasets: []string{"public"}}}
		if got := docValidationWarehouseID("", p); got != models.DefaultWarehouseID {
			t.Errorf("legacy single = %q, want %q", got, models.DefaultWarehouseID)
		}
	})

	t.Run("legacy stays on default even after primary moves", func(t *testing.T) {
		// Project grew to multi-warehouse and the primary moved to wh_b, but the
		// original default warehouse still exists — the legacy discovery ran on it.
		p := &models.Project{
			PrimaryWarehouseID: "wh_b",
			Warehouses: []models.WarehouseConfig{
				{ID: models.DefaultWarehouseID, Provider: "postgres"},
				{ID: "wh_b", Provider: "redshift"},
			},
		}
		if got := docValidationWarehouseID("", p); got != models.DefaultWarehouseID {
			t.Errorf("legacy w/ moved primary = %q, want %q (must not follow the new primary)", got, models.DefaultWarehouseID)
		}
	})

	t.Run("legacy falls back to primary when default is gone", func(t *testing.T) {
		p := &models.Project{
			PrimaryWarehouseID: "wh_b",
			Warehouses:         []models.WarehouseConfig{{ID: "wh_a", Provider: "postgres"}, {ID: "wh_b", Provider: "redshift"}},
		}
		if got := docValidationWarehouseID("", p); got != "wh_b" {
			t.Errorf("legacy w/o default = %q, want wh_b (effective primary)", got)
		}
	})
}
