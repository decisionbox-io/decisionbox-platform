package warehouse

import "encoding/hex"

// Auth-method flows. Flow names how a method obtains its credential, which is
// what decides whether the UI can render a form for it at all.
const (
	// FlowStaticConfig is a credential the operator types in — a password, a
	// service-account key, a role ARN. It is the zero value, so every auth
	// method declared before flows existed keeps its meaning.
	FlowStaticConfig = ""

	// FlowAuthorizationCode is three-legged OAuth: the user is sent to the
	// provider's consent screen, the provider hands back a code, and the code
	// is exchanged for a durable refresh token stored as the datasource's
	// credential. There is no field to type; the credential is the outcome of
	// a conversation with the provider.
	//
	// This exists because a downloadable credential is not always obtainable.
	// constraints/iam.disableServiceAccountKeyCreation — a CIS Google Cloud
	// Benchmark control that regulated organisations enforce org-wide — makes
	// a service-account key impossible to create, and for those deployments a
	// user-delegated grant is the only way to connect at all.
	FlowAuthorizationCode = "authorization_code"
)

// AuthorizationCode carries what a three-legged auth method needs that a
// static one does not: where to send the user, where to redeem the code, and
// what the resulting grant must cover.
//
// It is declared at registration, alongside the rest of ProviderMeta, so a
// caller can build a consent URL without constructing a provider — which it
// could not do anyway, since the credential is what the flow is for.
type AuthorizationCode struct {
	AuthURL  string `json:"auth_url"`
	TokenURL string `json:"token_url"`

	// Scopes are requested at consent and required of the result. Providers
	// with granular consent let a user approve some and withhold others, and
	// the resulting token is valid but cannot do the job — so this list is
	// what the exchange checks the grant against, not merely what it asks for.
	Scopes []string `json:"scopes"`
}

// AuthMethodByID returns the provider's declared auth method with the given
// id. An empty id resolves to the provider's first declared method, which is
// how a datasource saved before it had a choice of methods still resolves to
// the one it was configured under.
func (m ProviderMeta) AuthMethodByID(id string) (AuthMethod, bool) {
	if id == "" {
		if len(m.AuthMethods) == 0 {
			return AuthMethod{}, false
		}
		return m.AuthMethods[0], true
	}
	for _, a := range m.AuthMethods {
		if a.ID == id {
			return a, true
		}
	}
	return AuthMethod{}, false
}

// OAuth app-registration fields. The deployment registers its own OAuth client
// with the provider — its own client_id, client_secret and redirect_uri — so
// no DecisionBox-owned application ever holds a customer's data grant, and an
// app of the provider's "internal" type needs no vendor verification.
const (
	OAuthFieldClientID     = "client_id"
	OAuthFieldClientSecret = "client_secret"
	OAuthFieldRedirectURI  = "redirect_uri"
)

// OAuthAppFields is the full registration, in the order a form should render
// it.
var OAuthAppFields = []string{OAuthFieldClientID, OAuthFieldClientSecret, OAuthFieldRedirectURI}

// oauthAppKeyPrefix namespaces the app registration away from per-project
// warehouse credentials. The registration is instance-scoped — one OAuth
// client per deployment per provider, shared by every project that connects
// that kind of source.
const oauthAppKeyPrefix = "warehouse-oauth-app"

// OAuthAppKey returns the secret-provider key holding one field of a
// provider's instance-scoped OAuth app registration.
//
// The provider slug is hex-encoded for the same reason CredentialsKey encodes
// a warehouse id: cloud secret backends compose this key straight into the
// provider-side secret name and restrict that name's charset — GCP allows
// [A-Za-z0-9_-], Azure Key Vault only [A-Za-z0-9-]. Hex plus a hyphen
// delimiter is accepted everywhere, is deterministic, and cannot collide
// across distinct slugs the way a lossy character substitution can.
func OAuthAppKey(provider, field string) string {
	return oauthAppKeyPrefix + "-" + hex.EncodeToString([]byte(provider)) + "-" + hex.EncodeToString([]byte(field))
}

// OAuthAppConfigKey returns the ProviderConfig key an app-registration field
// is passed to a provider factory under.
//
// It is namespaced away from the provider's own config fields deliberately:
// "client_id" is a plausible name for something a provider asks the operator
// for, and a collision would silently overwrite one with the other.
func OAuthAppConfigKey(field string) string {
	return "oauth_app_" + field
}
