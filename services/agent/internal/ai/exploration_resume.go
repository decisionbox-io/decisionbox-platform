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
	"errors"
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
	case "complete":
		// The model's own summary. The step struct has no field for it, and
		// a replayed completion that loses it would have to invent one.
		args.CompletionReason = action.Reason
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
	Summary      string           `json:"summary,omitempty"`
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
	case "complete":
		ra.Done = true
		ra.Summary = cp.Args.CompletionReason
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

// replayStep appends one already-executed step's turn to the conversation and
// returns the step as it should appear in result.Steps.
//
// terminal is true for a step that ENDED the original exploration — an
// accepted `complete`. The live loop breaks on it before appending any result
// message, so replay must not append one either, and must not carry on past
// it: the model had already stopped.
func (e *ExplorationEngine) replayStep(ctx context.Context, conversation *Conversation, cp models.ExplorationCheckpoint) (step models.ExplorationStep, terminal bool) {
	conversation.AddAssistantMessage(replayedActionJSON(cp))

	step = cp.Step

	// An action that is about to RUN AGAIN must not carry the previous
	// attempt's error into this attempt's result. The execute* methods only
	// ever assign step.Error on a failure path — on a live run that is
	// enough, because the step starts zero-valued — so a step that failed
	// before and succeeds now would keep an error describing neither
	// execution. It would then be re-checkpointed, indexed and rendered as
	// an errored step, and the novelty rule treats any errored step as
	// unjudgeable, so it would also drop out of the stopping evidence for
	// the rest of the run.
	//
	// query_data is deliberately NOT in that set: it is not re-executed,
	// its rows come back from the checkpoint, so its recorded error is still
	// the true account of what happened.
	if reExecutedOnReplay(step.Action) {
		step.Error = ""
	}

	var resultMsg string
	switch step.Action {
	case "complete":
		// The exploration finished here. The live path breaks out of the
		// loop at this point, so there is no result message to replay — and
		// appending one would put a turn in the transcript the original run
		// never had.
		//
		// Reachable when the process died between this step's checkpoint and
		// the exploration summary's. Without this case the step fell through
		// to "Unknown action: complete" and the run carried on exploring
		// past a completion it had already earned, which is the exact spend
		// resume exists to avoid.
		return step, true
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
	return step, false
}

// reExecutedOnReplay reports whether replaying this action runs it again
// rather than restoring what it returned.
//
// The cheap actions are re-run: they cost no warehouse query and re-running
// them is what restores their per-run budgets and shows the model what the
// schema cache says now. query_data is replayed from its persisted rows.
func reExecutedOnReplay(action string) bool {
	switch action {
	case "lookup_schema", "search_tables", "get_correlations":
		return true
	default:
		return false
	}
}

// replayOutcome is what a replayed prefix tells the loop.
type replayOutcome struct {
	// Steps seed result.Steps.
	Steps []models.ExplorationStep
	// Completed is true when the prefix ends with an accepted `complete`, so
	// there is nothing left to explore and the loop must not run.
	Completed bool
	// CompletionMsg is the model's own summary from that step.
	CompletionMsg string
	// Superseded is true when another attempt took the run over mid-replay,
	// so this process must stop without exploring.
	Superseded bool
}

// replayPrefix rebuilds the conversation for an already-executed prefix and
// returns the steps to seed result.Steps with.
//
// onStep is deliberately NOT called for these steps. It is the live hook: it
// writes a feed row AND moves the progress field AND bumps the run's query /
// schema-action counters, and the attempt that executed these steps already
// did all three. Firing it again would double-count the run's totals.
//
// The feed row itself does have to be re-emitted, because the feed is scoped
// to the run's current attempt and these rows belong to an earlier one — the
// orchestrator does that separately and writes the row only
// (discovery.Orchestrator.replayLiveFeedForResume). That separation is the
// point: rows are per-attempt, the counters are per-run.
//
// The persist hook IS called, so a replayed prefix is re-checkpointed under
// this attempt and a run that dies twice still resumes from the same place.
func (e *ExplorationEngine) replayPrefix(ctx context.Context, conversation *Conversation, resume *ResumeState) replayOutcome {
	out := replayOutcome{}
	if resume.Len() == 0 {
		return out
	}
	steps := make([]models.ExplorationStep, 0, len(resume.Steps))
	for _, cp := range resume.Steps {
		step, terminal := e.replayStep(ctx, conversation, cp)
		steps = append(steps, step)
		if terminal {
			out.Completed = true
			out.CompletionMsg = cp.Args.CompletionReason
		}

		// Checkpoint first, for the reason the live loop checkpoints first:
		// the ownership probe rides on a write this attempt makes anyway,
		// and the re-index below is keyed on run_id alone. A superseded
		// attempt that re-indexed before asking would write into the live
		// attempt's per-run collection. The content it writes is the same
		// content — both attempts replay from the same fenced checkpoint row
		// — so the damage is one redundant upsert rather than a corrupted
		// point, but the asymmetry with the live path is not worth keeping,
		// and the next shared write added here would not be so forgiving.
		if e.checkpoint(ctx, step, cp.Args) {
			out.Superseded = true
			return out
		}

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
		// Index HEALTH is restored above; the novelty OBSERVATIONS
		// deliberately are not, so a resumed run re-establishes its judged
		// steps from scratch.
		//
		// They cannot be restored honestly. A novelty judgement is a
		// neighbour search against the index as it stood at that step, and
		// Nearest excludes only the step itself — not the steps that came
		// after it. When the per-run collection survived the crash it
		// already holds the whole previous prefix, so judging replayed step
		// 3 would score it against steps 4..N and call it a repeat of work
		// that, in the run being replayed, had not happened yet. The
		// stopping rule acting on that is exactly the failure the comment
		// above exists to prevent, with the sign flipped.
		//
		// The cost of resetting is bounded and in the safe direction: the
		// rule can only ever LENGTHEN a run, so a resumed run may spend a
		// few extra steps re-proving it has run out of new ground, and can
		// never end early on evidence it does not have.
	}

	out.Steps = steps
	logger.WithFields(logger.Fields{
		"superseded":        out.Superseded,
		"replayed_steps":    len(steps),
		"resume_at":         len(steps) + 1,
		"max_steps":         e.maxSteps,
		"already_completed": out.Completed,
	}).Info("exploration: replayed checkpointed prefix; resuming")
	return out
}

// ErrAttemptSuperseded means another attempt of this run has taken over, so
// this process must stop.
//
// It is reachable without anything exotic: the API's startup sweep marks
// in-flight runs `failed` after a restart WITHOUT reaping their workloads, so
// an operator resuming such a run leaves the previous agent alive. Everything
// that agent would write is refused, so continuing only spends LLM and
// warehouse money on results that will be discarded — and risks it writing a
// checkpoint for a step the live attempt has not reached yet, which a later
// resume would replay as if it were the live attempt's own work.
//
// A PersistStep hook returns it to stop the exploration loop. Every other
// error from the hook is logged and swallowed.
var ErrAttemptSuperseded = errors.New("another attempt of this run has taken over")

// checkpoint durably records one completed step, and reports whether the run
// may continue.
//
// An ordinary failure is logged and swallowed: the run is working, and losing
// the ability to resume it is strictly better than killing it over a Mongo
// hiccup. ErrAttemptSuperseded is the one exception — it does not mean the
// write failed, it means this process is no longer the run.
func (e *ExplorationEngine) checkpoint(ctx context.Context, step models.ExplorationStep, args models.CheckpointArgs) (superseded bool) {
	if e.persistStep == nil {
		return false
	}
	err := e.persistStep(ctx, step, args)
	if err == nil {
		return false
	}
	if errors.Is(err, ErrAttemptSuperseded) {
		logger.WithFields(logger.Fields{
			"step": step.Step,
		}).Warn("exploration: another attempt of this run has taken over; stopping so this one writes nothing further")
		return true
	}
	logger.WithFields(logger.Fields{
		"step":  step.Step,
		"error": err.Error(),
	}).Warn("exploration: checkpoint write failed; this step will be re-explored if the run is resumed")
	return false
}
