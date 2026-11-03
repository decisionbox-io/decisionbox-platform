package models

import "time"

// CheckpointArgs carries the parts of an exploration turn that the
// ExplorationStep itself does not record: the arguments of the action the
// model emitted.
//
// A step records what came back (rows, row count, error, digest), which is
// enough to render the result half of a replayed turn. It does not record
// what was asked — an ExplorationStep has no field for the table refs of a
// lookup_schema, the free-text query of a search_tables, or the datasource
// pair of a get_correlations. Replay has to reconstruct the assistant turn
// the model produced, so those arguments are persisted alongside the step.
//
// Every field is optional: exactly the ones belonging to the step's Action
// are set.
type CheckpointArgs struct {
	// Datasource is the target warehouse id the action named, empty when
	// it resolved to the run's primary (single-warehouse runs and legacy
	// prompts never set it).
	Datasource string `bson:"datasource_id,omitempty" json:"datasource_id,omitempty"`

	// LookupSchema / SearchTables / SearchTopK / CorrelationA /
	// CorrelationB mirror the ExplorationAction fields for the three
	// schema actions replay re-executes.
	LookupSchema []string `bson:"lookup_schema,omitempty" json:"lookup_schema,omitempty"`
	SearchTables string   `bson:"search_tables,omitempty" json:"search_tables,omitempty"`
	SearchTopK   int      `bson:"search_top_k,omitempty" json:"search_top_k,omitempty"`
	CorrelationA string   `bson:"correlation_a,omitempty" json:"correlation_a,omitempty"`
	CorrelationB string   `bson:"correlation_b,omitempty" json:"correlation_b,omitempty"`

	// RejectReason is the reason class behind a complete_rejected step
	// (one of the StopReason values). The nudge text the run showed the
	// model is re-derived from it rather than stored, so a replayed run
	// shows whatever the current rejection wording is.
	RejectReason string `bson:"reject_reason,omitempty" json:"reject_reason,omitempty"`

	// CompletionReason is the summary the model gave when it signalled done
	// on an accepted `complete` step. Kept so a replayed completion carries
	// the model's own words rather than a placeholder — and because the step
	// struct has no field for it.
	CompletionReason string `bson:"completion_reason,omitempty" json:"completion_reason,omitempty"`
}

// ExplorationCheckpoint is one checkpointed exploration step: the step as
// the engine built it (with its rows reduced to a bounded sample — see
// database.CheckpointStepInput) plus the arguments needed to rebuild the
// assistant turn that produced it.
type ExplorationCheckpoint struct {
	Step ExplorationStep
	Args CheckpointArgs
}

// ExplorationCheckpointSummary is the single summary document a run writes
// once exploration ends, whatever the outcome. Its presence is what tells a
// resumed run that exploration is already finished and it may go straight to
// analysis without a single LLM call or warehouse query.
type ExplorationCheckpointSummary struct {
	Completed     bool
	CompletionMsg string
	TotalSteps    int
	Duration      time.Duration

	// Attempt is the attempt that WROTE this summary, which is not
	// necessarily the newest attempt in the checkpoint set.
	//
	// It identifies the exploration a resumed run is about to work over,
	// and that is what the per-area analysis checkpoints are stamped and
	// filtered on: an area's insights cite exploration step numbers, so a
	// row analysed over a DIFFERENT exploration must not be restored. A
	// run that had to re-explore writes its summary under a higher
	// attempt, so the stale rows simply stop matching. See
	// AnalysisAreaCheckpoint.
	Attempt int
}

// AnalysisAreaCheckpoint is one COMPLETED analysis area, as a resumed run
// reads it back: everything the Phase 4 loop body produced for that area,
// so the loop can restore it instead of paying for it again.
//
// Only a completed area is ever checkpointed. An area whose analysis call
// failed is deliberately left unmarked so a resumed run retries it — see
// Orchestrator.checkpointAnalysisArea for why that distinction is what
// makes a run killed by DISCOVERY_MAX_DURATION recoverable at all.
type AnalysisAreaCheckpoint struct {
	// Step is the area's analysis step: its prompt and response, the
	// insights with their validation verdicts and evidence-quality labels
	// already stamped on, the validation results, and the picker
	// telemetry. It is everything the orchestrator appends to its
	// analysisLog and allInsights for that area, which — because the loop
	// body produces nothing else that outlives it — is the whole of what
	// restoring an area means.
	Step AnalysisStep

	// InsightsValidated is how many of this area's insights were actually
	// submitted to the verifier, so a resumed run restores the per-run
	// validation budget rather than handing the areas it still has to run
	// a fresh one.
	//
	// Recorded rather than derived from Step.ValidationResults: the count
	// comes straight from the counter validateInsights threads across
	// areas, so it cannot drift from the cap rule the way a second
	// implementation of that rule would.
	InsightsValidated int
}
