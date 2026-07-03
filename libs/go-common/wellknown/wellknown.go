// Package wellknown holds the deployment-level client-bootstrap config
// served, unauthenticated, at GET /.well-known/decisionbox.
//
// It answers one question a client asks before it has any credentials:
// "how do I authenticate against this deployment, and what does it
// support?" The built-in platform runs without authentication, so with
// nothing registered the discovery endpoint reports auth type "none".
// A build that installs an auth provider (via auth.RegisterProvider)
// registers the matching client-facing auth config here — issuer, mobile
// client id, scopes, audience — so the endpoint can advertise it.
//
// INVARIANT: any build that enables authentication MUST Register a
// Config whose Auth.Type is "oidc" (with a non-nil OIDC block) in the
// same init() that installs its auth provider. Otherwise the endpoint
// would advertise "none" while the deployment actually enforces auth —
// a client would try anonymous access and get 401. Keeping both in one
// init() makes the advertised mode and the enforced mode impossible to
// diverge.
//
// The registry mirrors the codebase's other init()-time registries
// (systeminfo, askoverride, the auth/policy/sources providers): a leaf
// package any build that composes the API can fill from init(), read by
// a plain community handler at request time.
package wellknown

import (
	"encoding/json"
	"sync"
)

// OIDC is the client-facing subset of a deployment's OIDC configuration —
// only what a login client needs to start an authorization-code flow.
// It deliberately excludes server-side validation config (claim mappings,
// default roles) that never leaves the API process.
type OIDC struct {
	// Issuer is the OIDC issuer URL. A client discovers the concrete
	// endpoints via {issuer}/.well-known/openid-configuration.
	Issuer string `json:"issuer"`
	// MobileClientID is the public OAuth client id the mobile app uses.
	MobileClientID string `json:"mobile_client_id"`
	// Scopes are the scopes the client should request.
	Scopes []string `json:"scopes"`
	// Audience is the API audience the client requests a token for.
	Audience string `json:"audience"`
}

// Auth is the authentication descriptor a build registers. It is a
// registry container, not the wire shape: the discovery handler reshapes
// it to the endpoint's contract (auth:{type} with a top-level oidc
// sibling), so these fields carry no JSON tags. Type is always explicit
// so a client never has to infer "no auth" from a missing field; OIDC is
// non-nil iff Type=="oidc".
type Auth struct {
	Type string
	OIDC *OIDC
}

// AuthTypeNone / AuthTypeOIDC are the two auth.Type values.
const (
	AuthTypeNone = "none"
	AuthTypeOIDC = "oidc"
)

// Config is what a build registers for the discovery endpoint to serve.
type Config struct {
	// Auth describes how clients authenticate. Required.
	Auth Auth
	// Branding is an optional opaque JSON blob a deployment overlay may
	// surface to clients (app name, logo URL, colors). The built-in build
	// leaves it nil; it is passed through verbatim when set.
	Branding json.RawMessage
}

var (
	mu         sync.RWMutex
	registered *Config
)

// Register installs the deployment's discovery config. Intended for
// init()-time registration by a build that composes the API. A later
// call replaces the earlier one (the last writer wins); this is a
// programmer-controlled process-global, not a per-request value.
func Register(c Config) {
	mu.Lock()
	defer mu.Unlock()
	cp := c
	registered = &cp
}

// Get returns the registered config and whether one was set. When ok is
// false the caller should treat the deployment as unauthenticated
// (auth type "none").
func Get() (Config, bool) {
	mu.RLock()
	defer mu.RUnlock()
	if registered == nil {
		return Config{}, false
	}
	return *registered, true
}

// ResetForTest clears the registry. Test-only; production code MUST NOT
// call it.
func ResetForTest() {
	mu.Lock()
	defer mu.Unlock()
	registered = nil
}
