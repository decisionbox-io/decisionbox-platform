// Package wellknown holds the deployment-level client-bootstrap config
// served, unauthenticated, at GET /.well-known/decisionbox.
//
// It answers one question a client asks before it has any credentials:
// "how do I authenticate against this deployment, and what does it
// support?" The built-in platform runs without authentication, so with
// nothing registered the discovery endpoint reports auth type "none". A
// build that installs an auth provider (via auth.RegisterProvider)
// registers the matching client-facing auth config here — issuer, mobile
// client id, scopes, audience — so the endpoint can advertise it.
//
// INVARIANT: any build that enables authentication MUST Register a Config
// whose Auth.Type says which token the API accepts — "oidc" (with a non-nil
// OIDC block) or "brokered" (with a non-nil Broker block) — in the SAME
// init() that installs its auth provider. Otherwise the endpoint advertises
// "none" while the deployment actually enforces auth — a client would try
// anonymous access and get 401 on every route, with nothing explaining
// why. Keeping both in one init() makes the advertised mode and the
// enforced mode impossible to diverge.
//
// The registry mirrors the codebase's other init()-time registries
// (systeminfo, askoverride, the auth/policy/sources providers): a leaf
// package any build that composes the API can fill from init(), read by a
// plain community handler at request time.
package wellknown

import (
	"encoding/json"
	"sync"
)

// Auth.Type values: how a client obtains the bearer token this deployment's
// API accepts.
const (
	// AuthTypeNone means the deployment enforces no authentication. It is
	// what the endpoint reports when nothing is registered.
	AuthTypeNone = "none"
	// AuthTypeOIDC means the deployment validates the identity provider's
	// own OIDC bearer tokens: a client signs in at the advertised issuer and
	// calls the API with the access token it receives.
	AuthTypeOIDC = "oidc"
	// AuthTypeBrokered means the API does NOT accept the identity provider's
	// tokens. A client signs in at the advertised issuer as usual, then
	// exchanges that identity at the advertised broker for a token scoped to
	// this one deployment, and calls the API with that.
	//
	// The distinction is not cosmetic: an IdP token says who the holder is,
	// and on a deployment that shares an identity provider with other
	// deployments it says nothing about which of them the holder may use. A
	// brokered token is issued for one deployment by the party that knows the
	// answer, so the API can decide from the token alone.
	AuthTypeBrokered = "brokered"
)

// OIDC is the client-facing subset of a deployment's OIDC configuration —
// only what a login client needs to start an authorization-code flow. It
// deliberately excludes the server-side validation config (claim mappings,
// default org/role) that never leaves the API process.
type OIDC struct {
	// Issuer is the OIDC issuer URL. A client discovers the concrete
	// endpoints from {issuer}/.well-known/openid-configuration.
	Issuer string `json:"issuer"`
	// MobileClientID is the public OAuth client id the mobile app uses.
	// May be empty: a deployment can run the dashboard client only and
	// never provision a mobile one. That is advertised honestly rather
	// than downgrading the auth mode.
	MobileClientID string `json:"mobile_client_id"`
	// Scopes are the scopes the client should request.
	Scopes []string `json:"scopes"`
	// Audience is the API audience the client requests a token for.
	Audience string `json:"audience"`
}

// Broker is the client-facing description of the service that exchanges an
// identity for a deployment-scoped token. Present iff Auth.Type is
// "brokered"; the OIDC block alongside it still says how to sign in, because
// the broker takes an identity it can verify.
type Broker struct {
	// URL is the broker's API base URL. The protocol spoken against it is
	// identified by Kind, not discovered from this document.
	URL string `json:"url"`
	// DeploymentID is how this deployment is named to the broker, so a client
	// that reaches the broker with several deployments available knows which
	// one it is pointed at.
	DeploymentID string `json:"deployment_id"`
	// Kind names the exchange protocol, so a client can tell whether it
	// implements it before starting a flow it cannot finish. A client that
	// does not recognise the value must say so rather than guess.
	Kind string `json:"kind"`
}

// Auth is the authentication descriptor a build registers.
//
// This is a registry container, not the wire shape: the discovery handler
// reshapes it to the endpoint's contract (an auth:{type} object with
// top-level oidc / broker siblings), so these fields carry no JSON tags.
// Type is always explicit so a client never has to infer "no auth" from a
// missing field. OIDC is non-nil for AuthTypeOIDC and for AuthTypeBrokered
// (both start with a sign-in at the issuer); Broker is non-nil iff
// Type == AuthTypeBrokered.
type Auth struct {
	Type   string
	OIDC   *OIDC
	Broker *Broker
}

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
// init()-time registration by a build that composes the API. A later call
// replaces the earlier one (last writer wins); this is a
// programmer-controlled process global, not a per-request value.
func Register(c Config) {
	mu.Lock()
	defer mu.Unlock()
	cp := c
	registered = &cp
}

// Get returns the registered config and whether one was set. When ok is
// false the caller treats the deployment as unauthenticated (auth type
// "none").
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
