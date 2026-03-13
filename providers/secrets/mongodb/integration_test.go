//go:build integration

package mongodb

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var testCol *mongo.Collection

func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := tcmongo.Run(ctx, "mongo:7.0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "MongoDB start failed: %v\n", err)
		os.Exit(1)
	}
	defer container.Terminate(ctx)

	uri, _ := container.ConnectionString(ctx)
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		fmt.Fprintf(os.Stderr, "MongoDB connect failed: %v\n", err)
		os.Exit(1)
	}
	defer client.Disconnect(ctx)

	testCol = client.Database("secrets_test").Collection("secrets")

	os.Exit(m.Run())
}

func newEncryptionKey() string {
	key := make([]byte, 32)
	rand.Read(key)
	return base64.StdEncoding.EncodeToString(key)
}

func TestInteg_SetAndGet_Encrypted(t *testing.T) {
	ctx := context.Background()
	p, err := NewMongoProvider(testCol, "test-ns", newEncryptionKey())
	if err != nil {
		t.Fatal(err)
	}

	// Set
	err = p.Set(ctx, "proj-1", "llm-api-key", "sk-ant-api03-secret-value-12345")
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Get
	val, err := p.Get(ctx, "proj-1", "llm-api-key")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if val != "sk-ant-api03-secret-value-12345" {
		t.Errorf("value = %q", val)
	}
}

func TestInteg_SetAndGet_Plaintext(t *testing.T) {
	ctx := context.Background()
	p, _ := NewMongoProvider(testCol, "test-plain", "")

	err := p.Set(ctx, "proj-plain", "key1", "plaintext-value")
	if err != nil {
		t.Fatal(err)
	}

	val, err := p.Get(ctx, "proj-plain", "key1")
	if err != nil {
		t.Fatal(err)
	}
	if val != "plaintext-value" {
		t.Errorf("value = %q", val)
	}
}

func TestInteg_GetNotFound(t *testing.T) {
	ctx := context.Background()
	p, _ := NewMongoProvider(testCol, "test-notfound", newEncryptionKey())

	_, err := p.Get(ctx, "nonexistent-proj", "nonexistent-key")
	if err != secrets.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestInteg_SetUpsert(t *testing.T) {
	ctx := context.Background()
	p, _ := NewMongoProvider(testCol, "test-upsert", newEncryptionKey())

	// Set initial
	p.Set(ctx, "proj-up", "key1", "value1")

	// Upsert with new value
	p.Set(ctx, "proj-up", "key1", "value2")

	val, _ := p.Get(ctx, "proj-up", "key1")
	if val != "value2" {
		t.Errorf("upsert failed: value = %q, want value2", val)
	}
}

func TestInteg_List(t *testing.T) {
	ctx := context.Background()
	p, _ := NewMongoProvider(testCol, "test-list", newEncryptionKey())

	// Set multiple secrets for same project
	p.Set(ctx, "proj-list", "llm-api-key", "sk-ant-api03-key-12345678")
	p.Set(ctx, "proj-list", "warehouse-creds", "wh-secret-value-abcdef")

	entries, err := p.List(ctx, "proj-list")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Verify masked values (never full)
	for _, e := range entries {
		if e.Key != "llm-api-key" && e.Key != "warehouse-creds" {
			t.Errorf("unexpected key: %q", e.Key)
		}
		if e.Masked == "" {
			t.Errorf("masked should not be empty for key %q", e.Key)
		}
		// Masked should contain *** and NOT contain the full value
		if e.Key == "llm-api-key" && e.Masked == "sk-ant-api03-key-12345678" {
			t.Error("masked value should not be the full value")
		}
	}
}

func TestInteg_NamespaceIsolation(t *testing.T) {
	ctx := context.Background()
	p1, _ := NewMongoProvider(testCol, "ns-alpha", newEncryptionKey())
	p2, _ := NewMongoProvider(testCol, "ns-beta", newEncryptionKey())

	// Set same key in different namespaces
	p1.Set(ctx, "proj-iso", "api-key", "alpha-value")
	p2.Set(ctx, "proj-iso", "api-key", "beta-value")

	// Each namespace sees its own value
	v1, _ := p1.Get(ctx, "proj-iso", "api-key")
	v2, _ := p2.Get(ctx, "proj-iso", "api-key")

	if v1 != "alpha-value" {
		t.Errorf("ns-alpha value = %q", v1)
	}
	if v2 != "beta-value" {
		t.Errorf("ns-beta value = %q", v2)
	}

	// List only shows own namespace
	list1, _ := p1.List(ctx, "proj-iso")
	list2, _ := p2.List(ctx, "proj-iso")
	if len(list1) != 1 || len(list2) != 1 {
		t.Errorf("namespace isolation broken: ns-alpha=%d, ns-beta=%d", len(list1), len(list2))
	}
}

func TestInteg_ProjectIsolation(t *testing.T) {
	ctx := context.Background()
	p, _ := NewMongoProvider(testCol, "test-projiso", newEncryptionKey())

	// Set secrets for different projects
	p.Set(ctx, "projA", "key1", "valueA")
	p.Set(ctx, "projB", "key1", "valueB")

	// Each project sees its own
	vA, _ := p.Get(ctx, "projA", "key1")
	vB, _ := p.Get(ctx, "projB", "key1")
	if vA != "valueA" || vB != "valueB" {
		t.Errorf("project isolation broken: A=%q, B=%q", vA, vB)
	}

	// List only shows own project
	listA, _ := p.List(ctx, "projA")
	listB, _ := p.List(ctx, "projB")
	if len(listA) != 1 || len(listB) != 1 {
		t.Errorf("project isolation broken in list: A=%d, B=%d", len(listA), len(listB))
	}
}

func TestInteg_EncryptedStoredDifferently(t *testing.T) {
	ctx := context.Background()
	p, _ := NewMongoProvider(testCol, "test-enccheck", newEncryptionKey())

	secret := "my-super-secret-api-key-value"
	p.Set(ctx, "proj-enc", "key1", secret)

	// Read raw from MongoDB — should be encrypted (not plaintext)
	var doc secretDoc
	err := testCol.FindOne(ctx, map[string]string{
		"namespace":  "test-enccheck",
		"project_id": "proj-enc",
		"key":        "key1",
	}).Decode(&doc)
	if err != nil {
		t.Fatal(err)
	}

	if !doc.Encrypted {
		t.Error("document should be marked as encrypted")
	}
	if doc.Value == secret {
		t.Error("stored value should NOT be plaintext — encryption failed")
	}

	// But Get should return the original value
	val, _ := p.Get(ctx, "proj-enc", "key1")
	if val != secret {
		t.Errorf("decrypted value = %q, want %q", val, secret)
	}
}
