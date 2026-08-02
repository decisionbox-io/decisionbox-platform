package models

import "testing"

// The agent model mirrors the API model's warehouse accessors; these
// tests guard against the two copies drifting.

func TestAgentEffectiveWarehousesAndAccessors(t *testing.T) {
	legacy := &Project{
		ID:        "p1",
		Warehouse: WarehouseConfig{Provider: "postgres", Datasets: []string{"public"}},
	}
	whs := legacy.EffectiveWarehouses()
	if len(whs) != 1 || whs[0].ID != DefaultWarehouseID || whs[0].Provider != "postgres" {
		t.Fatalf("legacy synthesis wrong: %+v", whs)
	}
	if legacy.PrimaryWarehouse().Provider != "postgres" {
		t.Errorf("legacy primary should be postgres")
	}
	if wh, ok := legacy.WarehouseByID(DefaultWarehouseID); !ok || wh.Provider != "postgres" {
		t.Errorf("default id should resolve legacy warehouse")
	}

	multi := &Project{
		PrimaryWarehouseID: "wh_b",
		Warehouses: []WarehouseConfig{
			{ID: "wh_a", Provider: "snowflake"},
			{ID: "wh_b", Provider: "redshift"},
		},
	}
	if multi.PrimaryWarehouse().Provider != "redshift" {
		t.Errorf("explicit primary should be redshift")
	}
	if wh, ok := multi.WarehouseByID("wh_a"); !ok || wh.Provider != "snowflake" {
		t.Errorf("wh_a should resolve snowflake")
	}
	if _, ok := multi.WarehouseByID("nope"); ok {
		t.Errorf("unknown id should not resolve")
	}

	if whs := (&Project{ID: "empty"}).EffectiveWarehouses(); whs != nil {
		t.Errorf("empty project should have no warehouses, got %+v", whs)
	}
}

// Discovery + schema-indexing wire their datasets and tenant filter from
// PrimaryWarehouse(), NOT the legacy singular Warehouse field. On a real
// multi-warehouse project those diverge, so the primary must win — else
// discovery would run the primary's provider against the legacy field's
// datasets/filter. A legacy project (Warehouses empty) still reads back the
// synthesised Warehouse, keeping the old behaviour.
func TestPrimaryWarehouseDatasetsAndFilterWinOverLegacy(t *testing.T) {
	p := &Project{
		// Legacy field points elsewhere; it must be ignored for the primary.
		Warehouse: WarehouseConfig{
			Provider: "postgres", Datasets: []string{"legacy_ds"},
			FilterField: "legacy_tenant", FilterValue: "legacy",
		},
		PrimaryWarehouseID: "wh_b",
		Warehouses: []WarehouseConfig{
			{ID: "wh_a", Provider: "snowflake", Datasets: []string{"a_ds"}},
			{ID: "wh_b", Provider: "redshift", Datasets: []string{"b_ds"},
				FilterField: "b_tenant", FilterValue: "acme"},
		},
	}
	prim := p.PrimaryWarehouse()
	if ds := prim.GetDatasets(); len(ds) != 1 || ds[0] != "b_ds" {
		t.Errorf("primary datasets = %v, want [b_ds] (not the legacy field)", ds)
	}
	if prim.FilterField != "b_tenant" || prim.FilterValue != "acme" {
		t.Errorf("primary filter = %s=%s, want b_tenant=acme", prim.FilterField, prim.FilterValue)
	}
	if prim.Provider != "redshift" {
		t.Errorf("primary provider = %s, want redshift", prim.Provider)
	}

	// Legacy project: primary synthesises from Warehouse, preserving behaviour.
	legacy := &Project{Warehouse: WarehouseConfig{
		Provider: "bigquery", Datasets: []string{"only_ds"}, FilterField: "org", FilterValue: "x",
	}}
	lp := legacy.PrimaryWarehouse()
	if ds := lp.GetDatasets(); len(ds) != 1 || ds[0] != "only_ds" || lp.FilterField != "org" {
		t.Errorf("legacy primary should mirror Warehouse, got datasets=%v filter=%s", lp.GetDatasets(), lp.FilterField)
	}
}
