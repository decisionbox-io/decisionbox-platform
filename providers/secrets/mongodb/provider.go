// Package mongodb provides a secrets.Provider backed by MongoDB with AES-256-GCM encryption.
// Default provider for local development and small deployments.
//
// Secrets are stored in the "secrets" collection, encrypted with a key from
// SECRET_ENCRYPTION_KEY env var. If no encryption key is provided, secrets are
// stored in plaintext with a warning.
//
// Configuration:
//
//	SECRET_PROVIDER=mongodb
//	SECRET_ENCRYPTION_KEY=base64-encoded-32-byte-key  (optional but recommended)
//	SECRET_NAMESPACE=decisionbox                      (default)
package mongodb

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func init() {
	secrets.Register("mongodb", func(cfg secrets.Config) (secrets.Provider, error) {
		return nil, fmt.Errorf("mongodb secret provider requires a mongo.Collection — use NewMongoProvider() directly")
	}, secrets.ProviderMeta{
		Name:        "MongoDB Encrypted",
		Description: "AES-256-GCM encrypted secrets in MongoDB — for local dev and small deployments",
	})
}

// secretDoc is the MongoDB document for a stored secret.
type secretDoc struct {
	Namespace   string    `bson:"namespace"`
	ProjectID   string    `bson:"project_id"`
	Key         string    `bson:"key"`
	Value       string    `bson:"value"`       // plaintext or base64(encrypted)
	Encrypted   bool      `bson:"encrypted"`
	CreatedAt   time.Time `bson:"created_at"`
	UpdatedAt   time.Time `bson:"updated_at"`
}

// MongoProvider implements secrets.Provider with AES-256-GCM encryption.
type MongoProvider struct {
	col       secretCollection
	namespace string
	gcm       cipher.AEAD // nil if no encryption key
}

// NewMongoProvider creates a MongoDB secret provider.
// If encryptionKey is empty, secrets are stored in plaintext.
func NewMongoProvider(col *mongo.Collection, namespace, encryptionKey string) (*MongoProvider, error) {
	if namespace == "" {
		namespace = "decisionbox"
	}

	p := &MongoProvider{
		col:       col,
		namespace: namespace,
	}

	if encryptionKey != "" {
		keyBytes, err := base64.StdEncoding.DecodeString(encryptionKey)
		if err != nil {
			return nil, fmt.Errorf("SECRET_ENCRYPTION_KEY must be base64-encoded: %w", err)
		}
		if len(keyBytes) != 32 {
			return nil, fmt.Errorf("SECRET_ENCRYPTION_KEY must be 32 bytes (got %d)", len(keyBytes))
		}
		block, err := aes.NewCipher(keyBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to create AES cipher: %w", err)
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("failed to create GCM: %w", err)
		}
		p.gcm = gcm
	}

	// Create unique index (skip if collection is nil — unit tests)
	if col != nil {
		_, _ = col.Indexes().CreateOne(context.Background(), mongo.IndexModel{
			Keys:    bson.D{{Key: "namespace", Value: 1}, {Key: "project_id", Value: 1}, {Key: "key", Value: 1}},
			Options: options.Index().SetUnique(true),
		})
	}

	return p, nil
}

func (p *MongoProvider) Get(ctx context.Context, projectID, key string) (string, error) {
	filter := bson.M{
		"namespace":  p.namespace,
		"project_id": projectID,
		"key":        key,
	}

	var doc secretDoc
	err := p.col.FindOne(ctx, filter).Decode(&doc)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return "", secrets.ErrNotFound
		}
		return "", fmt.Errorf("get secret: %w", err)
	}

	if doc.Encrypted && p.gcm != nil {
		decrypted, err := p.decrypt(doc.Value)
		if err != nil {
			return "", fmt.Errorf("decrypt secret: %w", err)
		}
		return decrypted, nil
	}

	return doc.Value, nil
}

func (p *MongoProvider) Set(ctx context.Context, projectID, key, value string) error {
	storedValue := value
	encrypted := false

	if p.gcm != nil {
		enc, err := p.encrypt(value)
		if err != nil {
			return fmt.Errorf("encrypt secret: %w", err)
		}
		storedValue = enc
		encrypted = true
	}

	now := time.Now()
	filter := bson.M{
		"namespace":  p.namespace,
		"project_id": projectID,
		"key":        key,
	}
	update := bson.M{
		"$set": bson.M{
			"value":      storedValue,
			"encrypted":  encrypted,
			"updated_at": now,
		},
		"$setOnInsert": bson.M{
			"namespace":  p.namespace,
			"project_id": projectID,
			"key":        key,
			"created_at": now,
		},
	}

	_, err := p.col.UpdateOne(ctx, filter, update, options.Update().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("set secret: %w", err)
	}

	return nil
}

func (p *MongoProvider) List(ctx context.Context, projectID string) ([]secrets.SecretEntry, error) {
	filter := bson.M{
		"namespace":  p.namespace,
		"project_id": projectID,
	}

	cursor, err := p.col.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	defer cursor.Close(ctx)

	entries := make([]secrets.SecretEntry, 0)
	for cursor.Next(ctx) {
		var doc secretDoc
		if err := cursor.Decode(&doc); err != nil {
			continue
		}

		// Decrypt to mask, but never return full value
		plaintext := doc.Value
		if doc.Encrypted && p.gcm != nil {
			if dec, err := p.decrypt(doc.Value); err == nil {
				plaintext = dec
			}
		}

		entries = append(entries, secrets.SecretEntry{
			Key:       doc.Key,
			Masked:    secrets.MaskValue(plaintext),
			UpdatedAt: doc.UpdatedAt,
		})
	}

	return entries, nil
}

// encrypt encrypts plaintext using AES-256-GCM and returns base64-encoded ciphertext.
func (p *MongoProvider) encrypt(plaintext string) (string, error) {
	nonce := make([]byte, p.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := p.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decrypt decodes base64 and decrypts using AES-256-GCM.
func (p *MongoProvider) decrypt(encoded string) (string, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	nonceSize := p.gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := p.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// EnsureIndexes creates the required indexes for the secrets collection.
func (p *MongoProvider) EnsureIndexes(ctx context.Context) error {
	_, err := p.col.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "namespace", Value: 1}, {Key: "project_id", Value: 1}, {Key: "key", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	return err
}
