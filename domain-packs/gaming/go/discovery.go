package gaming

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
		return filepath.Join(p, "gaming", "prompts")
	}
	return "domain-packs/gaming/prompts"
}

// getProfilesPath returns the path to profiles, checking env var on each call.
func getProfilesPath() string {
	if p := os.Getenv("DOMAIN_PACK_PATH"); p != "" {
		return filepath.Join(p, "gaming", "profiles")
	}
	return "domain-packs/gaming/profiles"
}

// Compile-time check: GamingPack implements DiscoveryPack.
var _ domainpack.DiscoveryPack = (*GamingPack)(nil)

// areaFile represents an analysis area definition from areas.json.
type areaFile struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Keywords    []string `json:"keywords"`
	Priority    int      `json:"priority"`
	PromptFile  string   `json:"prompt_file"`
}

// DomainCategories returns the game genre categories.
func (p *GamingPack) DomainCategories() []domainpack.DomainCategory {
	return []domainpack.DomainCategory{
		{
			ID:          "match3",
			Name:        "Match-3",
			Description: "Puzzle games with match-3 mechanics (e.g., Candy Crush, Homescapes)",
		},
	}
}

// AnalysisAreas returns base + category-specific analysis areas.
// Reads from areas.json files — no hardcoded area definitions.
func (p *GamingPack) AnalysisAreas(categoryID string) []domainpack.AnalysisArea {
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
// Reads area definitions from areas.json and loads corresponding prompt files.
func (p *GamingPack) Prompts(categoryID string) domainpack.PromptTemplates {
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

// ProfileSchema returns the merged JSON Schema for a given category.
func (p *GamingPack) ProfileSchema(categoryID string) map[string]interface{} {
	baseData, err := os.ReadFile(filepath.Join(getProfilesPath(), "schema.json"))
	if err != nil {
		return map[string]interface{}{"error": "base schema not found: " + err.Error()}
	}

	var base map[string]interface{}
	if err := json.Unmarshal(baseData, &base); err != nil {
		return map[string]interface{}{"error": err.Error()}
	}

	if categoryID == "" {
		return base
	}

	catPath := filepath.Join(getProfilesPath(), "categories", categoryID+".json")
	catData, err := os.ReadFile(catPath)
	if err != nil {
		return base
	}

	var catSchema map[string]interface{}
	if err := json.Unmarshal(catData, &catSchema); err != nil {
		return base
	}

	baseProps, _ := base["properties"].(map[string]interface{})
	catProps, _ := catSchema["properties"].(map[string]interface{})
	if baseProps != nil && catProps != nil {
		for k, v := range catProps {
			baseProps[k] = v
		}
	}

	return base
}

// loadAreas reads analysis area definitions from an areas.json file.
func loadAreas(path string) []areaFile {
	data, err := os.ReadFile(path)
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
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
