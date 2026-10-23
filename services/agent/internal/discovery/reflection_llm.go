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
	// CoveredCatalogItems are the cube metrics and dimensions the run queried.
	// A separate field rather than more entries in CoveredTables because the
	// two namespaces are untyped and overlapping — a catalog ref and a table
	// name are both bare strings — so merging them would make it impossible to
	// check either against the catalog it came from.
	CoveredCatalogItems []string `json:"covered_catalog_items"`
	CoveredAreas        []string `json:"covered_areas"`
	ConvergenceNote     string   `json:"convergence_note"`

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
		Reason string `json:"reason"`
		// ResolvedByFindingIDs are the findings that answered the task. Ids
		// come from either id space the prompt prints — a prior ledger finding
		// or one of this run's insights — and are resolved and validated on
		// the apply path.
		ResolvedByFindingIDs []string `json:"resolved_by_finding_ids"`
	} `json:"task_status_updates"`

	NextTasks []struct {
		Title      string `json:"title"`
		Text       string `json:"text"`
		Kind       string `json:"kind"`
		TargetType string `json:"target_type"`
		TargetID   string `json:"target_id"`
		Supersedes string `json:"supersedes"`
		// OriginFindingIDs are the findings the thread grew out of — the edge
		// that attributes the ROOT of a follow-up chain, which `supersedes`
		// cannot, because it only links a task to an earlier task.
		OriginFindingIDs []string `json:"origin_finding_ids"`
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
//
// cons carries what consolidation knows. Its ReSeen set gates the demand for a
// prior-finding re-judgement, and only that: it is the phase's grounded
// evidence about the past, and the finding list read back here cannot supply it,
// because consolidation has already merged this run's own findings into that
// list. Its insight→finding map is what makes this run's findings addressable
// at all — they are listed in the prompt by insight id, since a finding created
// moments ago has no ledger id the model could have seen.
func (o *Orchestrator) generateReflection(ctx context.Context, result *models.DiscoveryResult, pol agentplugin.DiscoveryPolicy, cons consolidationResult) (*parsedReflection, error) {
	prior, err := o.findingRepo.List(ctx, o.projectID)
	if err != nil {
		applog.WithError(err).Warn("Reflection: list prior findings for prompt failed")
		prior = nil
	}
	var tasks []commonmodels.LedgerTask
	if o.taskRepo != nil {
		tasks, _ = o.taskRepo.List(ctx, o.projectID, commonmodels.LedgerTaskStatusOpen)
	}

	demandPriorRejudgement := reSeenPriorIsOnThePage(prior, cons.ReSeen)
	// Whether the prompt carries any finding id at all. A run with no prior
	// findings on the page and no findings of its own has nothing for the model
	// to attribute a thread to, and demanding an attribution anyway leaves it
	// only the option of inventing one.
	// The findings the prompt actually carries — capped, so beyond the cap
	// the model never saw them. Remembered here because this is the moment
	// they are shown: re-reading the active set afterwards reads a set that
	// has moved (another reflection can archive one during the LLM call), and
	// an id this run genuinely showed would then be thrown out as invented.
	onThePage := priorFindingsForPrompt(prior)
	o.promptFindingIDs = make([]string, 0, len(onThePage))
	for i := range onThePage {
		o.promptFindingIDs = append(o.promptFindingIDs, onThePage[i].ID)
	}
	hasFindingIDs := len(onThePage) > 0 || len(result.Insights) > 0

	items := o.runCatalogItems()
	prompt := o.buildReflectionPrompt(result, prior, tasks, pol, items, demandPriorRejudgement, hasFindingIDs)

	window, modelOutputCap := o.resolveModelBudget()
	// Default to the model's own cap, mirroring the analysis and recommendation
	// paths; DISCOVERY_REFLECTION_MAX_OUTPUT stays available as an operator
	// override. The response is bounded by the prompt caps
	// (maxPriorFindingsInPrompt, maxLedgerTasksInPrompt, and
	// maxCatalogNamesInPrompt on each catalog list), so it does not grow
	// without limit — a fixed default was simply below what an ordinary ledger
	// needs, and truncated it mid-JSON (#403).
	outputCap := phaseOutputCap(discoveryReflectionMaxOutputEnv, modelOutputCap, 512, defaultDiscoveryReflectionMaxOutput)
	maxTokens := budgetedMaxOutputTokens(window, approxTokens(ctx, prompt), outputCap, analysisMinOutputTokens())

	format := reflectionResponseFormat(pol.EvolutionMode, demandPriorRejudgement, hasFindingIDs)
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
			attemptPrompt = prompt + reflectionRepairSuffix(lastErr, len(items) > 0)
			applog.WithField("attempt", attempt).Warn("Re-prompting reflection after an unusable response")
		}
		chatResult, cerr := o.aiClient.ChatWithFormat(ctx, attemptPrompt, "", maxTokens, format)
		if cerr != nil {
			return nil, fmt.Errorf("reflection llm call: %w", cerr)
		}
		parsed, perr := parseReflection(chatResult.Content)
		if perr != nil {
			lastErr = perr
			logOutputCapTruncation("Reflection", discoveryReflectionMaxOutputEnv, attempt, maxTokens, chatResult.TokensOut)
			continue
		}
		return parsed, nil
	}
	return nil, fmt.Errorf("reflection response unusable after %d attempt(s): %w", maxRetries+1, lastErr)
}

// buildReflectionPrompt renders the embedded template with the run's findings,
// the prior ledger findings (so the model can re-judge their status), the open
// task queue, and the mode/frontier policy that governs what it may propose.
func (o *Orchestrator) buildReflectionPrompt(result *models.DiscoveryResult, prior []commonmodels.LedgerFinding, tasks []commonmodels.LedgerTask, pol agentplugin.DiscoveryPolicy, catalogItems []string, demandPriorRejudgement, hasFindingIDs bool) string {
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
	p = strings.ReplaceAll(p, "{{PRIOR_STATUS_FIELD}}", renderPriorStatusField(demandPriorRejudgement))
	p = strings.ReplaceAll(p, "{{PROVENANCE_FIELDS}}", renderProvenanceFields(hasFindingIDs))
	p = strings.ReplaceAll(p, "{{OPEN_TASKS}}", renderOpenTasks(tasks))
	p = strings.ReplaceAll(p, "{{CATALOG_SECTION}}", renderCatalogSection(result.Schemas, catalogItems))
	p = strings.ReplaceAll(p, "{{COVERED_FIELDS}}", renderCoveredFields(len(catalogItems) > 0))
	return p
}

// runCatalogItems is every metric and dimension the run's cube-shaped
// datasources offer, flattened across them and sorted.
//
// Flattened because coverage is a project-level record and, to it, a name is a
// name. The per-datasource keying the run carries exists so that one
// datasource cannot vouch for another in a cross-datasource search — a
// different question from "did this run touch it".
func (o *Orchestrator) runCatalogItems() []string {
	if len(o.runCatalogRefs) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, len(o.runCatalogRefs))
	for _, refs := range o.runCatalogRefs {
		for _, ref := range refs {
			ref = strings.TrimSpace(ref)
			if ref == "" {
				continue
			}
			if _, dup := seen[ref]; dup {
				continue
			}
			seen[ref] = struct{}{}
			out = append(out, ref)
		}
	}
	sort.Strings(out)
	return out
}

func evolutionModeGuidance(mode agentplugin.EvolutionMode) string {
	if mode == agentplugin.EvolutionModeOff {
		return "Domain-pack evolution is OFF for this project: return an EMPTY next_tasks array and an EMPTY domain_pack_deltas array. You may still produce coverage, learnings, prior-finding status updates, and task_status_updates that close resolved open tasks."
	}
	return "Propose AT LEAST ONE next_task (a self-directed investigation thread for the next run), grounded in this run's findings and coverage — an empty next_tasks array is not an acceptable answer for this project. You may also propose domain_pack_deltas (analysis-area changes). Ground every proposal in the findings above."
}

// reflectionPriorStatusFieldBase is the prior_status_updates bullet of the
// output contract as the template carried it inline, kept verbatim for the run
// that has no prior findings to re-judge.
const reflectionPriorStatusFieldBase = "- **prior_status_updates**: for PRIOR findings only, by their `id`. Update a status ONLY with grounded evidence from this run — e.g. a new finding contradicts a prior one (`refuted`), or the same finding now shows a different magnitude (`changed`). **Do NOT mark a finding `resolved` just because it did not reappear** — discovery is not exhaustive, so absence is not proof. Leave findings you have no evidence about alone."

// renderPriorStatusField renders that bullet, demanding at least one
// re-judgement on a run that surfaced a carried finding again and whose
// prompt lists it.
//
// Conditional because the demand and the grounding rule above it only coexist
// when there is something to judge: an early run has no prior findings, and a
// model told to produce a verdict anyway has no id to attach it to but the one
// it invents. Where priors do exist the two do not conflict — a prior finding
// this run saw again is evidence, and saying so is what stops the model from
// reading "only with grounded evidence" as permission to skip the field
// entirely, which is what it had been doing (#434).
func renderPriorStatusField(demand bool) string {
	if !demand {
		return reflectionPriorStatusFieldBase
	}
	return reflectionPriorStatusFieldBase +
		" This run surfaced at least one of the prior findings above again, which IS grounded evidence about it — so re-judge **at least one**: still there at the same magnitude is `confirmed`, and a magnitude that moved is `changed`."
}

// renderRunFindings lists what this run found, each with its id.
//
// The id is this run's INSIGHT id, not a ledger finding id: by the time this
// prompt is built, consolidation has already turned each insight into (or
// merged it into) a ledger finding, but the model has never seen those ids and
// could not copy one. The apply path resolves an insight id back to the finding
// it landed on. Without an id here the model has nothing to point at when it
// closes a thread this run answered — which is almost always answered by a
// finding from this run, not a prior one.
func renderRunFindings(insights []models.Insight) string {
	if len(insights) == 0 {
		return "(this run produced no insights)"
	}
	var b strings.Builder
	for i := range insights {
		in := insights[i]
		if id := strings.TrimSpace(in.ID); id != "" {
			fmt.Fprintf(&b, "- id=%s [%s] %q (severity %s, affected %d)", id, in.AnalysisArea, in.Name, in.Severity, in.AffectedCount)
		} else {
			fmt.Fprintf(&b, "- [%s] %q (severity %s, affected %d)", in.AnalysisArea, in.Name, in.Severity, in.AffectedCount)
		}
		if d := truncate(in.Description, 240); d != "" {
			fmt.Fprintf(&b, " — %s", d)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderProvenanceFields renders the output-contract bullets for the two fields
// that attribute a thread to the findings around it.
//
// Conditional for the same reason renderPriorStatusField is: on a run whose
// prompt carries no finding id at all, an instruction to copy one has no
// grounding, and the only way left to comply is to invent an id.
func renderProvenanceFields(hasFindingIDs bool) string {
	if !hasFindingIDs {
		return "- **origin_finding_ids** / **resolved_by_finding_ids**: omit both — this run's prompt lists no finding ids to copy."
	}
	return "- **origin_finding_ids** (on each next_task): the `id=` values of the findings this thread grew out of — from THIS RUN'S FINDINGS or from PRIOR FINDINGS above, copied verbatim. This is what records *why* the thread exists; `supersedes` only links it to an earlier task.\n" +
		"- **resolved_by_finding_ids** (on each task_status_update you mark `done`): the `id=` values of the findings that ANSWERED the task, copied verbatim — usually findings from this run. Omit it on a `dropped`: a dead end has nothing to point at.\n" +
		"- Never invent an id. Copy only ids printed above; anything else is discarded."
}

// priorFindingsForPrompt is the subset of the ledger's findings the prompt
// actually carries: most-recently-seen first so the cap keeps the freshest
// context. The renderer and the re-judgement gate both go through it, because
// a demand to re-judge a finding is only honest about findings on the page.
func priorFindingsForPrompt(prior []commonmodels.LedgerFinding) []commonmodels.LedgerFinding {
	sort.SliceStable(prior, func(i, j int) bool { return prior[i].LastSeen.After(prior[j].LastSeen) })
	if len(prior) > maxPriorFindingsInPrompt {
		return prior[:maxPriorFindingsInPrompt]
	}
	return prior
}

// reSeenPriorIsOnThePage reports whether the prompt shows at least one finding
// this run surfaced again — the whole condition for demanding a re-judgement.
//
// Membership, not a count, because the list is capped and every finding this
// run touched shares the same LastSeen: a run that created enough new findings
// can tie the re-seen one off the end of the page. A model required to answer
// about a finding it cannot see answers about one it can, which is the
// invented verdict this gate exists to prevent. It also covers the empty-list
// case — a failed read renders "(no prior findings)" and shows nothing.
func reSeenPriorIsOnThePage(prior []commonmodels.LedgerFinding, reSeenPriorIDs map[string]struct{}) bool {
	if len(reSeenPriorIDs) == 0 {
		return false
	}
	for _, f := range priorFindingsForPrompt(prior) {
		if _, ok := reSeenPriorIDs[f.ID]; ok {
			return true
		}
	}
	return false
}

func renderPriorFindings(prior []commonmodels.LedgerFinding) string {
	if len(prior) == 0 {
		return "(no prior findings — this is an early run)"
	}
	var b strings.Builder
	for _, f := range priorFindingsForPrompt(prior) {
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

// maxCatalogNamesInPrompt caps each catalog list the reflection prompt carries,
// so a wide warehouse or a large cube cannot crowd out the findings the phase
// exists to consolidate. Shared by both lists so they cannot drift apart.
const maxCatalogNamesInPrompt = 300

// joinCappedNames renders a sorted name list as the prompt carries it: comma
// separated, capped, and honest about having been cut.
func joinCappedNames(names []string) string {
	truncated := false
	if len(names) > maxCatalogNamesInPrompt {
		names = names[:maxCatalogNamesInPrompt]
		truncated = true
	}
	out := strings.Join(names, ", ")
	if truncated {
		out += ", … (catalog truncated)"
	}
	return out
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
	return joinCappedNames(names)
}

// reflectionTableCatalogHeading is the warehouse-catalog heading. It is the
// line the template carried inline before this section could vary, and a run
// that reaches only tables must still render it to the byte.
const reflectionTableCatalogHeading = "## Warehouse catalog (all tables — pick which were covered vs. still frontier)"

// renderCatalogSection renders the catalog the model picks its coverage from.
//
// On a run that reaches only table-shaped datasources this is exactly the two
// lines the template used to carry, unchanged. A run that also reaches a cube
// gets a second catalog after it, because a cube contributes nothing to the
// first one: its queryable surface is metrics and dimensions, and a model asked
// to report coverage by copying table names verbatim has no name to copy for
// work it genuinely did. That is the whole failure — not a cube missing from a
// report, but a run that explored one recording nothing, so the next run
// inherits a world model saying there is nothing left to look at.
//
// The cube section says what a cube is NOT as well as what it is. "No frontier
// to tile" is the load-bearing half: the rest of this prompt is written around
// a frontier that shrinks as it is covered, and a model handed a list of 470
// metrics under that framing will either report them all as covered or treat
// them as a backlog to exhaust. Neither is true of a combinatorial surface.
func renderCatalogSection(schemas map[string]models.TableSchema, catalogItems []string) string {
	var b strings.Builder
	b.WriteString(reflectionTableCatalogHeading)
	b.WriteByte('\n')
	b.WriteString(renderCatalogTables(schemas))
	if len(catalogItems) == 0 {
		return b.String()
	}
	b.WriteString("\n\n## Cube catalog (metrics and dimensions — pick which this run actually queried)\n")
	b.WriteString("Some of this project's datasources are cube-shaped. A cube has NO tables: a query names a metric or a dimension from the list below, verbatim.\n")
	b.WriteString("A cube has no frontier to tile — its slices are combinatorial, so \"all of it\" is not a state a run can reach and coverage of it is not a fraction. Judge it by whether a new slice still yields something genuinely new. Report what this run queried in `covered_catalog_items`, and keep those names OUT of `covered_tables`: they are not tables.\n")
	b.WriteString(joinCappedNames(catalogItems))
	return b.String()
}

// reflectionCoveredTablesField is the covered_tables bullet as the template
// carried it inline, kept verbatim for the table-only render.
const reflectionCoveredTablesField = "- **covered_tables**: the fully-qualified tables (dataset.table) this run actually queried. Copy names verbatim from the catalog. Omit tables you did not touch."

// renderCoveredFields renders the coverage bullets of the output contract.
//
// Both namespaces are bare strings, and a cube's dimensions and metrics share a
// naming style with nothing — there is no shape to a name that tells the two
// apart after the fact. So the prompt names the catalog each field is copied
// from rather than leaving the model to infer it, and the apply path checks
// each list against exactly that catalog.
func renderCoveredFields(hasCube bool) string {
	if !hasCube {
		return reflectionCoveredTablesField
	}
	return "- **covered_tables**: the fully-qualified tables (dataset.table) this run actually queried. Copy names verbatim from the WAREHOUSE catalog above — never a cube metric or dimension. Omit tables you did not touch.\n" +
		"- **covered_catalog_items**: the cube metrics and dimensions this run actually queried. Copy names verbatim from the CUBE catalog above. Omit items you did not touch, and return `[]` if you queried none. This records what has already been sliced; it is not a coverage target."
}

// parseReflection decodes the model's response tolerantly. Accepts a bare object
// or a fenced one; unknown fields are ignored. Missing arrays decode as nil,
// which the apply path treats as "nothing to do". A non-nil error is returned
// only when the response is not a JSON object at all.
// isEmpty reports whether this reflection says nothing: no summary, no note, and every
// list empty.
//
// A zero-value comparison is not the test. A model that spells every field out as an
// empty array produces something semantically empty whose slices are non-nil, so it
// compared unequal to the zero value, passed as an answer, and silenced the real
// reflection behind it. Emptiness is a property of the contents, not of how the JSON
// happened to be written.
func (r parsedReflection) isEmpty() bool {
	return strings.TrimSpace(r.CoverageSummary) == "" &&
		strings.TrimSpace(r.ConvergenceNote) == "" &&
		len(r.CoveredTables) == 0 &&
		len(r.CoveredCatalogItems) == 0 &&
		len(r.CoveredAreas) == 0 &&
		len(r.StatusUpdates) == 0 &&
		len(r.Learnings) == 0 &&
		len(r.TaskStatusUpdates) == 0 &&
		len(r.NextTasks) == 0 &&
		len(r.PackDeltas) == 0
}

func parseReflection(response string) (*parsedReflection, error) {
	cleaned := cleanJSONResponse(response)
	if strings.TrimSpace(cleaned) == "" {
		return nil, fmt.Errorf("empty reflection response")
	}
	// The contract is an object. A bare `null` unmarshals into a non-pointer struct
	// without error, so it arrived as an empty reflection and was accepted instead of
	// retried; cleanJSONResponse leaves it untouched because it holds no brace. An
	// array or a scalar fails the decode anyway -- this makes the requirement explicit
	// rather than incidental.
	if !strings.HasPrefix(strings.TrimSpace(cleaned), "{") {
		return nil, fmt.Errorf("reflection response is not a JSON object")
	}
	vals, ferr := jsonValues(cleaned)
	if len(vals) == 0 {
		if ferr == nil {
			ferr = fmt.Errorf("no JSON value in response")
		}
		return nil, fmt.Errorf("reflection response is not a JSON object: %w", ferr)
	}
	// First value that says anything wins -- see response_values.go. A leading
	// placeholder, whether `{}` or every field spelled out empty, no longer silences the
	// real reflection behind it, and no longer costs a re-prompt either.
	var first *parsedReflection
	var firstErr error
	for i, val := range vals {
		var out parsedReflection
		if err := json.Unmarshal(val, &out); err != nil {
			if i == 0 {
				firstErr = fmt.Errorf("reflection response is not a JSON object: %w", err)
			}
			continue
		}
		if !out.isEmpty() {
			return &out, nil
		}
		if i == 0 {
			first = &out
		}
	}
	if first != nil {
		return first, nil
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, fmt.Errorf("reflection response is not a JSON object")
}

// reflectionRepairSuffix re-states the output contract after an unusable
// response. It names covered_catalog_items only on a run that has a cube
// catalog, so a table-only run is never told to fill a field its prompt never
// defined.
func reflectionRepairSuffix(err error, hasCube bool) string {
	reason := "it could not be parsed as JSON"
	if err != nil {
		reason = err.Error()
	}
	covered := "covered_tables, "
	if hasCube {
		covered = "covered_tables, covered_catalog_items, "
	}
	return "\n\nYour previous response could not be used: " + reason + ".\n" +
		"Respond with ONLY a single JSON object with the fields coverage_summary, " + covered +
		"covered_areas, prior_status_updates, task_status_updates, learnings, next_tasks, domain_pack_deltas, " +
		"convergence_note — no prose and no markdown fences."
}
