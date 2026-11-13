package models

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/decisionbox-io/decisionbox/libs/go-common/scoring"
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
//   - the risk configuration, when the pack carries one: see
//     RiskConfigIssues. A pack that declares none passes unchanged.
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

	// The risk configuration, last, and through the accumulating form so
	// there is one implementation of the rules rather than two phrasings of
	// them. This contract returns the FIRST failure; the enterprise pack
	// generator calls RiskConfigIssues directly, because its auto-fix loop
	// has three attempts and needs every issue in one round-trip.
	if issues := RiskConfigIssues(pack); len(issues) > 0 {
		return fmt.Errorf("%s", issues[0])
	}

	return nil
}

// RiskCategoryIDRegex is the shape of a risk category id.
//
// Underscores, unlike DomainPackSlugRegex: the engine's own cross-cutting ids
// include `data_quality`, and a rule that rejected the ids the engine ships
// would be a rule no pack could follow.
var RiskCategoryIDRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*[a-z0-9]$`)

// RiskConfigIssues returns everything wrong with a pack's risk configuration:
// its taxonomy, and the per-area mapping and weight override that read it.
//
// Accumulating rather than first-failure, because its other caller is the
// pack generator's auto-fix loop, which has a three-attempt budget: one issue
// per attempt would spend the whole budget on a taxonomy with three bad
// weights in it. ValidateDomainPack presents the first of these as its single
// error, so the two contracts cannot disagree about what is legal.
//
// A pack with no risk configuration at all produces no issues. That is the
// backward-compatibility rule stated where it is enforced: every pack written
// before this config existed must stay persistable.
//
// The rules:
//
//   - a taxonomy id is a lowercase slug, unique within the pack, with a label;
//   - every declared category states a weight, and every stated weight --
//     a category's or an area's override -- is inside (0, MaxSeverityWeight];
//   - an area's risk_categories are ids the pack declares or ids the engine
//     ships globally, each cited at most once.
func RiskConfigIssues(pack *DomainPack) []string {
	if pack == nil {
		return nil
	}

	var issues []string

	// The ids an area may cite: the engine's global set, plus whatever this
	// pack declares. Built before the areas are walked, so an area may cite a
	// category declared after it.
	known := make(map[string]bool, len(pack.RiskTaxonomy)+3)
	for _, id := range scoring.GlobalRiskCategories() {
		known[id] = true
	}

	// A category's weight is REQUIRED rather than defaulted, which
	// riskCategoryIssues enforces: the stored shape cannot tell an unstated
	// weight from one stated as zero, so a rule that let it default would be
	// a rule that could never reject `"weight": 0` -- the one value that
	// would delete every finding in the category from the ranking.
	declared := make(map[string]bool, len(pack.RiskTaxonomy))
	for i, cat := range pack.RiskTaxonomy {
		issues = append(issues, riskCategoryIssues(cat, fmt.Sprintf("risk_taxonomy[%d]", i), declared)...)
		if cat.ID != "" {
			// Citable even when its shape or its weight was rejected, so one
			// bad category produces one issue rather than one per area that
			// maps to it.
			known[cat.ID] = true
		}
	}

	for i, area := range pack.AnalysisAreas.Base {
		issues = append(issues, areaRiskIssues(area, known, fmt.Sprintf("base area %d", i))...)
	}
	// Sorted, because this list is accumulated rather than returned at the
	// first failure: Go randomises map iteration, and an issue list whose
	// order jitters is one the generator's retry context re-shuffles on every
	// attempt.
	catIDs := make([]string, 0, len(pack.AnalysisAreas.Categories))
	for catID := range pack.AnalysisAreas.Categories {
		catIDs = append(catIDs, catID)
	}
	sort.Strings(catIDs)
	for _, catID := range catIDs {
		for i, area := range pack.AnalysisAreas.Categories[catID] {
			issues = append(issues, areaRiskIssues(area, known, fmt.Sprintf("category %q area %d", catID, i))...)
		}
	}

	return issues
}

// ProjectRiskConfigIssues is RiskConfigIssues for a PROJECT's copy of the risk
// configuration: the taxonomy and area overrides snapshotted onto
// ProjectPrompts, plus the project-wide category weights.
//
// The same rules, over a different shape, because a project is editable where
// a pack is generated: the playbook page writes the per-area mapping and the
// override, and the project route writes the category weights. Without this,
// those two endpoints would accept a weight of 4 or a category id nothing
// declares and answer 200 — the engine ignores both, so nothing would be
// wrong with the stored score, but the editor's whole contract is that the
// server refuses what it cannot act on. A save that silently does nothing is
// worse than a rejected one.
//
// The union an area may cite is the PROJECT's taxonomy plus the engine's
// global ids, not the pack's: a project's copy is the one the run reads, and
// it can legitimately differ from the pack it was seeded from.
func ProjectRiskConfigIssues(prompts *ProjectPrompts, categoryWeights map[string]float64) []string {
	var issues []string

	known := make(map[string]bool, 3)
	for _, id := range scoring.GlobalRiskCategories() {
		known[id] = true
	}

	if prompts != nil {
		declared := make(map[string]bool, len(prompts.RiskTaxonomy))
		for i, cat := range prompts.RiskTaxonomy {
			issues = append(issues, riskCategoryIssues(cat, fmt.Sprintf("prompts.risk_taxonomy[%d]", i), declared)...)
			if cat.ID != "" {
				known[cat.ID] = true
			}
		}

		// Sorted, because this list is accumulated: Go randomises map
		// iteration, and an error whose text depends on it is one a client
		// cannot test against.
		areaIDs := make([]string, 0, len(prompts.AnalysisAreas))
		for id := range prompts.AnalysisAreas {
			areaIDs = append(areaIDs, id)
		}
		sort.Strings(areaIDs)
		for _, id := range areaIDs {
			area := prompts.AnalysisAreas[id]
			issues = append(issues, areaRiskIssues(PackAnalysisArea{
				ID:             id,
				RiskCategories: area.RiskCategories,
				SeverityWeight: area.SeverityWeight,
			}, known, fmt.Sprintf("analysis_areas[%q]", id))...)
		}
	}

	// The project's own category weights. An id nothing declares is inert
	// rather than an error -- it is a standing priority waiting for a pack
	// that has the category, which is what lets one set of priorities survive
	// a pack change -- but the WEIGHT still has to be legal, because an
	// illegal one is a number the engine will ignore while the client thinks
	// it took.
	ids := make([]string, 0, len(categoryWeights))
	for id := range categoryWeights {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !scoring.ValidSeverityWeight(categoryWeights[id]) {
			issues = append(issues, fmt.Sprintf("category_weights[%q]: weight %v is outside (0, %g]",
				id, categoryWeights[id], scoring.MaxSeverityWeight))
		}
	}

	return issues
}

// riskCategoryIssues is one taxonomy entry's rules, shared by the pack and the
// project forms so the two cannot come to disagree about what a category is.
//
// `declared` is updated as it goes, which is what detects a duplicate id.
func riskCategoryIssues(cat PackRiskCategory, label string, declared map[string]bool) []string {
	var issues []string
	if cat.ID == "" {
		issues = append(issues, label+": id is required")
	} else {
		if !RiskCategoryIDRegex.MatchString(cat.ID) {
			issues = append(issues, fmt.Sprintf(
				"%s: id %q must be a lowercase slug of letters, digits, hyphens or underscores (e.g. 'revenue_leak')",
				label, cat.ID))
		}
		if declared[cat.ID] {
			issues = append(issues, fmt.Sprintf("%s: id %q is declared twice", label, cat.ID))
		}
		declared[cat.ID] = true
	}
	if strings.TrimSpace(cat.Label) == "" {
		issues = append(issues, fmt.Sprintf("%s (%q): label is required — it is what a reader sees on the insight", label, cat.ID))
	}
	switch {
	case cat.Weight == 0:
		issues = append(issues, fmt.Sprintf(
			"%s (%q): weight is required and must be in (0, %g] — 1.0 means this category matters as much as an unweighted area",
			label, cat.ID, scoring.MaxSeverityWeight))
	case !scoring.ValidSeverityWeight(cat.Weight):
		issues = append(issues, fmt.Sprintf("%s (%q): weight %v is outside (0, %g]",
			label, cat.ID, cat.Weight, scoring.MaxSeverityWeight))
	}
	return issues
}

// areaRiskIssues is one area's half of the risk contract.
//
// The messages name the area the way every other area rule in this file does
// ("base area 2 (consent)"), and deliberately do NOT contain the word
// risk_taxonomy: the pack generator attributes an issue to the section that
// must fix it by substring, and an area's mapping is fixed by re-generating
// the areas, not the taxonomy.
func areaRiskIssues(area PackAnalysisArea, known map[string]bool, label string) []string {
	var issues []string
	seen := make(map[string]bool, len(area.RiskCategories))
	for i, id := range area.RiskCategories {
		switch {
		case id == "":
			issues = append(issues, fmt.Sprintf("%s (%s): risk_categories[%d] is empty", label, area.ID, i))
		case !known[id]:
			issues = append(issues, fmt.Sprintf(
				"%s (%s): risk_categories[%d] %q is not a declared risk category — cite one the pack declares, or one of the global %s",
				label, area.ID, i, id, strings.Join(scoring.GlobalRiskCategories(), ", ")))
		case seen[id]:
			issues = append(issues, fmt.Sprintf("%s (%s): risk_categories[%d] %q is cited twice",
				label, area.ID, i, id))
		}
		seen[id] = true
	}
	if area.SeverityWeight != nil && !scoring.ValidSeverityWeight(*area.SeverityWeight) {
		issues = append(issues, fmt.Sprintf("%s (%s): severity_weight %v is outside (0, %g]",
			label, area.ID, *area.SeverityWeight, scoring.MaxSeverityWeight))
	}
	return issues
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
