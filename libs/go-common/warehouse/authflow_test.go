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
			Provider:  "example",
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
	// Without the provider there is no app registration to look up, so the
	// method cannot authenticate at all.
	if back.Authorization.Provider != "example" {
		t.Errorf("round trip lost the OAuth provider: %+v", back.Authorization)
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
