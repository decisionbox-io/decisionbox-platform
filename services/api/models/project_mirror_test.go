package models

import (
	"os"
	"reflect"
	"strings"
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

// The project and run documents have TWO persisted copies -- this package's,
// which the API reads and writes, and the agent's own mirror behind an
// `internal` boundary -- and until this file nothing guarded either pair.
// Insight and Recommendation are guarded (insight_mirror_test.go) because an
// omission there was found the hard way: a field the agent persists and the
// mirror omits is dropped by BSON on decode, so the endpoint reports the zero
// value however non-zero the database is, and nothing fails.
//
// Scoped to the fields the severity-weight configuration adds, rather than an
// exhaustive partition of both structs. An exhaustive sweep here would
// probably fail on divergence that predates this feature, which is a separate
// finding and not one this change is entitled to decide. What it does cover it
// covers in both directions: the key, the wire name, and the Go shape.
const (
	agentProjectSource = "../../agent/internal/models/project.go"
	agentRunSource     = "../../agent/internal/models/run.go"
)

// mirroredField is one field both copies must declare the same way.
type mirroredField struct {
	// goName is the field as both structs name it. The two packages are
	// hand-synced, so the Go names match as well as the keys.
	goName string
	// bson is the stored key -- what BSON drops when the mirror omits it.
	bson string
	// json is the wire name a client reads.
	json string
	// kind is the Go shape the field must have. Load-bearing for
	// SeverityWeight: a value type there cannot express "stated zero", which
	// is the one weight validation has to reject.
	kind reflect.Kind
}

// riskConfigMirrors is every struct this feature adds a persisted field to,
// paired with the agent source that declares its twin.
var riskConfigMirrors = []struct {
	name   string
	source string
	fields []mirroredField
}{
	{
		name:   "Project",
		source: agentProjectSource,
		fields: []mirroredField{
			{"CategoryWeights", "category_weights", "category_weights", reflect.Map},
		},
	},
	{
		name:   "ProjectPrompts",
		source: agentProjectSource,
		fields: []mirroredField{
			{"RiskTaxonomy", "risk_taxonomy", "risk_taxonomy", reflect.Slice},
		},
	},
	{
		name:   "AnalysisAreaConfig",
		source: agentProjectSource,
		fields: []mirroredField{
			{"RiskCategories", "risk_categories", "risk_categories", reflect.Slice},
			{"SeverityWeight", "severity_weight", "severity_weight", reflect.Ptr},
		},
	},
	{
		name:   "DiscoveryRun",
		source: agentRunSource,
		fields: []mirroredField{
			// A pointer: nil is how a run that predates the pin says it
			// recorded no weights, which is different from one pinned with
			// none configured.
			{"ScoringConfig", "scoring_config", "scoring_config", reflect.Ptr},
		},
	},
}

// TestTheProjectMirrorsCarryTheRiskConfiguration -- the API side declares each
// field with the key, the wire name and the shape the contract states.
func TestTheProjectMirrorsCarryTheRiskConfiguration(t *testing.T) {
	byName := map[string]reflect.Type{
		"Project":            reflect.TypeOf(Project{}),
		"ProjectPrompts":     reflect.TypeOf(ProjectPrompts{}),
		"AnalysisAreaConfig": reflect.TypeOf(AnalysisAreaConfig{}),
		"DiscoveryRun":       reflect.TypeOf(DiscoveryRun{}),
	}

	for _, m := range riskConfigMirrors {
		rt, ok := byName[m.name]
		if !ok {
			t.Fatalf("no API struct registered for %s; the table and the map have drifted", m.name)
		}
		for _, f := range m.fields {
			field, ok := rt.FieldByName(f.goName)
			if !ok {
				t.Errorf("%s has no %s field", m.name, f.goName)
				continue
			}
			if got := strings.Split(field.Tag.Get("bson"), ",")[0]; got != f.bson {
				t.Errorf("%s.%s bson key = %q, want %q", m.name, f.goName, got, f.bson)
			}
			if got := strings.Split(field.Tag.Get("json"), ",")[0]; got != f.json {
				t.Errorf("%s.%s json name = %q, want %q", m.name, f.goName, got, f.json)
			}
			if field.Type.Kind() != f.kind {
				t.Errorf("%s.%s is %s, want %s", m.name, f.goName, field.Type.Kind(), f.kind)
			}
		}
	}
}

// TestTheAgentMirrorsCarryTheRiskConfiguration -- the agent's own copies
// declare the same stored keys.
//
// Reads the agent's source rather than importing the type, for the reason the
// insight guard does: the two live in different modules behind an `internal`
// boundary, and the alternative to reading the file is not checking at all.
func TestTheAgentMirrorsCarryTheRiskConfiguration(t *testing.T) {
	for _, m := range riskConfigMirrors {
		src, err := os.ReadFile(m.source)
		if err != nil {
			t.Skipf("agent source not readable (%v); this guard only runs in the monorepo", err)
		}
		have := map[string]bool{}
		for _, key := range agentStructBSONKeys(t, src, m.name, m.source) {
			have[key] = true
		}
		for _, f := range m.fields {
			if !have[f.bson] {
				t.Errorf("the agent's %s does not declare %q, so BSON drops it: the agent writes a run that "+
					"never reads this configuration back, or reads a project that appears not to have it.\n"+
					"Add the field to %s.", m.name, f.bson, m.source)
			}
		}
	}
}

// TestTheRiskTaxonomyIsOneStructEverywhere -- the taxonomy type is the shared
// one, not a per-package copy of it.
//
// The taxonomy is stored in four places that cannot all import each other:
// the pack, the project, the agent's project mirror and the engine. Three
// hand-written copies of a struct is how the stored shapes come to disagree
// about a field, and BSON reports that disagreement as an absent value rather
// than as an error -- so the type identity is asserted rather than assumed.
func TestTheRiskTaxonomyIsOneStructEverywhere(t *testing.T) {
	shared := reflect.TypeOf(gomodels.RiskCategory{})

	if got := reflect.TypeOf(DomainPack{}).Field(fieldIndex(t, reflect.TypeOf(DomainPack{}), "RiskTaxonomy")).Type.Elem(); got != shared {
		t.Errorf("DomainPack.RiskTaxonomy element type = %s, want %s", got, shared)
	}
	if got := reflect.TypeOf(ProjectPrompts{}).Field(fieldIndex(t, reflect.TypeOf(ProjectPrompts{}), "RiskTaxonomy")).Type.Elem(); got != shared {
		t.Errorf("ProjectPrompts.RiskTaxonomy element type = %s, want %s", got, shared)
	}
	// PackRiskCategory is the name the pack contract uses; it must be an
	// alias for the shared struct and not a second declaration of it.
	if got := reflect.TypeOf(PackRiskCategory{}); got != shared {
		t.Errorf("PackRiskCategory = %s, want it to be an alias for %s", got, shared)
	}

	// The run's pinned configuration, for the same reason: it is written by
	// the agent and read back by the agent, through two struct declarations
	// that would otherwise be two shapes.
	runCfg := reflect.TypeOf(gomodels.RunScoringConfig{})
	runField, ok := reflect.TypeOf(DiscoveryRun{}).FieldByName("ScoringConfig")
	if !ok {
		t.Fatal("DiscoveryRun has no ScoringConfig field")
	}
	if got := runField.Type.Elem(); got != runCfg {
		t.Errorf("DiscoveryRun.ScoringConfig points at %s, want %s", got, runCfg)
	}

	// The agent's copy, read from source: the element type is the shared one
	// there too.
	src, err := os.ReadFile(agentProjectSource)
	if err != nil {
		t.Skipf("agent source not readable (%v); this guard only runs in the monorepo", err)
	}
	if !strings.Contains(string(src), "RiskTaxonomy []gomodels.RiskCategory") {
		t.Errorf("the agent's ProjectPrompts does not declare RiskTaxonomy as []gomodels.RiskCategory; "+
			"a copy of the struct in %s would be a fourth shape of one contract", agentProjectSource)
	}
}

// fieldIndex is the index of a named field, failing the test rather than
// returning a zero that would silently assert something about field 0.
func fieldIndex(t *testing.T, rt reflect.Type, name string) int {
	t.Helper()
	field, ok := rt.FieldByName(name)
	if !ok {
		t.Fatalf("%s has no %s field", rt.Name(), name)
	}
	return field.Index[0]
}
