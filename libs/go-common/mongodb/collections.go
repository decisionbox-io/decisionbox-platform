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

	// CollectionDiscoveryCheckpoints holds one small document per
	// exploration step of an in-flight run, plus one summary document
	// (step_number 0) once exploration ends. It is the durability seam
	// that makes a crashed run resumable: the agent writes a row as each
	// step completes, a resumed run replays the contiguous prefix instead
	// of re-querying it, and the rows are deleted once the run reaches a
	// terminal-and-not-resumable outcome (a TTL is the backstop).
	//
	// One row per step rather than an array on the run document, for the
	// same reason the log collections above were split out: an embedded
	// array grows with run length and a long run hits the 16MB BSON limit
	// exactly when the checkpoint matters most.
	//
	// The agent (writer) owns the shape; the api reads whether a run has
	// a resumable prefix and deletes the rows on cancel.
	CollectionDiscoveryCheckpoints = "discovery_checkpoints"

	// CollectionDiscoveryAnalysisCheckpoints holds one document per
	// COMPLETED analysis area of an in-flight run: the area's whole
	// analysis step, insights and validation results. It is the sibling of
	// CollectionDiscoveryCheckpoints one phase further on — exploration
	// durability lets a resumed run skip Phase 3, and these rows let it
	// skip the analysis areas a previous attempt already finished.
	//
	// A separate collection rather than another `kind` in
	// discovery_checkpoints, because that collection's unique index is
	// (run_id, step_number) and an area has no step number — and because
	// both of its readers scan every row of a run in step order and end
	// the replayable prefix at the first unexpected number. Mixing a
	// second row kind into that scan would put those invariants at risk
	// for nothing; a sibling collection reuses the pattern and leaves
	// them alone.
	//
	// Deliberately NOT discovery_analysis_steps, which is the
	// tail-written audit log keyed by discovery_id and read by the
	// dashboard. These rows are keyed by (run_id, area_id), exist only
	// while a run is in flight, and are deleted once the run reaches a
	// terminal-and-not-resumable outcome (a TTL is the backstop).
	//
	// The agent (writer) owns the shape; the api only purges the rows
	// when a run is cancelled or discarded.
	CollectionDiscoveryAnalysisCheckpoints = "discovery_analysis_checkpoints"

	// CollectionDiscoveryAnalysisDrafts holds one document per analysis area
	// of an in-flight run that has been analysed but has NOT finished its
	// per-insight validation: the area's step as the loop body left it before
	// Phase 4.5, plus one completion marker per insight already validated.
	//
	// It is what makes per-insight resume possible at all. An area's insights
	// are the output of a single LLM call that produces all of them at once,
	// so there is no re-entering at insight 5 of 6 unless insights 1..6 are
	// themselves durable.
	//
	// A SEPARATE collection from CollectionDiscoveryAnalysisCheckpoints rather
	// than a stage field on the same (run_id, area_id) row, and the reason is
	// not tidiness. The two rows are read differently — a completed area is
	// restored and skipped, a draft is restored and FINISHED — and a reader
	// that cannot tell them apart restores a draft as finished, admitting
	// insights with no validation verdict into the result. Any discriminator
	// on the shared row is honoured only by readers that know about it, so an
	// older agent resuming the same run would apply the older meaning. A
	// reader cannot misread a collection it never queries.
	//
	// A draft is superseded by, never merged with, the completed row for the
	// same area: whoever loads both must prefer the completed one. The draft
	// is deleted when its area finishes, with the retention TTL as the
	// backstop, so that preference is what guarantees correctness rather than
	// the delete.
	//
	// The agent (writer) owns the shape; the api only purges the rows when a
	// run is cancelled or discarded.
	CollectionDiscoveryAnalysisDrafts = "discovery_analysis_drafts"

	// CollectionDiscoveryRecommendationCheckpoints holds one document per
	// in-flight run whose Phase 5 recommendation generation finished: the
	// recommendation step as the phase left it, the insight set it was
	// generated over, and one completion marker per recommendation already
	// validated in Phase 5.5.
	//
	// Run-scoped rather than area-scoped, which is why it is neither another
	// row in CollectionDiscoveryAnalysisDrafts (keyed by area) nor in
	// CollectionDiscoveryCheckpoints (whose readers scan a run's rows in step
	// order and end the replayable prefix at the first unexpected number).
	//
	// Restoring it is gated on the run having produced the SAME insight set
	// the recommendations were generated over, because a recommendation cites
	// insights by id and a re-analysed area produces different ones.
	//
	// The agent (writer) owns the shape; the api only purges the rows when a
	// run is cancelled or discarded.
	CollectionDiscoveryRecommendationCheckpoints = "discovery_recommendation_checkpoints"

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
	//   - CollectionDiscoveryLedgerLearnings: one doc per durable learning
	//     about the warehouse / domain, attributed to the run that produced it.
	CollectionDiscoveryLedger          = "discovery_ledger"
	CollectionDiscoveryLedgerFindings  = "discovery_ledger_findings"
	CollectionDiscoveryLedgerTasks     = "discovery_ledger_tasks"
	CollectionDiscoveryLedgerLearnings = "discovery_ledger_learnings"
	CollectionDiscoveryPackProposals   = "discovery_pack_proposals"
)

// ReflectingKey is the field on a discovery_ledger document holding the
// reflections currently in flight for that project — one `{run_id, since}`
// entry per run. The agent's reflection phase pushes and pulls its own entry;
// the one-shot investigation-graph backfill reads it to tell whether anything
// is still rewriting the project's finding documents. Run status cannot answer
// that: a run is stamped `completed` before its reflection phase starts.
//
// Shared here rather than in either module, because two modules agreeing on a
// field name by copying it is how they come to disagree on it.
const ReflectingKey = "reflecting"
