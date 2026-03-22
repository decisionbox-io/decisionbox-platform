package handler

import (
	"net/http"

	"github.com/decisionbox-io/decisionbox/libs/go-common/domainpack"
	"github.com/decisionbox-io/decisionbox/services/api/internal/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/internal/models"
)

// SeedProjectPrompts seeds a project with default prompts from the domain pack.
// Called on project creation.
func SeedProjectPrompts(project *models.Project) {
	pack, err := domainpack.Get(project.Domain)
	if err != nil {
		return
	}
	dp, ok := domainpack.AsDiscoveryPack(pack)
	if !ok {
		return
	}

	templates := dp.Prompts(project.Category)
	areas := dp.AnalysisAreas(project.Category)

	prompts := &models.ProjectPrompts{
		Exploration:     templates.Exploration,
		Recommendations: templates.Recommendations,
		BaseContext:     templates.BaseContext,
		AnalysisAreas:   make(map[string]models.AnalysisAreaConfig),
	}

	// Seed analysis areas from domain pack
	for _, area := range areas {
		prompt := ""
		if p, ok := templates.AnalysisAreas[area.ID]; ok {
			prompt = p
		}
		prompts.AnalysisAreas[area.ID] = models.AnalysisAreaConfig{
			Name:        area.Name,
			Description: area.Description,
			Keywords:    area.Keywords,
			Prompt:      prompt,
			IsBase:      area.IsBase,
			IsCustom:    false,
			Priority:    area.Priority,
			Enabled:     true,
		}
	}

	project.Prompts = prompts
}

// GetPrompts returns the prompts for a project.
// GET /api/v1/projects/{id}/prompts
func GetPrompts(projectRepo database.ProjectRepo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")

		p, err := projectRepo.GetByID(r.Context(), id)
		if err != nil || p == nil {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}

		if p.Prompts == nil {
			// Seed prompts if not yet present (migration for old projects)
			SeedProjectPrompts(p)
			if err := projectRepo.Update(r.Context(), id, p); err != nil {
				apilog.WithError(err).Warn("failed to seed project prompts")
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
