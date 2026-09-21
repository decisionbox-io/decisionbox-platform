package warehouse

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
	// Provider identifies whose OAuth app registration this method
	// authenticates with — "google", say. It is declared rather than derived
	// from the datasource slug because one registration serves several
	// consumers: a deployment that has registered a Google client for one
	// feature must not be asked to register a second for another.
	Provider string `json:"provider"`

	AuthURL  string `json:"auth_url"`
	TokenURL string `json:"token_url"`

	// RevokeURL ends a grant at the provider (RFC 7009).
	//
	// It is needed because forgetting a refresh token is not the same as
	// ending the authorization it belongs to. A datasource that is
	// re-authorized, removed, or switched to another method discards the only
	// copy of its token — and without this the grant stays live in the
	// provider's account, listed to a user who has every reason to think
	// disconnecting ended it.
	//
	// Optional: a provider that publishes no revocation endpoint simply cannot
	// be told, and the alternative is to hold a credential nobody wants.
	RevokeURL string `json:"revoke_url,omitempty"`

	// Scopes are requested at consent and required of the result. Providers
	// with granular consent let a user approve some and withhold others, and
	// the resulting token is valid but cannot do the job — so this list is
	// what the exchange checks the grant against, not merely what it asks for.
	Scopes []string `json:"scopes"`

	// IdentityScopes are requested at consent so the resulting connection can
	// be labelled with the account behind it, and are NOT required of the
	// result.
	//
	// They are separate from Scopes because they buy a label, not a
	// capability: withholding one leaves a grant that does everything the
	// datasource needs, and refusing it over a missing display name would be
	// absurd. Keeping them out of Scopes is what makes that true — the
	// exchange checks that list and no other.
	IdentityScopes []string `json:"identity_scopes,omitempty"`

	// UserInfoURL reports who authorized the grant, so two connections to the
	// same source can be told apart.
	//
	// It is declared here for the same reason RevokeURL is: it is a fact the
	// provider publishes alongside its endpoints, not deployment
	// configuration. Optional — a provider with no such endpoint, or a
	// consent that withheld IdentityScopes, yields an unlabelled connection
	// rather than a failed one.
	UserInfoURL string `json:"user_info_url,omitempty"`
}

// AuthMethodByID returns the provider's declared auth method with the given
// id.
//
// An empty id resolves to the provider's method only when it declares exactly
// one: a datasource saved while a provider offered a single method never
// stored a choice, and there is only one thing it can have been. With two or
// more, an empty id is genuinely ambiguous and resolves to nothing — guessing
// the first would answer for a datasource whose provider knows better, and a
// provider that later gains a second method would silently reclassify every
// datasource already connected under the original one.
func (m ProviderMeta) AuthMethodByID(id string) (AuthMethod, bool) {
	if id == "" {
		if len(m.AuthMethods) != 1 {
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
