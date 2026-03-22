package mongodb

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// mockSecretCollection implements secretCollection for unit testing.
type mockSecretCollection struct {
	findOneFn  func(ctx context.Context, filter interface{}, opts ...*options.FindOneOptions) *mongo.SingleResult
	updateOneFn func(ctx context.Context, filter interface{}, update interface{}, opts ...*options.UpdateOptions) (*mongo.UpdateResult, error)
	findFn     func(ctx context.Context, filter interface{}, opts ...*options.FindOptions) (*mongo.Cursor, error)
	indexesFn  func() mongo.IndexView
}

func (m *mockSecretCollection) FindOne(ctx context.Context, filter interface{}, opts ...*options.FindOneOptions) *mongo.SingleResult {
	if m.findOneFn != nil {
		return m.findOneFn(ctx, filter, opts...)
	}
	return mongo.NewSingleResultFromDocument(nil, fmt.Errorf("FindOne not implemented"), nil)
}

func (m *mockSecretCollection) UpdateOne(ctx context.Context, filter interface{}, update interface{}, opts ...*options.UpdateOptions) (*mongo.UpdateResult, error) {
	if m.updateOneFn != nil {
		return m.updateOneFn(ctx, filter, update, opts...)
	}
	return nil, fmt.Errorf("UpdateOne not implemented")
}

func (m *mockSecretCollection) Find(ctx context.Context, filter interface{}, opts ...*options.FindOptions) (*mongo.Cursor, error) {
	if m.findFn != nil {
		return m.findFn(ctx, filter, opts...)
	}
	return nil, fmt.Errorf("Find not implemented")
}

func (m *mockSecretCollection) Indexes() mongo.IndexView {
	if m.indexesFn != nil {
		return m.indexesFn()
	}
	return mongo.IndexView{}
}

// Compile-time check that mockSecretCollection satisfies secretCollection.
var _ secretCollection = (*mockSecretCollection)(nil)

// newTestMongoProvider creates a MongoProvider with a mock collection for testing.
// If encryptionKey is empty, no encryption is used.
func newTestMongoProvider(col secretCollection, namespace, encryptionKey string) *MongoProvider {
	if namespace == "" {
		namespace = "decisionbox"
	}

	p := &MongoProvider{
		col:       col,
		namespace: namespace,
	}

	if encryptionKey != "" {
		// Re-use the existing encryption setup logic from the real constructor
		keyBytes, err := base64.StdEncoding.DecodeString(encryptionKey)
		if err != nil {
			panic(fmt.Sprintf("invalid encryption key for test: %v", err))
		}
		realProvider, err := NewMongoProvider(nil, namespace, encryptionKey)
		if err != nil {
			panic(fmt.Sprintf("failed to create provider for encryption setup: %v", err))
		}
		p.gcm = realProvider.gcm
		_ = keyBytes
	}

	return p
}

// genEncryptionKey generates a random 32-byte base64-encoded encryption key for tests.
func genEncryptionKey() string {
	keyBytes := make([]byte, 32)
	rand.Read(keyBytes)
	return base64.StdEncoding.EncodeToString(keyBytes)
}

func TestMongoProvider_Get_Success(t *testing.T) {
	encKey := genEncryptionKey()
	p := newTestMongoProvider(nil, "decisionbox", encKey)

	// Encrypt a value to store in the mock document
	encrypted, err := p.encrypt("sk-ant-secret-value-12345")
	if err != nil {
		t.Fatalf("encrypt() error = %v", err)
	}

	mock := &mockSecretCollection{
		findOneFn: func(_ context.Context, filter interface{}, _ ...*options.FindOneOptions) *mongo.SingleResult {
			// Verify filter
			f, ok := filter.(bson.M)
			if !ok {
				t.Fatalf("filter is not bson.M: %T", filter)
			}
			if f["namespace"] != "decisionbox" {
				t.Errorf("filter namespace = %v, want decisionbox", f["namespace"])
			}
			if f["project_id"] != "proj-1" {
				t.Errorf("filter project_id = %v, want proj-1", f["project_id"])
			}
			if f["key"] != "llm-api-key" {
				t.Errorf("filter key = %v, want llm-api-key", f["key"])
			}

			doc := secretDoc{
				Namespace: "decisionbox",
				ProjectID: "proj-1",
				Key:       "llm-api-key",
				Value:     encrypted,
				Encrypted: true,
				UpdatedAt: time.Now(),
			}
			return mongo.NewSingleResultFromDocument(doc, nil, nil)
		},
	}
	p.col = mock

	val, err := p.Get(context.Background(), "proj-1", "llm-api-key")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if val != "sk-ant-secret-value-12345" {
		t.Errorf("Get() = %q, want %q", val, "sk-ant-secret-value-12345")
	}
}

func TestMongoProvider_Get_Success_NoEncryption(t *testing.T) {
	mock := &mockSecretCollection{
		findOneFn: func(_ context.Context, _ interface{}, _ ...*options.FindOneOptions) *mongo.SingleResult {
			doc := secretDoc{
				Namespace: "decisionbox",
				ProjectID: "proj-1",
				Key:       "plain-key",
				Value:     "plaintext-value",
				Encrypted: false,
				UpdatedAt: time.Now(),
			}
			return mongo.NewSingleResultFromDocument(doc, nil, nil)
		},
	}

	p := newTestMongoProvider(mock, "decisionbox", "")

	val, err := p.Get(context.Background(), "proj-1", "plain-key")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if val != "plaintext-value" {
		t.Errorf("Get() = %q, want %q", val, "plaintext-value")
	}
}

func TestMongoProvider_Get_NotFound(t *testing.T) {
	mock := &mockSecretCollection{
		findOneFn: func(_ context.Context, _ interface{}, _ ...*options.FindOneOptions) *mongo.SingleResult {
			return mongo.NewSingleResultFromDocument(bson.D{}, mongo.ErrNoDocuments, nil)
		},
	}

	p := newTestMongoProvider(mock, "decisionbox", "")

	_, err := p.Get(context.Background(), "proj-1", "nonexistent")
	if err != secrets.ErrNotFound {
		t.Errorf("Get() error = %v, want ErrNotFound", err)
	}
}

func TestMongoProvider_Get_DecodeError(t *testing.T) {
	mock := &mockSecretCollection{
		findOneFn: func(_ context.Context, _ interface{}, _ ...*options.FindOneOptions) *mongo.SingleResult {
			return mongo.NewSingleResultFromDocument(bson.D{}, fmt.Errorf("decode failure"), nil)
		},
	}

	p := newTestMongoProvider(mock, "decisionbox", "")

	_, err := p.Get(context.Background(), "proj-1", "key")
	if err == nil {
		t.Fatal("Get() should have returned an error")
	}
	if err == secrets.ErrNotFound {
		t.Error("Get() should not return ErrNotFound for decode errors")
	}
}

func TestMongoProvider_Set_Success(t *testing.T) {
	updateCalled := false

	mock := &mockSecretCollection{
		updateOneFn: func(_ context.Context, filter interface{}, update interface{}, opts ...*options.UpdateOptions) (*mongo.UpdateResult, error) {
			updateCalled = true

			// Verify filter
			f, ok := filter.(bson.M)
			if !ok {
				t.Fatalf("filter is not bson.M: %T", filter)
			}
			if f["namespace"] != "decisionbox" {
				t.Errorf("filter namespace = %v, want decisionbox", f["namespace"])
			}
			if f["project_id"] != "proj-1" {
				t.Errorf("filter project_id = %v, want proj-1", f["project_id"])
			}
			if f["key"] != "llm-api-key" {
				t.Errorf("filter key = %v, want llm-api-key", f["key"])
			}

			// Verify update has $set and $setOnInsert
			u, ok := update.(bson.M)
			if !ok {
				t.Fatalf("update is not bson.M: %T", update)
			}
			setFields, ok := u["$set"].(bson.M)
			if !ok {
				t.Fatal("update missing $set")
			}
			if setFields["encrypted"] != false {
				t.Errorf("$set encrypted = %v, want false (no encryption key)", setFields["encrypted"])
			}

			setOnInsert, ok := u["$setOnInsert"].(bson.M)
			if !ok {
				t.Fatal("update missing $setOnInsert")
			}
			if setOnInsert["namespace"] != "decisionbox" {
				t.Errorf("$setOnInsert namespace = %v, want decisionbox", setOnInsert["namespace"])
			}

			// Verify upsert option
			if len(opts) == 0 {
				t.Fatal("missing update options")
			}

			return &mongo.UpdateResult{
				UpsertedCount: 1,
			}, nil
		},
	}

	p := newTestMongoProvider(mock, "decisionbox", "")

	err := p.Set(context.Background(), "proj-1", "llm-api-key", "new-value")
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if !updateCalled {
		t.Error("UpdateOne was not called")
	}
}

func TestMongoProvider_Set_WithEncryption(t *testing.T) {
	encKey := genEncryptionKey()
	var storedValue string
	var storedEncrypted bool

	mock := &mockSecretCollection{
		updateOneFn: func(_ context.Context, _ interface{}, update interface{}, _ ...*options.UpdateOptions) (*mongo.UpdateResult, error) {
			u := update.(bson.M)
			setFields := u["$set"].(bson.M)
			storedValue = setFields["value"].(string)
			storedEncrypted = setFields["encrypted"].(bool)
			return &mongo.UpdateResult{UpsertedCount: 1}, nil
		},
	}

	p := newTestMongoProvider(mock, "decisionbox", encKey)

	err := p.Set(context.Background(), "proj-1", "key", "secret-value")
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	if !storedEncrypted {
		t.Error("stored encrypted flag should be true")
	}
	if storedValue == "secret-value" {
		t.Error("stored value should be encrypted, not plaintext")
	}
	if storedValue == "" {
		t.Error("stored value should not be empty")
	}

	// Verify the encrypted value can be decrypted back
	decrypted, err := p.decrypt(storedValue)
	if err != nil {
		t.Fatalf("decrypt() error = %v", err)
	}
	if decrypted != "secret-value" {
		t.Errorf("decrypt() = %q, want %q", decrypted, "secret-value")
	}
}

func TestMongoProvider_Set_Error(t *testing.T) {
	mock := &mockSecretCollection{
		updateOneFn: func(_ context.Context, _ interface{}, _ interface{}, _ ...*options.UpdateOptions) (*mongo.UpdateResult, error) {
			return nil, fmt.Errorf("write concern error: timeout")
		},
	}

	p := newTestMongoProvider(mock, "decisionbox", "")

	err := p.Set(context.Background(), "proj-1", "key", "value")
	if err == nil {
		t.Fatal("Set() should have returned an error")
	}
}

func TestMongoProvider_List_Success(t *testing.T) {
	now := time.Now()

	mock := &mockSecretCollection{
		findFn: func(_ context.Context, filter interface{}, _ ...*options.FindOptions) (*mongo.Cursor, error) {
			// Verify filter
			f, ok := filter.(bson.M)
			if !ok {
				t.Fatalf("filter is not bson.M: %T", filter)
			}
			if f["namespace"] != "decisionbox" {
				t.Errorf("filter namespace = %v, want decisionbox", f["namespace"])
			}
			if f["project_id"] != "proj-1" {
				t.Errorf("filter project_id = %v, want proj-1", f["project_id"])
			}

			docs := []interface{}{
				secretDoc{
					Namespace: "decisionbox",
					ProjectID: "proj-1",
					Key:       "llm-api-key",
					Value:     "sk-ant-api03-very-secret-key-12345",
					Encrypted: false,
					UpdatedAt: now,
				},
				secretDoc{
					Namespace: "decisionbox",
					ProjectID: "proj-1",
					Key:       "warehouse-creds",
					Value:     "short",
					Encrypted: false,
					UpdatedAt: now,
				},
			}
			return mongo.NewCursorFromDocuments(docs, nil, nil)
		},
	}

	p := newTestMongoProvider(mock, "decisionbox", "")

	entries, err := p.List(context.Background(), "proj-1")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("List() returned %d entries, want 2", len(entries))
	}

	if entries[0].Key != "llm-api-key" {
		t.Errorf("entries[0].Key = %q, want %q", entries[0].Key, "llm-api-key")
	}
	if entries[1].Key != "warehouse-creds" {
		t.Errorf("entries[1].Key = %q, want %q", entries[1].Key, "warehouse-creds")
	}

	// Long value should be masked (not generic ***)
	if entries[0].Masked == "sk-ant-api03-very-secret-key-12345" {
		t.Error("masked value should not be full secret")
	}
	if entries[0].Masked == "***" {
		t.Error("masked value for long secret should not be generic ***")
	}

	// Short value should use generic mask
	if entries[1].Masked != "***" {
		t.Errorf("masked value for short secret should be ***, got %q", entries[1].Masked)
	}
}

func TestMongoProvider_List_WithEncryption(t *testing.T) {
	encKey := genEncryptionKey()
	p := newTestMongoProvider(nil, "decisionbox", encKey)

	// Encrypt values
	encVal, err := p.encrypt("sk-ant-api03-very-secret-key-12345")
	if err != nil {
		t.Fatalf("encrypt() error = %v", err)
	}

	mock := &mockSecretCollection{
		findFn: func(_ context.Context, _ interface{}, _ ...*options.FindOptions) (*mongo.Cursor, error) {
			docs := []interface{}{
				secretDoc{
					Namespace: "decisionbox",
					ProjectID: "proj-1",
					Key:       "llm-api-key",
					Value:     encVal,
					Encrypted: true,
					UpdatedAt: time.Now(),
				},
			}
			return mongo.NewCursorFromDocuments(docs, nil, nil)
		},
	}
	p.col = mock

	entries, err := p.List(context.Background(), "proj-1")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("List() returned %d entries, want 1", len(entries))
	}

	// Verify the value is decrypted and then masked
	if entries[0].Masked == encVal {
		t.Error("masked value should not be the encrypted value")
	}
	if entries[0].Masked == "***" {
		t.Error("long secret should have prefix/suffix mask, not generic ***")
	}
}

func TestMongoProvider_List_Empty(t *testing.T) {
	mock := &mockSecretCollection{
		findFn: func(_ context.Context, _ interface{}, _ ...*options.FindOptions) (*mongo.Cursor, error) {
			return mongo.NewCursorFromDocuments([]interface{}{}, nil, nil)
		},
	}

	p := newTestMongoProvider(mock, "decisionbox", "")

	entries, err := p.List(context.Background(), "proj-1")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("List() returned %d entries, want 0", len(entries))
	}
}

func TestMongoProvider_List_FindError(t *testing.T) {
	mock := &mockSecretCollection{
		findFn: func(_ context.Context, _ interface{}, _ ...*options.FindOptions) (*mongo.Cursor, error) {
			return nil, fmt.Errorf("connection timeout")
		},
	}

	p := newTestMongoProvider(mock, "decisionbox", "")

	_, err := p.List(context.Background(), "proj-1")
	if err == nil {
		t.Fatal("List() should return error when Find fails")
	}
}

func TestMongoProvider_CustomNamespace(t *testing.T) {
	mock := &mockSecretCollection{
		findOneFn: func(_ context.Context, filter interface{}, _ ...*options.FindOneOptions) *mongo.SingleResult {
			f := filter.(bson.M)
			if f["namespace"] != "myapp-prod" {
				t.Errorf("filter namespace = %v, want myapp-prod", f["namespace"])
			}
			doc := secretDoc{
				Namespace: "myapp-prod",
				ProjectID: "proj-1",
				Key:       "key",
				Value:     "value",
				Encrypted: false,
			}
			return mongo.NewSingleResultFromDocument(doc, nil, nil)
		},
	}

	p := newTestMongoProvider(mock, "myapp-prod", "")

	val, err := p.Get(context.Background(), "proj-1", "key")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if val != "value" {
		t.Errorf("Get() = %q, want %q", val, "value")
	}
}
