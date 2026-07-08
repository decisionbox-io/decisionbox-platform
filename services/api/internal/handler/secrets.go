package handler

import (
	"net/http"

	"github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/internal/managedai"
)

// SecretsHandler handles per-project secret management.
type SecretsHandler struct {
	secretProvider secrets.Provider
	projectRepo    database.ProjectRepo
}

func NewSecretsHandler(sp secrets.Provider, projectRepo database.ProjectRepo) *SecretsHandler {
	return &SecretsHandler{secretProvider: sp, projectRepo: projectRepo}
}

// Set creates or updates a secret for a project.
// PUT /api/v1/projects/{id}/secrets/{key}
func (h *SecretsHandler) Set(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	key := r.PathValue("key")

	// Verify project exists
	p, err := h.projectRepo.GetByID(r.Context(), projectID)
	if err != nil || p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	// Managed-inference guard: in managed mode the analysis/blurb/embedding
	// credentials are the gateway key supplied via env fallback, never a
	// per-project secret. Refuse writes to those slots — otherwise a
	// crafted secret write would shadow the env key in resolveCredential
	// and re-route inference off the gateway. No-op when managed mode is
	// off (self-hosted keeps full per-project credential control).
	if managedai.IsManagedSecretKey(key) {
		writeError(w, http.StatusForbidden, "AI credentials are managed by the inference gateway on this deployment and cannot be set per project")
		return
	}

	var body struct {
		Value string `json:"value"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Value == "" {
		writeError(w, http.StatusBadRequest, "value is required")
		return
	}

	if err := h.secretProvider.Set(r.Context(), projectID, key, body.Value); err != nil {
		apilog.WithFields(apilog.Fields{
			"project_id": projectID, "key": key, "error": err.Error(),
		}).Error("Failed to set secret")
		writeError(w, http.StatusInternalServerError, "failed to set secret: "+err.Error())
		return
	}

	apilog.WithFields(apilog.Fields{
		"project_id": projectID, "key": key,
	}).Info("Secret set")

	writeJSON(w, http.StatusOK, map[string]string{
		"key":    key,
		"masked": secrets.MaskValue(body.Value),
		"status": "saved",
	})
}

// List returns all secret keys for a project (masked values only).
// GET /api/v1/projects/{id}/secrets
func (h *SecretsHandler) List(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")

	p, err := h.projectRepo.GetByID(r.Context(), projectID)
	if err != nil || p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	entries, err := h.secretProvider.List(r.Context(), projectID)
	if err != nil {
		apilog.WithFields(apilog.Fields{
			"project_id": projectID, "error": err.Error(),
		}).Error("Failed to list secrets")
		writeError(w, http.StatusInternalServerError, "failed to list secrets: "+err.Error())
		return
	}

	if entries == nil {
		entries = make([]secrets.SecretEntry, 0)
	}

	writeJSON(w, http.StatusOK, entries)
}
