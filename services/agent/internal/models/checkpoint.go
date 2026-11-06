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

// AnalysisAreaDraft is one analysis area that has been analysed but has NOT
// finished its per-insight validation, as a resumed run reads it back.
//
// It exists because per-item validation cannot be resumed without the item.
// An area's insights are the output of a single LLM call that produces all of
// them at once, so "re-enter at insight 5 of 6" is only meaningful if
// insights 1..6 are durable — and the same call is the expensive part of the
// area, so persisting it is what makes the whole area's spend recoverable
// rather than just its tail.
//
// A draft is always superseded by the AnalysisAreaCheckpoint for the same
// area: the completed row is written as the area finishes and describes the
// same work with its validation done, so a loader holding both must prefer
// it. See database.DiscoveryAnalysisDraftRepository.
type AnalysisAreaDraft struct {
	// Step is the area's analysis step as the loop body left it just before
	// Phase 4.5: prompt, response, token counts, picker telemetry, figure and
	// repair counters, and the insights with their figures settled, rendered
	// and repaired.
	//
	// Two things it does NOT carry, because neither has happened yet:
	// ValidationResults (filled by Phase 4.5) and each insight's Quality
	// label (attached after Phase 4.5). A restored draft re-enters the loop
	// ahead of both, so both are produced by the attempt that finishes the
	// area.
	Step AnalysisStep

	// InsightVerdicts is one completion marker per insight this or an earlier
	// attempt already submitted to the verifier, keyed by the insight id as
	// the draft holds it.
	//
	// Only a submitted insight is recorded — never one the per-run cap
	// skipped, which cost nothing — so len(InsightVerdicts) is exactly this
	// draft's slice of the run's validation budget. That is the opposite of
	// AnalysisAreaCheckpoint.InsightsValidated, which has to be recorded
	// because a completed area's ValidationResults DO include the
	// cap-skipped entries and so cannot be counted.
	InsightVerdicts map[string]ValidationResult
}

// RecommendationCheckpoint is an in-flight run's finished Phase 5, as a
// resumed run reads it back: the recommendation step the phase produced, the
// insight set it was generated over, and one completion marker per
// recommendation already validated in Phase 5.5.
//
// Only a run whose Phase 5 actually banked something is checkpointed. A
// generation call that failed, or whose response could not be parsed, carries
// no row — the same rule that keeps a failed analysis area unmarked, and for
// the same reason: marking it would make a resume restore the failure instead
// of retrying it.
type RecommendationCheckpoint struct {
	// Step is the whole RecommendationStep, which already carries the kept
	// recommendations under its own Recommendations field: both
	// applyRecommendationDropStats and applyUnresolvedFigureDropStats resync
	// it to the surviving slice, so there is one list rather than two that
	// can disagree.
	Step RecommendationStep

	// EligibleInsightIDs is the insight set Phase 5 was generated over, in
	// the ids the run's analysis checkpoints hold for those insights.
	//
	// It is the restore gate. A recommendation cites insights by id through
	// RelatedInsightIDs, and a resumed run mints fresh ids for every insight
	// it restores, so a stored recommendation is only usable if every id it
	// cites can be mapped onto an insight this attempt actually has. An
	// attempt that re-analysed an area produces insights no checkpoint names,
	// so the sets stop matching and the recommendations are regenerated —
	// which is correct, because they would otherwise describe findings the
	// run no longer holds.
	EligibleInsightIDs []string

	// AnalysisGeneration identifies the analysis those insights came from,
	// independently of the ids they carry: one entry per contributing area,
	// naming the moment that area's analysis ran.
	//
	// EligibleInsightIDs alone cannot carry that meaning. The ids are the only
	// handle a stored recommendation has on its insights, but a model-supplied
	// id survives re-analysis — so "the same ids are eligible" does not imply
	// "the same findings are eligible", and a recommendation reasoned over the
	// content an area used to hold would be restored over the content that
	// replaced it, keeping the validation verdict the old finding earned.
	//
	// See discovery.analysisGenerationFingerprint.
	AnalysisGeneration []string

	// RecVerdicts is one completion marker per recommendation already
	// validated, keyed by the recommendation id as the checkpoint holds it.
	// Same rule as AnalysisAreaDraft.InsightVerdicts: submitted documents
	// only, so the length is the budget.
	RecVerdicts map[string]ValidationResult
}
