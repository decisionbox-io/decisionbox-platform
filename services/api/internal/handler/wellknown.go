package handler

import (
	"encoding/json"
	"net/http"

	"github.com/decisionbox-io/decisionbox/libs/go-common/wellknown"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
)

// apiVersion is the API contract version this server serves. It is the
// version baked into every /api/v1 route, surfaced verbatim so a client
// can confirm compatibility before talking to the deployment.
const apiVersion = "v1"

// discoveryFeatures is the static capability set the chat client relies
// on for this phase (request/response only). It is intentionally a flat
// list, not derived from runtime config: the client reads it to know
// which reused endpoints exist on this deployment.
var discoveryFeatures = []string{
	"projects",
	"grounded_chat",
	"ask_sessions",
	"me",
	"ask_session_rename",
}

// discoveryAuth is the wire shape of the top-level `auth` object: only
// the explicit type. The OIDC details ride in a sibling `oidc` object so
// the client never has to reach into `auth` for them.
type discoveryAuth struct {
	Type string `json:"type"`
}

// discoveryResponse is the raw JSON served at GET /.well-known/decisionbox.
// It is NOT wrapped in the standard {"data": …} envelope — a client
// fetches this before it has any credentials or knowledge of the
// platform's conventions, so the payload is a plain, self-describing
// object.
type discoveryResponse struct {
	APIVersion string          `json:"api_version"`
	Features   []string        `json:"features"`
	Auth       discoveryAuth   `json:"auth"`
	OIDC       *wellknown.OIDC `json:"oidc,omitempty"`
	Branding   json.RawMessage `json:"branding,omitempty"`
}

// WellKnown serves the unauthenticated deployment discovery document.
// It is mounted on the pre-auth root mux (like /health) so a client can
// learn the API version, feature set, and auth mode before login.
//
// The auth block is sourced from the wellknown registry, which a build
// that installs an auth provider fills at init(); with nothing registered
// (the built-in NoAuth platform) the endpoint reports an explicit
// auth type of "none". The `oidc` sibling is present iff auth type is
// "oidc".
//
// GET /.well-known/decisionbox
func WellKnown(w http.ResponseWriter, _ *http.Request) {
	resp := discoveryResponse{
		APIVersion: apiVersion,
		Features:   discoveryFeatures,
		Auth:       discoveryAuth{Type: wellknown.AuthTypeNone},
	}

	if cfg, ok := wellknown.Get(); ok {
		if cfg.Auth.Type != "" {
			resp.Auth.Type = cfg.Auth.Type
		}
		if cfg.Auth.Type == wellknown.AuthTypeOIDC {
			resp.OIDC = cfg.Auth.OIDC
		}
		resp.Branding = cfg.Branding
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		// Status is already committed by Encode's first write; there is
		// nothing to recover, but log so a serialization regression is
		// visible rather than silent.
		apilog.WithError(err).Warn("failed to encode discovery response")
	}
}
