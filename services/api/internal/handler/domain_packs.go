package handler

import (
	"net/http"
	"strings"

	"github.com/decisionbox-io/decisionbox/services/api/internal/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/internal/models"
)

// maxDomainPackBodySize limits request body for domain pack create/update/import (2MB).
const maxDomainPackBodySize = 2 * 1024 * 1024

// DomainPacksHandler handles domain pack CRUD endpoints.
type DomainPacksHandler struct {
	repo database.DomainPackRepo
}

func NewDomainPacksHandler(repo database.DomainPackRepo) *DomainPacksHandler {
	return &DomainPacksHandler{repo: repo}
}

// List returns all domain packs.
// GET /api/v1/domain-packs
func (h *DomainPacksHandler) List(w http.ResponseWriter, r *http.Request) {
	packs, err := h.repo.List(r.Context(), false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list domain packs: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, packs)
}

// Get returns a domain pack by slug.
// GET /api/v1/domain-packs/{slug}
func (h *DomainPacksHandler) Get(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")

	pack, err := h.repo.GetBySlug(r.Context(), slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get domain pack: "+err.Error())
		return
	}
	if pack == nil {
		writeError(w, http.StatusNotFound, "domain pack not found: "+slug)
		return
	}

	writeJSON(w, http.StatusOK, pack)
}

// Create creates a new domain pack.
// POST /api/v1/domain-packs
func (h *DomainPacksHandler) Create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxDomainPackBodySize)

	var pack models.DomainPack
	if err := decodeJSON(r, &pack); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if err := ValidateDomainPack(&pack); err != nil {
		writeError(w, http.StatusBadRequest, "validation error: "+err.Error())
		return
	}

	if err := h.repo.Create(r.Context(), &pack); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			writeError(w, http.StatusConflict, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "failed to create domain pack: "+err.Error())
		}
		return
	}

	apilog.WithFields(apilog.Fields{"slug": pack.Slug}).Info("Domain pack created")
	writeJSON(w, http.StatusCreated, pack)
}

// Update updates a domain pack by slug.
// PUT /api/v1/domain-packs/{slug}
func (h *DomainPacksHandler) Update(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxDomainPackBodySize)
	slug := r.PathValue("slug")

	existing, err := h.repo.GetBySlug(r.Context(), slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get domain pack: "+err.Error())
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "domain pack not found: "+slug)
		return
	}

	var pack models.DomainPack
	if err := decodeJSON(r, &pack); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Preserve slug from URL
	pack.Slug = slug

	if err := ValidateDomainPack(&pack); err != nil {
		writeError(w, http.StatusBadRequest, "validation error: "+err.Error())
		return
	}

	if err := h.repo.Update(r.Context(), slug, &pack); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update domain pack: "+err.Error())
		return
	}

	// Return the complete pack with preserved fields
	pack.ID = existing.ID
	pack.CreatedAt = existing.CreatedAt

	apilog.WithFields(apilog.Fields{"slug": slug}).Info("Domain pack updated")
	writeJSON(w, http.StatusOK, pack)
}

// Delete deletes a domain pack by slug.
// DELETE /api/v1/domain-packs/{slug}
func (h *DomainPacksHandler) Delete(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")

	if err := h.repo.Delete(r.Context(), slug); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "domain pack not found: "+slug)
		} else {
			writeError(w, http.StatusInternalServerError, "failed to delete domain pack: "+err.Error())
		}
		return
	}

	apilog.WithFields(apilog.Fields{"slug": slug}).Info("Domain pack deleted")
	writeJSON(w, http.StatusOK, map[string]string{"deleted": slug})
}

// Import imports a domain pack from portable JSON format.
// POST /api/v1/domain-packs/import
func (h *DomainPacksHandler) Import(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxDomainPackBodySize)

	var portable portableFormat
	if err := decodeJSON(r, &portable); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if portable.Format != "decisionbox-domain-pack" {
		writeError(w, http.StatusBadRequest, "invalid format: expected 'decisionbox-domain-pack'")
		return
	}

	pack := &portable.Pack
	if err := ValidateDomainPack(pack); err != nil {
		writeError(w, http.StatusBadRequest, "validation error: "+err.Error())
		return
	}

	if err := h.repo.Create(r.Context(), pack); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			writeError(w, http.StatusConflict, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "failed to import domain pack: "+err.Error())
		}
		return
	}

	apilog.WithFields(apilog.Fields{"slug": pack.Slug}).Info("Domain pack imported")
	writeJSON(w, http.StatusCreated, pack)
}

// Export exports a domain pack in portable JSON format.
// GET /api/v1/domain-packs/{slug}/export
func (h *DomainPacksHandler) Export(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")

	pack, err := h.repo.GetBySlug(r.Context(), slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get domain pack: "+err.Error())
		return
	}
	if pack == nil {
		writeError(w, http.StatusNotFound, "domain pack not found: "+slug)
		return
	}

	// Strip MongoDB-specific fields for portable format
	exportPack := *pack
	exportPack.ID = ""

	portable := struct {
		Format        string            `json:"format"`
		FormatVersion int               `json:"format_version"`
		Pack          models.DomainPack `json:"pack"`
	}{
		Format:        "decisionbox-domain-pack",
		FormatVersion: 1,
		Pack:          exportPack,
	}

	writeJSON(w, http.StatusOK, portable)
}
