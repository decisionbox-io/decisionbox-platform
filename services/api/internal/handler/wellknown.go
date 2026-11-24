package handler

import (
	"encoding/json"
	"net/http"

	"github.com/decisionbox-io/decisionbox/libs/go-common/wellknown"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
)

// discoveryAPIVersion is the API contract version this server serves. It is
// the version baked into every /api/v1 route, surfaced verbatim so a client
// can confirm compatibility before it talks to the deployment at all.
const discoveryAPIVersion = "v1"

// Discovery feature tokens. A client reads these to know which reused
// endpoints exist on this deployment before it has credentials.
//
// Scope rule: a token belongs here only if the capability is mounted
// UNCONDITIONALLY by the community server (see NewWithRouteGroups). Two
// kinds of capability deliberately do NOT appear:
//
//   - Plugin features gated on a plan or a licence (executive summaries,
//     reports). Those are reported post-login by GET /api/v1/capabilities,
//     which knows the entitlement; advertising them here would be wrong as
//     often as right.
//   - The asynchronous Ask turn contract. Whether a given question runs as
//     an async turn depends on the project (does it have a warehouse) and
//     on the caller's role, so it is not a property of the deployment and
//     cannot be answered pre-login. A client discovers it per response: a
//     202 carrying a turn id instead of an answer.
const (
	FeatureProjects           = "projects"
	FeatureGroundedChat       = "grounded_chat"
	FeatureAskSessions        = "ask_sessions"
	FeatureAskSessionRename   = "ask_session_rename"
	FeatureMe                 = "me"
	FeatureInsights           = "insights"
	FeatureRecommendations    = "recommendations"
	FeatureLists              = "lists"
	FeatureReads              = "reads"
	FeatureSearch             = "search"
	FeatureCrossProjectSearch = "cross_project_search"
)

// discoveryFeatures is the capability set served by the endpoint, in a
// stable order so the document is byte-identical between requests.
var discoveryFeatures = []string{
	FeatureProjects,
	FeatureGroundedChat,
	FeatureAskSessions,
	FeatureAskSessionRename,
	FeatureMe,
	FeatureInsights,
	FeatureRecommendations,
	FeatureLists,
	FeatureReads,
	FeatureSearch,
	FeatureCrossProjectSearch,
}

// discoveryAuth is the wire shape of the top-level `auth` object: only the
// explicit type. The OIDC details ride in a sibling `oidc` object so a
// client never has to reach into `auth` for them.
type discoveryAuth struct {
	Type string `json:"type"`
}

// discoveryResponse is the raw JSON served at GET /.well-known/decisionbox.
//
// It is NOT wrapped in the standard {"data": …} envelope. A client fetches
// this before it has any credentials or any knowledge of the platform's
// conventions, so the payload is a plain, self-describing object.
type discoveryResponse struct {
	APIVersion string          `json:"api_version"`
	Features   []string        `json:"features"`
	Auth       discoveryAuth   `json:"auth"`
	OIDC       *wellknown.OIDC `json:"oidc,omitempty"`
	Branding   json.RawMessage `json:"branding,omitempty"`
}

// WellKnown serves the unauthenticated deployment discovery document. It is
// mounted on the pre-auth root mux (like /health) so a client can learn the
// API version, feature set and auth mode before login.
//
// The auth block comes from the wellknown registry, which a build that
// installs an auth provider fills at init(). With nothing registered — the
// built-in NoAuth platform — the endpoint reports an explicit auth type of
// "none" rather than omitting the field, so a client never has to infer the
// mode from an absence. The `oidc` sibling is present iff the type is
// "oidc".
//
// GET /.well-known/decisionbox
func WellKnown(w http.ResponseWriter, _ *http.Request) {
	resp := discoveryResponse{
		APIVersion: discoveryAPIVersion,
		Features:   discoveryFeatures,
		Auth:       discoveryAuth{Type: wellknown.AuthTypeNone},
	}

	if cfg, ok := wellknown.Get(); ok {
		if cfg.Auth.Type != "" {
			resp.Auth.Type = cfg.Auth.Type
		}
		if resp.Auth.Type == wellknown.AuthTypeOIDC {
			resp.OIDC = cfg.Auth.OIDC
		}
		resp.Branding = cfg.Branding
	}

	w.Header().Set("Content-Type", "application/json")
	// The document is deployment-level config that changes only on restart,
	// but it gates login: a client holding a stale copy would start the wrong
	// flow. no-store keeps a proxy from serving one deployment's auth mode to
	// a client pointed at another.
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		// The status is already committed by Encode's first write, so there is
		// nothing to recover — but log it, or a serialization regression here
		// would be silent and would look to a client like a malformed
		// deployment.
		apilog.WithError(err).Warn("failed to encode discovery response")
	}
}
