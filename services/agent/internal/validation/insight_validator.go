package validation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation/render"
)

// InsightValidator verifies LLM-generated insights by querying the warehouse.
// For each insight, it asks the LLM to generate a verification query,
// runs it (with self-healing SQL fix), and compares the result with the
// claimed numbers.
//
// The verifier is single-run-scoped: the orchestrator constructs a fresh
// instance per discovery run and wires the exploration log between the
// exploration and analysis phases via SetExplorationLog. It is NOT safe for
// concurrent use across runs (the analysis loop is sequential today, so this
// is not a current constraint to relax). Background and design discussion in
// plans/PLAN-INSIGHT-VERIFICATION-GROUNDING.md §6.9.
type InsightValidator struct {
	aiClient          *ai.Client
	warehouse         gowarehouse.Provider
	executor          SelfHealingExecutor
	schemaProvider    ai.SchemaProvider
	dataset           string
	filter            string
	schemaCtx         string
	explorationLog    []models.ExplorationStep
	explorationLogSet bool
}

// MaxLookupsPerVerification caps the number of `lookup_schema` actions the
// verifier's tool loop may issue per insight. The plan §4.3 settles on 6:
// generous enough for cross-table verifications (single insight rarely needs
// more than 2–3 tables) and bounded so a misbehaving model can't spam lookups
// instead of producing the verification SELECT COUNT.
const MaxLookupsPerVerification = 6

// SelfHealingExecutor executes queries with automatic SQL fix + retry. The
// validator forwards a FixOpts on every call so the SQL fixer sees the same
// column-grounding evidence the verification prompt was built on; on retries
// the fixer uses that evidence to substitute real column names rather than
// re-emit the hallucination that triggered the failure. Background:
// plans/PLAN-INSIGHT-VERIFICATION-GROUNDING.md §4.2.
type SelfHealingExecutor interface {
	Execute(ctx context.Context, query string, purpose string, opts queryexec.FixOpts) (rows []map[string]interface{}, err error)
}

// InsightValidatorOptions configures the insight validator.
type InsightValidatorOptions struct {
	AIClient  *ai.Client
	Warehouse gowarehouse.Provider
	Executor  SelfHealingExecutor // if set, uses self-healing query execution
	// SchemaProvider, when non-nil, enables the verifier's `lookup_schema`
	// tool loop (Layer 3). The verifier issues up to
	// MaxLookupsPerVerification on-demand schema lookups before producing
	// the final verification SQL — the LLM uses these to ground cross-table
	// verifications when source_steps does not cover every relevant
	// column. When nil, the verifier falls back to a single-shot generation
	// using only source steps + the catalog (Layer 1 + 2 only).
	SchemaProvider ai.SchemaProvider
	Dataset        string
	Filter         string
}

// NewInsightValidator creates a new insight validator.
func NewInsightValidator(opts InsightValidatorOptions) *InsightValidator {
	return &InsightValidator{
		aiClient:       opts.AIClient,
		warehouse:      opts.Warehouse,
		executor:       opts.Executor,
		schemaProvider: opts.SchemaProvider,
		dataset:        opts.Dataset,
		filter:         opts.Filter,
	}
}

// SetSchemaContext provides table schema information for verification query generation.
func (v *InsightValidator) SetSchemaContext(schemaJSON string) {
	v.schemaCtx = schemaJSON
}

// SetSchemaProvider enables the verifier's `lookup_schema` tool loop (Layer 3).
// When wired the validator runs up to MaxLookupsPerVerification rounds per
// insight to fetch column detail for tables source_steps does not cover; when
// nil the validator falls through to a single-shot generation. Mirror of the
// orchestrator's construction-then-set pattern (the SchemaProvider is built
// after the validator's options are populated, so this setter exists rather
// than a constructor field).
func (v *InsightValidator) SetSchemaProvider(p ai.SchemaProvider) {
	v.schemaProvider = p
}

// SetExplorationLog wires the exploration steps captured during the
// exploration phase. The verifier renders the SQL of each cited
// `source_steps` entry into the verification prompt so the LLM can adapt a
// known-good query into a `SELECT COUNT(...)` shape rather than inventing
// column names.
//
// MUST be called before ValidateInsights. Pass an empty slice (NOT nil) when
// the run produced no steps — passing nil is treated as a wiring bug and
// panics. The validator retains the provided slice and does not copy its
// elements or backing array; callers must not mutate the slice afterwards.
func (v *InsightValidator) SetExplorationLog(log []models.ExplorationStep) {
	if log == nil {
		panic("validation.InsightValidator: SetExplorationLog called with nil log; pass []models.ExplorationStep{} for empty-run cases")
	}
	v.explorationLog = log
	v.explorationLogSet = true
}

// ValidateInsights verifies each insight by running a warehouse query.
// Updates the insight's Validation field in-place and returns full results.
//
// Panics if SetExplorationLog was never called — the verifier's column
// grounding depends on the exploration log being wired by the orchestrator
// between the exploration and analysis phases. See plans/PLAN-INSIGHT-
// VERIFICATION-GROUNDING.md §1.1 (no-backward-compat stance).
func (v *InsightValidator) ValidateInsights(
	ctx context.Context,
	insights []models.Insight,
) []models.ValidationResult {
	if !v.explorationLogSet {
		panic("validation.InsightValidator: ValidateInsights called before SetExplorationLog; the orchestrator must wire the exploration log between the exploration and analysis phases")
	}
	results := make([]models.ValidationResult, 0, len(insights))

	for i, insight := range insights {
		applog.WithFields(applog.Fields{
			"insight": insight.Name,
			"area":    insight.AnalysisArea,
			"count":   insight.AffectedCount,
		}).Info("Validating insight against warehouse")

		vr := v.validateSingleInsight(ctx, &insight)
		results = append(results, vr)

		// Update insight validation in-place
		insights[i].Validation = &models.InsightValidation{
			Status:        vr.Status,
			VerifiedCount: vr.VerifiedCount,
			OriginalCount: vr.ClaimedCount,
			Query:         vr.Query,
			Reasoning:     vr.Reasoning,
			ValidatedAt:   vr.ValidatedAt,
		}
	}

	confirmed := 0
	adjusted := 0
	rejected := 0
	for _, r := range results {
		switch r.Status {
		case "confirmed":
			confirmed++
		case "adjusted":
			adjusted++
		case "rejected":
			rejected++
		}
	}

	applog.WithFields(applog.Fields{
		"total":     len(results),
		"confirmed": confirmed,
		"adjusted":  adjusted,
		"rejected":  rejected,
	}).Info("Insight validation completed")

	return results
}

// validateSingleInsight generates and runs a verification query for one insight.
func (v *InsightValidator) validateSingleInsight(
	ctx context.Context,
	insight *models.Insight,
) models.ValidationResult {
	vr := models.ValidationResult{
		InsightID:    insight.ID,
		AnalysisArea: insight.AnalysisArea,
		ValidatedAt:  time.Now(),
		ClaimedCount: insight.AffectedCount,
		ClaimedMetric: insight.Name,
	}

	// Ask LLM to generate a verification query. When a SchemaProvider is
	// wired the verifier runs a small tool loop (Layer 3) that lets the
	// model issue lookup_schema actions before producing the final SELECT
	// COUNT — its results are returned alongside the SQL so the SQL fixer
	// receives the same evidence on retry.
	applog.WithField("insight", insight.Name).Debug("Generating verification query via LLM")
	verificationQuery, lookups, err := v.generateVerificationQuery(ctx, insight)
	if err != nil {
		applog.WithFields(applog.Fields{
			"insight": insight.Name,
			"error":   err.Error(),
		}).Warn("Failed to generate verification query")
		vr.Status = "error"
		vr.QueryError = fmt.Sprintf("failed to generate verification query: %s", err.Error())
		vr.Reasoning = fmt.Sprintf("Could not generate a verification query: %s", err.Error())
		return vr
	}

	applog.WithFields(applog.Fields{
		"insight":   insight.Name,
		"query_len": len(verificationQuery),
		"lookups":   len(lookups),
	}).Debug("Verification query generated")

	vr.Query = verificationQuery

	// Run the verification query with self-healing (retry + SQL fix). The
	// FixOpts forward the same source-step evidence + any on-demand schema
	// lookups the verifier gathered, so when the warehouse rejects the SQL
	// with a column error the fixer has authoritative names to substitute
	// rather than re-emitting the hallucination.
	fixOpts := queryexec.FixOpts{
		VerificationContext: render.RenderVerificationContextWithLookups(
			v.explorationLog,
			insight.SourceSteps,
			lookups,
			render.DefaultBudgetChars,
		),
	}
	var verifiedCount int
	if v.executor != nil {
		rows, err := v.executor.Execute(ctx, verificationQuery, "validate insight: "+insight.Name, fixOpts)
		if err != nil {
			applog.WithFields(applog.Fields{
				"insight": insight.Name,
				"error":   err.Error(),
			}).Warn("Verification query failed after retries")
			vr.Status = "error"
			vr.QueryError = err.Error()
			vr.Reasoning = fmt.Sprintf("Verification query failed after retries: %s", err.Error())
			return vr
		}
		verifiedCount = extractCountFromRows(rows)
	} else {
		queryResult, err := v.warehouse.Query(ctx, verificationQuery, nil)
		if err != nil {
			applog.WithFields(applog.Fields{
				"insight": insight.Name,
				"error":   err.Error(),
			}).Warn("Verification query failed (no executor)")
			vr.Status = "error"
			vr.QueryError = err.Error()
			vr.Reasoning = fmt.Sprintf("Verification query failed: %s", err.Error())
			return vr
		}
		verifiedCount = v.extractCount(queryResult)
	}
	vr.VerifiedCount = verifiedCount

	// Compare claimed vs verified
	if insight.AffectedCount == 0 {
		applog.WithField("insight", insight.Name).Debug("No count to verify (affected_count=0), confirming")
		vr.Status = "confirmed"
		vr.Reasoning = "No count to verify"
		return vr
	}

	ratio := float64(verifiedCount) / float64(insight.AffectedCount)

	switch {
	case ratio >= 0.8 && ratio <= 1.2:
		vr.Status = "confirmed"
		vr.Reasoning = fmt.Sprintf("Verified count (%d) is within 20%% of claimed count (%d). Ratio: %.2f",
			verifiedCount, insight.AffectedCount, ratio)
	case ratio > 0 && (ratio < 0.8 || ratio > 1.2):
		vr.Status = "adjusted"
		vr.Reasoning = fmt.Sprintf("Verified count (%d) differs significantly from claimed count (%d). Ratio: %.2f. Adjusting to verified value.",
			verifiedCount, insight.AffectedCount, ratio)
	case verifiedCount == 0:
		vr.Status = "rejected"
		vr.Reasoning = fmt.Sprintf("Verification query returned 0 results. Claimed count was %d. The insight may be based on incorrect data.",
			insight.AffectedCount)
	default:
		vr.Status = "error"
		vr.Reasoning = "Unexpected verification result"
	}

	applog.WithFields(applog.Fields{
		"insight":  insight.Name,
		"status":   vr.Status,
		"claimed":  insight.AffectedCount,
		"verified": verifiedCount,
		"ratio":    fmt.Sprintf("%.2f", ratio),
	}).Info("Insight validation result")

	return vr
}

// generateVerificationQuery is the verifier's entry point. With a SchemaProvider
// wired (Layer 3) it runs a small tool loop: the LLM may issue lookup_schema
// actions to fetch column detail for tables source_steps does not cover, then
// emit the final SELECT COUNT. The gathered lookup detail is returned
// alongside the SQL so the SQL fixer receives the same evidence on retry.
//
// Without a SchemaProvider the function falls through to a single-shot
// generation (Layer 1 + 2 only) — the prompt still carries the source-step
// rendered evidence and the catalog.
func (v *InsightValidator) generateVerificationQuery(
	ctx context.Context,
	insight *models.Insight,
) (string, []ai.LookupTable, error) {
	if v.schemaProvider == nil {
		sql, err := v.generateSingleShotQuery(ctx, insight, nil)
		return sql, nil, err
	}
	return v.runVerificationLoop(ctx, insight)
}

// generateSingleShotQuery sends one prompt and returns the SQL. lookups, when
// non-empty, are folded into the rendered evidence block — the verifier loop
// passes the accumulated set on its forced final round so the model can adapt
// what it has gathered into a SELECT COUNT.
func (v *InsightValidator) generateSingleShotQuery(
	ctx context.Context,
	insight *models.Insight,
	lookups []ai.LookupTable,
) (string, error) {
	prompt := v.buildVerificationPrompt(insight, lookups, nil, false, false)
	chatResult, err := v.aiClient.Chat(ctx, prompt, "", 2000)
	if err != nil {
		return "", err
	}
	return cleanGeneratedSQL(chatResult.Content)
}

// runVerificationLoop drives the LLM through up to MaxLookupsPerVerification
// `lookup_schema` rounds before emitting the final query. If the budget is
// exhausted before the model emits a query, one final "you must produce a
// query NOW" round is allowed (NOT counted against the budget — same pattern
// the exploration engine uses on its complete-now turn). A failure to emit
// a query after the forced round is a hard error so the validator can
// surface a clear telemetry signal rather than re-using an arbitrary fallback.
func (v *InsightValidator) runVerificationLoop(
	ctx context.Context,
	insight *models.Insight,
) (string, []ai.LookupTable, error) {
	var (
		lookups   []ai.LookupTable
		notFound  []string // refs the SchemaProvider could not resolve, accumulated across rounds
		truncated bool     // true when any round returned res.Truncated
	)
	allowed := []string{"lookup_schema", "query_data"}

	for attempt := 0; attempt < MaxLookupsPerVerification; attempt++ {
		prompt := v.buildVerificationPrompt(insight, lookups, notFound, truncated, true)
		chatResult, err := v.aiClient.Chat(ctx, prompt, "", 2000)
		if err != nil {
			return "", lookups, err
		}

		action, parseErr := ai.ParseAction(chatResult.Content, allowed)
		if parseErr != nil {
			// Treat a bare SELECT response as a query — a common LLM
			// shape when the model "forgets" to wrap in JSON.
			if sql, ok := bareSQLFallback(chatResult.Content); ok {
				return sql, lookups, nil
			}
			applog.WithFields(applog.Fields{
				"insight": insight.Name,
				"attempt": attempt,
				"error":   parseErr.Error(),
			}).Warn("Verifier action parse failed; forcing query round")
			break
		}

		switch action.Action {
		case "query_data":
			return action.Query, lookups, nil
		case "lookup_schema":
			res, lerr := v.schemaProvider.Lookup(ctx, action.LookupSchema)
			if lerr != nil {
				return "", lookups, fmt.Errorf("schema lookup failed: %w", lerr)
			}
			lookups = mergeLookups(lookups, res.Tables)
			notFound = appendUniqueStrings(notFound, res.NotFound)
			if res.Truncated {
				truncated = true
			}
			applog.WithFields(applog.Fields{
				"insight":   insight.Name,
				"attempt":   attempt,
				"tables":    len(res.Tables),
				"not_found": len(res.NotFound),
				"truncated": res.Truncated,
				"acc_total": len(lookups),
			}).Debug("Verifier lookup_schema served")
		}
	}

	// Budget exhausted — force one final query round. NOT counted against the
	// per-insight budget; this matches the exploration engine's "complete-now"
	// final turn.
	applog.WithFields(applog.Fields{
		"insight":   insight.Name,
		"lookups":   len(lookups),
		"not_found": len(notFound),
	}).Info("Verifier lookup budget exhausted; forcing final query round")

	forcedPrompt := v.buildVerificationPrompt(insight, lookups, notFound, truncated, false) +
		"\n\nYou have exhausted your lookup_schema budget. Produce the verification query NOW using the evidence already gathered. Return ONLY the raw SQL query, no explanations, no markdown."

	chatResult, err := v.aiClient.Chat(ctx, forcedPrompt, "", 2000)
	if err != nil {
		return "", lookups, err
	}
	if sql, ok := bareSQLFallback(chatResult.Content); ok {
		return sql, lookups, nil
	}
	action, parseErr := ai.ParseAction(chatResult.Content, []string{"query_data"})
	if parseErr == nil && action.Query != "" {
		return action.Query, lookups, nil
	}
	return "", lookups, fmt.Errorf("verifier exhausted lookup budget without emitting a query")
}

// appendUniqueStrings appends `additions` to `acc` skipping values already
// present (case-sensitive). Used to dedupe accumulators where the model
// might re-issue the same NotFound ref across rounds.
func appendUniqueStrings(acc, additions []string) []string {
	if len(additions) == 0 {
		return acc
	}
	seen := make(map[string]bool, len(acc))
	for _, s := range acc {
		seen[s] = true
	}
	for _, s := range additions {
		if seen[s] {
			continue
		}
		seen[s] = true
		acc = append(acc, s)
	}
	return acc
}

// mergeLookups appends new lookup tables to the accumulator, deduplicating
// by qualified table name — the LLM sometimes re-requests a table it already
// has, and there's no reason to re-render the same columns twice.
func mergeLookups(acc, newly []ai.LookupTable) []ai.LookupTable {
	seen := make(map[string]bool, len(acc))
	for _, t := range acc {
		seen[t.Table] = true
	}
	for _, t := range newly {
		if seen[t.Table] {
			continue
		}
		seen[t.Table] = true
		acc = append(acc, t)
	}
	return acc
}

// buildVerificationPrompt renders the prompt for one round of the verifier.
// loopMode=true asks the LLM to choose between lookup_schema and query
// actions; loopMode=false expects raw SQL only (single-shot path or the
// forced final round of the loop). notFound and truncated come from
// previous-round LookupResults — surfaced to the model as a "Lookup
// Notices" section so it can self-correct (e.g. retry a misspelled
// dataset.table or stop re-requesting a known-missing ref). Both nil
// (the single-shot path) renders no notices section.
func (v *InsightValidator) buildVerificationPrompt(
	insight *models.Insight,
	lookups []ai.LookupTable,
	notFound []string,
	truncated bool,
	loopMode bool,
) string {
	insightJSON, _ := json.MarshalIndent(insight, "", "  ")

	schemaSection := ""
	if v.schemaCtx != "" {
		schemaSection = fmt.Sprintf("\n**Available Table Schemas**:\n%s\n", v.schemaCtx)
	}

	evidence := render.RenderVerificationContextWithLookups(
		v.explorationLog,
		insight.SourceSteps,
		lookups,
		render.DefaultBudgetChars,
	)
	evidenceSection := ""
	if evidence != "" {
		evidenceSection = "\n" + evidence + render.RuleInstruction + "\n"
	}

	noticesSection := ""
	if len(notFound) > 0 || truncated {
		var b strings.Builder
		b.WriteString("\n## Lookup Notices\n\n")
		if len(notFound) > 0 {
			b.WriteString("These refs were not found in the schema cache (verify the `dataset.table` name, or query without these tables):\n")
			for _, ref := range notFound {
				fmt.Fprintf(&b, "- `%s`\n", ref)
			}
			b.WriteString("\n")
		}
		if truncated {
			fmt.Fprintf(&b, "A previous lookup_schema call requested more tables than the per-call cap (%d) allows; only the first batch was returned. Issue follow-up calls for the remainder if you still need them.\n\n", ai.MaxLookupTablesPerCall)
		}
		noticesSection = b.String()
	}

	actionInstructions := `Return ONLY the raw SQL query, no explanations, no markdown.`
	if loopMode {
		actionInstructions = `Return ONE JSON object on a line by itself with EITHER:
  {"lookup_schema": ["dataset.table", ...]}  — to fetch column detail for additional tables (up to ` + fmt.Sprintf("%d", ai.MaxLookupTablesPerCall) + ` per call, ` + fmt.Sprintf("%d", MaxLookupsPerVerification) + ` rounds total per insight), OR
  {"query": "SELECT COUNT(...) AS count FROM dataset.table ..."}  — to run the verification query.

Use lookup_schema when you need column information that is not already shown in the evidence above (typical when the insight needs to JOIN or reference a table the source_steps did not query). Otherwise issue the query directly.`
	}

	return fmt.Sprintf(`Generate a SQL verification query for this insight. The query must verify the claimed numbers.

**Available Datasets**: %s
**SQL Dialect**: %s
**Filter**: %s
%s%s%s
**CRITICAL TABLE NAME RULES**:
- ALWAYS use fully qualified table names with backticks: `+"`dataset_name.table_name`"+`
- Example: `+"`events_prod.sessions`"+` NOT just `+"`sessions`"+`
- The dataset name MUST be included in every table reference
- Reference only column names that appear in the source exploration queries / lookup detail (when shown) or in the table schemas section. Do not invent column names that are not documented above.

**Insight to verify**:
%s

Generate a single SQL query that:
1. Counts the affected users/entities described in this insight
2. For user counts, prefer COUNT(DISTINCT user_id) — but only when a `+"`user_id`"+` (or the project's filter field) column is documented in the source queries or schemas above. Substitute the column name that actually exists; do not assume `+"`user_id`"+` is the right name.
3. Uses FULLY QUALIFIED table names: `+"`dataset.table`"+`
4. Includes the filter clause if provided
5. ALWAYS alias the result as "count": SELECT COUNT(...) AS count

%s`,
		v.dataset,
		v.warehouse.SQLDialect(),
		v.filter,
		evidenceSection,
		schemaSection,
		noticesSection,
		string(insightJSON),
		actionInstructions,
	)
}

// bareSQLFallback handles the common case where the LLM ignores the JSON
// envelope rule and returns raw SQL (with or without code fences). When the
// content unambiguously parses as SELECT we accept it rather than retry — the
// LLM's intent is clear. Returns ("", false) when there is no SELECT in the
// content.
func bareSQLFallback(content string) (string, bool) {
	sql, err := cleanGeneratedSQL(content)
	if err != nil {
		return "", false
	}
	return sql, true
}

// cleanGeneratedSQL strips markdown code fences and language tags from a raw
// LLM response and returns the embedded SQL. Errors when no SELECT is found —
// the caller surfaces that as a hard validation failure.
func cleanGeneratedSQL(content string) (string, error) {
	sql := strings.TrimSpace(content)

	if strings.Contains(sql, "```") {
		start := strings.Index(sql, "```")
		end := strings.LastIndex(sql, "```")
		if start != end {
			inner := sql[start+3 : end]
			if nl := strings.Index(inner, "\n"); nl != -1 {
				firstLine := strings.TrimSpace(inner[:nl])
				if len(firstLine) <= 10 && !strings.Contains(firstLine, " ") {
					inner = inner[nl+1:]
				}
			}
			sql = strings.TrimSpace(inner)
		} else {
			sql = strings.TrimPrefix(sql, "```sql")
			sql = strings.TrimPrefix(sql, "```json")
			sql = strings.TrimPrefix(sql, "```")
			sql = strings.TrimSpace(sql)
		}
	}

	if !strings.Contains(strings.ToUpper(sql), "SELECT") {
		return "", fmt.Errorf("generated response is not a SQL query")
	}

	return sql, nil
}

// extractCount extracts a count value from a query result.
func (v *InsightValidator) extractCount(result *gowarehouse.QueryResult) int {
	if result == nil || len(result.Rows) == 0 {
		return 0
	}

	row := result.Rows[0]

	// Try common count column names
	for _, key := range []string{"count", "total", "total_users", "total_count", "cnt", "user_count"} {
		if val, ok := row[key]; ok {
			return toInt(val)
		}
	}

	// Take the first numeric value in the first row
	for _, val := range row {
		if n := toInt(val); n > 0 {
			return n
		}
	}

	return 0
}

// extractCountFromRows extracts the count from query result rows.
// The verification prompt asks the LLM to alias as "count".
// Falls back to first numeric value if "count" alias is missing.
func extractCountFromRows(rows []map[string]interface{}) int {
	if len(rows) == 0 {
		return 0
	}
	row := rows[0]

	// Primary: the prompt instructs "AS count"
	if val, ok := row["count"]; ok {
		return toInt(val)
	}

	// Fallback: first numeric value in the row
	for _, val := range row {
		if n := toInt(val); n > 0 {
			return n
		}
	}
	return 0
}

func toInt(v interface{}) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case int32:
		return int(t)
	default:
		return 0
	}
}
