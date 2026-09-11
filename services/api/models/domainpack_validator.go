package models

import (
	"fmt"
	"regexp"
	"strings"

	warehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
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
// — the dashboard's POST / PUT / import handlers run it, and the
// enterprise packgen plugin's auto-fix loop runs it so a generated
// pack that would later be rejected at save time is caught at
// generation time instead.
//
// The seed loader deliberately does NOT: built-in packs ship in the
// binary, so a failure there is a build-time mistake rather than
// something an operator can act on at startup, and refusing to seed
// leaves a fresh install with no packs at all.
//
// The pack's OWN shape decides the shape-dependent checks here. That
// is right for a save: the pack being persisted is the pack, and it
// is the shape pack selection will later key on, so a pack labelled
// cube is one asked cube questions ever after. It is wrong mid-
// generation, where the shape comes from the target datasource and
// the model's raw output carries none — see
// ValidateDomainPackForTarget.
//
// Checks:
//   - slug shape (DomainPackSlugRegex, min length 2)
//   - name + at least one category, each with id + name
//   - a declared source shape is one this build knows (absent is legal)
//   - base prompts non-empty
//   - required template placeholders: {{PROFILE}} in base_context;
//     {{SCHEMA_INFO}}, {{ANALYSIS_AREAS}} in exploration;
//     {{INSIGHTS_DATA}} in recommendations — substituted by the
//     discovery agent at run-time and required by the agent's
//     prompt-rendering pipeline. {{DATASET}} joins them for every
//     target but a cube; see requiresTableIdentifier.
//   - at least one base analysis area; every area (base and
//     per-category) has id + name + prompt + at least one keyword.
func ValidateDomainPack(pack *DomainPack) error {
	return ValidateDomainPackForTarget(pack, pack.EffectiveShape())
}

// ValidateDomainPackForTarget is ValidateDomainPack with the shape
// supplied by the caller rather than read from the pack.
//
// The distinction is load-bearing exactly once: inside packgen's
// synth retry loop, where the thing being validated is the model's
// raw output. A generator never emits `shape` — it is not in the
// output schema and is stripped from the example pack — so reading
// the pack's own field there resolves every target to the default
// and leaves a cube's requirements as unsatisfiable as they were
// before this existed. Worse, it would let a hallucinated
// `"shape": "cube"` in the model's output switch the SQL checks off
// for a pack destined for a warehouse.
//
// Relaxing, not inverting: a cube-targeted pack that carries
// {{DATASET}} anyway still passes. The placeholder is harmless
// where nothing substitutes it, and the generator's prompt is what
// stops asking for it, not this.
func ValidateDomainPackForTarget(pack *DomainPack, target warehouse.SourceShape) error {
	if pack.Slug == "" {
		return fmt.Errorf("slug is required")
	}
	if len(pack.Slug) < 2 || !DomainPackSlugRegex.MatchString(pack.Slug) {
		return fmt.Errorf("slug must be lowercase alphanumeric with hyphens (e.g. 'gaming', 'e-commerce')")
	}
	if pack.Name == "" {
		return fmt.Errorf("name is required")
	}
	// A declared shape must be one the build understands. An unrecognised
	// spelling would otherwise persist happily and then match nothing wherever
	// shape is compared, which reads as a feature quietly not working rather
	// than as the typo it is. Absent stays legal — it means ShapeEntities.
	if pack.Shape != "" && !pack.Shape.Known() {
		return fmt.Errorf("shape %q is not a source shape (use %q or %q, or leave it unset for %q)",
			pack.Shape, warehouse.ShapeEntities, warehouse.ShapeCube, warehouse.ShapeEntities)
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
	if requiresTableIdentifier(target) && !strings.Contains(pack.Prompts.Base.Exploration, "{{DATASET}}") {
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

// requiresTableIdentifier reports whether a pack written for this target
// must name the tables it queries.
//
// {{DATASET}} is substituted with the connected datasource's dataset or
// schema names, so the exploration prompt can say which tables exist to
// query. A cube has none: a query there is a choice of metrics and
// dimensions against a property, and there is no identifier to qualify
// them with. Demanding the placeholder of such a pack is not a rule the
// generator can meet by trying harder — there is nothing for it to say —
// so it fails validation, exhausts its auto-fix budget, and produces
// nothing.
//
// Asked of everything that is not a cube, rather than of an allow-list of
// SQL shapes. A shape this build has never heard of is table-shaped until
// proven otherwise, which fails toward keeping the check rather than
// silently dropping it.
func requiresTableIdentifier(target warehouse.SourceShape) bool {
	return target != warehouse.ShapeCube
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
