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
