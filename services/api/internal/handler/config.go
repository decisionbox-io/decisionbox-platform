package handler

import (
	"net/http"

	"github.com/decisionbox-io/decisionbox/services/api/managedai"
)

// appConfigResponse is the GET /api/v1/config payload: deployment-level
// capability flags the dashboard reads once at load to decide what to
// render. Deliberately small and non-secret — it is served to any
// authenticated viewer and drives UI affordances only (the server, not
// the UI, is authoritative for enforcement).
type appConfigResponse struct {
	// AIConfigManaged is true when this deployment routes all inference
	// through a managed gateway (AI_GATEWAY_URL set). The dashboard hides
	// the AI/Embedding + Blurb settings tabs and the AI/embedding/blurb
	// new-project wizard steps when true — the config is preset and
	// immutable server-side, so exposing it would only confuse.
	AIConfigManaged bool `json:"ai_config_managed"`
}

// AppConfig returns deployment capability flags for the dashboard.
//
// GET /api/v1/config
func AppConfig(w http.ResponseWriter, r *http.Request) {
	// Never cache: the flag is cheap to compute and a stale value would
	// show or hide config UI incorrectly across a deployment change.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, appConfigResponse{
		AIConfigManaged: managedai.Enabled(),
	})
}
