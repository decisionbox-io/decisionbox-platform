// Package mongodb — collection name constants shared between services.
//
// Both the agent (writer) and the api (reader) need to address the same
// collections. Defining the names in libs/go-common keeps them in lockstep —
// renaming a collection requires changing one constant, not searching for
// stringly-typed matches across two modules.
//
// Service-internal collection names (e.g. control-plane only data, fixture
// scratch tables) MAY still be defined inside the service that owns them.
// What lives here is the **shared surface**: collections both services
// touch.
package mongodb

// Discovery collections.
//
// `discoveries` and `discovery_runs` are the parent documents written by
// the agent and read by the api. Their LLM dialog logs used to be embedded
// arrays on those documents; the 16MB BSON limit forced a split. Each log
// type now lives in its own collection (one row per step / area / result),
// keyed by the parent's _id.
const (
	CollectionDiscoveries                = "discoveries"
	CollectionDiscoveryRuns              = "discovery_runs"
	CollectionDiscoveryExplorationSteps  = "discovery_exploration_steps"
	CollectionDiscoveryAnalysisSteps     = "discovery_analysis_steps"
	CollectionDiscoveryValidationResults = "discovery_validation_results"
	CollectionDiscoveryRecommendationLog = "discovery_recommendation_log"
	CollectionDiscoveryRunSteps          = "discovery_run_steps"

	// CollectionDiscoveryQuestions holds the clarifying questions the agent
	// generates at the end of a run when it was uncertain about something a
	// business analyst could resolve. The agent (writer) inserts rows; the
	// enterprise API (reader) lists them and records answers / dismissals.
	CollectionDiscoveryQuestions = "discovery_questions"

	// Discovery Ledger collections (compounding discovery, enterprise#261).
	// The agent's end-of-run reflection phase writes them; the read path and
	// the enterprise API/RAG read them, so each run builds on the last.
	//
	//   - CollectionDiscoveryLedger:         one doc per project — the coverage
	//     map + convergence history.
	//   - CollectionDiscoveryLedgerFindings: one doc per durable finding, with
	//     substance (metric + SQL + evidence) and a lifecycle status.
	//   - CollectionDiscoveryLedgerTasks:    the open-thread / next-task queue.
	//   - CollectionDiscoveryPackProposals:  proposed domain-pack (analysis-area)
	//     deltas the enterprise approval workflow governs.
	CollectionDiscoveryLedger         = "discovery_ledger"
	CollectionDiscoveryLedgerFindings = "discovery_ledger_findings"
	CollectionDiscoveryLedgerTasks    = "discovery_ledger_tasks"
	CollectionDiscoveryPackProposals  = "discovery_pack_proposals"
)
