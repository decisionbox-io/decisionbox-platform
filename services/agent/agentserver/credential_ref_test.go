package agentserver

import (
	"context"
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
