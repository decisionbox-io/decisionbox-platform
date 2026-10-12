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

// A cross-project BigQuery run keys its schema cache on the three-part
// "dataproject.dataset.table" (SchemaDiscovery.discoverTable via
// gowarehouse.RefQualifier), while two-part "dataset.table" refs remain valid
// SQL there — BigQuery resolves them against the query's default project. So a
// failed statement can name a table in a form the canonical key does not
// match, and the interesting case is the one where the bare name cannot save
// it: two datasets both hold "events", so "events" is ambiguous while
// "shop.events" is not.
func TestSchemaColumnResolver_CrossProjectThreePartKeys(t *testing.T) {
	schemas := map[string]models.TableSchema{
		"dataproj.shop.events":  {Columns: []models.ColumnInfo{{Name: "shop_event_id"}}},
		"dataproj.crm.events":   {Columns: []models.ColumnInfo{{Name: "crm_event_id"}}},
		"dataproj.shop.returns": {Columns: []models.ColumnInfo{{Name: "return_id"}}},
	}
	resolve := schemaColumnResolver(schemas)
	tests := []struct {
		name, table string
		wantFirst   string // "" means: resolve to nothing
	}{
		{"exact three-part key", "dataproj.shop.events", "shop_event_id"},
		{"two-part ref, bare name ambiguous", "shop.events", "shop_event_id"},
		{"two-part ref, the other dataset", "crm.events", "crm_event_id"},
		{"two-part ref, different casing", "SHOP.EVENTS", "shop_event_id"},
		{"bare name is ambiguous across datasets", "events", ""},
		{"bare name, unambiguous", "returns", "return_id"},
		{"unknown dataset and table", "nope.nothing", ""},
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

// Two projects holding the same dataset.table makes the two-part form
// ambiguous too, and an ambiguous ref must resolve to nothing at every tier —
// otherwise the fixer hands the model one project's columns for another's
// table, which reads as authoritative.
func TestSchemaColumnResolver_AmbiguousTwoPartIsNotGuessed(t *testing.T) {
	resolve := schemaColumnResolver(map[string]models.TableSchema{
		"proj_a.shop.events": {Columns: []models.ColumnInfo{{Name: "a_id"}}},
		"proj_b.shop.events": {Columns: []models.ColumnInfo{{Name: "b_id"}}},
	})
	for _, ref := range []string{"shop.events", "events", "SHOP.EVENTS"} {
		if got := resolve(ref); len(got) != 0 {
			t.Errorf("ambiguous ref %q resolved to %v", ref, got)
		}
	}
	if got := resolve("proj_a.shop.events"); len(got) == 0 || got[0].Name != "a_id" {
		t.Errorf("the unambiguous full key must still resolve, got %v", got)
	}
}
