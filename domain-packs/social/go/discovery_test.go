package social

import (
	"os"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/domainpack"
)

func init() {
	// Tests run from domain-packs/social/go/ — DOMAIN_PACK_PATH is the domain-packs root
	os.Setenv("DOMAIN_PACK_PATH", "../..")
}

func TestSocialPackImplementsDiscoveryPack(t *testing.T) {
	pack := NewPack()
	dp, ok := domainpack.AsDiscoveryPack(pack)
	if !ok {
		t.Fatal("SocialPack does not implement DiscoveryPack")
	}
	if dp == nil {
		t.Fatal("AsDiscoveryPack returned nil")
	}
}

func TestSocialPackName(t *testing.T) {
	pack := NewPack()
	if pack.Name() != "social" {
		t.Errorf("Name() = %q, want %q", pack.Name(), "social")
	}
}

func TestDomainCategories(t *testing.T) {
	pack := NewPack()
	cats := pack.DomainCategories()

	if len(cats) == 0 {
		t.Fatal("DomainCategories returned empty list")
	}

	found := false
	for _, c := range cats {
		if c.ID == "content_sharing" {
			found = true
			if c.Name == "" {
				t.Error("content_sharing category has empty Name")
			}
			if c.Description == "" {
				t.Error("content_sharing category has empty Description")
			}
		}
	}
	if !found {
		t.Error("content_sharing category not found")
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

	for _, expected := range []string{"growth", "engagement", "retention"} {
		if !ids[expected] {
			t.Errorf("missing base area: %s", expected)
		}
	}
}

func TestAnalysisAreasContentSharing(t *testing.T) {
	pack := NewPack()
	areas := pack.AnalysisAreas("content_sharing")

	if len(areas) != 5 {
		t.Errorf("content_sharing areas = %d, want 5 (3 base + 2 category)", len(areas))
	}

	ids := make(map[string]bool)
	for _, a := range areas {
		ids[a.ID] = true
	}

	for _, expected := range []string{"growth", "engagement", "retention", "content_creation", "monetization"} {
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
	if !strings.Contains(prompts.Exploration, "Social Network Analytics Discovery") {
		t.Error("Exploration prompt missing expected header")
	}
	if prompts.Recommendations == "" {
		t.Error("Recommendations prompt is empty")
	}

	// Should have base analysis areas only
	for _, id := range []string{"growth", "engagement", "retention"} {
		if _, ok := prompts.AnalysisAreas[id]; !ok {
			t.Errorf("missing base analysis prompt: %s", id)
		}
	}

	// Should NOT have category-specific areas
	if _, ok := prompts.AnalysisAreas["content_creation"]; ok {
		t.Error("base prompts should not include 'content_creation' area")
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
	prompts := pack.Prompts("content_sharing")

	for id, content := range prompts.AnalysisAreas {
		if strings.Contains(content, "{{PROFILE}}") {
			t.Errorf("analysis prompt %q should not contain {{PROFILE}} — moved to base_context", id)
		}
		if strings.Contains(content, "{{PREVIOUS_CONTEXT}}") {
			t.Errorf("analysis prompt %q should not contain {{PREVIOUS_CONTEXT}} — moved to base_context", id)
		}
	}
}

func TestPromptsContentSharingMerge(t *testing.T) {
	pack := NewPack()
	prompts := pack.Prompts("content_sharing")

	// Exploration should contain both base and content_sharing context
	if !strings.Contains(prompts.Exploration, "Social Network Analytics Discovery") {
		t.Error("merged exploration missing base content")
	}
	if !strings.Contains(prompts.Exploration, "Content Sharing Platform Context") {
		t.Error("merged exploration missing content_sharing context")
	}

	// Should have base + category-specific analysis areas
	for _, id := range []string{"growth", "engagement", "retention", "content_creation", "monetization"} {
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

	for _, expected := range []string{"platform_info", "engagement_model", "monetization", "kpis"} {
		if _, ok := props[expected]; !ok {
			t.Errorf("base schema missing property: %s", expected)
		}
	}
}

func TestProfileSchemaContentSharingMerge(t *testing.T) {
	pack := NewPack()
	schema := pack.ProfileSchema("content_sharing")

	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema has no properties")
	}

	// Should have base properties
	for _, expected := range []string{"platform_info", "engagement_model", "monetization", "kpis"} {
		if _, ok := props[expected]; !ok {
			t.Errorf("merged schema missing base property: %s", expected)
		}
	}

	// Should have content_sharing-specific properties
	for _, expected := range []string{"content_types", "discovery_features", "interaction_types", "creator_tools"} {
		if _, ok := props[expected]; !ok {
			t.Errorf("merged schema missing content_sharing property: %s", expected)
		}
	}
}

func TestAnalysisAreaKeywords(t *testing.T) {
	pack := NewPack()
	areas := pack.AnalysisAreas("content_sharing")

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

func TestAnalysisAreaPriorityOrdering(t *testing.T) {
	pack := NewPack()
	areas := pack.AnalysisAreas("content_sharing")

	prevPriority := 0
	for _, a := range areas {
		if a.Priority < prevPriority {
			t.Errorf("area %q priority %d is less than previous priority %d — areas should be in ascending priority order", a.ID, a.Priority, prevPriority)
		}
		prevPriority = a.Priority
	}
}
