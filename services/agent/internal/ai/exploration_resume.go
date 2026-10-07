package ai

// Replaying an exploration prefix.
//
// A resumed run re-enters the exploration loop at step N+1, which means the
// conversation has to look exactly as it did when step N finished: the system
// prompt, the initial message, then the alternating (assistant action, user
// result) pairs for steps 1..N. The model then answers with step N+1, the
// same way it would have had the process never died.
//
// The replayed conversation IS the resume signal. No synthetic "you were
// resumed" message is injected — the model sees its own prior actions and the
// last result, which is a better statement of what it already tried than any
// summary we could write, and nothing in the loop needs to know.
//
// What gets re-executed, and why it is split that way:
//
//	query_data        never re-executed — the result message is rebuilt from
//	                  the checkpointed row sample and metadata. This is the
//	                  whole point of the feature: N warehouse queries are
//	                  what a resumed run must not pay for twice.
//	lookup_schema     re-executed. A map lookup plus a Mongo read; no
//	                  warehouse traffic, no LLM call.
//	search_tables     re-executed. One embedding call and one Qdrant query.
//	get_correlations  re-executed. An in-process plugin lookup. Free.
//	complete_rejected the nudge, re-derived from the persisted reason class.
//
// Running the cheap actions through their real code path is what restores
// lookupsUsed / searchesUsed / correlationLookupsUsed / fetchedTables as a
// SIDE EFFECT rather than by seeding counters. So a resumed run cannot be
// handed a fresh schema-lookup budget, the "N of M used, K remaining" lines
// the engine appends come back correct with no special-casing, and there is
// no second place where budget state has to be kept in sync.

import (
	"context"
	"encoding/json"
	"fmt"

	logger "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
)

// checkpointArgsFor captures the arguments of the action the model emitted,
// so the assistant half of the turn can be rebuilt on a later attempt. Only
// the fields belonging to the action's own mode are set.
func checkpointArgsFor(action *ExplorationAction) models.CheckpointArgs {
	if action == nil {
		return models.CheckpointArgs{}
	}
	args := models.CheckpointArgs{Datasource: action.Datasource}
	switch action.Action {
	case "lookup_schema":
		args.LookupSchema = action.LookupSchema
	case "search_tables":
		args.SearchTables = action.SearchTables
		args.SearchTopK = action.SearchTopK
	case "get_correlations":
		if action.GetCorrelations != nil {
			args.CorrelationA = action.GetCorrelations.A
			args.CorrelationB = action.GetCorrelations.B
		}
	}
	return args
}

// replayedAction is the JSON shape of a rebuilt assistant turn.
//
// A deliberately separate struct from ExplorationAction, with omitempty
// everywhere, rather than marshalling the action itself: ExplorationAction
// carries legacy and normalisation fields whose zero values would appear in
// the output, and its shape is driven by what the PARSER has to accept. What
// replay needs is the opposite — the minimal, stable rendering of one turn,
// so the output is deterministic and can be pinned by a golden test.
type replayedAction struct {
	Thinking     string           `json:"thinking,omitempty"`
	Action       string           `json:"action,omitempty"`
	Query        string           `json:"query,omitempty"`
	QueryPurpose string           `json:"query_purpose,omitempty"`
	Datasource   string           `json:"datasource_id,omitempty"`
	LookupSchema []string         `json:"lookup_schema,omitempty"`
	SearchTables string           `json:"search_tables,omitempty"`
	SearchTopK   int              `json:"search_top_k,omitempty"`
	Correlations *CorrelationPair `json:"get_correlations,omitempty"`
	Done         bool             `json:"done,omitempty"`
}

// replayedActionJSON renders the assistant message for one replayed step.
func replayedActionJSON(cp models.ExplorationCheckpoint) string {
	ra := replayedAction{
		Thinking:     cp.Step.Thinking,
		Action:       cp.Step.Action,
		Datasource:   cp.Args.Datasource,
		LookupSchema: cp.Args.LookupSchema,
		SearchTables: cp.Args.SearchTables,
		SearchTopK:   cp.Args.SearchTopK,
	}
	switch cp.Step.Action {
	case "query_data":
		ra.Query = cp.Step.Query
		ra.QueryPurpose = cp.Step.QueryPurpose
	case "get_correlations":
		if cp.Args.CorrelationA != "" || cp.Args.CorrelationB != "" {
			ra.Correlations = &CorrelationPair{A: cp.Args.CorrelationA, B: cp.Args.CorrelationB}
		}
	case "complete_rejected":
		// The model said done; the engine refused. The assistant turn it
		// actually produced was a completion signal, and showing it as
		// anything else would hide from the model that it already tried
		// this and was turned down.
		ra.Action = "complete"
		ra.Done = true
	}
	b, err := json.Marshal(ra)
	if err != nil {
		// replayedAction holds only strings, ints, bools and a small struct,
		// none of which can fail to marshal. Fall back to a minimal valid
		// envelope rather than panicking inside a run.
		logger.WithFields(logger.Fields{
			"step":  cp.Step.Step,
			"error": err.Error(),
		}).Warn("replay: could not marshal the replayed action; using a minimal envelope")
		return fmt.Sprintf(`{"action":%q}`, cp.Step.Action)
	}
	return string(b)
}

// replayedQueryResult rebuilds the user message a replayed query_data step
// produced, WITHOUT touching the warehouse.
//
// It goes through formatQuerySuccess so there is exactly one definition of
// what a query result looks like to the model. The checkpoint keeps the
// step's row sample (≤ the verifier's SampleRows, 50 by default) and the live
// message shows the first 10 rows, so for any result the model actually saw
// rows from, the replayed rows are the same rows. The row TOTAL comes from
// RowCount, which formatQuerySuccess treats as authoritative — so a sampled
// 50 000-row step still says "Showing 10 of 50000 rows" rather than claiming
// the query returned 50.
func (e *ExplorationEngine) replayedQueryResult(step models.ExplorationStep) string {
	if step.Error != "" {
		// Same wording as the live failure path. A replayed failure has to
		// read as a failure: rendering it as a success block would invite
		// the model to reason over rows that do not exist.
		return fmt.Sprintf("Query failed: %s\n\nPlease try a different approach.", step.Error)
	}
	return e.formatQuerySuccess(&queryexec.ExecuteResult{
		Data:            step.QueryResult,
		RowCount:        step.RowCount,
		ExecutionTimeMs: step.ExecutionTimeMs,
		Fixed:           step.Fixed,
		FixAttempts:     step.FixAttempts,
		Quality:         step.Quality,
	})
}

// replayStep appends one already-executed step's turn pair to the
// conversation and returns the step as it should appear in result.Steps.
func (e *ExplorationEngine) replayStep(ctx context.Context, conversation *Conversation, cp models.ExplorationCheckpoint) models.ExplorationStep {
	conversation.AddAssistantMessage(replayedActionJSON(cp))

	step := cp.Step
	var resultMsg string
	switch step.Action {
	case "query_data":
		resultMsg = e.replayedQueryResult(step)

	case "lookup_schema":
		// Re-executed against the schema cache. The replayed turn shows what
		// the cache says NOW, which is also what this run will query
		// against, so an intervening re-index is visible rather than hidden.
		replay := &ExplorationAction{
			Action:       step.Action,
			Thinking:     step.Thinking,
			Datasource:   cp.Args.Datasource,
			LookupSchema: cp.Args.LookupSchema,
		}
		resultMsg = e.executeLookupSchema(ctx, replay, &step)

	case "search_tables":
		replay := &ExplorationAction{
			Action:       step.Action,
			Thinking:     step.Thinking,
			Datasource:   cp.Args.Datasource,
			SearchTables: cp.Args.SearchTables,
			SearchTopK:   cp.Args.SearchTopK,
		}
		resultMsg = e.executeSearchTables(ctx, replay, &step)

	case "get_correlations":
		replay := &ExplorationAction{
			Action:   step.Action,
			Thinking: step.Thinking,
			GetCorrelations: &CorrelationPair{
				A: cp.Args.CorrelationA,
				B: cp.Args.CorrelationB,
			},
		}
		resultMsg = e.executeGetCorrelations(ctx, replay, &step)

	case "complete_rejected":
		// Re-derive the nudge from the reason class rather than storing the
		// text, so a replayed run shows the current wording. The nudge the
		// model reads depends only on the reason and the step number; the
		// counters that appear in the RECORDED reason are already on the
		// persisted step.
		resultMsg, _ = e.rejectionFor(cp.Args.RejectReason, step.Step)

	default:
		resultMsg = fmt.Sprintf("Unknown action: %s", step.Action)
	}
	conversation.AddUserMessage(resultMsg)
	return step
}

// replayPrefix rebuilds the conversation for an already-executed prefix and
// returns the steps to seed result.Steps with.
//
// onStep is deliberately NOT called for these steps. The previous attempt
// already emitted their live run-step rows and already incremented the run
// document's query / schema-action counters; re-emitting would duplicate the
// dashboard's step feed and double-count the run's totals. The persist hook
// IS called, so a replayed prefix is re-checkpointed under this attempt and a
// run that dies twice still resumes from the same place.
func (e *ExplorationEngine) replayPrefix(ctx context.Context, conversation *Conversation, resume *ResumeState) []models.ExplorationStep {
	if resume.Len() == 0 {
		return nil
	}
	steps := make([]models.ExplorationStep, 0, len(resume.Steps))
	for _, cp := range resume.Steps {
		step := e.replayStep(ctx, conversation, cp)
		steps = append(steps, step)

		// Re-indexing a replayed step is exact and idempotent: the embedded
		// text is the step's purpose plus its query, both checkpointed, and
		// the point id is derived from (runID, step). So this is a no-op
		// upsert when the per-run collection survived the crash and a
		// rebuild when it did not — which is what keeps resume working after
		// a hard kill, where no deferred cleanup ran.
		if e.stepIndexer != nil {
			err := e.stepIndexer.Upsert(ctx, step)
			if err != nil {
				logger.WithFields(logger.Fields{
					"step":  step.Step,
					"error": err.Error(),
				}).Warn("replay: re-indexing a replayed step failed; analysis ranking will degrade for it")
			}
			// Counted like a live upsert so the stopping rule's view of the
			// index's health reflects the replay too. Without this a resumed
			// run would treat a dead index as healthy until its first new
			// step, and the novelty rule would act on an answer that meant
			// nothing.
			e.recordIndexOutcome(err == nil)
		}
		e.checkpoint(ctx, step, cp.Args)
	}

	logger.WithFields(logger.Fields{
		"replayed_steps": len(steps),
		"resume_at":      len(steps) + 1,
		"max_steps":      e.maxSteps,
	}).Info("exploration: replayed checkpointed prefix; resuming")
	return steps
}

// checkpoint durably records one completed step. A failure is logged and
// swallowed: the run is working, and losing the ability to resume it is
// strictly better than killing it over a Mongo hiccup.
func (e *ExplorationEngine) checkpoint(ctx context.Context, step models.ExplorationStep, args models.CheckpointArgs) {
	if e.persistStep == nil {
		return
	}
	if err := e.persistStep(ctx, step, args); err != nil {
		logger.WithFields(logger.Fields{
			"step":  step.Step,
			"error": err.Error(),
		}).Warn("exploration: checkpoint write failed; this step will be re-explored if the run is resumed")
	}
}
