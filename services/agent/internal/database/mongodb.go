package database

import (
	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"go.mongodb.org/mongo-driver/mongo"
)

// Collection names shared between agent and API.
// Both services read/write the same MongoDB database.
const (
	CollectionProjects                 = "projects"
	CollectionDiscoveries              = "discoveries"
	CollectionProjectContext           = "project_context"
	CollectionDebugLogs                = "discovery_debug_logs"
	CollectionFeedback                 = "feedback"
	CollectionSchemaIndexProgress      = "project_schema_index_progress"
)

// DB wraps go-common's MongoDB client.
type DB struct {
	client *gomongo.Client
}

func New(client *gomongo.Client) *DB {
	return &DB{client: client}
}

func (db *DB) Client() *gomongo.Client {
	return db.client
}

func (db *DB) Collection(name string) *mongo.Collection {
	return db.client.Collection(name)
}

func (db *DB) Database() *mongo.Database {
	return db.client.Database()
}
