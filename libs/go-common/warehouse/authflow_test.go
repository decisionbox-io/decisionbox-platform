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
	for _, absent := range []string{"flow", "authorization", "revoke_url"} {
		if strings.Contains(string(b), `"`+absent+`"`) {
			t.Errorf("a static method emitted %q: %s", absent, b)
		}
	}
}

func TestAuthMethod_AThreeLeggedMethodCarriesItsEndpoints(t *testing.T) {
	b, err := json.Marshal(AuthMethod{
		ID: "oauth_user", Name: "Sign in", Flow: FlowAuthorizationCode,
		Authorization: &AuthorizationCode{
			AuthURL:   "https://accounts.example/o/oauth2/auth",
			TokenURL:  "https://oauth2.example/token",
			RevokeURL: "https://oauth2.example/revoke",
			Scopes:    []string{"scope.readonly"},
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
	// Losing this one is silent: everything still works, and a grant outlives
	// the connection that held it.
	if back.Authorization.RevokeURL != "https://oauth2.example/revoke" {
		t.Errorf("round trip lost the revocation endpoint: %+v", back.Authorization)
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
	if _, ok := meta.AuthMethodByID("nonexistent"); ok {
		t.Error("an unknown method id resolved to something")
	}
	if _, ok := metaWithMethods().AuthMethodByID(""); ok {
		t.Error("a provider declaring no auth methods resolved one")
	}
}

// A datasource saved while its provider offered a single method never stored a
// choice, and there is only one thing it can have been.
func TestAuthMethodByID_AnUnsetIDResolvesOnlyWhenThereIsNoChoice(t *testing.T) {
	if got, ok := metaWithMethods("sa_key").AuthMethodByID(""); !ok || got.ID != "sa_key" {
		t.Errorf("an unset method on a single-method provider = %+v,%v, want the one method", got, ok)
	}
	// With two, an unset id is ambiguous. Answering "the first" would
	// reclassify every datasource connected under the original method the day
	// a provider gains a second — silently, and in whichever direction the new
	// method happened to be declared.
	if got, ok := metaWithMethods("oauth_user", "sa_key").AuthMethodByID(""); ok {
		t.Errorf("an unset method resolved to %+v on a provider offering two", got)
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
