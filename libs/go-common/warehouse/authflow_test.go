package warehouse

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every auth method that existed before flows did declares none, so the zero
// value has to keep meaning "a form the operator fills in". A default that
// read as anything else would reclassify every provider in the registry.
func TestFlow_TheZeroValueIsTheOldBehaviour(t *testing.T) {
	var m AuthMethod
	if m.Flow != FlowStaticConfig {
		t.Errorf("an undeclared flow = %q, want the static-config flow", m.Flow)
	}
}

// The wire shape is a contract with the dashboard, which renders the connect
// form from it. A provider that declares no flow must serialise exactly as it
// did before the field existed, or every existing provider's payload changes
// shape for a capability none of them use.
func TestAuthMethod_AStaticMethodSerialisesUnchanged(t *testing.T) {
	b, err := json.Marshal(AuthMethod{
		ID: "sa_key", Name: "Service Account Key", Description: "help",
		Fields: []ConfigField{{Key: "credentials_json", Type: "credential"}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, absent := range []string{"flow", "authorization"} {
		if strings.Contains(string(b), `"`+absent+`"`) {
			t.Errorf("a static method emitted %q: %s", absent, b)
		}
	}
}

func TestAuthMethod_AThreeLeggedMethodCarriesItsEndpoints(t *testing.T) {
	b, err := json.Marshal(AuthMethod{
		ID: "oauth_user", Name: "Sign in", Flow: FlowAuthorizationCode,
		Authorization: &AuthorizationCode{
			AuthURL:  "https://accounts.example/o/oauth2/auth",
			TokenURL: "https://oauth2.example/token",
			Scopes:   []string{"scope.readonly"},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back AuthMethod
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Flow != FlowAuthorizationCode || back.Authorization == nil {
		t.Fatalf("round trip lost the flow: %+v", back)
	}
	if back.Authorization.AuthURL == "" || back.Authorization.TokenURL == "" || len(back.Authorization.Scopes) != 1 {
		t.Errorf("round trip lost the endpoints: %+v", back.Authorization)
	}
}

// --- resolving the selected method ---

func metaWithMethods(ids ...string) ProviderMeta {
	m := ProviderMeta{}
	for _, id := range ids {
		m.AuthMethods = append(m.AuthMethods, AuthMethod{ID: id})
	}
	return m
}

func TestAuthMethodByID(t *testing.T) {
	meta := metaWithMethods("oauth_user", "sa_key")

	if got, ok := meta.AuthMethodByID("sa_key"); !ok || got.ID != "sa_key" {
		t.Errorf("by id = %+v,%v", got, ok)
	}
	// A datasource configured before the provider offered a choice stores no
	// method. Resolving that to nothing would leave it unable to authenticate
	// at all, so it resolves to the provider's first — the one it was
	// configured under.
	if got, ok := meta.AuthMethodByID(""); !ok || got.ID != "oauth_user" {
		t.Errorf("an unset method = %+v,%v, want the first declared", got, ok)
	}
	if _, ok := meta.AuthMethodByID("nonexistent"); ok {
		t.Error("an unknown method id resolved to something")
	}
	if _, ok := metaWithMethods().AuthMethodByID(""); ok {
		t.Error("a provider declaring no auth methods resolved one")
	}
}

// --- the app-registration secret keys ---

// Cloud secret backends compose this key into the backing secret's name and
// restrict its charset; Azure Key Vault is the strictest at [A-Za-z0-9-]. A
// key outside that alphabet fails to store, and it fails at connect time on
// one deployment and not another.
func TestOAuthAppKey_StaysInsideTheStrictestBackendsAlphabet(t *testing.T) {
	for _, provider := range []string{"ga4", "salesforce", "some_provider", "odd:slug/v2"} {
		for _, field := range OAuthAppFields {
			key := OAuthAppKey(provider, field)
			for _, r := range key {
				if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
					continue
				}
				t.Fatalf("key %q for (%q,%q) contains %q, which Azure Key Vault rejects", key, provider, field, r)
			}
		}
	}
}

// The registration is instance-scoped and per provider, so two providers must
// never share a slot — connecting Salesforce would otherwise overwrite the
// OAuth client GA4 connections were issued against and break all of them.
func TestOAuthAppKey_IsDistinctPerProviderAndField(t *testing.T) {
	seen := map[string]string{}
	for _, provider := range []string{"ga4", "salesforce", "ga-4", "ga_4"} {
		for _, field := range OAuthAppFields {
			key := OAuthAppKey(provider, field)
			if prev, clash := seen[key]; clash {
				t.Fatalf("%s/%s collides with %s on key %q", provider, field, prev, key)
			}
			seen[key] = provider + "/" + field
		}
	}
}

// The key is stored data: changing it orphans every registration already
// saved, and the symptom is a working deployment reporting its OAuth app as
// unconfigured after an upgrade.
func TestOAuthAppKey_IsStable(t *testing.T) {
	if got, want := OAuthAppKey("ga4", OAuthFieldClientID), "warehouse-oauth-app-676134-636c69656e745f6964"; got != want {
		t.Errorf("OAuthAppKey = %q, want %q", got, want)
	}
}

// The registration is passed to a provider factory through the same flat map
// as its own config fields, where "client_id" is a plausible name for
// something a provider asks for itself. The namespace is what stops one
// silently overwriting the other.
func TestOAuthAppConfigKey_DoesNotCollideWithAProvidersOwnFields(t *testing.T) {
	for _, f := range OAuthAppFields {
		if got := OAuthAppConfigKey(f); got == f {
			t.Errorf("OAuthAppConfigKey(%q) = %q — un-namespaced", f, got)
		}
	}
}
