package apiserver

import (
	"net/http"

	"github.com/decisionbox-io/decisionbox/services/api/internal/askoverride"
)

// RegisterAskOverride installs h as the handler for
// `POST /api/v1/projects/{id}/ask`. When set, the community Ask route
// defers to h instead of running the built-in RAG synthesis. Plugins use
// this to layer agentic / tool-use behaviour on top of the platform
// without forking the community handler.
//
// Calling RegisterAskOverride more than once panics — the override is
// process-wide and a second registration would silently shadow the first.
// Calling it with nil panics for the same reason.
//
// The actual override slot lives in services/api/internal/askoverride so
// both this package (which exports the registration function) and the
// community route handler (which reads it on every request) can depend
// on it without an import cycle.
func RegisterAskOverride(h http.Handler) {
	askoverride.Register(h)
}

// GetAskOverride returns the registered override and a boolean indicating
// whether one is set. Exported for diagnostics; the community route reads
// from the same slot directly.
func GetAskOverride() (http.Handler, bool) {
	return askoverride.Get()
}

// ResetAskOverrideForTest clears the registered override. Test-only.
// Production code MUST NOT call it.
func ResetAskOverrideForTest() {
	askoverride.ResetForTest()
}
