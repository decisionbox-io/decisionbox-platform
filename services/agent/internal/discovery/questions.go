package discovery

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	goconfig "github.com/decisionbox-io/decisionbox/libs/go-common/config"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/libs/go-common/policy"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/google/uuid"
)

//go:embed prompts/questions.md
var questionsPromptTemplate string

// Env knobs for the clarifying-questions phase (Rule 2 — all parametric).
const (
	// discoveryQuestionsEnabledEnv is the deployment-availability gate. Default
	// off: the loop is only useful where the answers can be captured (enterprise
	// + sources), so the enterprise agent Helm overlay turns it on. This is
	// independent of the per-project Settings toggle (default on), which is the
	// user-facing control — both must be on for questions to generate.
	discoveryQuestionsEnabledEnv    = "DISCOVERY_QUESTIONS_ENABLED"
	discoveryQuestionsMaxEnv        = "DISCOVERY_QUESTIONS_MAX"
	discoveryQuestionsMaxOutputEnv  = "DISCOVERY_QUESTIONS_MAX_OUTPUT"
	discoveryQuestionsConfPctEnv    = "DISCOVERY_QUESTIONS_CONFIDENCE_MAX_PCT"
	discoveryQuestionsParseRetryEnv = "DISCOVERY_QUESTIONS_PARSE_MAX_RETRIES"
	discoveryQuestionsTimeoutEnv    = "DISCOVERY_QUESTIONS_TIMEOUT"
)

const (
	defaultDiscoveryQuestionsMax        = 5
	defaultDiscoveryQuestionsMaxOutput  = 2000
	defaultDiscoveryQuestionsConfPct    = 50 // confidence below 50% is treated as "low"
	defaultDiscoveryQuestionsParseRetry = 1
	defaultDiscoveryQuestionsTimeout    = 3 * time.Minute
)

// questionPersister is the write/read surface the questions phase needs. Held as
// an interface so unit tests can inject a fake without MongoDB; the concrete
// writer is *database.DiscoveryQuestionRepository, wired by agentserver.go.
type questionPersister interface {
	Insert(ctx context.Context, questions []commonmodels.DiscoveryQuestion) error
	ListForProject(ctx context.Context, projectID string, statuses ...string) ([]commonmodels.DiscoveryQuestion, error)
}

// questionsContext derives the dedicated write/LLM ctx for the questions phase.
// Like persistContext it uses WithoutCancel so the phase — which runs after the
// discovery is already finalized — gets its own budget independent of the
// (possibly already-expired) compute-phase deadline. This is what keeps question
// generation a separate hop that neither extends nor is starved by the
// discovery's wall-clock budget.
func questionsContext(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := goconfig.GetEnvAsDuration(discoveryQuestionsTimeoutEnv, defaultDiscoveryQuestionsTimeout)
	return context.WithTimeout(context.WithoutCancel(parent), timeout)
}

// uncertaintyItem is one signal the generator is grounded in: a finding the run
// was genuinely unsure about, tied to the target the resulting question links to.
type uncertaintyItem struct {
	TargetType string
	TargetID   string
	Label      string
	Reason     string
	Detail     string
}

// RunPhaseQuestions is the best-effort clarifying-questions hop. It is invoked
// by agentserver AFTER RunDiscovery has returned and the completion event +
// telemetry have already fired, so a slow (or timed-out) generation call can
// neither delay the user-facing completion notification nor consume the
// exploration step budget. Findings come from the persisted result. Any failure
// is logged and swallowed — the run has already succeeded. Gated by the
// deployment flag (Layer A) and the per-project toggle (Layer B); short-circuits
// with no LLM call when nothing was genuinely uncertain.
func (o *Orchestrator) RunPhaseQuestions(ctx context.Context, result *models.DiscoveryResult) {
	if !o.clarifyingQuestionsEnabled {
		return // Layer B: per-project Settings toggle is off.
	}
	if !goconfig.GetEnvAsBool(discoveryQuestionsEnabledEnv, false) {
		return // Layer A: feature not available on this deployment.
	}
	if o.aiClient == nil || o.questionRepo == nil || result == nil {
		return // Missing deps (unit/single-binary builds) — nothing to do.
	}

	insights := result.Insights
	recommendations := result.Recommendations
	confMax := float64(clampInt(goconfig.GetEnvAsInt(discoveryQuestionsConfPctEnv, defaultDiscoveryQuestionsConfPct), 0, 100)) / 100
	// Failed-area signals aren't reconstructed from the result (a weak signal vs.
	// validation verdicts + low confidence); pass nil for the analysis log.
	// Built from in-memory result data (no I/O), so short-circuit a clean run
	// before setting up the timeout context or touching the policy checker.
	items := buildUncertaintyDigest(insights, recommendations, nil, confMax)
	if len(items) == 0 {
		applog.WithField("project_id", o.projectID).Info("Discovery had no uncertainty signals; skipping clarifying-question generation")
		return
	}

	// Everything below (the possibly-slow entitlement check, the Mongo reads, and
	// the LLM call) runs under the dedicated questions timeout so nothing can keep
	// the agent process alive after the run was marked complete.
	qctx, cancel := questionsContext(ctx)
	defer cancel()

	// License entitlement: answering questions writes knowledge-base notes, so
	// generation follows the same `sources_enabled` entitlement the enterprise
	// answer API is gated on — otherwise an entitlement-less enterprise deployment
	// would spend LLM tokens generating questions the API hides. On the enterprise
	// agent the license plugin registers a license-backed checker; on community /
	// self-hosted the Noop checker returns true, so the env flag + per-project
	// toggle remain the effective gates.
	if enabled, _ := policy.GetChecker().FeatureEnabled(qctx, "", policy.FeatureSources); !enabled {
		return
	}

	// Load already-asked questions in every terminal state — pending, answered,
	// AND dismissed — so none is re-generated. Dismissed questions must stay
	// suppressed (the analyst rejected them), and answered ones are resolved.
	existing, err := o.questionRepo.ListForProject(qctx, o.projectID,
		commonmodels.DiscoveryQuestionStatusPending,
		commonmodels.DiscoveryQuestionStatusAnswered,
		commonmodels.DiscoveryQuestionStatusDismissed)
	if err != nil {
		applog.WithError(err).Warn("Failed to load existing discovery questions; proceeding without dedup context")
		existing = nil
	}

	maxN := clampInt(goconfig.GetEnvAsInt(discoveryQuestionsMaxEnv, defaultDiscoveryQuestionsMax), 1, 50)
	validTargets := validTargetSet(insights, recommendations, nil)
	final, err := o.generateQuestions(qctx, items, existing, validTargets, maxN)
	if err != nil {
		applog.WithError(err).Warn("Clarifying-question generation failed; discovery run is unaffected")
		return
	}
	if len(final) == 0 {
		return
	}

	now := time.Now()
	for i := range final {
		final[i].ID = uuid.New().String()
		final[i].ProjectID = o.projectID
		final[i].RunID = o.runID
		final[i].DiscoveryID = result.ID
		final[i].Status = commonmodels.DiscoveryQuestionStatusPending
		final[i].CreatedAt = now
		final[i].UpdatedAt = now
	}

	if err := o.questionRepo.Insert(qctx, final); err != nil {
		applog.WithError(err).Warn("Failed to persist discovery clarifying questions")
		return
	}
	applog.WithFields(applog.Fields{
		"project_id": o.projectID,
		"run_id":     o.runID,
		"count":      len(final),
	}).Info("Generated discovery clarifying questions")
}

// generateQuestions runs the bounded, schema-constrained LLM call (mirrors
// generateRecommendations): budget the output against the model window, attach
// the structured-output format where the provider supports it, and self-heal a
// bounded number of times. Post-processing (grounding / dedup / cap / answer-type
// normalization) runs INSIDE the loop so a response that parses but whose items
// all fail validation triggers the repair prompt — exactly the malformed output
// the retry is meant to fix — instead of silently yielding zero questions. A
// legitimately empty response (`{"questions": []}`) returns zero without retry.
func (o *Orchestrator) generateQuestions(ctx context.Context, items []uncertaintyItem, existing []commonmodels.DiscoveryQuestion, validTargets map[string]bool, maxN int) ([]commonmodels.DiscoveryQuestion, error) {
	prompt := o.buildQuestionsPrompt(items, existing, maxN)
	// Let already-answered notes surface so the model doesn't re-raise resolved
	// ambiguities (the answer is now in the KB).
	prompt = o.injectKnowledgeSources(ctx, prompt, "clarifying questions about "+strings.Join(o.datasets, ", "), knowledgeTopKRecommendations)

	window, modelOutputCap := o.resolveModelBudget()
	outputCap := clampInt(goconfig.GetEnvAsInt(discoveryQuestionsMaxOutputEnv, defaultDiscoveryQuestionsMaxOutput), 256, 32000)
	// Never request more output than the model/operator allows — mirror the
	// analysis/recommendation paths, which budget against resolveModelBudget's
	// output cap. Without this, a high DISCOVERY_QUESTIONS_MAX_OUTPUT (or a model
	// whose real cap is lower) could send a max_tokens the provider 4xx-rejects.
	if modelOutputCap > 0 && outputCap > modelOutputCap {
		outputCap = modelOutputCap
	}
	maxTokens := budgetedMaxOutputTokens(window, approxTokens(ctx, prompt), outputCap, analysisMinOutputTokens())

	format := questionsResponseFormat()
	if o.aiClient.SupportsStructuredOutput() {
		applog.Info("Clarifying-question generation using schema-constrained output")
	}

	maxRetries := goconfig.GetEnvAsInt(discoveryQuestionsParseRetryEnv, defaultDiscoveryQuestionsParseRetry)
	if maxRetries < 0 {
		maxRetries = 0
	}

	existingKeys := normalizedKeySet(existing)
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		attemptPrompt := prompt
		if attempt > 0 {
			attemptPrompt = prompt + questionsRepairSuffix(lastErr)
			applog.WithField("attempt", attempt).Warn("Re-prompting for clarifying questions after an unusable response")
		}
		chatResult, err := o.aiClient.ChatWithFormat(ctx, attemptPrompt, "", maxTokens, format)
		if err != nil {
			return nil, fmt.Errorf("questions llm call: %w", err)
		}
		parsed, rawCount, perr := parseQuestions(chatResult.Content)
		if perr != nil {
			lastErr = perr
			continue
		}
		final := postProcessQuestions(parsed, validTargets, existingKeys, maxN)
		if len(final) > 0 || rawCount == 0 {
			// Usable questions, or the model legitimately returned an empty array
			// — done. rawCount (not len(parsed)) is the "was it empty" signal, so
			// a batch of non-object / malformed items that parseQuestions dropped
			// still triggers the repair below instead of looking legitimately empty.
			return final, nil
		}
		// The model produced items but every one was unparseable / ungrounded /
		// malformed / already-asked. Re-prompt with the specific reason.
		lastErr = fmt.Errorf("all %d generated question item(s) were unusable", rawCount)
	}
	return nil, fmt.Errorf("clarifying questions unusable after %d attempt(s): %w", maxRetries+1, lastErr)
}

// buildQuestionsPrompt renders the embedded template with the run's uncertainty
// digest, the already-asked questions, the cap, and the output language.
func (o *Orchestrator) buildQuestionsPrompt(items []uncertaintyItem, existing []commonmodels.DiscoveryQuestion, maxN int) string {
	lang := o.language
	if strings.TrimSpace(lang) == "" {
		lang = "English"
	}
	p := questionsPromptTemplate
	p = strings.ReplaceAll(p, "{{LANGUAGE}}", lang)
	p = strings.ReplaceAll(p, "{{MAX_QUESTIONS}}", fmt.Sprintf("%d", maxN))
	p = strings.ReplaceAll(p, "{{UNCERTAINTY_DIGEST}}", renderDigest(items))
	p = strings.ReplaceAll(p, "{{EXISTING_QUESTIONS}}", renderExistingQuestions(existing))
	return p
}

// buildUncertaintyDigest collects the run's genuine uncertainty signals: insights
// / recommendations the verifier could not confirm (unverifiable / partial /
// skipped) or that carry low confidence, plus analysis areas that failed. An
// empty result means "nothing to ask about" and the caller skips the LLM call.
func buildUncertaintyDigest(insights []models.Insight, recs []models.Recommendation, analysisLog []models.AnalysisStep, confMax float64) []uncertaintyItem {
	var items []uncertaintyItem
	for i := range insights {
		in := insights[i]
		if reason, ok := uncertaintyReason(in.Validation, in.Confidence, confMax); ok {
			items = append(items, uncertaintyItem{
				TargetType: commonmodels.QuestionTargetInsight,
				TargetID:   in.ID,
				Label:      in.Name,
				Reason:     reason,
				Detail:     truncate(in.Description, 240),
			})
		}
	}
	for i := range recs {
		rec := recs[i]
		if reason, ok := uncertaintyReason(rec.Validation, rec.Confidence, confMax); ok {
			items = append(items, uncertaintyItem{
				TargetType: commonmodels.QuestionTargetRecommendation,
				TargetID:   rec.ID,
				Label:      rec.Title,
				Reason:     reason,
				Detail:     truncate(rec.Description, 240),
			})
		}
	}
	for i := range analysisLog {
		step := analysisLog[i]
		if strings.TrimSpace(step.Error) != "" {
			items = append(items, uncertaintyItem{
				TargetType: commonmodels.QuestionTargetArea,
				TargetID:   step.AreaID,
				Label:      step.AreaName,
				Reason:     "analysis of this area failed, so it may hide findings",
				Detail:     truncate(step.Error, 160),
			})
		}
	}
	return items
}

// uncertaintyReason maps a validation verdict / confidence to a human reason,
// and whether the item counts as genuinely uncertain.
func uncertaintyReason(v *models.InsightValidation, confidence, confMax float64) (string, bool) {
	if v != nil {
		switch v.Combined {
		case valmodels.StatusUnverifiable:
			return "the verifier could not find evidence to confirm this claim", true
		case valmodels.StatusPartial:
			return "the claim was only partially verified", true
		case valmodels.StatusSkippedBudgetCap:
			return "validation was skipped for this item (run budget cap)", true
		}
	}
	// Confidence below the threshold (including 0 — the least-confident, or a
	// finding the model gave no confidence for) is an uncertainty signal per the
	// DISCOVERY_QUESTIONS_CONFIDENCE_MAX_PCT contract. Grounding + the hard cap +
	// the "ask only genuine uncertainty" prompt keep this from over-generating.
	if confidence < confMax {
		return fmt.Sprintf("low confidence (%.0f%%)", confidence*100), true
	}
	return "", false
}

func renderDigest(items []uncertaintyItem) string {
	if len(items) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "- [%s %s] %q — %s.", it.TargetType, it.TargetID, it.Label, it.Reason)
		if it.Detail != "" {
			fmt.Fprintf(&b, " Detail: %s", it.Detail)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// maxExistingQuestionsInPrompt caps how many prior questions are rendered into
// the generation prompt. The FULL set is still used for exact-key dedup (a cheap
// Set), but a long-lived project can accumulate hundreds of terminal questions,
// and rendering all of them would eventually overflow the model window. The list
// is newest-first, so the cap keeps the most recent context.
const maxExistingQuestionsInPrompt = 50

func renderExistingQuestions(existing []commonmodels.DiscoveryQuestion) string {
	if len(existing) == 0 {
		return "(none)"
	}
	if len(existing) > maxExistingQuestionsInPrompt {
		existing = existing[:maxExistingQuestionsInPrompt]
	}
	var b strings.Builder
	for _, q := range existing {
		var status string
		switch q.Status {
		case commonmodels.DiscoveryQuestionStatusAnswered:
			status = "answered"
		case commonmodels.DiscoveryQuestionStatusDismissed:
			status = "dismissed by the analyst"
		default:
			status = "still pending"
		}
		fmt.Fprintf(&b, "- (%s) %s\n", status, q.Question)
	}
	return strings.TrimRight(b.String(), "\n")
}

// validTargetSet returns a lookup used by post-processing to reject a question
// whose linked_target does not resolve to a real thing in this run (grounding
// guard). Insight/recommendation targets must match a run item by id; area
// targets must be a known area; table targets are accepted when non-empty (the
// warehouse catalog is not re-validated here).
func validTargetSet(insights []models.Insight, recs []models.Recommendation, analysisLog []models.AnalysisStep) map[string]bool {
	valid := map[string]bool{}
	for _, in := range insights {
		if in.ID != "" {
			valid[commonmodels.QuestionTargetInsight+":"+in.ID] = true
		}
		if in.AnalysisArea != "" {
			valid[commonmodels.QuestionTargetArea+":"+in.AnalysisArea] = true
		}
	}
	for _, rec := range recs {
		if rec.ID != "" {
			valid[commonmodels.QuestionTargetRecommendation+":"+rec.ID] = true
		}
	}
	for _, step := range analysisLog {
		if step.AreaID != "" {
			valid[commonmodels.QuestionTargetArea+":"+step.AreaID] = true
		}
	}
	return valid
}

func normalizedKeySet(existing []commonmodels.DiscoveryQuestion) map[string]bool {
	set := map[string]bool{}
	for _, q := range existing {
		key := q.NormalizedKey
		if key == "" {
			key = commonmodels.NormalizedQuestionKey(q.Question)
		}
		set[key] = true
	}
	return set
}

// parsedQuestion is the wire shape the model emits (see questions_schema.go).
type parsedQuestion struct {
	Question     string `json:"question"`
	Rationale    string `json:"rationale"`
	LinkedTarget struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	} `json:"linked_target"`
	AnswerType string `json:"answer_type"`
	Options    []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	} `json:"options"`
}

// parseQuestions decodes the model's response tolerantly and per-item, mirroring
// parseRecommendations. Accepts the {"questions": [...]} envelope or a bare
// top-level array; a malformed item is skipped rather than failing the batch.
//
// Returns (kept, rawCount, err): rawCount is the number of items the model
// actually emitted (before per-item drops), so the caller can tell a legitimately
// empty array (rawCount 0 → no retry) from a non-empty array whose items were all
// unparseable (rawCount > 0, kept empty → retry). A non-nil error is returned
// only when the response is not recognizable as questions at all.
func parseQuestions(response string) ([]parsedQuestion, int, error) {
	cleaned := cleanJSONResponse(response)

	var raws []json.RawMessage
	if strings.HasPrefix(strings.TrimSpace(cleaned), "[") {
		if err := json.Unmarshal([]byte(cleaned), &raws); err != nil {
			return nil, 0, err
		}
	} else {
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(cleaned), &envelope); err != nil {
			return nil, 0, err
		}
		var qRaw json.RawMessage
		found := false
		for k, v := range envelope {
			if strings.EqualFold(k, "questions") {
				qRaw, found = v, true
				break
			}
		}
		if !found {
			return nil, 0, fmt.Errorf(`response is missing the "questions" key`)
		}
		if strings.TrimSpace(string(qRaw)) == "null" {
			return nil, 0, fmt.Errorf(`"questions" is null`)
		}
		if err := json.Unmarshal(qRaw, &raws); err != nil {
			return nil, 0, fmt.Errorf(`"questions" is not an array: %w`, err)
		}
	}

	out := make([]parsedQuestion, 0, len(raws))
	for i, raw := range raws {
		var q parsedQuestion
		if err := json.Unmarshal(raw, &q); err != nil {
			applog.WithFields(applog.Fields{"index": i, "reason": err.Error()}).
				Warn("Dropping unparseable clarifying question; keeping the rest of the batch")
			continue
		}
		out = append(out, q)
	}
	return out, len(raws), nil
}

// postProcessQuestions turns raw parsed questions into persistable ones,
// enforcing every guardrail: mandatory rationale, a linked_target that resolves
// to a real run item (grounding), a valid answer type, an always-present
// "Other" escape on choice questions, dedup vs already-asked and within the
// batch, and the hard cap N. IDs / timestamps are stamped by the caller.
func postProcessQuestions(parsed []parsedQuestion, validTargets, existingKeys map[string]bool, maxN int) []commonmodels.DiscoveryQuestion {
	seen := map[string]bool{}
	for k := range existingKeys {
		seen[k] = true
	}
	out := make([]commonmodels.DiscoveryQuestion, 0, len(parsed))
	for _, pq := range parsed {
		q := strings.TrimSpace(pq.Question)
		rationale := strings.TrimSpace(pq.Rationale)
		if q == "" || rationale == "" {
			continue // grounding: both are mandatory
		}
		target := commonmodels.QuestionTarget{
			Type: strings.TrimSpace(pq.LinkedTarget.Type),
			ID:   strings.TrimSpace(pq.LinkedTarget.ID),
		}
		if !commonmodels.ValidQuestionTargetType(target.Type) || target.ID == "" {
			continue
		}
		// Grounding: insight/recommendation/area targets must resolve to a real
		// run item; table targets are accepted as-is (non-empty).
		if target.Type != commonmodels.QuestionTargetTable && !validTargets[target.Type+":"+target.ID] {
			continue
		}
		answerType := strings.TrimSpace(pq.AnswerType)
		if !commonmodels.ValidAnswerType(answerType) {
			continue // malformed shape; the self-heal retry is the net
		}
		options := cleanOptions(pq.Options)
		switch answerType {
		case commonmodels.AnswerTypeSingleChoice, commonmodels.AnswerTypeMultiChoice:
			if len(options) == 0 {
				// A choice with no real options is useless — degrade to free text
				// rather than trap the analyst with an empty picker.
				answerType = commonmodels.AnswerTypeFreeText
				options = nil
			} else {
				options = append(options, commonmodels.QuestionOption{ID: commonmodels.OtherOptionID, Label: "Other / add a note"})
			}
		default:
			options = nil // boolean / free_text carry no options
		}

		key := commonmodels.NormalizedQuestionKey(q)
		if seen[key] {
			continue // dedup vs already-asked and earlier in this batch
		}
		seen[key] = true

		out = append(out, commonmodels.DiscoveryQuestion{
			Question:      q,
			Rationale:     rationale,
			LinkedTarget:  target,
			AnswerType:    answerType,
			Options:       options,
			NormalizedKey: key,
		})
		if len(out) >= maxN {
			break // hard cap
		}
	}
	return out
}

// cleanOptions trims option ids/labels, drops empties, dedups by id, and drops
// any reserved "__other" id the model may have added itself (the server owns it).
func cleanOptions(in []struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}) []commonmodels.QuestionOption {
	seen := map[string]bool{}
	out := make([]commonmodels.QuestionOption, 0, len(in))
	for _, o := range in {
		id := strings.TrimSpace(o.ID)
		label := strings.TrimSpace(o.Label)
		if id == "" || label == "" || id == commonmodels.OtherOptionID {
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, commonmodels.QuestionOption{ID: id, Label: label})
	}
	return out
}

func questionsRepairSuffix(err error) string {
	reason := "it could not be parsed as JSON"
	if err != nil {
		reason = err.Error()
	}
	return "\n\nYour previous response could not be used: " + reason + ".\n" +
		`Respond with ONLY a single JSON object of the form {"questions": [ ... ]} ` +
		"— no prose, no markdown fences, and not a bare top-level array. Each question " +
		"MUST have `question`, `rationale`, a `linked_target` object, and an `answer_type` " +
		"of boolean / single_choice / multi_choice / free_text."
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:max]) + "…"
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
