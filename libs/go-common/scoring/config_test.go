package scoring

import (
	"reflect"
	"testing"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

// TestDefaultConfigIsTheCurrentVersion keeps the stamped version and the table
// key from drifting apart. A Config whose Version says 1 but which is
// registered under 2 would make every breakdown explain itself with the wrong
// cutoffs.
func TestDefaultConfigIsTheCurrentVersion(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Version != EngineVersion {
		t.Errorf("DefaultConfig().Version = %d, want %d", cfg.Version, EngineVersion)
	}
	resolved, ok := ConfigForVersion(EngineVersion)
	if !ok {
		t.Fatalf("ConfigForVersion(%d) is not known to this build", EngineVersion)
	}
	if !reflect.DeepEqual(resolved, cfg) {
		t.Error("ConfigForVersion(EngineVersion) differs from DefaultConfig()")
	}
}

// TestConfigForVersionRefusesAnUnknownVersion is the mixed-image case: a
// document written by a newer agent must come back "not known here" rather
// than being explained with this build's numbers under the other version's
// name.
func TestConfigForVersionRefusesAnUnknownVersion(t *testing.T) {
	for _, v := range []int{0, -1, EngineVersion + 1, 99} {
		if _, ok := ConfigForVersion(v); ok {
			t.Errorf("ConfigForVersion(%d) resolved; it must not", v)
		}
	}
}

// TestKnownVersionsIsAscendingAndContainsTheCurrent is what the read-only API
// serves, so order and completeness are part of the contract.
func TestKnownVersionsIsAscendingAndContainsTheCurrent(t *testing.T) {
	vs := KnownVersions()
	if len(vs) == 0 {
		t.Fatal("KnownVersions() is empty")
	}
	for i := 1; i < len(vs); i++ {
		if vs[i-1] >= vs[i] {
			t.Errorf("KnownVersions() is not ascending: %v", vs)
			break
		}
	}
	found := false
	for _, v := range vs {
		if v == EngineVersion {
			found = true
		}
		if _, ok := ConfigForVersion(v); !ok {
			t.Errorf("KnownVersions() lists %d, which ConfigForVersion does not resolve", v)
		}
	}
	if !found {
		t.Errorf("KnownVersions() = %v, missing the current version %d", vs, EngineVersion)
	}
}

// TestDefaultConfigMapsAreNotShared is why configs() builds a Config rather
// than holding one: Config carries a map, and a caller that mutated a shared
// one would silently re-calibrate every later run in the process.
func TestDefaultConfigMapsAreNotShared(t *testing.T) {
	a := DefaultConfig()
	a.Confidence.Bases[valmodels.StatusSupported] = 0.01
	if got := DefaultConfig().Confidence.Bases[valmodels.StatusSupported]; got != 0.80 {
		t.Errorf("mutating one Config changed the next: supported base = %v, want 0.8", got)
	}
}

// TestDefaultConfigPricesEveryCheckedVerdict pins which statuses get a base of
// their own and which fall through to the default. A status added to the
// taxonomy and not priced here lands in not_confirmed, which is a decision
// someone should make rather than inherit.
func TestDefaultConfigPricesEveryCheckedVerdict(t *testing.T) {
	cfg := DefaultConfig().Confidence
	priced := map[valmodels.Status]float64{
		valmodels.StatusConfirmed:    0.95,
		valmodels.StatusSupported:    0.80,
		valmodels.StatusPartial:      0.55,
		valmodels.StatusRejected:     0.05,
		valmodels.StatusUnverifiable: 0.20,
	}
	if len(cfg.Bases) != len(priced) {
		t.Errorf("Bases has %d entries, want %d: %v", len(cfg.Bases), len(priced), cfg.Bases)
	}
	for status, want := range priced {
		if got, ok := cfg.Bases[status]; !ok || got != want {
			t.Errorf("Bases[%q] = %v (present=%v), want %v", status, got, ok, want)
		}
	}
	for _, unpriced := range []valmodels.Status{
		valmodels.StatusValidationDisabled, valmodels.StatusSkippedBudgetCap,
	} {
		if _, ok := cfg.Bases[unpriced]; ok {
			t.Errorf("Bases prices %q; the never-checked statuses must take the default", unpriced)
		}
	}
}

// TestDefaultConfigDockAmountsAndCaps pins the calibration itself. These are
// the numbers the band distribution was measured against, so a change here is
// a re-calibration and belongs behind a version bump.
func TestDefaultConfigDockAmountsAndCaps(t *testing.T) {
	d := DefaultConfig().Confidence.Docks
	for _, tc := range []struct {
		name string
		spec DockSpec
		code string
		amt  float64
	}{
		{"degraded evidence", d.DegradedEvidence, DockDegradedEvidence, 0.10},
		{"refuted arithmetic", d.RefutedArithmetic, DockRefutedArithmetic, 0.10},
		{"refuted quantifier", d.RefutedQuantifier, DockRefutedQuantifier, 0.10},
		{"unresolved figure", d.UnresolvedFigure, DockUnresolvedFigure, 0.05},
		{"sample ended early", d.SampleEndedEarly, DockSampleEndedEarly, 0.05},
	} {
		if tc.spec.Code != tc.code {
			t.Errorf("%s code = %q, want %q", tc.name, tc.spec.Code, tc.code)
		}
		if tc.spec.Amount != tc.amt {
			t.Errorf("%s amount = %v, want %v", tc.name, tc.spec.Amount, tc.amt)
		}
		if tc.spec.Label == "" {
			t.Errorf("%s carries no label; the label is stored on every document and rendered verbatim", tc.name)
		}
	}
	if d.DegradedEvidenceCap != 0.20 {
		t.Errorf("DegradedEvidenceCap = %v, want 0.2", d.DegradedEvidenceCap)
	}
	if d.RefutedCap != 0.15 {
		t.Errorf("RefutedCap = %v, want 0.15", d.RefutedCap)
	}
	cfg := DefaultConfig().Confidence
	if cfg.Default != 0.25 || cfg.HighAt != 0.75 || cfg.MediumAt != 0.40 || cfg.Floor != 0.05 {
		t.Errorf("cutoffs drifted: default=%v high=%v medium=%v floor=%v",
			cfg.Default, cfg.HighAt, cfg.MediumAt, cfg.Floor)
	}
}
