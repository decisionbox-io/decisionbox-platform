package telemetry

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const telemetryCollection = "telemetry_settings"

// telemetryDoc is the MongoDB document for persistent telemetry settings.
type telemetryDoc struct {
	ID        string    `bson:"_id"`
	InstallID string    `bson:"install_id"`
	CreatedAt time.Time `bson:"created_at"`
}

// GetOrCreateInstallID retrieves or generates a persistent anonymous install ID.
// The ID is stored in MongoDB so it survives container restarts.
func GetOrCreateInstallID(ctx context.Context, db *mongo.Database) string {
	coll := db.Collection(telemetryCollection)

	// Try to read existing
	var doc telemetryDoc
	err := coll.FindOne(ctx, bson.M{"_id": "install"}).Decode(&doc)
	if err == nil {
		return doc.InstallID
	}

	// Generate new UUID v4
	id := generateUUID()
	doc = telemetryDoc{
		ID:        "install",
		InstallID: id,
		CreatedAt: time.Now().UTC(),
	}

	opts := options.Update().SetUpsert(true)
	_, err = coll.UpdateOne(ctx, bson.M{"_id": "install"}, bson.M{"$setOnInsert": doc}, opts)
	if err != nil {
		// Silently return the generated ID — persistence failed but telemetry can still work
		return id
	}

	// Re-read in case of race (setOnInsert guarantees first writer wins)
	err = coll.FindOne(ctx, bson.M{"_id": "install"}).Decode(&doc)
	if err != nil {
		return id
	}
	return doc.InstallID
}

// generateUUID generates a random UUID v4 without external dependencies.
func generateUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // Variant RFC 4122
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
