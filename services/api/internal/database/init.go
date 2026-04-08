package database

import (
	"context"
	"fmt"
	"time"

	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// All collections and indexes used by the DecisionBox platform.
// The API creates them on startup (idempotent — safe to run every time).
//
// Collection names must match between API and Agent:
//   projects             — project config (API writes, Agent reads)
//   discoveries          — discovery results (Agent writes, API reads)
//   project_context      — learning context (Agent reads/writes)
//   discovery_debug_logs — debug logs (Agent writes, TTL auto-cleanup)
var schema = []struct {
	Name    string
	Indexes []mongo.IndexModel
}{
	{
		Name: "projects",
		Indexes: []mongo.IndexModel{
			{Keys: bson.D{{Key: "created_at", Value: -1}}},
			{Keys: bson.D{{Key: "domain", Value: 1}}},
			{Keys: bson.D{{Key: "status", Value: 1}}},
		},
	},
	{
		Name: "discoveries",
		Indexes: []mongo.IndexModel{
			{Keys: bson.D{{Key: "project_id", Value: 1}, {Key: "discovery_date", Value: -1}}},
			{Keys: bson.D{{Key: "project_id", Value: 1}}},
			{Keys: bson.D{{Key: "created_at", Value: -1}}},
		},
	},
	{
		Name: "project_context",
		Indexes: []mongo.IndexModel{
			{
				Keys:    bson.D{{Key: "project_id", Value: 1}},
				Options: options.Index().SetUnique(true),
			},
			{Keys: bson.D{{Key: "updated_at", Value: -1}}},
		},
	},
	{
		Name: "discovery_runs",
		Indexes: []mongo.IndexModel{
			{Keys: bson.D{{Key: "project_id", Value: 1}, {Key: "started_at", Value: -1}}},
			{Keys: bson.D{{Key: "status", Value: 1}}},
		},
	},
	{
		Name:    "pricing",
		Indexes: []mongo.IndexModel{},
	},
	{
		Name: "feedback",
		Indexes: []mongo.IndexModel{
			{Keys: bson.D{{Key: "discovery_id", Value: 1}}},
			{
				Keys:    bson.D{{Key: "discovery_id", Value: 1}, {Key: "target_type", Value: 1}, {Key: "target_id", Value: 1}},
				Options: options.Index().SetUnique(true),
			},
		},
	},
	{
		Name: "domain_packs",
		Indexes: []mongo.IndexModel{
			{
				Keys:    bson.D{{Key: "slug", Value: 1}},
				Options: options.Index().SetUnique(true),
			},
			{Keys: bson.D{{Key: "is_published", Value: 1}}},
			{Keys: bson.D{{Key: "created_at", Value: -1}}},
		},
	},
	{
		Name: "discovery_debug_logs",
		Indexes: []mongo.IndexModel{
			{Keys: bson.D{{Key: "project_id", Value: 1}, {Key: "timestamp", Value: -1}}},
			{Keys: bson.D{{Key: "discovery_run_id", Value: 1}}},
			{
				Keys:    bson.D{{Key: "timestamp", Value: 1}},
				Options: options.Index().SetExpireAfterSeconds(30 * 24 * 60 * 60), // 30 day TTL
			},
		},
	},
}

// InitDatabase creates all collections and indexes on startup.
// Idempotent — safe to call on every startup.
func InitDatabase(ctx context.Context, db *DB) error {
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	for _, col := range schema {
		if len(col.Indexes) > 0 {
			if _, err := db.Collection(col.Name).Indexes().CreateMany(initCtx, col.Indexes); err != nil {
				return fmt.Errorf("init %s indexes: %w", col.Name, err)
			}
		}
		apilog.WithFields(apilog.Fields{
			"collection": col.Name,
			"indexes":    len(col.Indexes),
		}).Debug("Collection initialized")
	}

	return nil
}
