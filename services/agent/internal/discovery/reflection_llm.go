package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	goconfig "github.com/decisionbox-io/decisionbox/libs/go-common/config"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// parsedReflection is the wire shape the reflection LLM emits (see
// reflection_schema.go). Server-assigned fields (ids, timestamps, statuses) are
// deliberately absent — the model produces only judgment content.
type parsedReflection struct {
	CoverageSummary string   `json:"coverage_summary"`
	CoveredTables   []string `json:"covered_tables"`
	CoveredAreas    []string `json:"covered_areas"`
	ConvergenceNote string   `json:"convergence_note"`

	StatusUpdates []struct {
		FindingID string `json:"finding_id"`
		Status    string `json:"status"`
		Reason    string `json:"reason"`
	} `json:"prior_status_updates"`

	Learnings []struct {
		Category  string  `json:"category"`
		Note      string  `json:"note"`
		Relevance float64 `json:"relevance"`
	} `json:"learnings"`

	TaskStatusUpdates []struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"` // done | dropped
	} `json:"task_status_updates"`

	NextTasks []struct {
		Title      string `json:"title"`
		Text       string `json:"text"`
		Kind       string `json:"kind"`
		TargetType string `json:"target_type"`
		TargetID   string `json:"target_id"`
		Supersedes string `json:"supersedes"`
	} `json:"next_tasks"`

	PackDeltas []struct {
		Action    string   `json:"action"`
		AreaID    string   `json:"area_id"`
		AreaName  string   `json:"area_name"`
		Prompt    string   `json:"prompt"`
		Keywords  []string `json:"keywords"`
		Rationale string   `json:"rationale"`
	} `json:"domain_pack_deltas"`
}

// generateReflection runs the bounded, schema-constrained LLM call (mirrors
// generateQuestions / generateRecommendations): budget the output against the
// model window, attach the structured-output format where supported, and
// self-heal a bounded number of times on an unparseable response.
func (o *Orchestrator) generateReflection(ctx context.Context, result *models.DiscoveryResult, pol agentplugin.DiscoveryPolicy) (*parsedReflection, error) {
	prior, err := o.findingRepo.List(ctx, o.projectID)
	if err != nil {
		applog.WithError(err).Warn("Reflection: list prior findings for prompt failed")
		prior = nil
	}
	var tasks []commonmodels.LedgerTask
	if o.taskRepo != nil {
		tasks, _ = o.taskRepo.List(ctx, o.projectID, commonmodels.LedgerTaskStatusOpen)
	}

	prompt := o.buildReflectionPrompt(result, prior, tasks, pol)

	window, modelOutputCap := o.resolveModelBudget()
	outputCap := clampInt(goconfig.GetEnvAsInt(discoveryReflectionMaxOutputEnv, defaultDiscoveryReflectionMaxOutput), 512, 32000)
	if modelOutputCap > 0 && outputCap > modelOutputCap {
		outputCap = modelOutputCap
	}
	maxTokens := budgetedMaxOutputTokens(window, approxTokens(ctx, prompt), outputCap, analysisMinOutputTokens())

	format := reflectionResponseFormat()
	if o.aiClient.SupportsStructuredOutput() {
		applog.Info("Reflection generation using schema-constrained output")
	}

	maxRetries := goconfig.GetEnvAsInt(discoveryReflectionParseRetryEnv, defaultDiscoveryReflectionParseRetry)
	if maxRetries < 0 {
		maxRetries = 0
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		attemptPrompt := prompt
		if attempt > 0 {
			attemptPrompt = prompt + reflectionRepairSuffix(lastErr)
			applog.WithField("attempt", attempt).Warn("Re-prompting reflection after an unusable response")
		}
		chatResult, cerr := o.aiClient.ChatWithFormat(ctx, attemptPrompt, "", maxTokens, format)
		if cerr != nil {
			return nil, fmt.Errorf("reflection llm call: %w", cerr)
		}
		parsed, perr := parseReflection(chatResult.Content)
		if perr != nil {
			lastErr = perr
			continue
		}
		return parsed, nil
	}
	return nil, fmt.Errorf("reflection response unusable after %d attempt(s): %w", maxRetries+1, lastErr)
}

// buildReflectionPrompt renders the embedded template with the run's findings,
// the prior ledger findings (so the model can re-judge their status), the open
// task queue, and the mode/frontier policy that governs what it may propose.
func (o *Orchestrator) buildReflectionPrompt(result *models.DiscoveryResult, prior []commonmodels.LedgerFinding, tasks []commonmodels.LedgerTask, pol agentplugin.DiscoveryPolicy) string {
	lang := o.language
	if strings.TrimSpace(lang) == "" {
		lang = "English"
	}

	p := reflectionPromptTemplate
	p = strings.ReplaceAll(p, "{{LANGUAGE}}", lang)
	p = strings.ReplaceAll(p, "{{DATASETS}}", strings.Join(o.datasets, ", "))
	p = strings.ReplaceAll(p, "{{FRONTIER_POLICY}}", string(pol.FrontierPolicy))
	p = strings.ReplaceAll(p, "{{EVOLUTION_MODE}}", string(pol.EvolutionMode))
	p = strings.ReplaceAll(p, "{{EVOLUTION_GUIDANCE}}", evolutionModeGuidance(pol.EvolutionMode))
	p = strings.ReplaceAll(p, "{{RUN_FINDINGS}}", renderRunFindings(result.Insights))
	p = strings.ReplaceAll(p, "{{PRIOR_FINDINGS}}", renderPriorFindings(prior))
	p = strings.ReplaceAll(p, "{{OPEN_TASKS}}", renderOpenTasks(tasks))
	p = strings.ReplaceAll(p, "{{CATALOG_TABLES}}", renderCatalogTables(result.Schemas))
	return p
}

func evolutionModeGuidance(mode agentplugin.EvolutionMode) string {
	if mode == agentplugin.EvolutionModeOff {
		return "Domain-pack evolution is OFF for this project: return an EMPTY next_tasks array and an EMPTY domain_pack_deltas array. You may still produce coverage, learnings, prior-finding status updates, and task_status_updates that close resolved open tasks."
	}
	return "You may propose next_tasks (self-directed investigation threads for the next run) and domain_pack_deltas (analysis-area changes). Ground every proposal in the findings above."
}

func renderRunFindings(insights []models.Insight) string {
	if len(insights) == 0 {
		return "(this run produced no insights)"
	}
	var b strings.Builder
	for i := range insights {
		in := insights[i]
		fmt.Fprintf(&b, "- [%s] %q (severity %s, affected %d)", in.AnalysisArea, in.Name, in.Severity, in.AffectedCount)
		if d := truncate(in.Description, 240); d != "" {
			fmt.Fprintf(&b, " — %s", d)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderPriorFindings(prior []commonmodels.LedgerFinding) string {
	if len(prior) == 0 {
		return "(no prior findings — this is an early run)"
	}
	// Most-recently-seen first so the cap keeps the freshest context.
	sort.SliceStable(prior, func(i, j int) bool { return prior[i].LastSeen.After(prior[j].LastSeen) })
	if len(prior) > maxPriorFindingsInPrompt {
		prior = prior[:maxPriorFindingsInPrompt]
	}
	var b strings.Builder
	for _, f := range prior {
		fmt.Fprintf(&b, "- id=%s [%s] %q (status %s, seen %d)", f.ID, f.Area, f.Name, f.Status, f.SeenCount)
		if f.KeyMetric != "" {
			fmt.Fprintf(&b, " metric: %s", f.KeyMetric)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderOpenTasks(tasks []commonmodels.LedgerTask) string {
	if len(tasks) == 0 {
		return "(no open tasks)"
	}
	if len(tasks) > maxLedgerTasksInPrompt {
		tasks = tasks[:maxLedgerTasksInPrompt]
	}
	var b strings.Builder
	for _, t := range tasks {
		fmt.Fprintf(&b, "- id=%s (%s) %s\n", t.ID, t.Kind, t.Text)
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderCatalogTables lists the warehouse catalog so the model can report which
// tables it covered and which remain on the frontier. Capped to keep the prompt
// bounded on large warehouses.
func renderCatalogTables(schemas map[string]models.TableSchema) string {
	if len(schemas) == 0 {
		return "(catalog unavailable)"
	}
	names := make([]string, 0, len(schemas))
	for k := range schemas {
		names = append(names, k)
	}
	sort.Strings(names)
	const cap = 300
	truncated := false
	if len(names) > cap {
		names = names[:cap]
		truncated = true
	}
	out := strings.Join(names, ", ")
	if truncated {
		out += ", … (catalog truncated)"
	}
	return out
}

// parseReflection decodes the model's response tolerantly. Accepts a bare object
// or a fenced one; unknown fields are ignored. Missing arrays decode as nil,
// which the apply path treats as "nothing to do". A non-nil error is returned
// only when the response is not a JSON object at all.
func parseReflection(response string) (*parsedReflection, error) {
	cleaned := cleanJSONResponse(response)
	if strings.TrimSpace(cleaned) == "" {
		return nil, fmt.Errorf("empty reflection response")
	}
	var out parsedReflection
	if err := json.Unmarshal([]byte(cleaned), &out); err != nil {
		return nil, fmt.Errorf("reflection response is not a JSON object: %w", err)
	}
	return &out, nil
}

func reflectionRepairSuffix(err error) string {
	reason := "it could not be parsed as JSON"
	if err != nil {
		reason = err.Error()
	}
	return "\n\nYour previous response could not be used: " + reason + ".\n" +
		"Respond with ONLY a single JSON object with the fields coverage_summary, covered_tables, " +
		"covered_areas, prior_status_updates, task_status_updates, learnings, next_tasks, domain_pack_deltas, " +
		"convergence_note — no prose and no markdown fences."
}
