// Package oauthreg names the deployment's own OAuth app registration: the
// client_id, client_secret and redirect_uri an operator registers once with a
// provider, so that no DecisionBox-owned application ever holds a customer's
// data grant.
//
// # Why this is not part of either registry
//
// Two unrelated registries have consumers that authenticate against the same
// provider — a data source in the warehouse registry, an external-storage
// connector in the connectors one. Both need the same three fields, and a
// customer who has registered a Google client should not have to register a
// second one because the feature that needs it lives in a different registry.
//
// So the registration is keyed by the OAuth provider ("google"), not by the
// consumer, and the key scheme lives in a package neither registry owns. Which
// provider a consumer belongs to is declared in its own registry metadata; this
// package only knows how the resulting key is spelled.
package oauthreg

import "strings"

// Registration fields. A three-legged flow needs exactly these three: who is
// asking (client_id), proof it is really them (client_secret), and the origin
// the authorization code was issued against (redirect_uri).
const (
	FieldClientID     = "client_id"
	FieldClientSecret = "client_secret"
	FieldRedirectURI  = "redirect_uri"
)

// Fields is the full registration, in the order a form should render it.
var Fields = []string{FieldClientID, FieldClientSecret, FieldRedirectURI}

// keyPrefix namespaces the app registration away from per-project credentials.
// The registration is instance-scoped — one OAuth client per deployment per
// provider, shared by every consumer and every project.
const keyPrefix = "oauth-app"

// Key returns the secret-provider key holding one field of a provider's
// instance-scoped OAuth app registration — e.g. oauth-app-google-client-id.
//
// Lowercase alphanumerics and hyphens only. Cloud secret backends compose their
// provider-side secret name straight from this key and restrict that name's
// charset: GCP allows [A-Za-z0-9_-], Azure Key Vault only [A-Za-z0-9-]. Azure is
// the strictest, so its alphabet is the one used here.
//
// The substitution is lossy — two ids differing only in their separators produce
// the same key. That is safe here and would not be for arbitrary input: both
// arguments come from a closed set of compile-time constants (this package's
// three fields, and the provider ids declared in registry metadata), so a
// collision is a thing a reviewer can see rather than a thing a customer can
// cause.
func Key(provider, field string) string {
	return keyPrefix + "-" + safe(provider) + "-" + safe(field)
}

// ConfigKey returns the provider-config key an app-registration field is passed
// to a factory under.
//
// It is namespaced away from a provider's own config fields deliberately:
// "client_id" is a plausible name for something a provider asks the operator
// for, and a collision would silently overwrite one with the other.
func ConfigKey(field string) string {
	return "oauth_app_" + field
}

// safe maps a key fragment onto [a-z0-9-].
func safe(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}
	return b.String()
}
