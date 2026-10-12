package discovery

import (
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func schemaFixture() map[string]models.TableSchema {
	return map[string]models.TableSchema{
		"shop.events": {TableName: "shop.events", Columns: []models.ColumnInfo{
			{Name: "event_id"}, {Name: "event_ts"},
		}},
		"shop.orders": {TableName: "shop.orders", Columns: []models.ColumnInfo{{Name: "order_id"}}},
		// Same bare name under two datasets — a bare reference is ambiguous.
		"warehouse_a.items": {TableName: "warehouse_a.items", Columns: []models.ColumnInfo{{Name: "a_id"}}},
		"warehouse_b.items": {TableName: "warehouse_b.items", Columns: []models.ColumnInfo{{Name: "b_id"}}},
	}
}

func TestSchemaColumnResolver(t *testing.T) {
	resolve := schemaColumnResolver(schemaFixture())
	tests := []struct {
		name, table string
		wantFirst   string // "" means: resolve to nothing
	}{
		{"exact qualified key", "shop.events", "event_id"},
		{"different casing", "SHOP.EVENTS", "event_id"},
		{"bare name, unambiguous", "orders", "order_id"},
		{"bare name, different casing", "Orders", "order_id"},
		{"other dataset prefix, unambiguous bare match", "public.orders", "order_id"},
		{"unknown table", "shop.nope", ""},
		{"ambiguous bare name resolves to nothing", "items", ""},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolve(tc.table)
			if tc.wantFirst == "" {
				if len(got) != 0 {
					t.Fatalf("want no columns for %q, got %v", tc.table, got)
				}
				return
			}
			if len(got) == 0 || got[0].Name != tc.wantFirst {
				t.Fatalf("want first column %q for %q, got %v", tc.wantFirst, tc.table, got)
			}
		})
	}
}

// An ambiguous bare name must yield nothing rather than an arbitrary pick:
// handing the model a different table's columns is worse than handing it none,
// because it reads as authoritative.
func TestSchemaColumnResolver_AmbiguityIsNotGuessed(t *testing.T) {
	resolve := schemaColumnResolver(schemaFixture())
	if got := resolve("items"); len(got) != 0 {
		t.Fatalf("ambiguous bare name resolved to %v", got)
	}
	if got := resolve("warehouse_a.items"); len(got) == 0 || got[0].Name != "a_id" {
		t.Fatalf("qualifying the name should still resolve; got %v", got)
	}
}

func TestSchemaColumnResolver_EmptyCache(t *testing.T) {
	if got := schemaColumnResolver(nil)("shop.events"); len(got) != 0 {
		t.Fatalf("want nothing from an empty cache, got %v", got)
	}
}
