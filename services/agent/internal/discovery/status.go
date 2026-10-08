package discovery

import (
	"context"
	"fmt"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	logger "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// terminalOutcome is what a terminal run-status write tells the caller about
// ownership, and the one thing it governs is whether this attempt may delete
// a result — its own, or another attempt's.
//
// Three states, because a boolean could not carry that. "Not claimed" has
// several causes with opposite correct responses, and only ONE of them
// licenses deleting this attempt's own output: positive evidence that a
// newer attempt owns the run. A write that errored does not establish that.
// Neither does a write that simply matched nothing — the filter is fenced on
// the attempt AND a non-terminal status, so an already-terminal run owned by
// this very attempt matches nothing too.
//
// Both of those mistakes have been made here, and each one destroyed a
// complete discovery that nobody had superseded. Hence the rule the three
// states encode: never delete your own output without being told who else
// owns the run.
type terminalOutcome int

const (
	// terminalClaimed: the write landed. This attempt owns the run, and may
	// retire the other attempts' results.
	terminalClaimed terminalOutcome = iota

	// terminalSuperseded: the run has moved to a newer attempt, established
	// by a positive ownership read and not merely by the write failing to
	// match. This attempt must delete its OWN output and exit quietly — the
	// run is alive and owned by someone else.
	terminalSuperseded

	// terminalUnknown: this attempt could not be shown to have lost the run,
	// but did not claim it either. Delete nothing, claim nothing, and report
	// the failure honestly; the result stays on disk, reachable by run_id.
	//
	// Two things land here, and they share every consequence: the write
	// errored, so ownership is undetermined; or the write matched nothing
	// while this attempt still owns the run, which means the run document
	// was already terminal — the API's startup sweep marking in-flight runs
	// `failed` without reaping their agents. Guessing supersession in
	// either case is how a Mongo blip or an API restart turns into a
	// destroyed result.
	terminalUnknown
)

// runDocWriter is the slice of *database.RunRepository that StatusReporter
// calls. Held as an interface for the same reason runStepWriter below is: so
// the reporter's behaviour — which is now where the attempt fence lives, and
// so where "this attempt no longer owns the run" is decided — can be
// exercised by a unit test instead of only through a MongoDB container.
type runDocWriter interface {
	UpdateStatus(ctx context.Context, runID string, status, phase, detail string, progress int, attempt int) error
	Complete(ctx context.Context, runID, discoveryID string, insightsFound int, attempt int) (bool, error)
	Fail(ctx context.Context, runID, discoveryID, errMsg string, attempt int) (bool, error)
	OwnsRun(ctx context.Context, runID string, attempt int) (bool, error)
	MarkExplorationCheckpoint(ctx context.Context, runID string, step int, attempt int) (bool, error)
	AddActiveTime(ctx context.Context, runID string, d time.Duration, attempt int) error
	AppendLifecycle(ctx context.Context, runID string, ev models.RunLifecycleEvent, attempt int) error
	IncrementQueryCount(ctx context.Context, runID string, success bool, attempt int) error
	IncrementSchemaActionCalls(ctx context.Context, runID, action string, delta int, attempt int) error
	IncrementAnalysisCounter(ctx context.Context, runID, metric string, delta int, attempt int) error
	RecordSchemaContextTelemetry(ctx context.Context, runID string, tokens, tableCount int, attempt int) error
}

// runStepWriter is the slice of *database.RunStepRepository that
// StatusReporter actually calls. Held as an interface so unit tests can
// inject a fake without bringing up MongoDB.
type runStepWriter interface {
	AddStep(ctx context.Context, runID, projectID string, step models.RunStep) error
}

// StatusReporter writes live status updates to MongoDB during a discovery run.
// If runID is empty, status reporting is disabled (agent run without API).
//
// runStepRepo persists individual step rows into the discovery_run_steps
// collection; repo handles the run-document-level updates (status, phase,
// progress, counters). The split exists because the previous design pushed
// step rows into an embedded `steps` array on the discovery_runs doc, which
// grew unbounded under streaming and ran into the same 16MB BSON limit
// that killed discovery saves.
type StatusReporter struct {
	repo        runDocWriter
	runStepRepo runStepWriter
	projectID   string
	runID       string
	maxSteps    int
	// attempt is which attempt of the run this process is. It fences the
	// terminal status write against a previous attempt's agent that is still
	// alive — see database.attemptFilter. Zero means "unknown", which
	// matches any attempt and is the behaviour every caller had before
	// resume existed.
	attempt int
}

// NewStatusReporter creates a status reporter. Pass empty runID to disable.
// runStepRepo MUST be provided when runID is non-empty — without it the
// status reporter would silently drop every per-step update. projectID is
// stamped on each step doc so per-project filters work without a join.
func NewStatusReporter(repo *database.RunRepository, runStepRepo *database.RunStepRepository, projectID, runID string, maxSteps int) *StatusReporter {
	if maxSteps <= 0 {
		maxSteps = 100
	}
	return newStatusReporter(repo, runStepRepo, projectID, runID, maxSteps)
}

// newStatusReporter is the internal constructor that takes the runStepRepo
// as the interface type so unit tests can wire a fake. It also normalises
// a typed-nil concrete pointer back to an untyped-nil interface so the
// `s.runStepRepo != nil` check in enabled() does not get fooled by Go's
// interface-conversion semantics.
func newStatusReporter(repo runDocWriter, runStepRepo runStepWriter, projectID, runID string, maxSteps int) *StatusReporter {
	if rs, ok := runStepRepo.(*database.RunStepRepository); ok && rs == nil {
		runStepRepo = nil
	}
	// Same typed-nil → untyped-nil normalisation: a nil *RunRepository boxed
	// into the interface would make the enabled() guard false-negative and
	// every write below dereference it.
	if r, ok := repo.(*database.RunRepository); ok && r == nil {
		repo = nil
	}
	return &StatusReporter{
		repo:        repo,
		runStepRepo: runStepRepo,
		projectID:   projectID,
		runID:       runID,
		maxSteps:    maxSteps,
	}
}

func (s *StatusReporter) enabled() bool {
	return s.runID != "" && s.repo != nil && s.runStepRepo != nil
}

// SetPhase updates the current phase and progress.
func (s *StatusReporter) SetPhase(ctx context.Context, phase, detail string, progress int) {
	if !s.enabled() {
		return
	}
	if err := s.repo.UpdateStatus(ctx, s.runID, models.RunStatusRunning, phase, detail, progress, s.attempt); err != nil {
		logger.WithError(err).Warn("failed to update run status")
	}
}

// AddStep appends a step to the live log via the discovery_run_steps
// collection. Each call is one InsertOne — no $push, no embedded array
// growth on the run doc.
func (s *StatusReporter) AddStep(ctx context.Context, step models.RunStep) {
	if !s.enabled() {
		return
	}
	if err := s.runStepRepo.AddStep(ctx, s.runID, s.projectID, step); err != nil {
		logger.WithError(err).Warn("failed to add run step")
	}
}

// AddExplorationStep logs an exploration step with LLM thinking and query.
//
// The action argument distinguishes step types so the live UI and the
// persisted run document can render them differently:
//
//   - "query_data"        — a real SQL query; increments the query counter.
//   - "lookup_schema"     — on-demand schema fetch; increments the
//     schema_lookup_calls counter, not the query
//     counter.
//   - "search_tables"     — on-demand semantic table search; increments
//     schema_search_calls.
//   - "complete_rejected" — early-done signal refused, by the MinSteps
//     floor or by the no-new-signal rule a
//     cube-reaching run uses instead; written with
//     Type="complete_rejected", no counter bumps,
//     kept in the log so the UI shows that the
//     model tried to stop. The engine's reason is
//     rendered as the message, so the log names
//     the rule that actually refused.
//
// Any unrecognised action falls through to the "query" rendering for
// safety, but no counter is bumped.
//
// inputTokens / outputTokens are stamped from the ChatResult of the LLM
// call that produced this step. Every action the engine emits today —
// including "complete_rejected", which records the rejected early-done
// LLM call — comes from at least one LLM round, so non-zero usage is
// the norm. Zero values are stored absent (omitempty), preserving the
// "unknown vs. zero spent" distinction for any future path that
// produces a step without an LLM call.
func (s *StatusReporter) AddExplorationStep(ctx context.Context, stepNum int, action, thinking, query string, rowCount int, queryTimeMs int64, queryFixed bool, errStr string, inputTokens, outputTokens int, warehouseID string) {
	if !s.enabled() {
		return
	}

	stepType, msg := classifyExplorationStep(action, stepNum, thinking, errStr)

	resultSummary := ""
	if rowCount > 0 {
		resultSummary = fmt.Sprintf("%d rows returned", rowCount)
	}

	step := models.RunStep{
		Phase:        models.PhaseExploration,
		StepNum:      stepNum,
		Type:         stepType,
		Message:      msg,
		LLMThinking:  thinking,
		Query:        query,
		QueryResult:  resultSummary,
		RowCount:     rowCount,
		QueryTimeMs:  queryTimeMs,
		QueryFixed:   queryFixed,
		WarehouseID:  warehouseID,
		Error:        errStr,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}

	if err := s.runStepRepo.AddStep(ctx, s.runID, s.projectID, step); err != nil {
		logger.WithError(err).Warn("failed to add exploration step")
	}

	// Update progress: exploration is 10-60% of total
	progress := 10 + (stepNum * 50 / s.maxSteps)
	if progress > 60 {
		progress = 60
	}
	detail := fmt.Sprintf("Step %d/%d: exploring data...", stepNum, s.maxSteps)
	if err := s.repo.UpdateStatus(ctx, s.runID, models.RunStatusRunning, models.PhaseExploration, detail, progress, s.attempt); err != nil {
		logger.WithError(err).Warn("failed to update exploration status")
	}

	// Per-action counter bumps — kept in one place so a future action
	// type lands in the right bucket.
	switch action {
	case "query_data":
		if err := s.repo.IncrementQueryCount(ctx, s.runID, errStr == "", s.attempt); err != nil {
			logger.WithError(err).Warn("failed to increment query count")
		}
	case "lookup_schema", "search_tables", "get_correlations":
		if err := s.repo.IncrementSchemaActionCalls(ctx, s.runID, action, 1, s.attempt); err != nil {
			logger.WithError(err).Warn("failed to increment schema-action count")
		}
	}
}

// classifyExplorationStep returns the (stepType, message) pair for an
// exploration step based on the engine action. Pulled out so the
// AddExplorationStep body stays linear and so unit tests can pin the
// classification without spinning up MongoDB.
// reason, when non-empty, is the engine's own account of why the step reads
// the way it does. A rejected completion carries one, and it is the only thing
// that says WHICH rule refused: a run that can query a cube ignores the
// min-steps floor entirely, so naming the floor there sends an operator to a
// setting that had no part in it.
func classifyExplorationStep(action string, stepNum int, thinking, reason string) (string, string) {
	t := thinking
	if len(t) > 200 {
		t = t[:200] + "..."
	}
	suffix := ""
	if t != "" {
		suffix = ": " + t
	}

	switch action {
	case "complete_rejected":
		if reason != "" {
			return "complete_rejected", fmt.Sprintf("Step %d: %s", stepNum, reason)
		}
		return "complete_rejected", fmt.Sprintf("Step %d: rejected premature completion (min-steps floor)", stepNum)
	case "lookup_schema":
		return "lookup_schema", fmt.Sprintf("Step %d (lookup_schema)%s", stepNum, suffix)
	case "search_tables":
		return "search_tables", fmt.Sprintf("Step %d (search_tables)%s", stepNum, suffix)
	case "get_correlations":
		return "get_correlations", fmt.Sprintf("Step %d (get_correlations)%s", stepNum, suffix)
	default:
		// "query_data" and any unknown action render as a query step;
		// counter bumps are routed by the explicit switch in
		// AddExplorationStep so an unknown action does NOT inflate
		// the query counter.
		return "query", fmt.Sprintf("Step %d%s", stepNum, suffix)
	}
}

// AddAnalysisStep logs an analysis area completion.
//
// inputTokens / outputTokens come from the area's analysis LLM call.
// On error before the LLM call returned, callers pass zeros.
func (s *StatusReporter) AddAnalysisStep(ctx context.Context, areaID, areaName string, insightCount int, errStr string, inputTokens, outputTokens int) {
	if !s.enabled() {
		return
	}

	msg := fmt.Sprintf("Analyzed %s: %d insights found", areaName, insightCount)
	stepType := "analysis"
	if errStr != "" {
		msg = fmt.Sprintf("Analysis of %s failed: %s", areaName, errStr)
		stepType = "error"
	}

	step := models.RunStep{
		Phase:        models.PhaseAnalysis,
		Type:         stepType,
		Message:      msg,
		Error:        errStr,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}

	if err := s.runStepRepo.AddStep(ctx, s.runID, s.projectID, step); err != nil {
		logger.WithError(err).Warn("failed to add analysis step")
	}
}

// AddInsightStep logs a discovered insight.
func (s *StatusReporter) AddInsightStep(ctx context.Context, name, severity, area string) {
	if !s.enabled() {
		return
	}

	step := models.RunStep{
		Phase:           models.PhaseAnalysis,
		Type:            "insight",
		Message:         fmt.Sprintf("Found: %s (%s)", name, severity),
		InsightName:     name,
		InsightSeverity: severity,
	}

	if err := s.runStepRepo.AddStep(ctx, s.runID, s.projectID, step); err != nil {
		logger.WithError(err).Warn("failed to add insight step")
	}
}

// AddRecommendationStep logs the recommendation-phase LLM call as a single
// RunStep row, so the live UI carries its per-step token usage alongside
// exploration and analysis steps. When errStr is non-empty the row is
// written with Type="error" so the dashboard renders the failure.
//
// droppedCount is the number of recommendations the orchestrator parsed
// from the LLM response but discarded because their related_insight_ids
// could not be resolved to an eligible insight. When non-zero, it is
// appended to the message so users see "Generated N recommendations
// (M dropped due to invalid related_insight_ids)" — without that hint
// a discovery where the model emits slug-style ids instead of UUIDs
// silently shows fewer recs than expected.
func (s *StatusReporter) AddRecommendationStep(ctx context.Context, recommendationCount, droppedCount int, errStr string, inputTokens, outputTokens int) {
	if !s.enabled() {
		return
	}

	msg := fmt.Sprintf("Generated %d recommendations", recommendationCount)
	if droppedCount > 0 {
		msg = fmt.Sprintf("%s (%d dropped due to invalid related_insight_ids)", msg, droppedCount)
	}
	stepType := "recommendation"
	if errStr != "" {
		msg = fmt.Sprintf("Recommendation generation failed: %s", errStr)
		stepType = "error"
	}

	step := models.RunStep{
		Phase:        models.PhaseRecommendations,
		Type:         stepType,
		Message:      msg,
		Error:        errStr,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}

	if err := s.runStepRepo.AddStep(ctx, s.runID, s.projectID, step); err != nil {
		logger.WithError(err).Warn("failed to add recommendation step")
	}
}

// AddValidationStep logs a validation check result.
//
// inputTokens / outputTokens are the accumulated totals across every LLM
// call the verifier made for this single insight — up to three calls per
// insight today (initial verification, lookup-loop rounds, forced final
// round) collapse onto one validation RunStep.
func (s *StatusReporter) AddValidationStep(ctx context.Context, insightName, status string, claimed, verified, inputTokens, outputTokens int) {
	if !s.enabled() {
		return
	}

	msg := fmt.Sprintf("Validated \"%s\": %s", insightName, status)
	if claimed > 0 {
		msg = fmt.Sprintf("Validated \"%s\": %s (claimed: %d, verified: %d)", insightName, status, claimed, verified)
	}

	step := models.RunStep{
		Phase:        models.PhaseValidation,
		Type:         "validation",
		Message:      msg,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}

	if err := s.runStepRepo.AddStep(ctx, s.runID, s.projectID, step); err != nil {
		logger.WithError(err).Warn("failed to add validation step")
	}
}

// Complete marks the run as completed and stamps the discovery_id
// the run produced. discoveryID must be the `_id` of the
// `discoveries` document the orchestrator just saved — see
// RunRepository.Complete for why the back-reference matters.
// Reports what the write established about ownership. See terminalOutcome —
// an error and an unmatched attempt are NOT the same answer.
func (s *StatusReporter) Complete(ctx context.Context, discoveryID string, insightsFound int) terminalOutcome {
	if !s.enabled() {
		// Nothing to claim and nothing to protect: a run without status
		// reporting has no run document and no competing attempt.
		return terminalClaimed
	}
	applied, err := s.repo.Complete(ctx, s.runID, discoveryID, insightsFound, s.attempt)
	if err != nil {
		logger.WithError(err).Warn("failed to complete run; ownership undetermined, so nothing will be cleaned up")
		return terminalUnknown
	}
	if !applied {
		return s.classifyUnappliedTerminal(ctx, "completion")
	}
	return terminalClaimed
}

// classifyUnappliedTerminal decides what a terminal write that matched
// nothing actually proved.
//
// It is NOT proof of supersession on its own, and treating it as such
// destroys data. The write is fenced on two things — this attempt AND a
// non-terminal status — so it also matches nothing when this attempt still
// owns a run that has already been marked terminal by somebody else. The
// API's startup sweep does exactly that, routinely: it marks in-flight runs
// `failed` after a restart WITHOUT reaping their agents, so a perfectly
// healthy agent finishes, saves its discovery, finds its own terminal write
// unmatched, and — before this — concluded it had been superseded and
// deleted the result it had just written. Nobody had resumed anything.
//
// So supersession now needs positive evidence: the run must no longer be on
// this attempt. One indexed read, once per run, and only when the write did
// not land. The ownership read deliberately does not filter on status, which
// is what lets it tell the two cases apart.
func (s *StatusReporter) classifyUnappliedTerminal(ctx context.Context, what string) terminalOutcome {
	owns, err := s.repo.OwnsRun(ctx, s.runID, s.attempt)
	if err != nil {
		logger.WithError(err).WithField("run_id", s.runID).Warn("could not establish whether this attempt still owns the run; nothing will be cleaned up")
		return terminalUnknown
	}
	if owns {
		// Still ours, so there is no newer attempt to defer to and nothing
		// to retire. The run document is already terminal — our own outcome
		// is the one that did not get recorded, which is worth saying
		// loudly, but the result stays on disk reachable by run_id.
		logger.WithFields(logger.Fields{
			"run_id": s.runID, "attempt": s.attempt,
		}).Warn("the run was already terminal when this attempt tried to record its " + what + "; the outcome was not recorded, but this attempt still owns the run so its result is kept")
		return terminalUnknown
	}
	logger.WithFields(logger.Fields{
		"run_id": s.runID, "attempt": s.attempt,
	}).Warn("this attempt no longer owns the run; its " + what + " was not recorded")
	return terminalSuperseded
}

// MarkExplorationCheckpoint records that this run now has a checkpoint for
// the given exploration step — what the dashboard reads to offer Resume on a
// failed run.
// Returns whether this attempt still owns the run. The write is
// attempt-fenced, so its applied-ness answers that for free — no extra read on
// a path that runs once per step.
func (s *StatusReporter) MarkExplorationCheckpoint(ctx context.Context, step int) bool {
	if !s.enabled() {
		// No run document, so no competing attempt to lose to.
		return true
	}
	applied, err := s.repo.MarkExplorationCheckpoint(ctx, s.runID, step, s.attempt)
	if err != nil {
		// A transient failure is not evidence of being superseded. Say we
		// still own the run so the step is checkpointed anyway — losing the
		// marker costs the dashboard's Resume affordance, not the run.
		logger.WithError(err).Warn("failed to stamp the exploration checkpoint marker; the dashboard may not offer Resume for this run")
		return true
	}
	return applied
}

// OwnsRun reports whether this attempt still owns the run.
func (s *StatusReporter) OwnsRun(ctx context.Context) bool {
	if !s.enabled() {
		return true
	}
	owns, err := s.repo.OwnsRun(ctx, s.runID, s.attempt)
	if err != nil {
		// Same reasoning as above: a failed read is not evidence of being
		// superseded.
		logger.WithError(err).Warn("could not confirm this attempt still owns the run")
		return true
	}
	return owns
}

// AddActiveTime adds one attempt's elapsed compute time to the run's
// cumulative total, so elapsed time still means something after a resume.
func (s *StatusReporter) AddActiveTime(ctx context.Context, d time.Duration) {
	if !s.enabled() {
		return
	}
	if err := s.repo.AddActiveTime(ctx, s.runID, d, s.attempt); err != nil {
		logger.WithError(err).Warn("failed to add this attempt's active time to the run")
	}
}

// AppendLifecycle records one transition on the run's append-only lifecycle
// log. See models.RunLifecycleEvent.
func (s *StatusReporter) AppendLifecycle(ctx context.Context, ev models.RunLifecycleEvent) {
	if !s.enabled() {
		return
	}
	if err := s.repo.AppendLifecycle(ctx, s.runID, ev, s.attempt); err != nil {
		logger.WithError(err).Warn("failed to append a run lifecycle event")
	}
}

// RecordSchemaTelemetry stamps the rendered schema-context counters on
// the run doc. No-op when status reporting is disabled (agent run
// without API).
func (s *StatusReporter) RecordSchemaTelemetry(ctx context.Context, tokens, tableCount int) {
	if !s.enabled() {
		return
	}
	if err := s.repo.RecordSchemaContextTelemetry(ctx, s.runID, tokens, tableCount, s.attempt); err != nil {
		logger.WithError(err).Warn("failed to record schema-context telemetry")
	}
}

// IncrementSchemaActionCalls bumps the per-action counter on the run
// doc when the engine serves a lookup_schema or search_tables turn.
// action must be one of "lookup_schema" or "search_tables"; other
// values no-op so callers can pass action.Action verbatim.
func (s *StatusReporter) IncrementSchemaActionCalls(ctx context.Context, action string, delta int) {
	if !s.enabled() {
		return
	}
	if err := s.repo.IncrementSchemaActionCalls(ctx, s.runID, action, delta, s.attempt); err != nil {
		logger.WithError(err).Warn("failed to increment schema-action calls")
	}
}

// IncrementAnalysisCounter bumps one of the analysis-phase
// compaction counters on the run doc. metric is one of
// "step_index_upserts", "step_index_search_calls",
// "steps_dropped"; other values no-op.
func (s *StatusReporter) IncrementAnalysisCounter(ctx context.Context, metric string, delta int) {
	if !s.enabled() {
		return
	}
	if err := s.repo.IncrementAnalysisCounter(ctx, s.runID, metric, delta, s.attempt); err != nil {
		logger.WithError(err).Warn("failed to increment analysis counter")
	}
}

// Fail marks the run as failed. discoveryID is the _id of the partial
// DiscoveryResult that was persisted before the failure, or empty
// when the failure produced no persisted discovery. When non-empty
// the underlying repo stamps it on the run doc so plugin-hooks Hook
// 5 and the discovery-log APIs can navigate to the partial result
// the same way they would for a completed run.
// Reports what the write established about ownership — see Complete.
func (s *StatusReporter) Fail(ctx context.Context, discoveryID, errMsg string) terminalOutcome {
	if !s.enabled() {
		return terminalClaimed
	}
	applied, err := s.repo.Fail(ctx, s.runID, discoveryID, errMsg, s.attempt)
	if err != nil {
		logger.WithError(err).Warn("failed to mark run as failed; ownership undetermined, so nothing will be cleaned up")
		return terminalUnknown
	}
	if !applied {
		return s.classifyUnappliedTerminal(ctx, "failure")
	}
	return terminalClaimed
}
