package handler

import (
	"net/http"

	"github.com/decisionbox-io/decisionbox/libs/go-common/systeminfo"
)

// systemInfoResponse is the GET /api/v1/system payload: a component
// inventory built entirely from the systeminfo registry. The list is
// never hardcoded here — whatever the registry reports is what ships.
type systemInfoResponse struct {
	Components []systeminfo.Descriptor `json:"components"`
}

// SystemInfo returns the running platform's component inventory —
// services and the workers that ride inside them, each with its version
// and (for workers) a note explaining it shares its parent's image
// version. Built from systeminfo.Collect so additional components
// registered by out-of-tree builds appear without changing this handler.
//
// GET /api/v1/system
func SystemInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, systemInfoResponse{
		Components: systeminfo.Collect(r.Context()),
	})
}
