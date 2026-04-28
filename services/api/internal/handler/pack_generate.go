package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/decisionbox-io/decisionbox/libs/go-common/packgen"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// PackGenerateHandler exposes pack-generation endpoints. The handler is
// intentionally thin — it validates input, looks up the project, and
// delegates to packgen.GetProvider(). When no provider is configured
// the registry returns the no-op Provider whose methods all return
// packgen.ErrNotConfigured; this handler maps that to HTTP 404 so the
// dashboard can hide the feature on deployments where it isn't
// available.
type PackGenerateHandler struct {
	repo database.ProjectRepo
}

// NewPackGenerateHandler constructs a PackGenerateHandler.
func NewPackGenerateHandler(repo database.ProjectRepo) *PackGenerateHandler {
	return &PackGenerateHandler{repo: repo}
}

// Generate kicks off pack generation for the given project. POST
// /api/v1/projects/{id}/pack-generate.
//
// Pre-conditions:
//   - Project must exist (404 if not).
//   - A non-no-op packgen Provider must be configured (404 otherwise).
//   - Project state must be pack_generation_pending (409 otherwise).
//   - Project.GeneratePack must be populated (400 otherwise).
//
// Body is ignored — generation inputs come from the project document.
func (h *PackGenerateHandler) Generate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}

	if !packgen.IsAvailable() {
		writeError(w, http.StatusNotFound, "pack generation is not available on this deployment")
		return
	}

	p, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load project: "+err.Error())
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	if p.EffectiveState() != models.ProjectStatePackGenerationPending {
		writeError(w, http.StatusConflict, "project is not in pack_generation_pending state (current: "+p.EffectiveState()+")")
		return
	}
	if p.GeneratePack == nil || !p.GeneratePack.Enabled {
		writeError(w, http.StatusBadRequest, "project has no pending pack-generation request")
		return
	}

	req := packgen.GenerateRequest{
		ProjectID:   p.ID,
		PackName:    p.GeneratePack.PackName,
		PackSlug:    p.GeneratePack.PackSlug,
		Description: p.GeneratePack.Description,
	}
	res, err := packgen.GetProvider().Generate(r.Context(), req)
	if err != nil {
		if errors.Is(err, packgen.ErrNotConfigured) {
			writeError(w, http.StatusNotFound, "pack generation is not available on this deployment")
			return
		}
		apilog.WithFields(apilog.Fields{"project_id": id, "error": err.Error()}).Error("pack-generate: provider returned error")
		writeError(w, http.StatusInternalServerError, "pack generation failed: "+err.Error())
		return
	}

	apilog.WithFields(apilog.Fields{
		"project_id": id,
		"pack_slug":  req.PackSlug,
		"run_id":     res.RunID,
		"async":      res.Async,
	}).Info("pack-generate: dispatched")

	if res.Async {
		writeJSON(w, http.StatusAccepted, res)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// regenerateSectionRequest is the JSON body shape of POST
// /api/v1/projects/{id}/pack-generate/regenerate. It maps onto
// packgen.RegenerateSectionRequest with the project ID coming from the
// URL and the pack slug from the project document.
type regenerateSectionRequest struct {
	Section  string `json:"section"`
	Feedback string `json:"feedback"`
}

// RegenerateSection synchronously re-emits a single section of the
// project's draft pack using user-supplied feedback. POST
// /api/v1/projects/{id}/pack-generate/regenerate.
//
// Pre-conditions:
//   - Project must exist (404 if not).
//   - A non-no-op packgen Provider must be configured (404 otherwise).
//   - Project state must be pack_generation_done (409 otherwise — section
//     regeneration mutates a draft pack the user is reviewing).
//   - Project.Domain (filled at the end of generation) must be set.
func (h *PackGenerateHandler) RegenerateSection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}

	if !packgen.IsAvailable() {
		writeError(w, http.StatusNotFound, "pack generation is not available on this deployment")
		return
	}

	var body regenerateSectionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Section == "" {
		writeError(w, http.StatusBadRequest, "section is required")
		return
	}
	if body.Feedback == "" {
		writeError(w, http.StatusBadRequest, "feedback is required")
		return
	}

	p, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load project: "+err.Error())
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	if p.EffectiveState() != models.ProjectStatePackGenerationDone {
		writeError(w, http.StatusConflict, "project is not in pack_generation_done state (current: "+p.EffectiveState()+")")
		return
	}
	if p.Domain == "" {
		writeError(w, http.StatusBadRequest, "project has no associated pack to regenerate")
		return
	}

	req := packgen.RegenerateSectionRequest{
		ProjectID: p.ID,
		PackSlug:  p.Domain,
		Section:   body.Section,
		Feedback:  body.Feedback,
	}
	res, err := packgen.GetProvider().RegenerateSection(r.Context(), req)
	if err != nil {
		if errors.Is(err, packgen.ErrNotConfigured) {
			writeError(w, http.StatusNotFound, "pack generation is not available on this deployment")
			return
		}
		apilog.WithFields(apilog.Fields{"project_id": id, "section": body.Section, "error": err.Error()}).Error("pack-generate: regenerate section failed")
		writeError(w, http.StatusInternalServerError, "regenerate failed: "+err.Error())
		return
	}

	apilog.WithFields(apilog.Fields{
		"project_id": id,
		"section":    body.Section,
		"attempts":   res.Attempts,
	}).Info("pack-generate: section regenerated")
	writeJSON(w, http.StatusOK, res)
}
