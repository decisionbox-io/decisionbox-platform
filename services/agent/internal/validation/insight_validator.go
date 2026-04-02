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
)

// InsightValidator verifies LLM-generated insights by querying the warehouse.
// For each insight, it asks the LLM to generate a verification query,
// runs it (with self-healing SQL fix), and compares the result with the claimed numbers.
type InsightValidator struct {
	aiClient  *ai.Client
	warehouse gowarehouse.Provider
	executor  SelfHealingExecutor
	dataset   string
	filter    string
	schemaCtx string
}

// SelfHealingExecutor executes queries with automatic SQL fix + retry.
type SelfHealingExecutor interface {
	Execute(ctx context.Context, query string, purpose string) (rows []map[string]interface{}, err error)
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

// ValidateInsights verifies each insight by running a warehouse query.
// Updates the insight's Validation field in-place and returns full results.
func (v *InsightValidator) ValidateInsights(
	ctx context.Context,
	insights []models.Insight,
) []models.ValidationResult {
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

	// Run the verification query with self-healing (retry + SQL fix)
	var verifiedCount int
	if v.executor != nil {
		rows, err := v.executor.Execute(ctx, verificationQuery, "validate insight: "+insight.Name)
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

// generateVerificationQuery asks the LLM to create a SQL query that verifies the insight.
func (v *InsightValidator) generateVerificationQuery(
	ctx context.Context,
	insight *models.Insight,
) (string, error) {
	insightJSON, _ := json.MarshalIndent(insight, "", "  ")

	schemaSection := ""
	if v.schemaCtx != "" {
		schemaSection = fmt.Sprintf("\n**Available Table Schemas**:\n%s\n", v.schemaCtx)
	}

	prompt := fmt.Sprintf(`Generate a SQL verification query for this insight. The query must verify the claimed numbers.

**Available Datasets**: %s
**SQL Dialect**: %s
**Filter**: %s
%s
**CRITICAL TABLE NAME RULES**:
- ALWAYS use fully qualified table names with backticks: `+"`dataset_name.table_name`"+`
- Example: `+"`events_prod.sessions`"+` NOT just `+"`sessions`"+`
- The dataset name MUST be included in every table reference
- ONLY use column names that exist in the table schemas above

**Insight to verify**:
%s

Generate a single SQL query that:
1. Counts the affected users/entities described in this insight
2. Uses COUNT(DISTINCT user_id) for user counts
3. Uses FULLY QUALIFIED table names: `+"`dataset.table`"+`
4. ONLY references columns that exist in the provided table schemas
5. Includes the filter clause if provided
6. ALWAYS alias the result as "count": SELECT COUNT(...) AS count

Return ONLY the raw SQL query, no explanations, no markdown.`,
		v.dataset,
		v.warehouse.SQLDialect(),
		v.filter,
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
