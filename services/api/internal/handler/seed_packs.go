package handler

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

var slugRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

//go:embed seed/*.json
var seedFS embed.FS

// portableFormat is the envelope for portable domain pack JSON files.
type portableFormat struct {
	Format        string           `json:"format"`
	FormatVersion int              `json:"format_version"`
	Pack          models.DomainPack `json:"pack"`
}

// SeedBuiltInPacks loads built-in domain packs from embedded JSON files
// into MongoDB if they don't already exist. Safe to call on every startup.
func SeedBuiltInPacks(ctx context.Context, repo database.DomainPackRepo) {
	seedCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ctx = seedCtx

	entries, err := seedFS.ReadDir("seed")
	if err != nil {
		apilog.WithError(err).Warn("Failed to read seed directory")
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		data, err := seedFS.ReadFile("seed/" + entry.Name())
		if err != nil {
			apilog.WithFields(apilog.Fields{"error": err, "file": entry.Name()}).Warn("Failed to read seed file")
			continue
		}

		var portable portableFormat
		if err := json.Unmarshal(data, &portable); err != nil {
			apilog.WithFields(apilog.Fields{"error": err, "file": entry.Name()}).Warn("Failed to parse seed file")
			continue
		}

		if portable.Format != "decisionbox-domain-pack" {
			apilog.WithFields(apilog.Fields{"file": entry.Name()}).Warn("Unknown seed format, skipping")
			continue
		}

		pack := &portable.Pack

		// Check if pack already exists — don't overwrite user modifications
		existing, err := repo.GetBySlug(ctx, pack.Slug)
		if err != nil {
			apilog.WithFields(apilog.Fields{"error": err, "slug": pack.Slug}).Warn("Failed to check existing pack")
			continue
		}
		if existing != nil {
			continue
		}

		if err := repo.Create(ctx, pack); err != nil {
			apilog.WithFields(apilog.Fields{"error": err, "slug": pack.Slug}).Warn("Failed to seed domain pack")
			continue
		}

		apilog.WithField("slug", pack.Slug).Info("Seeded built-in domain pack")
	}
}

// ValidateDomainPack checks that a domain pack has all required fields and prompts.
func ValidateDomainPack(pack *models.DomainPack) error {
	if pack.Slug == "" {
		return fmt.Errorf("slug is required")
	}
	if len(pack.Slug) < 2 || !slugRegexp.MatchString(pack.Slug) {
		return fmt.Errorf("slug must be lowercase alphanumeric with hyphens (e.g. 'gaming', 'e-commerce')")
	}
	if pack.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len(pack.Categories) == 0 {
		return fmt.Errorf("at least one category is required")
	}

	// Validate categories have IDs
	for i, cat := range pack.Categories {
		if cat.ID == "" {
			return fmt.Errorf("category %d: id is required", i)
		}
		if cat.Name == "" {
			return fmt.Errorf("category %q: name is required", cat.ID)
		}
	}

	// Validate base prompts
	if pack.Prompts.Base.BaseContext == "" {
		return fmt.Errorf("base_context prompt is required")
	}
	if pack.Prompts.Base.Exploration == "" {
		return fmt.Errorf("exploration prompt is required")
	}
	if pack.Prompts.Base.Recommendations == "" {
		return fmt.Errorf("recommendations prompt is required")
	}

	// Validate template variables in prompts
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

	// Validate base analysis areas
	if len(pack.AnalysisAreas.Base) == 0 {
		return fmt.Errorf("at least one base analysis area is required")
	}
	for i, area := range pack.AnalysisAreas.Base {
		if err := validateAnalysisArea(area, fmt.Sprintf("base area %d", i)); err != nil {
			return err
		}
	}

	// Validate category analysis areas
	for catID, areas := range pack.AnalysisAreas.Categories {
		for i, area := range areas {
			if err := validateAnalysisArea(area, fmt.Sprintf("category %q area %d", catID, i)); err != nil {
				return err
			}
		}
	}

	return nil
}

func validateAnalysisArea(area models.PackAnalysisArea, label string) error {
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
