package models

import (
	"strings"
	"testing"

	warehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// noDatasetPack is the minimal valid pack with the table-identifier
// placeholder taken out of its exploration prompt — a pack written for a
// source that has no tables to name.
func noDatasetPack(t *testing.T) *DomainPack {
	t.Helper()
	pack := testDomainPack("analytics", "web")
	pack.Prompts.Base.Exploration = "Explore using {{SCHEMA_INFO}} areas: {{ANALYSIS_AREAS}}"
	pack.AnalysisAreas.Base[0].Prompt = "Analyze the property: {{QUERY_RESULTS}}"
	return pack
}

// TestValidate_CubePackNeedsNoDataset is the blocker this change removes.
//
// {{DATASET}} is substituted with the datasource's dataset or schema names.
// A cube has neither, so the requirement is not merely unmet on such a pack
// — it is unsatisfiable, and a generator asked to satisfy it re-rolls until
// its auto-fix budget is gone.
func TestValidate_CubePackNeedsNoDataset(t *testing.T) {
	pack := noDatasetPack(t)
	pack.Shape = warehouse.ShapeCube
	if err := ValidateDomainPack(pack); err != nil {
		t.Errorf("a cube-shaped pack without {{DATASET}} should save: %v", err)
	}
}

// TestValidate_CubePackMayStillCarryDataset pins that this relaxes the rule
// rather than inverting it. The placeholder is harmless where nothing
// substitutes it, and the generator's prompt is what stops asking for it —
// so a pack that carries one anyway must not become unsavable.
func TestValidate_CubePackMayStillCarryDataset(t *testing.T) {
	pack := testDomainPack("analytics", "web")
	pack.Shape = warehouse.ShapeCube
	if err := ValidateDomainPack(pack); err != nil {
		t.Errorf("a cube pack carrying {{DATASET}} should still save: %v", err)
	}
}

// TestValidate_TableTargetStillRequiresDataset asserts the SQL rule is
// untouched, in both spellings of table-shaped: declared entities, and the
// absent shape that every pack in the existing corpus carries.
func TestValidate_TableTargetStillRequiresDataset(t *testing.T) {
	for _, shape := range []warehouse.SourceShape{warehouse.ShapeEntities, ""} {
		pack := noDatasetPack(t)
		pack.Shape = shape
		err := ValidateDomainPack(pack)
		if err == nil || !strings.Contains(err.Error(), "{{DATASET}}") {
			t.Errorf("shape %q: should still require {{DATASET}}, got %v", shape, err)
		}
	}
}

// TestValidateForTarget_ShapeComesFromTheCaller is the case the exported
// no-target form cannot serve, and the reason this signature exists.
//
// Mid-generation the pack is the model's raw output, which never carries
// shape: it is not in the output schema and it is stripped from the example
// pack the model imitates. Reading the pack's own field there resolves every
// target to the default, which would leave the cube requirement exactly as
// unsatisfiable as it was before this change — the same way W9's first
// enterprise attempt did nothing.
func TestValidateForTarget_ShapeComesFromTheCaller(t *testing.T) {
	pack := noDatasetPack(t)
	if pack.Shape != "" {
		t.Fatalf("this test is about a pack with no shape, got %q", pack.Shape)
	}
	if err := ValidateDomainPackForTarget(pack, warehouse.ShapeCube); err != nil {
		t.Errorf("a cube target should not demand {{DATASET}} of an unlabelled pack: %v", err)
	}
}

// TestValidateForTarget_TargetBeatsAHallucinatedShape is the other half of
// the same reason. A generator that invents "shape": "cube" in its output
// must not thereby switch the SQL checks off for a pack destined for a
// warehouse — the target is the fact, the pack's field is a claim.
func TestValidateForTarget_TargetBeatsAHallucinatedShape(t *testing.T) {
	pack := noDatasetPack(t)
	pack.Shape = warehouse.ShapeCube
	err := ValidateDomainPackForTarget(pack, warehouse.ShapeEntities)
	if err == nil || !strings.Contains(err.Error(), "{{DATASET}}") {
		t.Errorf("a table target should require {{DATASET}} whatever the pack claims, got %v", err)
	}
}

// TestValidateForTarget_UnknownTargetKeepsTheCheck pins the direction the
// predicate fails in. A shape this build has never heard of is treated as
// table-shaped, so a future spelling arriving from an older peer keeps the
// SQL requirement rather than silently dropping it.
func TestValidateForTarget_UnknownTargetKeepsTheCheck(t *testing.T) {
	pack := noDatasetPack(t)
	err := ValidateDomainPackForTarget(pack, warehouse.SourceShape("something-later"))
	if err == nil || !strings.Contains(err.Error(), "{{DATASET}}") {
		t.Errorf("an unknown target should keep the {{DATASET}} check, got %v", err)
	}
}

// TestValidate_CubeRelaxesOnlyTheDataset confines the exemption. Every other
// placeholder is substituted for a cube run exactly as it is for a SQL one —
// the schema slice becomes the catalog descriptor, the areas and profile do
// not change at all — so dropping any of them would break a cube pack at
// run-time rather than accommodate it.
func TestValidate_CubeRelaxesOnlyTheDataset(t *testing.T) {
	cases := []struct {
		name    string
		break_  func(*DomainPack)
		wantVar string
	}{
		{"schema info", func(p *DomainPack) {
			p.Prompts.Base.Exploration = "Explore areas: {{ANALYSIS_AREAS}}"
		}, "{{SCHEMA_INFO}}"},
		{"analysis areas", func(p *DomainPack) {
			p.Prompts.Base.Exploration = "Explore using {{SCHEMA_INFO}}"
		}, "{{ANALYSIS_AREAS}}"},
		{"profile", func(p *DomainPack) {
			p.Prompts.Base.BaseContext = "Context with no profile"
		}, "{{PROFILE}}"},
		{"insights data", func(p *DomainPack) {
			p.Prompts.Base.Recommendations = "Recommend from nothing"
		}, "{{INSIGHTS_DATA}}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pack := noDatasetPack(t)
			pack.Shape = warehouse.ShapeCube
			tc.break_(pack)
			err := ValidateDomainPack(pack)
			if err == nil || !strings.Contains(err.Error(), tc.wantVar) {
				t.Errorf("a cube pack should still require %s, got %v", tc.wantVar, err)
			}
		})
	}
}
