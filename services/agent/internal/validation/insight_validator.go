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
	dataset           string
	filter            string
	schemaCtx         string
	explorationLog    []models.ExplorationStep
	explorationLogSet bool
}

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
	Dataset   string
	Filter    string
}

// NewInsightValidator creates a new insight validator.
func NewInsightValidator(opts InsightValidatorOptions) *InsightValidator {
	return &InsightValidator{
		aiClient:  opts.AIClient,
		warehouse: opts.Warehouse,
		executor:  opts.Executor,
		dataset:   opts.Dataset,
		filter:    opts.Filter,
	}
}

// SetSchemaContext provides table schema information for verification query generation.
func (v *InsightValidator) SetSchemaContext(schemaJSON string) {
	v.schemaCtx = schemaJSON
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

	// Ask LLM to generate a verification query
	applog.WithField("insight", insight.Name).Debug("Generating verification query via LLM")
	verificationQuery, err := v.generateVerificationQuery(ctx, insight)
	if err != nil {
		applog.WithFields(applog.Fields{
			"insight": insight.Name,
			"error":   err.Error(),
		}).Warn("Failed to generate verification query")
		vr.Status = "error"
		vr.QueryError = fmt.Sprintf("failed to generate verification query: %s", err.Error())
		vr.Reasoning = "Could not generate a verification query for this insight"
		return vr
	}

	applog.WithFields(applog.Fields{
		"insight":   insight.Name,
		"query_len": len(verificationQuery),
	}).Debug("Verification query generated")

	vr.Query = verificationQuery

	// Run the verification query with self-healing (retry + SQL fix). The
	// FixOpts forward the same source-step evidence the generation prompt
	// was built on, so when the warehouse rejects the SQL with a column
	// error the fixer has authoritative names to substitute rather than
	// re-emitting the hallucination.
	fixOpts := queryexec.FixOpts{
		VerificationContext: render.RenderVerificationContext(
			v.explorationLog,
			insight.SourceSteps,
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

// generateVerificationQuery asks the LLM to create a SQL query that verifies
// the insight. The prompt is layered, in priority order:
//
//   1. Source exploration queries — the SQL of the steps the analyst LLM cited
//      as `source_steps`. These are the highest-priority evidence: the queries
//      already executed successfully against this warehouse, so their column
//      references are authoritative. Rendered above the catalog so the LLM
//      sees them first.
//   2. Available table schemas — the catalog (one line per table). Used as
//      supplementary context for tables that were not touched by source steps.
//
// Background: prior to this layering the verifier received only the catalog,
// and on warehouses with non-English / abbreviated columns the model hallucin-
// ated names (customer ticket 2026-04-30, plans/PLAN-INSIGHT-VERIFICATION-
// GROUNDING.md). Source-step grounding closes that gap for single-table
// insights — Layer 3 (lookup_schema in the verifier) closes the cross-table
// gap in a follow-up PR.
func (v *InsightValidator) generateVerificationQuery(
	ctx context.Context,
	insight *models.Insight,
) (string, error) {
	insightJSON, _ := json.MarshalIndent(insight, "", "  ")

	schemaSection := ""
	if v.schemaCtx != "" {
		schemaSection = fmt.Sprintf("\n**Available Table Schemas**:\n%s\n", v.schemaCtx)
	}

	sourceQueries := render.RenderVerificationContext(
		v.explorationLog,
		insight.SourceSteps,
		render.DefaultBudgetChars,
	)
	sourceSection := ""
	if sourceQueries != "" {
		sourceSection = "\n" + sourceQueries + render.RuleInstruction + "\n"
	}

	prompt := fmt.Sprintf(`Generate a SQL verification query for this insight. The query must verify the claimed numbers.

**Available Datasets**: %s
**SQL Dialect**: %s
**Filter**: %s
%s%s
**CRITICAL TABLE NAME RULES**:
- ALWAYS use fully qualified table names with backticks: `+"`dataset_name.table_name`"+`
- Example: `+"`events_prod.sessions`"+` NOT just `+"`sessions`"+`
- The dataset name MUST be included in every table reference
- Reference only column names that appear in the source exploration queries (when shown) or in the table schemas section. Do not invent column names that are not documented above.

**Insight to verify**:
%s

Generate a single SQL query that:
1. Counts the affected users/entities described in this insight
2. For user counts, prefer COUNT(DISTINCT user_id) — but only when a `+"`user_id`"+` (or the project's filter field) column is documented in the source queries or schemas above. Substitute the column name that actually exists; do not assume `+"`user_id`"+` is the right name.
3. Uses FULLY QUALIFIED table names: `+"`dataset.table`"+`
4. Includes the filter clause if provided
5. ALWAYS alias the result as "count": SELECT COUNT(...) AS count

Return ONLY the raw SQL query, no explanations, no markdown.`,
		v.dataset,
		v.warehouse.SQLDialect(),
		v.filter,
		sourceSection,
		schemaSection,
		string(insightJSON),
	)

	chatResult, err := v.aiClient.Chat(ctx, prompt, "", 2000)
	if err != nil {
		return "", err
	}

	sql := strings.TrimSpace(chatResult.Content)

	// Clean up markdown code blocks (handles ```sql, ```json, ```, etc.)
	if strings.Contains(sql, "```") {
		// Extract content between first ``` and last ```
		start := strings.Index(sql, "```")
		end := strings.LastIndex(sql, "```")
		if start != end {
			inner := sql[start+3 : end]
			// Strip language tag on first line (sql, json, etc.)
			if nl := strings.Index(inner, "\n"); nl != -1 {
				firstLine := strings.TrimSpace(inner[:nl])
				// If first line is just a language tag (no spaces, short), skip it
				if len(firstLine) <= 10 && !strings.Contains(firstLine, " ") {
					inner = inner[nl+1:]
				}
			}
			sql = strings.TrimSpace(inner)
		} else {
			// Single ``` — just strip it
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
