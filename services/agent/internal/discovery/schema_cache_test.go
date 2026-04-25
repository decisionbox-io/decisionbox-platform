package discovery

import (
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// WarehouseConfigHash is the single invalidation surface for the schema
// cache. Every field that changes what DiscoverSchemas returns MUST
// change the hash; fields that don't affect the catalog pass MUST NOT.
//
// These tests lock that contract in — drift here would silently cause
// stale cache reuse (worst-case: running against the old dataset
// forever) or spurious misses (paying the 40-minute catalog pass on
// every run).

func baseCfg() models.WarehouseConfig {
	return models.WarehouseConfig{
		Provider:    "bigquery",
		ProjectID:   "my-gcp-project",
		Location:    "US",
		Datasets:    []string{"sales", "marketing"},
		FilterField: "is_test",
		FilterValue: "false",
		Config: map[string]string{
			"auth_method": "adc",
			"region":      "us-east-1",
		},
	}
}

func TestWarehouseConfigHash_HexLength(t *testing.T) {
	// SHA-256 is 32 bytes → hex-encoded = 64 chars. Guards against
	// a silent migration to a shorter digest.
	h := WarehouseConfigHash(baseCfg())
	if len(h) != 64 {
		t.Errorf("hash length = %d, want 64 (hex SHA-256)", len(h))
	}
}

func TestWarehouseConfigHash_Deterministic(t *testing.T) {
	h1 := WarehouseConfigHash(baseCfg())
	h2 := WarehouseConfigHash(baseCfg())
	if h1 != h2 {
		t.Errorf("hash not deterministic: %q != %q", h1, h2)
	}
}

func TestWarehouseConfigHash_MapOrderIndependent(t *testing.T) {
	a := baseCfg()
	b := baseCfg()
	// Rebuild b.Config with insertion order reversed — Go's map
	// iteration is randomised, but we still want to be explicit that
	// content, not order, determines the hash.
	b.Config = map[string]string{}
	b.Config["region"] = "us-east-1"
	b.Config["auth_method"] = "adc"
	if WarehouseConfigHash(a) != WarehouseConfigHash(b) {
		t.Error("hash changed after rebuilding Config with different insertion order")
	}
}

func TestWarehouseConfigHash_DatasetOrderIndependent(t *testing.T) {
	a := baseCfg()
	b := baseCfg()
	b.Datasets = []string{"marketing", "sales"} // reversed
	if WarehouseConfigHash(a) != WarehouseConfigHash(b) {
		t.Error("hash changed after reversing Datasets slice (we sort before hashing)")
	}
}

func TestWarehouseConfigHash_EveryFieldMatters(t *testing.T) {
	base := WarehouseConfigHash(baseCfg())

	mutations := []struct {
		name   string
		mutate func(*models.WarehouseConfig)
	}{
		{"provider changes", func(c *models.WarehouseConfig) { c.Provider = "snowflake" }},
		{"project_id changes", func(c *models.WarehouseConfig) { c.ProjectID = "other-gcp" }},
		{"location changes", func(c *models.WarehouseConfig) { c.Location = "EU" }},
		{"dataset added", func(c *models.WarehouseConfig) { c.Datasets = append(c.Datasets, "finance") }},
		{"dataset removed", func(c *models.WarehouseConfig) { c.Datasets = []string{"sales"} }},
		{"dataset renamed", func(c *models.WarehouseConfig) { c.Datasets = []string{"SALES", "marketing"} }},
		{"filter_field changes", func(c *models.WarehouseConfig) { c.FilterField = "env" }},
		{"filter_value changes", func(c *models.WarehouseConfig) { c.FilterValue = "true" }},
		{"config value changes", func(c *models.WarehouseConfig) { c.Config["region"] = "eu-west-1" }},
		{"config key added", func(c *models.WarehouseConfig) { c.Config["workgroup"] = "default" }},
		{"config key removed", func(c *models.WarehouseConfig) { delete(c.Config, "region") }},
	}
	for _, m := range mutations {
		cfg := baseCfg()
		m.mutate(&cfg)
		got := WarehouseConfigHash(cfg)
		if got == base {
			t.Errorf("%s: hash did not change (still %q) — cache would silently return stale schemas", m.name, got)
		}
	}
}

func TestWarehouseConfigHash_EmptyConfigStable(t *testing.T) {
	// A brand-new warehouse with no config yet still produces a
	// deterministic hash so the cache doesn't panic / misbehave.
	empty := models.WarehouseConfig{}
	if len(WarehouseConfigHash(empty)) != 64 {
		t.Error("empty config should still produce a 64-char hash")
	}
	if WarehouseConfigHash(empty) == WarehouseConfigHash(baseCfg()) {
		t.Error("empty config must not collide with a populated config")
	}
}

func TestWarehouseConfigHash_NilVsEmptyConfigMap(t *testing.T) {
	// Defensive: a WarehouseConfig with Config=nil and one with
	// Config=map{} must hash identically — both mean "no extra config."
	a := models.WarehouseConfig{Provider: "postgres"}
	b := models.WarehouseConfig{Provider: "postgres", Config: map[string]string{}}
	if WarehouseConfigHash(a) != WarehouseConfigHash(b) {
		t.Error("nil Config and empty Config map must produce the same hash")
	}
}

func TestWarehouseConfigHash_FilterFieldWithoutValue(t *testing.T) {
	// Partially-filled filter (field set, value empty) shouldn't
	// collide with the unfiltered case — an operator editing just the
	// field still expects a fresh discovery.
	a := baseCfg()
	a.FilterField = ""
	a.FilterValue = ""

	b := baseCfg()
	b.FilterField = "is_test"
	b.FilterValue = ""

	if WarehouseConfigHash(a) == WarehouseConfigHash(b) {
		t.Error("setting filter_field alone must change the hash")
	}
}

func TestWarehouseConfigHash_CollisionResistance(t *testing.T) {
	// The hash string is built with explicit separators precisely so
	// concatenated fields can't collide. This locks that in — `a|b`
	// and `ab|` must hash differently even though a naive strcat would
	// render them the same.
	a := models.WarehouseConfig{Provider: "foo", ProjectID: "bar"}
	b := models.WarehouseConfig{Provider: "foobar"}
	if WarehouseConfigHash(a) == WarehouseConfigHash(b) {
		t.Error("field-boundary collision: provider+project_id must not hash like a concatenated provider")
	}
}
