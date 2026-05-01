package database

import (
	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"go.mongodb.org/mongo-driver/mongo"
)

// Collection names shared between agent and API.
// Both services read/write the same MongoDB database.
const (
	CollectionProjects                  = "projects"
	CollectionDiscoveries               = "discoveries"
	CollectionProjectContext            = "project_context"
	CollectionDebugLogs                 = "discovery_debug_logs"
	CollectionFeedback                  = "feedback"
	CollectionSchemaIndexProgress       = "project_schema_index_progress"
	CollectionSchemaCache               = "project_schema_cache"
	// Per-step / per-area / per-result collections — split out of the
	// discoveries doc so a 100+ step run cannot blow past the 16MB BSON
	// document limit. Each row carries discovery_id (the parent
	// DiscoveryResult's _id) so the API can rehydrate the logs on demand.
	CollectionDiscoveryExplorationSteps = "discovery_exploration_steps"
	CollectionDiscoveryAnalysisSteps    = "discovery_analysis_steps"
	CollectionDiscoveryValidationResults = "discovery_validation_results"
	CollectionDiscoveryRecommendationLog = "discovery_recommendation_log"
	// Live status updates for a run (StatusReporter $push target). One
	// doc per RunStep keyed by run_id; replaces the previous embedded
	// `steps` array on discovery_runs which had the same unbounded-growth
	// problem as the discoveries log fields.
	CollectionDiscoveryRunSteps = "discovery_run_steps"
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
