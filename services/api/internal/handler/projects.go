package handler

import (
	"net/http"
	"strconv"

	"github.com/decisionbox-io/decisionbox/libs/go-common/policy"
	"github.com/decisionbox-io/decisionbox/libs/go-common/telemetry"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// ProjectsHandler handles project CRUD endpoints.
type ProjectsHandler struct {
	repo           database.ProjectRepo
	domainPackRepo database.DomainPackRepo
}

func NewProjectsHandler(repo database.ProjectRepo, domainPackRepo database.DomainPackRepo) *ProjectsHandler {
	return &ProjectsHandler{repo: repo, domainPackRepo: domainPackRepo}
}

// Create creates a new project.
// POST /api/v1/projects
func (h *ProjectsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var p models.Project
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if p.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if p.Domain == "" {
		writeError(w, http.StatusBadRequest, "domain is required")
		return
	}
	if p.Category == "" {
		writeError(w, http.StatusBadRequest, "category is required")
		return
	}

	// Seed default prompts from domain pack
	if p.Prompts == nil && h.domainPackRepo != nil {
		pack, err := h.domainPackRepo.GetBySlug(r.Context(), p.Domain)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load domain pack: "+err.Error())
			return
		}
		if pack == nil {
			writeError(w, http.StatusBadRequest, "domain pack not found: "+p.Domain)
			return
		}
		SeedProjectPrompts(&p, pack)
	}

	// Plan-gate: provider allow-list. Self-hosted Noop permits everything.
	ck := policy.GetChecker()
	if err := ck.CheckLLMProviderAllowed(r.Context(), "", p.LLM.Provider); err != nil {
		if writePolicyError(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "policy check failed: "+err.Error())
		return
	}

	// Plan-gate: projects-per-deployment. Reservation is consumed on a
	// successful repo insert and released on failure.
	res, err := ck.CheckCreateProject(r.Context(), "", policy.ProjectIntent{
		ProjectID:   p.ID,
		Name:        p.Name,
		LLMProvider: p.LLM.Provider,
	})
	if err != nil {
		if writePolicyError(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "policy check failed: "+err.Error())
		return
	}

	// Plan-gate: data-sources-per-deployment. A project today carries
	// exactly one warehouse (the data-source unit), so adding a project
	// that configures a warehouse is also adding a data source.
	// Self-hosted Noop is a no-op.
	var dsRes *policy.Reservation
	if p.Warehouse.Provider != "" {
		dsRes, err = ck.CheckAddDataSource(r.Context(), "")
		if err != nil {
			if res != nil {
				if relErr := ck.Release(r.Context(), res.ID); relErr != nil {
					apilog.WithError(relErr).Warn("failed to release project-create reservation after data-source denial")
				}
			}
			if writePolicyError(w, err) {
				return
			}
			writeError(w, http.StatusInternalServerError, "policy check failed: "+err.Error())
			return
		}
	}

	if err := h.repo.Create(r.Context(), &p); err != nil {
		apilog.WithError(err).Error("Failed to create project")
		if res != nil {
			if relErr := ck.Release(r.Context(), res.ID); relErr != nil {
				apilog.WithError(relErr).Warn("failed to release project-create reservation after insert failure")
			}
		}
		if dsRes != nil {
			if relErr := ck.Release(r.Context(), dsRes.ID); relErr != nil {
				apilog.WithError(relErr).Warn("failed to release data-source reservation after insert failure")
			}
		}
		writeError(w, http.StatusInternalServerError, "failed to create project: "+err.Error())
		return
	}

	apilog.WithFields(apilog.Fields{
		"project_id": p.ID,
		"name":       p.Name,
		"domain":     p.Domain,
		"category":   p.Category,
		"llm":        p.LLM.Provider,
		"warehouse":  p.Warehouse.Provider,
	}).Info("Project created")

	telemetry.TrackProjectCreated(p.Warehouse.Provider, p.LLM.Provider, p.Domain)

	writeJSON(w, http.StatusCreated, p)
}

// List returns all projects.
// GET /api/v1/projects
func (h *ProjectsHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	projects, err := h.repo.List(r.Context(), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list projects: "+err.Error())
		return
	}

	if projects == nil {
		projects = make([]*models.Project, 0)
	}

	writeJSON(w, http.StatusOK, projects)
}

// Get returns a project by ID.
// GET /api/v1/projects/{id}
func (h *ProjectsHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	p, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get project: "+err.Error())
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	writeJSON(w, http.StatusOK, p)
}

// Update updates a project.
// PUT /api/v1/projects/{id}
// Merges incoming fields with existing project — preserves fields not in the request
// (e.g., settings page doesn't send prompts, prompts page doesn't send warehouse).
func (h *ProjectsHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	existing, err := h.repo.GetByID(r.Context(), id)
	if err != nil || existing == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	var incoming models.Project
	if err := decodeJSON(r, &incoming); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Plan-gate: if the request changes the LLM provider, validate the
	// new provider against the plan's allow-list before persisting.
	if incoming.LLM.Provider != "" && incoming.LLM.Provider != existing.LLM.Provider {
		if err := policy.GetChecker().CheckLLMProviderAllowed(r.Context(), "", incoming.LLM.Provider); err != nil {
			if writePolicyError(w, err) {
				return
			}
			writeError(w, http.StatusInternalServerError, "policy check failed: "+err.Error())
			return
		}
	}

	// Merge: update only fields that are present in the request
	if incoming.Name != "" {
		existing.Name = incoming.Name
	}
	if incoming.Description != "" || incoming.Name != "" {
		existing.Description = incoming.Description
	}
	if incoming.Warehouse.Provider != "" {
		existing.Warehouse = incoming.Warehouse
	}
	if incoming.LLM.Provider != "" {
		existing.LLM = incoming.LLM
	}
	if incoming.Schedule.CronExpr != "" || incoming.Schedule.Enabled {
		existing.Schedule = incoming.Schedule
	}
	if incoming.Profile != nil {
		existing.Profile = incoming.Profile
	}
	if incoming.Prompts != nil {
		existing.Prompts = incoming.Prompts
	}
	if incoming.Embedding.Provider != "" {
		existing.Embedding = incoming.Embedding
	}

	if err := h.repo.Update(r.Context(), id, existing); err != nil {
		apilog.WithFields(apilog.Fields{"project_id": id, "error": err.Error()}).Error("Failed to update project")
		writeError(w, http.StatusInternalServerError, "failed to update project: "+err.Error())
		return
	}

	apilog.WithField("project_id", id).Info("Project updated")
	writeJSON(w, http.StatusOK, existing)
}

// Delete deletes a project.
// DELETE /api/v1/projects/{id}
func (h *ProjectsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if err := h.repo.Delete(r.Context(), id); err != nil {
		apilog.WithFields(apilog.Fields{"project_id": id, "error": err.Error()}).Error("Failed to delete project")
		writeError(w, http.StatusInternalServerError, "failed to delete project: "+err.Error())
		return
	}

	apilog.WithField("project_id", id).Info("Project deleted")
	writeJSON(w, http.StatusOK, map[string]string{"deleted": id})
}
