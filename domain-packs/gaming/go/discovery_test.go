package gaming

import (
	"os"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/domainpack"
)

func init() {
	// Tests run from domain-packs/gaming/go/ — DOMAIN_PACK_PATH is the domain-packs root
	os.Setenv("DOMAIN_PACK_PATH", "../..")
}

func TestGamingPackImplementsDiscoveryPack(t *testing.T) {
	pack := NewPack()
	dp, ok := domainpack.AsDiscoveryPack(pack)
	if !ok {
		t.Fatal("GamingPack does not implement DiscoveryPack")
	}
	if dp == nil {
		t.Fatal("AsDiscoveryPack returned nil")
	}
}

func TestDomainCategories(t *testing.T) {
	pack := NewPack()
	cats := pack.DomainCategories()

	if len(cats) != 3 {
		t.Fatalf("DomainCategories returned %d categories, want 3", len(cats))
	}

	expectedCats := map[string]bool{"match3": false, "idle": false, "casual": false}
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

	if len(areas) != 3 {
		t.Errorf("base areas = %d, want 3", len(areas))
	}

	ids := make(map[string]bool)
	for _, a := range areas {
		ids[a.ID] = true
		if !a.IsBase {
			t.Errorf("area %q should be IsBase=true", a.ID)
		}
	}

	for _, expected := range []string{"churn", "engagement", "monetization"} {
		if !ids[expected] {
			t.Errorf("missing base area: %s", expected)
		}
	}
}

func TestAnalysisAreasMatch3(t *testing.T) {
	pack := NewPack()
	areas := pack.AnalysisAreas("match3")

	if len(areas) != 5 {
		t.Errorf("match3 areas = %d, want 5 (3 base + 2 category)", len(areas))
	}

	ids := make(map[string]bool)
	for _, a := range areas {
		ids[a.ID] = true
	}

	for _, expected := range []string{"churn", "engagement", "monetization", "levels", "boosters"} {
		if !ids[expected] {
			t.Errorf("missing area: %s", expected)
		}
	}
}

func TestAnalysisAreasUnknownCategory(t *testing.T) {
	pack := NewPack()
	areas := pack.AnalysisAreas("nonexistent")

	// Should return only base areas
	if len(areas) != 3 {
		t.Errorf("unknown category areas = %d, want 3 (base only)", len(areas))
	}
}

func TestPromptsBase(t *testing.T) {
	pack := NewPack()
	prompts := pack.Prompts("")

	if prompts.Exploration == "" {
		t.Error("Exploration prompt is empty")
	}
	if !strings.Contains(prompts.Exploration, "Gaming Analytics Discovery") {
		t.Error("Exploration prompt missing expected header")
	}
	if prompts.Recommendations == "" {
		t.Error("Recommendations prompt is empty")
	}

	// Should have base analysis areas only
	for _, id := range []string{"churn", "engagement", "monetization"} {
		if _, ok := prompts.AnalysisAreas[id]; !ok {
			t.Errorf("missing base analysis prompt: %s", id)
		}
	}

	// Should NOT have category-specific areas
	if _, ok := prompts.AnalysisAreas["levels"]; ok {
		t.Error("base prompts should not include 'levels' area")
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

	// {{PROFILE}} and {{PREVIOUS_CONTEXT}} should NOT be in exploration prompt
	// (they come from base_context which is prepended by the orchestrator)
	if strings.Contains(prompts.Exploration, "{{PROFILE}}") {
		t.Error("exploration prompt should not contain {{PROFILE}} — moved to base_context")
	}
	if strings.Contains(prompts.Exploration, "{{PREVIOUS_CONTEXT}}") {
		t.Error("exploration prompt should not contain {{PREVIOUS_CONTEXT}} — moved to base_context")
	}
}

func TestPromptsBase_NoProfileInAnalysis(t *testing.T) {
	pack := NewPack()
	prompts := pack.Prompts("match3")

	for id, content := range prompts.AnalysisAreas {
		if strings.Contains(content, "{{PROFILE}}") {
			t.Errorf("analysis prompt %q should not contain {{PROFILE}} — moved to base_context", id)
		}
		if strings.Contains(content, "{{PREVIOUS_CONTEXT}}") {
			t.Errorf("analysis prompt %q should not contain {{PREVIOUS_CONTEXT}} — moved to base_context", id)
		}
	}
}

func TestPromptsMatch3Merge(t *testing.T) {
	pack := NewPack()
	prompts := pack.Prompts("match3")

	// Exploration should contain both base and match3 context
	if !strings.Contains(prompts.Exploration, "Gaming Analytics Discovery") {
		t.Error("merged exploration missing base content")
	}
	if !strings.Contains(prompts.Exploration, "Match-3 Game Context") {
		t.Error("merged exploration missing match3 context")
	}

	// Should have base + category-specific analysis areas
	for _, id := range []string{"churn", "engagement", "monetization", "levels", "boosters"} {
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

	for _, expected := range []string{"basic_info", "gameplay", "monetization", "social_features", "live_ops", "kpis"} {
		if _, ok := props[expected]; !ok {
			t.Errorf("base schema missing property: %s", expected)
		}
	}
}

func TestProfileSchemaMatch3Merge(t *testing.T) {
	pack := NewPack()
	schema := pack.ProfileSchema("match3")

	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema has no properties")
	}

	// Should have base properties
	for _, expected := range []string{"basic_info", "gameplay", "monetization", "kpis"} {
		if _, ok := props[expected]; !ok {
			t.Errorf("merged schema missing base property: %s", expected)
		}
	}

	// Should have match3-specific properties
	for _, expected := range []string{"progression", "boosters", "iap_packages", "lootboxes"} {
		if _, ok := props[expected]; !ok {
			t.Errorf("merged schema missing match3 property: %s", expected)
		}
	}
}

func TestAnalysisAreasIdle(t *testing.T) {
	pack := NewPack()
	areas := pack.AnalysisAreas("idle")

	if len(areas) != 5 {
		t.Errorf("idle areas = %d, want 5 (3 base + 2 category)", len(areas))
	}

	ids := make(map[string]bool)
	for _, a := range areas {
		ids[a.ID] = true
	}

	for _, expected := range []string{"churn", "engagement", "monetization", "progression", "economy"} {
		if !ids[expected] {
			t.Errorf("missing area: %s", expected)
		}
	}
}

func TestAnalysisAreasCasual(t *testing.T) {
	pack := NewPack()
	areas := pack.AnalysisAreas("casual")

	if len(areas) != 5 {
		t.Errorf("casual areas = %d, want 5 (3 base + 2 category)", len(areas))
	}

	ids := make(map[string]bool)
	for _, a := range areas {
		ids[a.ID] = true
	}

	for _, expected := range []string{"churn", "engagement", "monetization", "ad_performance", "session_flow"} {
		if !ids[expected] {
			t.Errorf("missing area: %s", expected)
		}
	}
}

func TestPromptsIdleMerge(t *testing.T) {
	pack := NewPack()
	prompts := pack.Prompts("idle")

	if !strings.Contains(prompts.Exploration, "Gaming Analytics Discovery") {
		t.Error("merged exploration missing base content")
	}
	if !strings.Contains(prompts.Exploration, "Idle / Incremental Game Context") {
		t.Error("merged exploration missing idle context")
	}

	for _, id := range []string{"churn", "engagement", "monetization", "progression", "economy"} {
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

func TestPromptsCasualMerge(t *testing.T) {
	pack := NewPack()
	prompts := pack.Prompts("casual")

	if !strings.Contains(prompts.Exploration, "Gaming Analytics Discovery") {
		t.Error("merged exploration missing base content")
	}
	if !strings.Contains(prompts.Exploration, "Casual / Hyper-Casual Game Context") {
		t.Error("merged exploration missing casual context")
	}

	for _, id := range []string{"churn", "engagement", "monetization", "ad_performance", "session_flow"} {
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

func TestProfileSchemaIdleMerge(t *testing.T) {
	pack := NewPack()
	schema := pack.ProfileSchema("idle")

	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema has no properties")
	}

	// Base properties
	for _, expected := range []string{"basic_info", "gameplay", "monetization", "kpis"} {
		if _, ok := props[expected]; !ok {
			t.Errorf("merged schema missing base property: %s", expected)
		}
	}

	// Idle-specific properties
	for _, expected := range []string{"progression", "currencies", "generators"} {
		if _, ok := props[expected]; !ok {
			t.Errorf("merged schema missing idle property: %s", expected)
		}
	}
}

func TestProfileSchemaCasualMerge(t *testing.T) {
	pack := NewPack()
	schema := pack.ProfileSchema("casual")

	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema has no properties")
	}

	// Base properties
	for _, expected := range []string{"basic_info", "gameplay", "monetization", "kpis"} {
		if _, ok := props[expected]; !ok {
			t.Errorf("merged schema missing base property: %s", expected)
		}
	}

	// Casual-specific properties
	for _, expected := range []string{"core_loop", "onboarding", "ad_setup"} {
		if _, ok := props[expected]; !ok {
			t.Errorf("merged schema missing casual property: %s", expected)
		}
	}
}

func TestAnalysisAreaKeywords(t *testing.T) {
	pack := NewPack()
	areas := pack.AnalysisAreas("match3")

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
