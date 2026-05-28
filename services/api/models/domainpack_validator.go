package models

import (
	"fmt"
	"regexp"
	"strings"
)

// DomainPackSlugRegex is the canonical slug shape for a saved domain
// pack. Mirrors the regex enforced by the API on pack creation and
// import; exported so the enterprise packgen plugin can validate
// generated slugs against the same contract before persistence.
var DomainPackSlugRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

// ValidateDomainPack returns nil if the pack passes every check the
// API's save / import flow runs, or an error describing the first
// failure.
//
// This is the single source of truth for "is this pack persistable"
// — the dashboard's PUT /api/v1/domain-packs/{slug} handler runs
// it, the seed-loading flow runs it, and the enterprise packgen
// plugin's auto-fix loop runs it (via its own import) so a
// generated pack that would later be rejected at save time is
// caught at generation time instead.
//
// Checks:
//   - slug shape (DomainPackSlugRegex, min length 2)
//   - name + at least one category, each with id + name
//   - base prompts non-empty
//   - required template placeholders: {{PROFILE}} in base_context;
//     {{DATASET}}, {{SCHEMA_INFO}}, {{ANALYSIS_AREAS}} in exploration;
//     {{INSIGHTS_DATA}} in recommendations — substituted by the
//     discovery agent at run-time and required by the agent's
//     prompt-rendering pipeline.
//   - at least one base analysis area; every area (base and
//     per-category) has id + name + prompt + at least one keyword.
func ValidateDomainPack(pack *DomainPack) error {
	if pack.Slug == "" {
		return fmt.Errorf("slug is required")
	}
	if len(pack.Slug) < 2 || !DomainPackSlugRegex.MatchString(pack.Slug) {
		return fmt.Errorf("slug must be lowercase alphanumeric with hyphens (e.g. 'gaming', 'e-commerce')")
	}
	if pack.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len(pack.Categories) == 0 {
		return fmt.Errorf("at least one category is required")
	}

	for i, cat := range pack.Categories {
		if cat.ID == "" {
			return fmt.Errorf("category %d: id is required", i)
		}
		if cat.Name == "" {
			return fmt.Errorf("category %q: name is required", cat.ID)
		}
	}

	if pack.Prompts.Base.BaseContext == "" {
		return fmt.Errorf("base_context prompt is required")
	}
	if pack.Prompts.Base.Exploration == "" {
		return fmt.Errorf("exploration prompt is required")
	}
	if pack.Prompts.Base.Recommendations == "" {
		return fmt.Errorf("recommendations prompt is required")
	}

	if !strings.Contains(pack.Prompts.Base.BaseContext, "{{PROFILE}}") {
		return fmt.Errorf("base_context must contain {{PROFILE}} template variable")
	}
	if !strings.Contains(pack.Prompts.Base.Exploration, "{{DATASET}}") {
		return fmt.Errorf("exploration must contain {{DATASET}} template variable")
	}
	if !strings.Contains(pack.Prompts.Base.Exploration, "{{SCHEMA_INFO}}") {
		return fmt.Errorf("exploration must contain {{SCHEMA_INFO}} template variable")
	}
	if !strings.Contains(pack.Prompts.Base.Exploration, "{{ANALYSIS_AREAS}}") {
		return fmt.Errorf("exploration must contain {{ANALYSIS_AREAS}} template variable")
	}
	if !strings.Contains(pack.Prompts.Base.Recommendations, "{{INSIGHTS_DATA}}") {
		return fmt.Errorf("recommendations must contain {{INSIGHTS_DATA}} template variable")
	}

	if len(pack.AnalysisAreas.Base) == 0 {
		return fmt.Errorf("at least one base analysis area is required")
	}
	for i, area := range pack.AnalysisAreas.Base {
		if err := validateAnalysisArea(area, fmt.Sprintf("base area %d", i)); err != nil {
			return err
		}
	}

	for catID, areas := range pack.AnalysisAreas.Categories {
		for i, area := range areas {
			if err := validateAnalysisArea(area, fmt.Sprintf("category %q area %d", catID, i)); err != nil {
				return err
			}
		}
	}

	return nil
}

func validateAnalysisArea(area PackAnalysisArea, label string) error {
	if area.ID == "" {
		return fmt.Errorf("%s: id is required", label)
	}
	if area.Name == "" {
		return fmt.Errorf("%s (%s): name is required", label, area.ID)
	}
	if area.Prompt == "" {
		return fmt.Errorf("%s (%s): prompt is required", label, area.ID)
	}
	if len(area.Keywords) == 0 {
		return fmt.Errorf("%s (%s): at least one keyword is required", label, area.ID)
	}
	return nil
}
