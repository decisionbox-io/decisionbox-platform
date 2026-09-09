package agentserver

import (
	"context"
	"strings"
	"sync"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// capturedConfig is the config the last construction of the capture stub saw.
// initWarehouseProvider builds the provider for every discovery run, ask turn,
// schema index and connection test in the product, so what it hands the factory
// is the thing worth pinning.
var (
	capturedMu sync.Mutex
	captured   gowarehouse.ProviderConfig
)

func init() {
	gowarehouse.RegisterWithMeta("test-capture-source",
		func(cfg gowarehouse.ProviderConfig) (gowarehouse.Provider, error) {
			capturedMu.Lock()
			captured = cfg
			capturedMu.Unlock()
			return nil, nil
		},
		gowarehouse.ProviderMeta{
			Name:       "Capture test source",
			Capability: gowarehouse.Capability{Shape: gowarehouse.ShapeCube},
		})
}

// buildWith runs initWarehouseProvider over a one-datasource project and returns
// the config the factory received. Construction itself returns a nil provider,
// which surfaces as an error the caller ignores — the config is what is under
// test.
func buildWith(t *testing.T, cfg map[string]string, secrets *fakeSecretProvider) gowarehouse.ProviderConfig {
	t.Helper()
	capturedMu.Lock()
	captured = nil
	capturedMu.Unlock()

	project := &models.Project{
		ID:         "p1",
		Warehouses: []models.WarehouseConfig{{ID: "wh_1", Provider: "test-capture-source", Config: cfg}},
	}
	_, _ = initWarehouseProvider(context.Background(), project, "wh_1", secrets, "p1") //nolint:errcheck // the stub factory returns nil; the config is the assertion

	capturedMu.Lock()
	defer capturedMu.Unlock()
	return captured
}

// Characterization: with no credential_ref, the credential comes from the key
// derived from the datasource id — which is every datasource that exists today.
// This must not change.
func TestInitWarehouseProvider_DerivesTheCredentialKeyFromTheDatasourceID(t *testing.T) {
	secrets := &fakeSecretProvider{store: map[string]string{
		"p1/" + gowarehouse.CredentialsKey("wh_1"): "the-derived-credential",
	}}

	cfg := buildWith(t, nil, secrets)
	if cfg == nil {
		t.Fatal("the provider factory was never reached")
	}
	if cfg["credentials_json"] != "the-derived-credential" {
		t.Fatalf("credentials_json = %q, want the value under the derived key", cfg["credentials_json"])
	}
}

// A datasource with no stored credential still constructs: the provider reports
// what it needs in its own words, which is a better message than anything here.
func TestInitWarehouseProvider_NoStoredCredentialIsNotAnError(t *testing.T) {
	cfg := buildWith(t, nil, &fakeSecretProvider{store: map[string]string{}})
	if cfg == nil {
		t.Fatal("the provider factory was never reached")
	}
	if _, present := cfg["credentials_json"]; present {
		t.Fatalf("credentials_json = %q, want it absent", cfg["credentials_json"])
	}
}

// The point of the ref: a credential obtained once, under a key that belongs to
// no single datasource, read by whichever datasource names it.
func TestInitWarehouseProvider_ReadsTheNamedCredentialWhenSet(t *testing.T) {
	secrets := &fakeSecretProvider{store: map[string]string{
		"p1/" + sharedKey(t, "abc"):                "the-shared-grant",
		"p1/" + gowarehouse.CredentialsKey("wh_1"): "the-derived-credential",
	}}

	cfg := buildWith(t, map[string]string{gowarehouse.CredentialRefKey: "abc"}, secrets)
	if cfg["credentials_json"] != "the-shared-grant" {
		t.Fatalf("credentials_json = %q, want the credential the ref names", cfg["credentials_json"])
	}
}

// Two datasources naming the same slot both authenticate with it — which is the
// whole reason the ref exists, and what a derived key cannot do.
func TestInitWarehouseProvider_TwoDatasourcesCanShareOneCredential(t *testing.T) {
	secrets := &fakeSecretProvider{store: map[string]string{
		"p1/" + sharedKey(t, "abc"): "the-shared-grant",
	}}
	project := &models.Project{
		ID: "p1",
		Warehouses: []models.WarehouseConfig{
			{ID: "wh_1", Provider: "test-capture-source", Config: map[string]string{gowarehouse.CredentialRefKey: "abc"}},
			{ID: "wh_2", Provider: "test-capture-source", Config: map[string]string{gowarehouse.CredentialRefKey: "abc"}},
		},
	}
	for _, id := range []string{"wh_1", "wh_2"} {
		capturedMu.Lock()
		captured = nil
		capturedMu.Unlock()
		_, _ = initWarehouseProvider(context.Background(), project, id, secrets, "p1") //nolint:errcheck // the stub factory returns nil
		capturedMu.Lock()
		got := captured["credentials_json"]
		capturedMu.Unlock()
		if got != "the-shared-grant" {
			t.Errorf("%s got credentials_json = %q, want the shared grant", id, got)
		}
	}
}

// An empty ref is not a ref. It has to fall back rather than read a secret named
// "", which would silently leave the datasource with no credential at all.
func TestInitWarehouseProvider_AnEmptyRefFallsBackToTheDerivedKey(t *testing.T) {
	secrets := &fakeSecretProvider{store: map[string]string{
		"p1/" + gowarehouse.CredentialsKey("wh_1"): "the-derived-credential",
	}}
	for _, ref := range []string{"", "   "} {
		cfg := buildWith(t, map[string]string{gowarehouse.CredentialRefKey: ref}, secrets)
		if cfg["credentials_json"] != "the-derived-credential" {
			t.Errorf("ref %q: credentials_json = %q, want the derived one", ref, cfg["credentials_json"])
		}
	}
}

// The ref names where a credential was found, which is no more a provider's
// business than the derived key it replaces. Leaving it in the config would also
// let a provider treat it as one of its own fields.
func TestInitWarehouseProvider_TheRefIsNotPassedToTheProvider(t *testing.T) {
	secrets := &fakeSecretProvider{store: map[string]string{"p1/" + sharedKey(t, "abc"): "grant"}}
	cfg := buildWith(t, map[string]string{gowarehouse.CredentialRefKey: "abc"}, secrets)
	if v, present := cfg[gowarehouse.CredentialRefKey]; present {
		t.Fatalf("the provider was handed %s = %q", gowarehouse.CredentialRefKey, v)
	}
}

// The ref is an id composed INTO a key, not a key. That is what stops a
// datasource config — writable by anyone who can edit the project — from naming
// another secret the project holds, and the read must land in the shared
// namespace whatever the id looks like.
func TestInitWarehouseProvider_ARefCannotNameAnotherProjectSecret(t *testing.T) {
	secrets := &fakeSecretProvider{store: map[string]string{
		"p1/llm-credentials":                       "the-llm-secret",
		"p1/" + gowarehouse.CredentialsKey("wh_1"): "the-derived-credential",
	}}

	cfg := buildWith(t, map[string]string{gowarehouse.CredentialRefKey: "llm-credentials"}, secrets)
	if got := cfg["credentials_json"]; got == "the-llm-secret" {
		t.Fatal("a data source read another feature's credential by naming its key")
	}
	// And it does not silently fall back to the derived one either: the ref was
	// honoured, it just named a slot that does not exist.
	if got := cfg["credentials_json"]; got == "the-derived-credential" {
		t.Fatal("a ref that named nothing fell back to the derived key")
	}
}

// An id no key can be formed from is refused rather than falling back to the
// derived key, which would look like a working connection authenticating as
// something else.
func TestInitWarehouseProvider_AnUnusableRefIsRefused(t *testing.T) {
	secrets := &fakeSecretProvider{store: map[string]string{
		"p1/" + gowarehouse.CredentialsKey("wh_1"): "the-derived-credential",
	}}
	project := &models.Project{
		ID: "p1",
		Warehouses: []models.WarehouseConfig{{
			ID: "wh_1", Provider: "test-capture-source",
			Config: map[string]string{gowarehouse.CredentialRefKey: strings.Repeat("a", 200)},
		}},
	}
	if _, err := initWarehouseProvider(context.Background(), project, "wh_1", secrets, "p1"); err == nil {
		t.Error("an id no key can be formed from was accepted")
	}
}

// sharedKey composes the slot an id names, so a test seeds exactly where the
// agent will look.
func sharedKey(t *testing.T, id string) string {
	t.Helper()
	key, ok := gowarehouse.SharedCredentialKey(id)
	if !ok {
		t.Fatalf("SharedCredentialKey(%q) was refused", id)
	}
	return key
}
