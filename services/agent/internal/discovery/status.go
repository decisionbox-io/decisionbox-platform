package discovery

import (
	"context"
	"fmt"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	logger "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

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
	repo        *database.RunRepository
	runStepRepo runStepWriter
	projectID   string
	runID       string
	maxSteps    int
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
func newStatusReporter(repo *database.RunRepository, runStepRepo runStepWriter, projectID, runID string, maxSteps int) *StatusReporter {
	if rs, ok := runStepRepo.(*database.RunStepRepository); ok && rs == nil {
		runStepRepo = nil
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
	if err := s.repo.UpdateStatus(ctx, s.runID, models.RunStatusRunning, phase, detail, progress); err != nil {
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
//                            schema_lookup_calls counter, not the query
//                            counter.
//   - "search_tables"     — on-demand semantic table search; increments
//                            schema_search_calls.
//   - "complete_rejected" — early-done signal rejected by MinSteps;
//                            written with Type="complete_rejected", no
//                            counter bumps, kept in the log so the UI
//                            shows that the model tried to stop.
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
func (s *StatusReporter) AddExplorationStep(ctx context.Context, stepNum int, action, thinking, query string, rowCount int, queryTimeMs int64, queryFixed bool, errStr string, inputTokens, outputTokens int) {
	if !s.enabled() {
		return
	}

	stepType, msg := classifyExplorationStep(action, stepNum, thinking)

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
	if err := s.repo.UpdateStatus(ctx, s.runID, models.RunStatusRunning, models.PhaseExploration, detail, progress); err != nil {
		logger.WithError(err).Warn("failed to update exploration status")
	}

	// Per-action counter bumps — kept in one place so a future action
	// type lands in the right bucket.
	switch action {
	case "query_data":
		if err := s.repo.IncrementQueryCount(ctx, s.runID, errStr == ""); err != nil {
			logger.WithError(err).Warn("failed to increment query count")
		}
	case "lookup_schema", "search_tables":
		if err := s.repo.IncrementSchemaActionCalls(ctx, s.runID, action, 1); err != nil {
			logger.WithError(err).Warn("failed to increment schema-action count")
		}
	}
}

// classifyExplorationStep returns the (stepType, message) pair for an
// exploration step based on the engine action. Pulled out so the
// AddExplorationStep body stays linear and so unit tests can pin the
// classification without spinning up MongoDB.
func classifyExplorationStep(action string, stepNum int, thinking string) (string, string) {
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
		return "complete_rejected", fmt.Sprintf("Step %d: rejected premature completion (min-steps floor)", stepNum)
	case "lookup_schema":
		return "lookup_schema", fmt.Sprintf("Step %d (lookup_schema)%s", stepNum, suffix)
	case "search_tables":
		return "search_tables", fmt.Sprintf("Step %d (search_tables)%s", stepNum, suffix)
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
func (s *StatusReporter) AddRecommendationStep(ctx context.Context, recommendationCount int, errStr string, inputTokens, outputTokens int) {
	if !s.enabled() {
		return
	}

	msg := fmt.Sprintf("Generated %d recommendations", recommendationCount)
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
func (s *StatusReporter) Complete(ctx context.Context, discoveryID string, insightsFound int) {
	if !s.enabled() {
		return
	}
	if err := s.repo.Complete(ctx, s.runID, discoveryID, insightsFound); err != nil {
		logger.WithError(err).Warn("failed to complete run")
	}
}

// RecordSchemaTelemetry stamps the rendered schema-context counters on
// the run doc. No-op when status reporting is disabled (agent run
// without API).
func (s *StatusReporter) RecordSchemaTelemetry(ctx context.Context, tokens, tableCount int) {
	if !s.enabled() {
		return
	}
	if err := s.repo.RecordSchemaContextTelemetry(ctx, s.runID, tokens, tableCount); err != nil {
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
	if err := s.repo.IncrementSchemaActionCalls(ctx, s.runID, action, delta); err != nil {
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
	if err := s.repo.IncrementAnalysisCounter(ctx, s.runID, metric, delta); err != nil {
		logger.WithError(err).Warn("failed to increment analysis counter")
	}
}

// Fail marks the run as failed.
func (s *StatusReporter) Fail(ctx context.Context, errMsg string) {
	if !s.enabled() {
		return
	}
	if err := s.repo.Fail(ctx, s.runID, errMsg); err != nil {
		logger.WithError(err).Warn("failed to mark run as failed")
	}
}
