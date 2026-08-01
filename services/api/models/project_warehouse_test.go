package models

import (
	"encoding/json"
	"testing"
)

// legacyProject is a project shaped like every pre-multi-warehouse doc:
// a single embedded Warehouse, no Warehouses slice.
func legacyProject() *Project {
	return &Project{
		ID: "p1",
		Warehouse: WarehouseConfig{
			Provider: "postgres",
			Datasets: []string{"public"},
		},
	}
}

func TestEffectiveWarehouses(t *testing.T) {
	t.Run("legacy single warehouse synthesises a default id", func(t *testing.T) {
		whs := legacyProject().EffectiveWarehouses()
		if len(whs) != 1 {
			t.Fatalf("len = %d, want 1", len(whs))
		}
		if whs[0].ID != DefaultWarehouseID {
			t.Errorf("synthesised id = %q, want %q", whs[0].ID, DefaultWarehouseID)
		}
		if whs[0].Provider != "postgres" {
			t.Errorf("provider = %q, want postgres", whs[0].Provider)
		}
	})

	t.Run("explicit warehouses returned as-is", func(t *testing.T) {
		p := &Project{Warehouses: []WarehouseConfig{
			{ID: "wh_a", Provider: "snowflake"},
			{ID: "wh_b", Provider: "redshift"},
		}}
		whs := p.EffectiveWarehouses()
		if len(whs) != 2 || whs[0].ID != "wh_a" || whs[1].ID != "wh_b" {
			t.Fatalf("unexpected warehouses: %+v", whs)
		}
	})

	t.Run("no warehouse configured returns nil", func(t *testing.T) {
		if whs := (&Project{ID: "empty"}).EffectiveWarehouses(); whs != nil {
			t.Errorf("want nil, got %+v", whs)
		}
	})

	t.Run("warehouses slice wins over legacy field", func(t *testing.T) {
		p := legacyProject()
		p.Warehouses = []WarehouseConfig{{ID: "wh_a", Provider: "snowflake"}}
		whs := p.EffectiveWarehouses()
		if len(whs) != 1 || whs[0].Provider != "snowflake" {
			t.Errorf("legacy field should be ignored once Warehouses is set: %+v", whs)
		}
	})
}

func TestPrimaryWarehouse(t *testing.T) {
	multi := &Project{
		PrimaryWarehouseID: "wh_b",
		Warehouses: []WarehouseConfig{
			{ID: "wh_a", Provider: "snowflake"},
			{ID: "wh_b", Provider: "redshift"},
		},
	}

	tests := []struct {
		name         string
		p            *Project
		wantProvider string
	}{
		{"legacy synthesised primary", legacyProject(), "postgres"},
		{"explicit primary id", multi, "redshift"},
		{"unset primary id falls back to first", &Project{Warehouses: multi.Warehouses}, "snowflake"},
		{"unknown primary id falls back to first", &Project{PrimaryWarehouseID: "nope", Warehouses: multi.Warehouses}, "snowflake"},
		{"no warehouse yields zero value", &Project{ID: "empty"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.PrimaryWarehouse().Provider; got != tc.wantProvider {
				t.Errorf("primary provider = %q, want %q", got, tc.wantProvider)
			}
		})
	}
}

func TestWarehouseByID(t *testing.T) {
	multi := &Project{Warehouses: []WarehouseConfig{
		{ID: "wh_a", Provider: "snowflake"},
		{ID: "wh_b", Provider: "redshift"},
	}}

	t.Run("empty id resolves to primary", func(t *testing.T) {
		wh, ok := multi.WarehouseByID("")
		if !ok || wh.ID != "wh_a" {
			t.Errorf("empty id -> %+v, ok=%v; want primary wh_a", wh, ok)
		}
	})
	t.Run("explicit id match", func(t *testing.T) {
		wh, ok := multi.WarehouseByID("wh_b")
		if !ok || wh.Provider != "redshift" {
			t.Errorf("wh_b -> %+v, ok=%v", wh, ok)
		}
	})
	t.Run("default id matches synthesised legacy warehouse", func(t *testing.T) {
		wh, ok := legacyProject().WarehouseByID(DefaultWarehouseID)
		if !ok || wh.Provider != "postgres" {
			t.Errorf("default -> %+v, ok=%v", wh, ok)
		}
	})
	t.Run("unknown id is not found", func(t *testing.T) {
		if _, ok := multi.WarehouseByID("nope"); ok {
			t.Error("unknown id should not be found")
		}
	})
	t.Run("empty id with no warehouse is not found", func(t *testing.T) {
		if _, ok := (&Project{ID: "empty"}).WarehouseByID(""); ok {
			t.Error("empty project should report no warehouse")
		}
	})
}

// TestProjectWarehouses_JSONRoundTrip verifies the new multi-warehouse
// fields survive a marshal/unmarshal cycle with their tags intact.
func TestProjectWarehouses_JSONRoundTrip(t *testing.T) {
	original := Project{
		ID:                 "p1",
		PrimaryWarehouseID: "wh_a",
		Warehouses: []WarehouseConfig{
			{
				ID:          "wh_a",
				Label:       "Snowflake — Sales",
				Description: "revenue and orders",
				Domain:      "ecommerce",
				Provider:    "snowflake",
				Datasets:    []string{"sales"},
				Card: &WarehouseCard{
					SubjectAreas: []string{"revenue"},
					KeyEntities:  []string{"customer", "order"},
					KeyMetrics:   []string{"gmv"},
				},
			},
		},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Project
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.PrimaryWarehouseID != "wh_a" || len(decoded.Warehouses) != 1 {
		t.Fatalf("round-trip lost fields: %+v", decoded)
	}
	w := decoded.Warehouses[0]
	if w.ID != "wh_a" || w.Label != "Snowflake — Sales" || w.Description != "revenue and orders" || w.Domain != "ecommerce" {
		t.Errorf("warehouse scalar fields lost: %+v", w)
	}
	if w.Card == nil || len(w.Card.KeyEntities) != 2 || w.Card.SubjectAreas[0] != "revenue" {
		t.Errorf("card lost: %+v", w.Card)
	}
}
