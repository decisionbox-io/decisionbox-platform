package systemtest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/decisionbox-io/decisionbox/libs/go-common/domainpack"
)

// getPromptsPath returns the path to prompts, checking env var on each call.
func getPromptsPath() string {
	if p := os.Getenv("DOMAIN_PACK_PATH"); p != "" {
		return filepath.Join(p, "system-test", "prompts")
	}
	return "domain-packs/system-test/prompts"
}

// getProfilesPath returns the path to profiles, checking env var on each call.
func getProfilesPath() string {
	if p := os.Getenv("DOMAIN_PACK_PATH"); p != "" {
		return filepath.Join(p, "system-test", "profiles")
	}
	return "domain-packs/system-test/profiles"
}

// Compile-time check: SystemTestPack implements DiscoveryPack.
var _ domainpack.DiscoveryPack = (*SystemTestPack)(nil)

// areaFile represents an analysis area definition from areas.json.
type areaFile struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Keywords    []string `json:"keywords"`
	Priority    int      `json:"priority"`
	PromptFile  string   `json:"prompt_file"`
}

// DomainCategories returns the test depth categories.
func (p *SystemTestPack) DomainCategories() []domainpack.DomainCategory {
	return []domainpack.DomainCategory{
		{
			ID:          "quick",
			Name:        "Quick Validation",
			Description: "Basic connectivity and schema enumeration — verifies the warehouse connection works and tables are accessible (~10 queries)",
		},
		{
			ID:          "standard",
			Name:        "Standard Validation",
			Description: "Full data type mapping and data profiling — validates type conversion, NULL handling, and column statistics (~30-50 queries)",
		},
		{
			ID:          "thorough",
			Name:        "Thorough Validation",
			Description: "Exhaustive SQL dialect and edge case testing — validates complex queries, window functions, CTEs, and boundary conditions (~80-100 queries)",
		},
	}
}

// AnalysisAreas returns base + category-specific analysis areas.
func (p *SystemTestPack) AnalysisAreas(categoryID string) []domainpack.AnalysisArea {
	var areas []domainpack.AnalysisArea

	// Load base areas
	baseAreas := loadAreas(filepath.Join(getPromptsPath(), "base", "areas.json"))
	for _, a := range baseAreas {
		areas = append(areas, domainpack.AnalysisArea{
			ID: a.ID, Name: a.Name, Description: a.Description,
			Keywords: a.Keywords, IsBase: true, Priority: a.Priority,
		})
	}

	// Load category-specific areas
	if categoryID != "" {
		catAreas := loadAreas(filepath.Join(getPromptsPath(), "categories", categoryID, "areas.json"))
		for _, a := range catAreas {
			areas = append(areas, domainpack.AnalysisArea{
				ID: a.ID, Name: a.Name, Description: a.Description,
				Keywords: a.Keywords, IsBase: false, Priority: a.Priority,
			})
		}
	}

	return areas
}

// Prompts returns merged prompt templates for a given category.
func (p *SystemTestPack) Prompts(categoryID string) domainpack.PromptTemplates {
	templates := domainpack.PromptTemplates{
		AnalysisAreas: make(map[string]string),
	}

	// Load base exploration prompt
	templates.Exploration = readPromptFile(filepath.Join(getPromptsPath(), "base", "exploration.md"))

	// Merge category-specific exploration context
	if categoryID != "" {
		contextPath := filepath.Join(getPromptsPath(), "categories", categoryID, "exploration_context.md")
		if context := readPromptFile(contextPath); context != "" {
			templates.Exploration = templates.Exploration + "\n\n" + context
		}
	}

	// Load base context (shared across analysis + recommendations)
	templates.BaseContext = readPromptFile(filepath.Join(getPromptsPath(), "base", "base_context.md"))

	// Load recommendations prompt
	templates.Recommendations = readPromptFile(filepath.Join(getPromptsPath(), "base", "recommendations.md"))

	// Load analysis prompts from areas.json definitions
	baseAreas := loadAreas(filepath.Join(getPromptsPath(), "base", "areas.json"))
	for _, area := range baseAreas {
		path := filepath.Join(getPromptsPath(), "base", area.PromptFile)
		if content := readPromptFile(path); content != "" {
			templates.AnalysisAreas[area.ID] = content
		}
	}

	// Load category-specific analysis prompts
	if categoryID != "" {
		catAreas := loadAreas(filepath.Join(getPromptsPath(), "categories", categoryID, "areas.json"))
		for _, area := range catAreas {
			path := filepath.Join(getPromptsPath(), "categories", categoryID, area.PromptFile)
			if content := readPromptFile(path); content != "" {
				templates.AnalysisAreas[area.ID] = content
			}
		}
	}

	return templates
}

// ProfileSchema returns the profile schema (minimal — single text field).
func (p *SystemTestPack) ProfileSchema(categoryID string) map[string]interface{} {
	baseData, err := os.ReadFile(filepath.Join(getProfilesPath(), "schema.json"))
	if err != nil {
		return map[string]interface{}{"error": "base schema not found: " + err.Error()}
	}

	var base map[string]interface{}
	if err := json.Unmarshal(baseData, &base); err != nil {
		return map[string]interface{}{"error": err.Error()}
	}

	return base
}

// loadAreas reads analysis area definitions from an areas.json file.
func loadAreas(path string) []areaFile {
	data, err := os.ReadFile(path) //nolint:gosec // trusted internal path
	if err != nil {
		return nil
	}

	var areas []areaFile
	if err := json.Unmarshal(data, &areas); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to parse %s: %v\n", path, err)
		return nil
	}

	return areas
}

func readPromptFile(path string) string {
	data, err := os.ReadFile(path) //nolint:gosec // trusted internal path
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
