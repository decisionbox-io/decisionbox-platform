//go:build integration

package azure

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	"github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
)

var (
	testProvider  *AzureProvider
	testVaultURL  string
	testNamespace string
	testCred      azcore.TokenCredential
	cleanupClient *azsecrets.Client
)

func TestMain(m *testing.M) {
	testVaultURL = os.Getenv("INTEGRATION_TEST_AZURE_VAULT_URL")
	if testVaultURL == "" {
		fmt.Println("INTEGRATION_TEST_AZURE_VAULT_URL not set, skipping Azure Key Vault integration tests")
		os.Exit(0)
	}

	ctx := context.Background()

	// Unique namespace per test run to avoid collisions
	b := make([]byte, 4)
	rand.Read(b)
	testNamespace = fmt.Sprintf("integ-test-%s", hex.EncodeToString(b))

	// Use AzureCLICredential for integration tests — DefaultAzureCredential
	// probes ManagedIdentity which times out on non-Azure machines (GCP, AWS).
	var err error
	testCred, err = azidentity.NewAzureCLICredential(nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create Azure CLI credential: %v\n", err)
		os.Exit(1)
	}

	testProvider, err = newTestProvider(testVaultURL, testNamespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create Azure provider: %v\n", err)
		os.Exit(1)
	}

	cleanupClient, err = azsecrets.NewClient(testVaultURL, testCred, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create Azure client for cleanup: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	// Clean up all test secrets (both primary and alt namespaces)
	cleanupPrefix(ctx, testNamespace+"-")
	cleanupPrefix(ctx, testNamespace+"-alt-")

	os.Exit(code)
}

// newTestProvider creates a provider using AzureCLICredential for integration tests.
func newTestProvider(vaultURL, namespace string) (*AzureProvider, error) {
	client, err := azsecrets.NewClient(vaultURL, testCred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}
	return NewAzureProviderWithClient(client, namespace), nil
}

func cleanupPrefix(ctx context.Context, prefix string) {
	pager := cleanupClient.NewListSecretPropertiesPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Cleanup list error for prefix %s: %v\n", prefix, err)
			return
		}
		for _, prop := range page.Value {
			if prop == nil || prop.ID == nil {
				continue
			}
			name := extractSecretName(prop.ID)
			if strings.HasPrefix(name, prefix) {
				if _, err := cleanupClient.DeleteSecret(ctx, name, nil); err != nil {
					fmt.Fprintf(os.Stderr, "Cleanup delete error for %s: %v\n", name, err)
					continue
				}
				// Purge the soft-deleted secret so the name is fully released.
				// Without purging, the name stays occupied for the retention period
				// (default 90 days) and would cause 409 Conflict on re-creation.
				if _, err := cleanupClient.PurgeDeletedSecret(ctx, name, nil); err != nil {
					fmt.Fprintf(os.Stderr, "Cleanup purge error for %s: %v\n", name, err)
				}
			}
		}
	}
}

// waitForList retries List until the expected count is reached or retries are exhausted.
// Azure Key Vault may have slight eventual consistency delays.
func waitForList(t *testing.T, provider secrets.Provider, projectID string, wantAtLeast int) []secrets.SecretEntry {
	t.Helper()
	ctx := context.Background()
	var entries []secrets.SecretEntry
	var err error
	for range 10 {
		entries, err = provider.List(ctx, projectID)
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}
		if len(entries) >= wantAtLeast {
			return entries
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("expected at least %d entries after retries, got %d", wantAtLeast, len(entries))
	return nil
}

func TestIntegration_SetAndGet(t *testing.T) {
	ctx := context.Background()

	err := testProvider.Set(ctx, "proj-integ", "test-key", "test-secret-value-12345")
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	val, err := testProvider.Get(ctx, "proj-integ", "test-key")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if val != "test-secret-value-12345" {
		t.Errorf("Get = %q, want %q", val, "test-secret-value-12345")
	}
}

func TestIntegration_GetNotFound(t *testing.T) {
	ctx := context.Background()

	_, err := testProvider.Get(ctx, "nonexistent-project", "nonexistent-key")
	if err != secrets.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestIntegration_SetUpsert(t *testing.T) {
	ctx := context.Background()

	err := testProvider.Set(ctx, "proj-upsert", "key1", "value-original")
	if err != nil {
		t.Fatalf("Set original failed: %v", err)
	}

	err = testProvider.Set(ctx, "proj-upsert", "key1", "value-updated")
	if err != nil {
		t.Fatalf("Set updated failed: %v", err)
	}

	val, err := testProvider.Get(ctx, "proj-upsert", "key1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if val != "value-updated" {
		t.Errorf("upsert failed: Get = %q, want %q", val, "value-updated")
	}
}

func TestIntegration_List(t *testing.T) {
	ctx := context.Background()

	if err := testProvider.Set(ctx, "proj-list", "api-key", "sk-ant-api03-key-12345678"); err != nil {
		t.Fatalf("Set api-key failed: %v", err)
	}
	if err := testProvider.Set(ctx, "proj-list", "warehouse-creds", "wh-secret-value-abcdef"); err != nil {
		t.Fatalf("Set warehouse-creds failed: %v", err)
	}

	entries := waitForList(t, testProvider, "proj-list", 2)

	found := map[string]bool{}
	for _, e := range entries {
		found[e.Key] = true
		if e.Masked == "" {
			t.Errorf("masked should not be empty for key %q", e.Key)
		}
		if e.Key == "api-key" && e.Masked == "sk-ant-api03-key-12345678" {
			t.Error("masked value should not be the full value")
		}
	}
	if !found["api-key"] || !found["warehouse-creds"] {
		t.Errorf("expected keys api-key and warehouse-creds, got %v", found)
	}
}

func TestIntegration_NamespaceIsolation(t *testing.T) {
	ctx := context.Background()

	altNamespace := testNamespace + "-alt"
	altProvider, err := newTestProvider(testVaultURL, altNamespace)
	if err != nil {
		t.Fatalf("Failed to create alt provider: %v", err)
	}

	if err := testProvider.Set(ctx, "proj-iso", "shared-key", "primary-value"); err != nil {
		t.Fatalf("Set primary failed: %v", err)
	}
	if err := altProvider.Set(ctx, "proj-iso", "shared-key", "alt-value"); err != nil {
		t.Fatalf("Set alt failed: %v", err)
	}

	v1, err := testProvider.Get(ctx, "proj-iso", "shared-key")
	if err != nil {
		t.Fatalf("Get primary failed: %v", err)
	}
	v2, err := altProvider.Get(ctx, "proj-iso", "shared-key")
	if err != nil {
		t.Fatalf("Get alt failed: %v", err)
	}

	if v1 != "primary-value" {
		t.Errorf("primary namespace value = %q, want %q", v1, "primary-value")
	}
	if v2 != "alt-value" {
		t.Errorf("alt namespace value = %q, want %q", v2, "alt-value")
	}

	waitForList(t, testProvider, "proj-iso", 1)
	waitForList(t, altProvider, "proj-iso", 1)
}

func TestIntegration_ProjectIsolation(t *testing.T) {
	ctx := context.Background()

	if err := testProvider.Set(ctx, "proj-a", "key1", "value-a"); err != nil {
		t.Fatalf("Set proj-a failed: %v", err)
	}
	if err := testProvider.Set(ctx, "proj-b", "key1", "value-b"); err != nil {
		t.Fatalf("Set proj-b failed: %v", err)
	}

	vA, err := testProvider.Get(ctx, "proj-a", "key1")
	if err != nil {
		t.Fatalf("Get proj-a failed: %v", err)
	}
	vB, err := testProvider.Get(ctx, "proj-b", "key1")
	if err != nil {
		t.Fatalf("Get proj-b failed: %v", err)
	}

	if vA != "value-a" {
		t.Errorf("proj-a value = %q, want %q", vA, "value-a")
	}
	if vB != "value-b" {
		t.Errorf("proj-b value = %q, want %q", vB, "value-b")
	}

	entriesA := waitForList(t, testProvider, "proj-a", 1)
	entriesB := waitForList(t, testProvider, "proj-b", 1)
	if len(entriesA) != 1 {
		t.Errorf("proj-a should have 1 entry, got %d", len(entriesA))
	}
	if len(entriesB) != 1 {
		t.Errorf("proj-b should have 1 entry, got %d", len(entriesB))
	}
}

func TestIntegration_ViaFactory(t *testing.T) {
	// The factory path uses DefaultAzureCredential which probes ManagedIdentity
	// (~2min timeout on non-Azure machines). Skip unless AZURE_TENANT_ID is set,
	// which indicates EnvironmentCredential or a native Azure environment.
	if os.Getenv("AZURE_TENANT_ID") == "" {
		t.Skip("skipping factory test: AZURE_TENANT_ID not set (DefaultAzureCredential would timeout on non-Azure machines)")
	}

	ctx := context.Background()

	provider, err := secrets.NewProvider(secrets.Config{
		Provider:      "azure",
		AzureVaultURL: testVaultURL,
		Namespace:     testNamespace,
	})
	if err != nil {
		t.Fatalf("Factory error: %v", err)
	}

	err = provider.Set(ctx, "proj-factory", "factory-key", "factory-value-abc")
	if err != nil {
		t.Fatalf("Set via factory failed: %v", err)
	}

	val, err := provider.Get(ctx, "proj-factory", "factory-key")
	if err != nil {
		t.Fatalf("Get via factory failed: %v", err)
	}
	if val != "factory-value-abc" {
		t.Errorf("factory Get = %q, want %q", val, "factory-value-abc")
	}
}
