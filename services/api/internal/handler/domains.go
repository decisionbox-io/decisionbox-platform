package handler

import (
	"net/http"

	"github.com/decisionbox-io/decisionbox/services/api/internal/database"
	"github.com/decisionbox-io/decisionbox/services/api/internal/models"
)

// DomainsHandler handles domain listing endpoints used by the project creation flow.
// Reads from the domain_packs MongoDB collection.
type DomainsHandler struct {
	repo database.DomainPackRepo
}

func NewDomainsHandler(repo database.DomainPackRepo) *DomainsHandler {
	return &DomainsHandler{repo: repo}
}

// ListDomains returns all published domain packs with their categories.
// GET /api/v1/domains
func (h *DomainsHandler) ListDomains(w http.ResponseWriter, r *http.Request) {
	type categoryInfo struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	type domainInfo struct {
		ID         string         `json:"id"`
		Categories []categoryInfo `json:"categories"`
	}

	packs, err := h.repo.List(r.Context(), true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list domains: "+err.Error())
		return
	}

	domains := make([]domainInfo, 0, len(packs))
	for _, pack := range packs {
		info := domainInfo{ID: pack.Slug}
		for _, cat := range pack.Categories {
			info.Categories = append(info.Categories, categoryInfo{
				ID:          cat.ID,
				Name:        cat.Name,
				Description: cat.Description,
			})
		}
		domains = append(domains, info)
	}

	writeJSON(w, http.StatusOK, domains)
}

// ListCategories returns categories for a domain.
// GET /api/v1/domains/{domain}/categories
func (h *DomainsHandler) ListCategories(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")

	pack, err := h.repo.GetBySlug(r.Context(), domain)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get domain: "+err.Error())
		return
	}
	if pack == nil {
		writeError(w, http.StatusNotFound, "domain not found: "+domain)
		return
	}

	writeJSON(w, http.StatusOK, pack.Categories)
}

// GetProfileSchema returns the merged JSON Schema for a domain/category.
// GET /api/v1/domains/{domain}/categories/{category}/schema
func (h *DomainsHandler) GetProfileSchema(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	category := r.PathValue("category")

	pack, err := h.repo.GetBySlug(r.Context(), domain)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get domain: "+err.Error())
		return
	}
	if pack == nil {
		writeError(w, http.StatusNotFound, "domain not found: "+domain)
		return
	}

	schema := mergeProfileSchema(pack, category)
	writeJSON(w, http.StatusOK, schema)
}

// GetAnalysisAreas returns analysis areas for a domain/category.
// GET /api/v1/domains/{domain}/categories/{category}/areas
func (h *DomainsHandler) GetAnalysisAreas(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	category := r.PathValue("category")

	pack, err := h.repo.GetBySlug(r.Context(), domain)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get domain: "+err.Error())
		return
	}
	if pack == nil {
		writeError(w, http.StatusNotFound, "domain not found: "+domain)
		return
	}

	type areaInfo struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Keywords    []string `json:"keywords"`
		IsBase      bool     `json:"is_base"`
		Priority    int      `json:"priority"`
	}

	var areas []areaInfo
	for _, a := range pack.AnalysisAreas.Base {
		areas = append(areas, areaInfo{
			ID: a.ID, Name: a.Name, Description: a.Description,
			Keywords: a.Keywords, IsBase: true, Priority: a.Priority,
		})
	}
	if catAreas, ok := pack.AnalysisAreas.Categories[category]; ok {
		for _, a := range catAreas {
			areas = append(areas, areaInfo{
				ID: a.ID, Name: a.Name, Description: a.Description,
				Keywords: a.Keywords, IsBase: false, Priority: a.Priority,
			})
		}
	}

	if areas == nil {
		areas = make([]areaInfo, 0)
	}
	writeJSON(w, http.StatusOK, areas)
}

// mergeProfileSchema merges base + category profile schemas.
// Returns a shallow copy to avoid mutating the pack's stored data.
func mergeProfileSchema(pack *models.DomainPack, category string) map[string]interface{} {
	base := pack.ProfileSchema.Base
	if base == nil {
		return map[string]interface{}{}
	}

	if category == "" {
		return base
	}

	catSchema, ok := pack.ProfileSchema.Categories[category]
	if !ok || catSchema == nil {
		return base
	}

	baseProps, _ := base["properties"].(map[string]interface{})
	catProps, _ := catSchema["properties"].(map[string]interface{})
	if baseProps == nil || catProps == nil {
		return base
	}

	// Copy base to avoid mutating the stored pack
	merged := make(map[string]interface{}, len(base))
	for k, v := range base {
		merged[k] = v
	}
	mergedProps := make(map[string]interface{}, len(baseProps)+len(catProps))
	for k, v := range baseProps {
		mergedProps[k] = v
	}
	for k, v := range catProps {
		mergedProps[k] = v
	}
	merged["properties"] = mergedProps

	return merged
}
