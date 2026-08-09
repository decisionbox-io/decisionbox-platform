package debug

import "testing"

// ForWarehouse derives a per-datasource logger so each datasource's query
// executor stamps its own provider + warehouse id on warehouse-query rows.
func TestLogger_ForWarehouse(t *testing.T) {
	base := NewLogger(LoggerOptions{
		AppID:             "app1",
		DiscoveryRunID:    "run1",
		WarehouseProvider: "postgres",
		Enabled:           true,
	})

	d := base.ForWarehouse("wh_oracle", "oracle")
	if d.warehouseID != "wh_oracle" || d.warehouseProvider != "oracle" {
		t.Fatalf("ForWarehouse labels wrong: id=%q provider=%q", d.warehouseID, d.warehouseProvider)
	}
	// Shares run identity + enabled state with the base logger.
	if d.discoveryRunID != base.discoveryRunID || d.appID != base.appID || !d.IsEnabled() {
		t.Fatalf("derived logger must share run identity + enabled state")
	}
	// The base logger is unchanged.
	if base.warehouseID != "" || base.warehouseProvider != "postgres" {
		t.Fatalf("base logger mutated: id=%q provider=%q", base.warehouseID, base.warehouseProvider)
	}
	// Empty provider keeps the base provider label but takes the new id.
	d2 := base.ForWarehouse("default", "")
	if d2.warehouseProvider != "postgres" || d2.warehouseID != "default" {
		t.Fatalf("empty provider should keep base label: provider=%q id=%q", d2.warehouseProvider, d2.warehouseID)
	}
	// Nil-safe.
	var nilLogger *Logger
	if nilLogger.ForWarehouse("x", "y") != nil {
		t.Fatalf("ForWarehouse on nil logger must return nil")
	}
}
