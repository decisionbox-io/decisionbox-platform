package agentserver

import (
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// The profile carries the warehouse's currency, which discovery reads to settle what an
// unmarked amount is in. A project with two datasources reporting in different
// currencies needs the per-warehouse profile to win, which is what the field documents
// and what the orchestrator was not being given.
func TestWarehouseProfileOr(t *testing.T) {
	project := map[string]interface{}{"financials": map[string]interface{}{"local_currency": "USD"}}
	warehouse := map[string]interface{}{"financials": map[string]interface{}{"local_currency": "TRY"}}

	for name, tc := range map[string]struct {
		wh   models.WarehouseConfig
		want string
	}{
		"the warehouse states its own": {models.WarehouseConfig{Profile: warehouse}, "TRY"},
		"empty falls back":             {models.WarehouseConfig{}, "USD"},
		"an empty map falls back":      {models.WarehouseConfig{Profile: map[string]interface{}{}}, "USD"},
	} {
		got := warehouseProfileOr(tc.wh, project)
		financials, ok := got["financials"].(map[string]interface{})
		if !ok {
			t.Fatalf("%s: no financials in the resolved profile", name)
		}
		if financials["local_currency"] != tc.want {
			t.Errorf("%s: local_currency = %v, want %q", name, financials["local_currency"], tc.want)
		}
	}
}

func TestWarehouseProfileOrWithoutAProjectProfile(t *testing.T) {
	if got := warehouseProfileOr(models.WarehouseConfig{}, nil); got != nil {
		t.Errorf("want nil with nothing configured, got %v", got)
	}
}
