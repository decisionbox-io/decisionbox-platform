package models

import (
	"testing"

	warehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// The method has to be the shared function and not a paraphrase of it: two
// implementations of the cache key are two chances to disagree about which
// cached rows describe the datasource as it is now.
func TestWarehouseConfig_CacheHashIsTheSharedHash(t *testing.T) {
	cfg := WarehouseConfig{
		Provider: "bigquery", ProjectID: "p", Location: "EU",
		Datasets: []string{"b", "a"}, FilterField: "tenant", FilterValue: "t1",
		Config: map[string]string{"z": "1", "a": "2"},
	}
	want := warehouse.ConfigHash(cfg.Provider, cfg.ProjectID, cfg.Location,
		cfg.Datasets, cfg.FilterField, cfg.FilterValue, cfg.Config)
	if got := cfg.CacheHash(); got != want {
		t.Errorf("CacheHash = %s, want %s", got, want)
	}
}

// Fields the hash must ignore. Credentials never reach it — they live in the
// secret provider — and a label or a description changes nothing about what
// discovery would find, so neither may force a re-index.
func TestWarehouseConfig_CacheHashIgnoresWhatDoesNotAffectDiscovery(t *testing.T) {
	base := WarehouseConfig{Provider: "bigquery", ProjectID: "p", Datasets: []string{"sales"}}
	renamed := base
	renamed.ID = "wh_other"
	renamed.Label = "Renamed"
	renamed.Description = "A description"

	if base.CacheHash() != renamed.CacheHash() {
		t.Error("renaming a datasource changed its cache key, which would re-index it for nothing")
	}
}
