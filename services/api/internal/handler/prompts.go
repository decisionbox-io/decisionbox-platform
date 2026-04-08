package handler

import (
	"net/http"

	"github.com/decisionbox-io/decisionbox/services/api/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// SeedProjectPrompts seeds a project with default prompts from a domain pack.
// Called on project creation. The pack is loaded from MongoDB by the caller.
func SeedProjectPrompts(project *models.Project, pack *models.DomainPack) {
	if pack == nil {
		return
	}

	category := project.Category

	// Build exploration prompt: base + category context
	exploration := pack.Prompts.Base.Exploration
	if catPrompts, ok := pack.Prompts.Categories[category]; ok {
		if catPrompts.ExplorationContext != "" {
			exploration = exploration + "\n\n" + catPrompts.ExplorationContext
		}
	}

	prompts := &models.ProjectPrompts{
		Exploration:     exploration,
		Recommendations: pack.Prompts.Base.Recommendations,
		BaseContext:     pack.Prompts.Base.BaseContext,
		AnalysisAreas:   make(map[string]models.AnalysisAreaConfig),
	}

	// Seed base analysis areas
	for _, area := range pack.AnalysisAreas.Base {
		prompts.AnalysisAreas[area.ID] = models.AnalysisAreaConfig{
			Name:        area.Name,
			Description: area.Description,
			Keywords:    area.Keywords,
			Prompt:      area.Prompt,
			IsBase:      true,
			IsCustom:    false,
			Priority:    area.Priority,
			Enabled:     true,
		}
	}

	// Seed category-specific analysis areas
	if catAreas, ok := pack.AnalysisAreas.Categories[category]; ok {
		for _, area := range catAreas {
			prompts.AnalysisAreas[area.ID] = models.AnalysisAreaConfig{
				Name:        area.Name,
				Description: area.Description,
				Keywords:    area.Keywords,
				Prompt:      area.Prompt,
				IsBase:      false,
				IsCustom:    false,
				Priority:    area.Priority,
				Enabled:     true,
			}
		}
	}

	project.Prompts = prompts
}

// GetPrompts returns the prompts for a project.
// GET /api/v1/projects/{id}/prompts
func GetPrompts(projectRepo database.ProjectRepo, domainPackRepo database.DomainPackRepo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")

		p, err := projectRepo.GetByID(r.Context(), id)
		if err != nil || p == nil {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}

		if p.Prompts == nil {
			// Seed prompts if not yet present (migration for old projects)
			pack, err := domainPackRepo.GetBySlug(r.Context(), p.Domain)
			if err == nil && pack != nil {
				SeedProjectPrompts(p, pack)
				if err := projectRepo.Update(r.Context(), id, p); err != nil {
					apilog.WithError(err).Warn("failed to seed project prompts")
				}
			}
		}

		writeJSON(w, http.StatusOK, p.Prompts)
	}
}

// UpdatePrompts updates the prompts for a project.
// PUT /api/v1/projects/{id}/prompts
func UpdatePrompts(projectRepo database.ProjectRepo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")

		p, err := projectRepo.GetByID(r.Context(), id)
		if err != nil || p == nil {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}

		var prompts models.ProjectPrompts
		if err := decodeJSON(r, &prompts); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}

		// Update via project repo
		p.Prompts = &prompts
		if err := projectRepo.Update(r.Context(), id, p); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update prompts: "+err.Error())
			return
		}

		writeJSON(w, http.StatusOK, prompts)
	}
}
