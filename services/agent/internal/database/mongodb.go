package database

import (
	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"go.mongodb.org/mongo-driver/mongo"
)

// Collection names shared between agent and API.
// Both services read/write the same MongoDB database.
//
// Discovery / discovery-run / split-log collection names live in
// libs/go-common/mongodb/collections.go so the api side can reference the
// same constants without restating string literals. Agent-internal
// collections (debug logs, feedback, schema-index progress, schema cache)
// stay here — they're written by the agent only.
const (
	CollectionProjects            = "projects"
	CollectionProjectContext      = "project_context"
	CollectionDebugLogs           = "discovery_debug_logs"
	CollectionFeedback            = "feedback"
	CollectionSchemaIndexProgress = "project_schema_index_progress"
	CollectionSchemaCache         = "project_schema_cache"

	// Re-exports from libs/go-common/mongodb so existing call sites in
	// the agent module (database/discovery_repo.go, run_repo.go,
	// discovery_log_repo.go, run_step_repo.go) keep using the same
	// `database.Collection*` namespace they always have.
	CollectionDiscoveries                = gomongo.CollectionDiscoveries
	CollectionDiscoveryRuns              = gomongo.CollectionDiscoveryRuns
	CollectionDiscoveryExplorationSteps  = gomongo.CollectionDiscoveryExplorationSteps
	CollectionDiscoveryAnalysisSteps     = gomongo.CollectionDiscoveryAnalysisSteps
	CollectionDiscoveryValidationResults = gomongo.CollectionDiscoveryValidationResults
	CollectionDiscoveryRecommendationLog = gomongo.CollectionDiscoveryRecommendationLog
	CollectionDiscoveryRunSteps          = gomongo.CollectionDiscoveryRunSteps
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
