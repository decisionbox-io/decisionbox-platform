package warehouse

import (
	"context"
	"testing"
)

func TestCredentialsKey(t *testing.T) {
	tests := []struct {
		warehouseID string
		want        string
	}{
		{"", LegacyCredentialsKey},                             // unset → primary/legacy
		{DefaultWarehouseID, LegacyCredentialsKey},             // "default" → legacy (no migration)
		{"wh_snowflake", "warehouse-credentials:wh_snowflake"}, // additional warehouse
		{"wh_b", "warehouse-credentials:wh_b"},
	}
	for _, tc := range tests {
		if got := CredentialsKey(tc.warehouseID); got != tc.want {
			t.Errorf("CredentialsKey(%q) = %q, want %q", tc.warehouseID, got, tc.want)
		}
	}
}

func TestWarehouseIDContext(t *testing.T) {
	ctx := context.Background()
	if got := WarehouseIDFromContext(ctx); got != "" {
		t.Errorf("bare context should carry no warehouse id, got %q", got)
	}
	ctx = WithWarehouseID(ctx, "wh_a")
	if got := WarehouseIDFromContext(ctx); got != "wh_a" {
		t.Errorf("want wh_a, got %q", got)
	}
	// Warehouse id and project id are independent carriers.
	ctx = WithProjectID(ctx, "p1")
	if WarehouseIDFromContext(ctx) != "wh_a" || ProjectIDFromContext(ctx) != "p1" {
		t.Errorf("project and warehouse ids must not clobber each other")
	}
}
