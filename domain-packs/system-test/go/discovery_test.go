package systemtest

import (
	"os"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/domainpack"
)

func init() {
	// Tests run from domain-packs/system-test/go/ — DOMAIN_PACK_PATH is the domain-packs root
	os.Setenv("DOMAIN_PACK_PATH", "../..")
}

func TestSystemTestPackImplementsDiscoveryPack(t *testing.T) {
	pack := NewPack()
	dp, ok := domainpack.AsDiscoveryPack(pack)
	if !ok {
		t.Fatal("SystemTestPack does not implement DiscoveryPack")
	}
	if dp == nil {
		t.Fatal("AsDiscoveryPack returned nil")
	}
}

func TestName(t *testing.T) {
	pack := NewPack()
	if pack.Name() != "system-test" {
		t.Errorf("Name() = %q, want %q", pack.Name(), "system-test")
	}
}

func TestDomainCategories(t *testing.T) {
	pack := NewPack()
	cats := pack.DomainCategories()

	if len(cats) != 3 {
		t.Fatalf("DomainCategories returned %d categories, want 3", len(cats))
	}

	expectedCats := map[string]bool{"quick": false, "standard": false, "thorough": false}
	for _, c := range cats {
		if _, ok := expectedCats[c.ID]; ok {
			expectedCats[c.ID] = true
			if c.Name == "" {
				t.Errorf("%s category has empty Name", c.ID)
			}
			if c.Description == "" {
				t.Errorf("%s category has empty Description", c.ID)
			}
		}
	}
	for id, found := range expectedCats {
		if !found {
			t.Errorf("category %q not found", id)
		}
	}
}

func TestAnalysisAreasBase(t *testing.T) {
	pack := NewPack()
	areas := pack.AnalysisAreas("")

	if len(areas) != 2 {
		t.Errorf("base areas = %d, want 2", len(areas))
	}

	ids := make(map[string]bool)
	for _, a := range areas {
		ids[a.ID] = true
		if !a.IsBase {
			t.Errorf("area %q should be IsBase=true", a.ID)
		}
	}

	for _, expected := range []string{"connectivity", "schema_discovery"} {
		if !ids[expected] {
			t.Errorf("missing base area: %s", expected)
		}
	}
}

func TestAnalysisAreasQuick(t *testing.T) {
	pack := NewPack()
	areas := pack.AnalysisAreas("quick")

	// Quick has no extra areas — just base (2)
	if len(areas) != 2 {
		t.Errorf("quick areas = %d, want 2 (base only)", len(areas))
	}
}

func TestAnalysisAreasStandard(t *testing.T) {
	pack := NewPack()
	areas := pack.AnalysisAreas("standard")

	if len(areas) != 4 {
		t.Errorf("standard areas = %d, want 4 (2 base + 2 category)", len(areas))
	}

	ids := make(map[string]bool)
	for _, a := range areas {
		ids[a.ID] = true
	}

	for _, expected := range []string{"connectivity", "schema_discovery", "type_mapping", "data_profiling"} {
		if !ids[expected] {
			t.Errorf("missing area: %s", expected)
		}
	}
}

func TestAnalysisAreasThorough(t *testing.T) {
	pack := NewPack()
	areas := pack.AnalysisAreas("thorough")

	if len(areas) != 6 {
		t.Errorf("thorough areas = %d, want 6 (2 base + 4 category)", len(areas))
	}

	ids := make(map[string]bool)
	for _, a := range areas {
		ids[a.ID] = true
	}

	for _, expected := range []string{"connectivity", "schema_discovery", "type_mapping", "data_profiling", "query_patterns", "edge_cases"} {
		if !ids[expected] {
			t.Errorf("missing area: %s", expected)
		}
	}
}

func TestAnalysisAreasUnknownCategory(t *testing.T) {
	pack := NewPack()
	areas := pack.AnalysisAreas("nonexistent")

	if len(areas) != 2 {
		t.Errorf("unknown category areas = %d, want 2 (base only)", len(areas))
	}
}

func TestAnalysisAreaKeywords(t *testing.T) {
	pack := NewPack()
	areas := pack.AnalysisAreas("thorough")

	for _, a := range areas {
		if len(a.Keywords) == 0 {
			t.Errorf("area %q has no keywords", a.ID)
		}
		if a.Name == "" {
			t.Errorf("area %q has empty Name", a.ID)
		}
		if a.Priority == 0 {
			t.Errorf("area %q has zero Priority", a.ID)
		}
	}
}

func TestPromptsBase(t *testing.T) {
	pack := NewPack()
	prompts := pack.Prompts("")

	if prompts.Exploration == "" {
		t.Error("Exploration prompt is empty")
	}
	if !strings.Contains(prompts.Exploration, "Warehouse System Validation") {
		t.Error("Exploration prompt missing expected header")
	}
	if prompts.Recommendations == "" {
		t.Error("Recommendations prompt is empty")
	}

	// Should have base analysis areas only
	for _, id := range []string{"connectivity", "schema_discovery"} {
		if _, ok := prompts.AnalysisAreas[id]; !ok {
			t.Errorf("missing base analysis prompt: %s", id)
		}
	}

	// Should NOT have category-specific areas
	if _, ok := prompts.AnalysisAreas["type_mapping"]; ok {
		t.Error("base prompts should not include 'type_mapping' area")
	}

	// BaseContext should be loaded
	if prompts.BaseContext == "" {
		t.Error("BaseContext is empty — base_context.md not loaded")
	}
	if !strings.Contains(prompts.BaseContext, "{{PROFILE}}") {
		t.Error("BaseContext missing {{PROFILE}} placeholder")
	}
	if !strings.Contains(prompts.BaseContext, "{{PREVIOUS_CONTEXT}}") {
		t.Error("BaseContext missing {{PREVIOUS_CONTEXT}} placeholder")
	}
}

func TestPromptsBase_NoProfileInExploration(t *testing.T) {
	pack := NewPack()
	prompts := pack.Prompts("")

	if strings.Contains(prompts.Exploration, "{{PROFILE}}") {
		t.Error("exploration prompt should not contain {{PROFILE}} — moved to base_context")
	}
	if strings.Contains(prompts.Exploration, "{{PREVIOUS_CONTEXT}}") {
		t.Error("exploration prompt should not contain {{PREVIOUS_CONTEXT}} — moved to base_context")
	}
}

func TestPromptsBase_NoProfileInAnalysis(t *testing.T) {
	pack := NewPack()
	prompts := pack.Prompts("thorough")

	for id, content := range prompts.AnalysisAreas {
		if strings.Contains(content, "{{PROFILE}}") {
			t.Errorf("analysis prompt %q should not contain {{PROFILE}} — moved to base_context", id)
		}
		if strings.Contains(content, "{{PREVIOUS_CONTEXT}}") {
			t.Errorf("analysis prompt %q should not contain {{PREVIOUS_CONTEXT}} — moved to base_context", id)
		}
	}
}

func TestPromptsQuickMerge(t *testing.T) {
	pack := NewPack()
	prompts := pack.Prompts("quick")

	if !strings.Contains(prompts.Exploration, "Warehouse System Validation") {
		t.Error("merged exploration missing base content")
	}
	if !strings.Contains(prompts.Exploration, "Quick Validation Context") {
		t.Error("merged exploration missing quick context")
	}

	// Should have base areas only (quick adds no extra areas)
	for _, id := range []string{"connectivity", "schema_discovery"} {
		content, ok := prompts.AnalysisAreas[id]
		if !ok {
			t.Errorf("missing analysis prompt: %s", id)
			continue
		}
		if content == "" {
			t.Errorf("empty analysis prompt: %s", id)
		}
	}
}

func TestPromptsStandardMerge(t *testing.T) {
	pack := NewPack()
	prompts := pack.Prompts("standard")

	if !strings.Contains(prompts.Exploration, "Warehouse System Validation") {
		t.Error("merged exploration missing base content")
	}
	if !strings.Contains(prompts.Exploration, "Standard Validation Context") {
		t.Error("merged exploration missing standard context")
	}

	for _, id := range []string{"connectivity", "schema_discovery", "type_mapping", "data_profiling"} {
		content, ok := prompts.AnalysisAreas[id]
		if !ok {
			t.Errorf("missing analysis prompt: %s", id)
			continue
		}
		if content == "" {
			t.Errorf("empty analysis prompt: %s", id)
		}
	}
}

func TestPromptsThoroughMerge(t *testing.T) {
	pack := NewPack()
	prompts := pack.Prompts("thorough")

	if !strings.Contains(prompts.Exploration, "Warehouse System Validation") {
		t.Error("merged exploration missing base content")
	}
	if !strings.Contains(prompts.Exploration, "Thorough Validation Context") {
		t.Error("merged exploration missing thorough context")
	}

	for _, id := range []string{"connectivity", "schema_discovery", "type_mapping", "data_profiling", "query_patterns", "edge_cases"} {
		content, ok := prompts.AnalysisAreas[id]
		if !ok {
			t.Errorf("missing analysis prompt: %s", id)
			continue
		}
		if content == "" {
			t.Errorf("empty analysis prompt: %s", id)
		}
	}
}

func TestProfileSchemaBase(t *testing.T) {
	pack := NewPack()
	schema := pack.ProfileSchema("")

	if _, ok := schema["error"]; ok {
		t.Fatalf("ProfileSchema returned error: %v", schema["error"])
	}

	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema has no properties")
	}

	if _, ok := props["test_notes"]; !ok {
		t.Error("schema missing test_notes property")
	}
}

func TestProfileSchemaIgnoresCategory(t *testing.T) {
	pack := NewPack()
	baseSchema := pack.ProfileSchema("")
	quickSchema := pack.ProfileSchema("quick")
	thoroughSchema := pack.ProfileSchema("thorough")

	// No category extensions — all categories return the same base schema
	baseProps, _ := baseSchema["properties"].(map[string]interface{})
	quickProps, _ := quickSchema["properties"].(map[string]interface{})
	thoroughProps, _ := thoroughSchema["properties"].(map[string]interface{})

	if len(baseProps) != len(quickProps) {
		t.Error("quick schema should match base schema (no category extensions)")
	}
	if len(baseProps) != len(thoroughProps) {
		t.Error("thorough schema should match base schema (no category extensions)")
	}
}

func TestEnvGatedRegistration(t *testing.T) {
	// The pack registers only when DECISIONBOX_ENABLE_SYSTEM_TEST=true.
	// In test context, init() in pack.go runs but the env var is not set
	// (unless the test runner sets it). We verify the pack can be created
	// and used directly — registration is a separate concern.
	pack := NewPack()
	if pack.Name() != "system-test" {
		t.Errorf("Name() = %q, want %q", pack.Name(), "system-test")
	}
}
