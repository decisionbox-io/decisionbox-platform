package agentserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// authFlowSlugSeq makes every registered stub distinct for the life of the
// process.
var authFlowSlugSeq atomic.Int64

// registerAuthFlowProvider publishes a provider whose only purpose is to
// declare an auth method of the given flow, and returns its slug. The factory
// is never called — applyOAuthAppRegistration reads metadata and nothing else.
//
// The slug is unique per registration because the registry is process-global
// and panics on a duplicate: anything reused — including the test's own name,
// which repeats under `go test -count=2` — turns a rerun into a panic with
// nothing wrong in the code under test.
func registerAuthFlowProvider(t *testing.T, methods ...gowarehouse.AuthMethod) string {
	t.Helper()
	slug := fmt.Sprintf("test-authflow-%d-%s", authFlowSlugSeq.Add(1), strings.ToLower(t.Name()))
	gowarehouse.RegisterWithMeta(slug,
		func(gowarehouse.ProviderConfig) (gowarehouse.Provider, error) {
			t.Fatal("the provider factory should not run here")
			return nil, nil
		},
		gowarehouse.ProviderMeta{Name: slug, AuthMethods: methods},
	)
	return slug
}

func threeLegged(id string) gowarehouse.AuthMethod {
	return gowarehouse.AuthMethod{
		ID: id, Flow: gowarehouse.FlowAuthorizationCode,
		Authorization: &gowarehouse.AuthorizationCode{
			AuthURL: "https://accounts.example/auth", TokenURL: "https://oauth.example/token",
			Scopes: []string{"example.readonly"},
		},
	}
}

func appSecrets(t *testing.T, slug string, fields map[string]string) *fakeSecretProvider {
	t.Helper()
	sp := &fakeSecretProvider{store: map[string]string{}}
	for f, v := range fields {
		sp.store["/"+gowarehouse.OAuthAppKey(slug, f)] = v
	}
	return sp
}

// The refresh token stored as the datasource's credential cannot mint an
// access token without the client that issued it, and that client is
// registered per deployment rather than per datasource — so it has to be
// added here or the provider has half a credential.
func TestApplyOAuthAppRegistration_AddsTheDeploymentsClient(t *testing.T) {
	slug := registerAuthFlowProvider(t, threeLegged("oauth_user"))
	sp := appSecrets(t, slug, map[string]string{
		gowarehouse.OAuthFieldClientID:     "cid",
		gowarehouse.OAuthFieldClientSecret: "sec",
		gowarehouse.OAuthFieldRedirectURI:  "https://dash.example",
	})

	cfg := gowarehouse.ProviderConfig{"auth_method": "oauth_user"}
	if err := applyOAuthAppRegistration(context.Background(), sp, slug, cfg); err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := map[string]string{
		gowarehouse.OAuthAppConfigKey(gowarehouse.OAuthFieldClientID):     "cid",
		gowarehouse.OAuthAppConfigKey(gowarehouse.OAuthFieldClientSecret): "sec",
		gowarehouse.OAuthAppConfigKey(gowarehouse.OAuthFieldRedirectURI):  "https://dash.example",
	}
	for k, v := range want {
		if cfg[k] != v {
			t.Errorf("cfg[%q] = %q, want %q", k, cfg[k], v)
		}
	}
}

// Every other datasource in a deployment authenticates with a key or a
// password. Reading an OAuth registration for those would be a secret lookup
// per provider construction that can only ever come back empty.
func TestApplyOAuthAppRegistration_LeavesAStaticMethodAlone(t *testing.T) {
	slug := registerAuthFlowProvider(t, gowarehouse.AuthMethod{ID: "sa_key"})
	sp := appSecrets(t, slug, map[string]string{gowarehouse.OAuthFieldClientID: "cid"})

	cfg := gowarehouse.ProviderConfig{"auth_method": "sa_key"}
	if err := applyOAuthAppRegistration(context.Background(), sp, slug, cfg); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(cfg) != 1 {
		t.Errorf("a static method's config was added to: %v", cfg)
	}
}

// A provider offering both methods must be read from what the datasource
// selected, not from what the provider is capable of. Answering from the
// provider would inject an OAuth client into a key-authenticated datasource
// and, worse, skip it for an OAuth one on a provider whose first method is a
// key.
func TestApplyOAuthAppRegistration_ReadsTheSelectedMethodNotTheProvider(t *testing.T) {
	slug := registerAuthFlowProvider(t, gowarehouse.AuthMethod{ID: "sa_key"}, threeLegged("oauth_user"))
	fields := map[string]string{gowarehouse.OAuthFieldClientID: "cid"}
	clientIDKey := gowarehouse.OAuthAppConfigKey(gowarehouse.OAuthFieldClientID)

	onKey := gowarehouse.ProviderConfig{"auth_method": "sa_key"}
	if err := applyOAuthAppRegistration(context.Background(), appSecrets(t, slug, fields), slug, onKey); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, added := onKey[clientIDKey]; added {
		t.Error("a key-authenticated datasource on a provider that also offers OAuth got an OAuth client")
	}

	onOAuth := gowarehouse.ProviderConfig{"auth_method": "oauth_user"}
	if err := applyOAuthAppRegistration(context.Background(), appSecrets(t, slug, fields), slug, onOAuth); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if onOAuth[clientIDKey] != "cid" {
		t.Error("an OAuth datasource on a provider whose first method is a key got no OAuth client")
	}
}

// An unset field means the operator has not registered the app. The provider
// says so in its own words, naming what it needs; a secret-store detail
// surfaced here would not.
func TestApplyOAuthAppRegistration_AnUnregisteredAppIsNotAnError(t *testing.T) {
	slug := registerAuthFlowProvider(t, threeLegged("oauth_user"))

	cfg := gowarehouse.ProviderConfig{"auth_method": "oauth_user"}
	if err := applyOAuthAppRegistration(context.Background(), appSecrets(t, slug, nil), slug, cfg); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(cfg) != 1 {
		t.Errorf("nothing was registered, yet config gained entries: %v", cfg)
	}
}

// A secret store that cannot be read has not said the field is empty. Treating
// the two alike would report a configured deployment as unconfigured and send
// an operator to re-enter credentials that are already stored.
func TestApplyOAuthAppRegistration_AnUnreadableStoreIsAnError(t *testing.T) {
	slug := registerAuthFlowProvider(t, threeLegged("oauth_user"))
	sp := &fakeSecretProvider{getErr: errors.New("secret store unavailable")}

	err := applyOAuthAppRegistration(context.Background(), sp, slug, gowarehouse.ProviderConfig{"auth_method": "oauth_user"})
	if err == nil {
		t.Fatal("an unreadable secret store was reported as an unregistered app")
	}
}

// An unregistered slug is reported by NewProvider, which has the name and the
// list of what is registered. Failing earlier here would replace that with a
// worse message.
func TestApplyOAuthAppRegistration_AnUnknownProviderIsLeftToNewProvider(t *testing.T) {
	cfg := gowarehouse.ProviderConfig{"auth_method": "oauth_user"}
	if err := applyOAuthAppRegistration(context.Background(), &fakeSecretProvider{}, "nope-not-registered", cfg); err != nil {
		t.Fatalf("apply: %v", err)
	}
}

// The oauth_app_* namespace belongs to the deployment. A project document that
// carries a value in it — written before the datasource routes reserved the
// namespace, or by anything else that writes project documents — must not
// reach a provider, or a missing app registration would hide behind a stale
// per-project one and the separation would be worth nothing.
func TestApplyOAuthAppRegistration_ProjectSuppliedClientFieldsNeverSurvive(t *testing.T) {
	slug := registerAuthFlowProvider(t, threeLegged("oauth_user"))
	clientID := gowarehouse.OAuthAppConfigKey(gowarehouse.OAuthFieldClientID)
	clientSecret := gowarehouse.OAuthAppConfigKey(gowarehouse.OAuthFieldClientSecret)

	// Nothing registered: the project's own values must not stand in for it.
	cfg := gowarehouse.ProviderConfig{
		"auth_method": "oauth_user",
		clientID:      "from-the-project-document",
		clientSecret:  "and-a-secret-with-it",
	}
	if err := applyOAuthAppRegistration(context.Background(), appSecrets(t, slug, nil), slug, cfg); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, k := range []string{clientID, clientSecret} {
		if v, present := cfg[k]; present {
			t.Errorf("cfg[%q] = %q survived from the project document", k, v)
		}
	}

	// Registered: the deployment's value is what the provider sees.
	cfg = gowarehouse.ProviderConfig{"auth_method": "oauth_user", clientID: "from-the-project-document"}
	registered := appSecrets(t, slug, map[string]string{gowarehouse.OAuthFieldClientID: "the-deployments"})
	if err := applyOAuthAppRegistration(context.Background(), registered, slug, cfg); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if cfg[clientID] != "the-deployments" {
		t.Errorf("cfg[%q] = %q, want the deployment's registration", clientID, cfg[clientID])
	}
}

// The namespace is cleared whatever the datasource authenticates with. A
// key-authenticated source has no use for these fields, and leaving a
// project-supplied value sitting in a namespace the platform owns is a state
// worth not having.
func TestApplyOAuthAppRegistration_TheNamespaceIsClearedForEveryMethod(t *testing.T) {
	slug := registerAuthFlowProvider(t, gowarehouse.AuthMethod{ID: "sa_key"})
	clientSecret := gowarehouse.OAuthAppConfigKey(gowarehouse.OAuthFieldClientSecret)

	cfg := gowarehouse.ProviderConfig{"auth_method": "sa_key", clientSecret: "from-the-project-document"}
	if err := applyOAuthAppRegistration(context.Background(), appSecrets(t, slug, nil), slug, cfg); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, present := cfg[clientSecret]; present {
		t.Error("a project-supplied client secret survived on a key-authenticated datasource")
	}
}
