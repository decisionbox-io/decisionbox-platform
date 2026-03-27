//go:build integration

package gcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"google.golang.org/api/iterator"
)

var (
	testProvider   *GCPProvider
	testGCPProject string
	testNamespace  string
	cleanupClient  *secretmanager.Client
)

func TestMain(m *testing.M) {
	testGCPProject = os.Getenv("INTEGRATION_TEST_GCP_PROJECT_ID")
	if testGCPProject == "" {
		fmt.Println("INTEGRATION_TEST_GCP_PROJECT_ID not set, skipping GCP secret integration tests")
		os.Exit(0)
	}

	ctx := context.Background()

	// Unique namespace per test run to avoid collisions
	b := make([]byte, 4)
	rand.Read(b)
	testNamespace = fmt.Sprintf("integ-test-%s", hex.EncodeToString(b))

	var err error
	testProvider, err = NewGCPProvider(ctx, testGCPProject, testNamespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create GCP provider: %v\n", err)
		os.Exit(1)
	}

	// Separate client for cleanup (DeleteSecret is not in the provider interface)
	cleanupClient, err = secretmanager.NewClient(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create cleanup client: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	// Clean up all test secrets
	cleanupNamespace(ctx, testNamespace)
	cleanupNamespace(ctx, testNamespace+"-alt")

	os.Exit(code)
}

func cleanupNamespace(ctx context.Context, ns string) {
	parent := fmt.Sprintf("projects/%s", testGCPProject)
	it := cleanupClient.ListSecrets(ctx, &secretmanagerpb.ListSecretsRequest{
		Parent: parent,
		Filter: fmt.Sprintf("labels.namespace=%s", ns),
	})
	for {
		secret, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Cleanup list error for namespace %s: %v\n", ns, err)
			break
		}
		if err := cleanupClient.DeleteSecret(ctx, &secretmanagerpb.DeleteSecretRequest{
			Name: secret.Name,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Cleanup delete error for %s: %v\n", secret.Name, err)
		}
	}
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

	entries, err := testProvider.List(ctx, "proj-list")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

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
	altProvider, err := NewGCPProvider(ctx, testGCPProject, altNamespace)
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

	list1, _ := testProvider.List(ctx, "proj-iso")
	list2, _ := altProvider.List(ctx, "proj-iso")
	if len(list1) < 1 {
		t.Errorf("primary namespace should have at least 1 entry, got %d", len(list1))
	}
	if len(list2) < 1 {
		t.Errorf("alt namespace should have at least 1 entry, got %d", len(list2))
	}
}

func TestIntegration_ProjectIsolation(t *testing.T) {
	ctx := context.Background()

	// GCP labels require lowercase values — use lowercase project IDs
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

	listA, _ := testProvider.List(ctx, "proj-a")
	listB, _ := testProvider.List(ctx, "proj-b")
	if len(listA) != 1 {
		t.Errorf("proj-a should have 1 entry, got %d", len(listA))
	}
	if len(listB) != 1 {
		t.Errorf("proj-b should have 1 entry, got %d", len(listB))
	}
}

func TestIntegration_ViaFactory(t *testing.T) {
	ctx := context.Background()

	provider, err := secrets.NewProvider(secrets.Config{
		Provider:     "gcp",
		GCPProjectID: testGCPProject,
		Namespace:    testNamespace,
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
