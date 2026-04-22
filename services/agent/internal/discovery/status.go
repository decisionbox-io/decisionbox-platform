package discovery

import (
	"context"
	"fmt"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	logger "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// StatusReporter writes live status updates to MongoDB during a discovery run.
// If runID is empty, status reporting is disabled (agent run without API).
type StatusReporter struct {
	repo     *database.RunRepository
	runID    string
	maxSteps int
}

// NewStatusReporter creates a status reporter. Pass empty runID to disable.
func NewStatusReporter(repo *database.RunRepository, runID string, maxSteps int) *StatusReporter {
	if maxSteps <= 0 {
		maxSteps = 100
	}
	return &StatusReporter{repo: repo, runID: runID, maxSteps: maxSteps}
}

func (s *StatusReporter) enabled() bool {
	return s.runID != "" && s.repo != nil
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

// AddStep appends a step to the live log.
func (s *StatusReporter) AddStep(ctx context.Context, step models.RunStep) {
	if !s.enabled() {
		return
	}
	if err := s.repo.AddStep(ctx, s.runID, step); err != nil {
		logger.WithError(err).Warn("failed to add run step")
	}
}

// AddExplorationStep logs an exploration step with LLM thinking and query.
//
// The action argument distinguishes real query steps from non-query events
// emitted by the exploration engine. Today the only non-query event is
// "complete_rejected" — recorded when MinSteps rejects a premature done
// signal — which is written to the run log with Type="complete_rejected",
// carries no Query text, and does NOT increment the run's query counter.
// Any unrecognized action falls through to the legacy "query" behaviour.
func (s *StatusReporter) AddExplorationStep(ctx context.Context, stepNum int, action, thinking, query string, rowCount int, queryTimeMs int64, queryFixed bool, errStr string) {
	if !s.enabled() {
		return
	}

	isRejected := action == "complete_rejected"

	var (
		stepType string
		msg      string
	)
	switch {
	case isRejected:
		stepType = "complete_rejected"
		msg = fmt.Sprintf("Step %d: rejected premature completion (min-steps floor)", stepNum)
	default:
		stepType = "query"
		msg = fmt.Sprintf("Step %d", stepNum)
		if thinking != "" {
			t := thinking
			if len(t) > 200 {
				t = t[:200] + "..."
			}
			msg = fmt.Sprintf("Step %d: %s", stepNum, t)
		}
	}

	resultSummary := ""
	if rowCount > 0 {
		resultSummary = fmt.Sprintf("%d rows returned", rowCount)
	}

	step := models.RunStep{
		Phase:       models.PhaseExploration,
		StepNum:     stepNum,
		Type:        stepType,
		Message:     msg,
		LLMThinking: thinking,
		Query:       query,
		QueryResult: resultSummary,
		RowCount:    rowCount,
		QueryTimeMs: queryTimeMs,
		QueryFixed:  queryFixed,
		Error:       errStr,
	}

	if err := s.repo.AddStep(ctx, s.runID, step); err != nil {
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

	// Only real queries count toward the run's query counter. A
	// complete_rejected step didn't execute any SQL.
	if !isRejected {
		if err := s.repo.IncrementQueryCount(ctx, s.runID, errStr == ""); err != nil {
			logger.WithError(err).Warn("failed to increment query count")
		}
	}
}

// AddAnalysisStep logs an analysis area completion.
func (s *StatusReporter) AddAnalysisStep(ctx context.Context, areaID, areaName string, insightCount int, errStr string) {
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
		Phase:   models.PhaseAnalysis,
		Type:    stepType,
		Message: msg,
		Error:   errStr,
	}

	if err := s.repo.AddStep(ctx, s.runID, step); err != nil {
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

	if err := s.repo.AddStep(ctx, s.runID, step); err != nil {
		logger.WithError(err).Warn("failed to add insight step")
	}
}

// AddValidationStep logs a validation check result.
func (s *StatusReporter) AddValidationStep(ctx context.Context, insightName, status string, claimed, verified int) {
	if !s.enabled() {
		return
	}

	msg := fmt.Sprintf("Validated \"%s\": %s", insightName, status)
	if claimed > 0 {
		msg = fmt.Sprintf("Validated \"%s\": %s (claimed: %d, verified: %d)", insightName, status, claimed, verified)
	}

	step := models.RunStep{
		Phase:   models.PhaseValidation,
		Type:    "validation",
		Message: msg,
	}

	if err := s.repo.AddStep(ctx, s.runID, step); err != nil {
		logger.WithError(err).Warn("failed to add validation step")
	}
}

// Complete marks the run as completed.
func (s *StatusReporter) Complete(ctx context.Context, insightsFound int) {
	if !s.enabled() {
		return
	}
	if err := s.repo.Complete(ctx, s.runID, insightsFound); err != nil {
		logger.WithError(err).Warn("failed to complete run")
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
