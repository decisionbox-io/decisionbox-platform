package validation

import (
	"context"
	"fmt"
	"strings"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/debug"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation/render"
)

// UserCountValidator validates user counts in discovery insights against
// warehouse totals.
//
// Layer 4 of the verification-grounding fix: the validator's total-users
// probe used to fail silently on warehouses where the user-identifier column
// isn't named `user_id` — the hardcoded `COUNT(DISTINCT user_id) FROM
// sessions/events/app_users` queries returned `Invalid column` errors and
// every insight's affected_count claim went through unverified. The probe
// now runs through the self-healing executor with `FixOpts` carrying the
// source-step grounding evidence, so the SQL fixer can substitute the
// real column name (e.g. `KULLANICI_ID`, `customer_id`) on retry.
// Background: plans/PLAN-INSIGHT-VERIFICATION-GROUNDING.md §4.4.
type UserCountValidator struct {
	warehouse      gowarehouse.Provider
	executor       SelfHealingExecutor // optional; when set, probe queries are self-healing
	debugLogger    *debug.Logger
	dataset        string
	filter         string // e.g., "WHERE app_id = 'xyz'" or ""
	explorationLog []models.ExplorationStep

	totalUsers       int
	totalUsersCached bool
}

// UserCountValidatorOptions configures the validator.
type UserCountValidatorOptions struct {
	Warehouse gowarehouse.Provider
	// Executor, when non-nil, routes the user-count probe through the self-
	// healing query executor. The validator forwards the rendered
	// `VerificationContext` (source-step SQL from `explorationLog`) via
	// `FixOpts` so the SQL fixer can ground retries in real warehouse
	// column names — closing the gap on warehouses where the user-id
	// column isn't named `user_id`.
	Executor    SelfHealingExecutor
	DebugLogger *debug.Logger
	Dataset     string
	Filter      string
}

// NewUserCountValidator creates a new user count validator.
func NewUserCountValidator(opts UserCountValidatorOptions) *UserCountValidator {
	return &UserCountValidator{
		warehouse:   opts.Warehouse,
		executor:    opts.Executor,
		debugLogger: opts.DebugLogger,
		dataset:     opts.Dataset,
		filter:      opts.Filter,
	}
}

// SetExplorationLog wires the exploration steps captured during the
// exploration phase. The log is rendered into `FixOpts.VerificationContext`
// when the executor self-heals a probe query, so the SQL fixer can
// substitute the real user-id column name on retry. Calling this is
// optional — when the log is unset or empty the executor still receives
// a valid (empty) FixOpts and the SQL fixer just operates without
// column-grounding evidence. Passing nil panics (use an empty slice for
// "no steps" — nil is treated as a wiring bug).
//
// Invalidates the cached total-users count, since the next probe should
// use the new evidence.
func (v *UserCountValidator) SetExplorationLog(log []models.ExplorationStep) {
	if log == nil {
		panic("validation.UserCountValidator: SetExplorationLog called with nil log; pass []models.ExplorationStep{} for empty-run cases")
	}
	v.explorationLog = log
	v.totalUsersCached = false
	v.totalUsers = 0
}

// SetExecutor wires the self-healing query executor. Mirror of the
// orchestrator's construct-then-set pattern (the executor is built inside
// RunDiscovery, after the validator's options are populated, so this setter
// exists rather than a constructor field). Pass nil to disable self-healing
// and fall back to direct warehouse.Query calls.
//
// Invalidates the cached total-users count so subsequent calls route through
// the new executor — otherwise a swap mid-run would silently keep returning
// the stale value via the prior path.
func (v *UserCountValidator) SetExecutor(exec SelfHealingExecutor) {
	v.executor = exec
	v.totalUsersCached = false
	v.totalUsers = 0
}

// GetTotalUsers fetches the total unique users from the warehouse. When an
// `Executor` is wired, each probe runs through the self-healing path with
// `FixOpts.VerificationContext` carrying the rendered source-step SQL — so a
// `user_id`-column hallucination on a warehouse using `KULLANICI_ID` or
// `customer_id` is repaired by the SQL fixer on retry rather than failing
// silently. Without an Executor the validator falls through to direct
// `warehouse.Query` calls (legacy behaviour).
func (v *UserCountValidator) GetTotalUsers(ctx context.Context) (int, error) {
	if v.totalUsersCached {
		return v.totalUsers, nil
	}

	filterClause := ""
	if v.filter != "" {
		filterClause = v.filter
	}

	// Try multiple tables that might contain user counts
	queries := []string{
		fmt.Sprintf("SELECT COUNT(DISTINCT user_id) as total_users FROM `%s.sessions` %s", v.dataset, filterClause),
		fmt.Sprintf("SELECT COUNT(DISTINCT user_id) as total_users FROM `%s.events` %s", v.dataset, filterClause),
		fmt.Sprintf("SELECT COUNT(*) as total_users FROM `%s.app_users` %s", v.dataset, filterClause),
	}

	// The probe is run-wide (no per-insight source_steps), so when an
	// executor is wired we render the union of ALL `query_data` step IDs
	// from the exploration log into FixOpts.VerificationContext. The
	// budget cap inside RenderVerificationContext drops the oldest steps
	// when the rendered block would exceed the limit. Built lazily — the
	// no-executor path doesn't use FixOpts and rendering is the most
	// expensive thing in this function.
	var fixOpts queryexec.FixOpts
	if v.executor != nil {
		fixOpts = queryexec.FixOpts{
			VerificationContext: render.RenderVerificationContext(
				v.explorationLog,
				collectQueryStepIDs(v.explorationLog),
				render.DefaultBudgetChars,
			),
		}
	}

	var lastErr error
	for _, query := range queries {
		count, err := v.runProbeQuery(ctx, query, fixOpts)
		if err != nil {
			lastErr = err
			continue
		}
		if count > 0 {
			v.totalUsers = count
			v.totalUsersCached = true
			applog.WithFields(applog.Fields{
				"total_users": count,
			}).Info("Total unique users fetched")
			return count, nil
		}
	}

	if lastErr != nil {
		return 0, fmt.Errorf("failed to fetch total users: %w", lastErr)
	}

	return 0, fmt.Errorf("could not determine total users")
}

// runProbeQuery dispatches a single probe via the self-healing executor when
// one is wired, falling back to a direct warehouse call otherwise. Returns
// the extracted total_users count (or 0 if the row is missing the field).
func (v *UserCountValidator) runProbeQuery(ctx context.Context, query string, opts queryexec.FixOpts) (int, error) {
	if v.executor != nil {
		rows, err := v.executor.Execute(ctx, query, "user-count probe", opts)
		if err != nil {
			return 0, err
		}
		return extractTotalUsersFromRows(rows), nil
	}

	results, err := v.warehouse.Query(ctx, query, nil)
	if err != nil {
		return 0, err
	}
	if results == nil {
		return 0, nil
	}
	return extractTotalUsersFromRows(results.Rows), nil
}

func extractTotalUsersFromRows(rows []map[string]interface{}) int {
	if len(rows) == 0 {
		return 0
	}
	// Snowflake (and a few other warehouses) return unquoted column aliases
	// folded to upper case — `total_users` becomes `TOTAL_USERS` in the row
	// map. Match the column key case-insensitively so the probe is portable
	// without forcing every dialect-specific probe to double-quote the alias.
	var totalUsers interface{}
	for k, v := range rows[0] {
		if strings.EqualFold(k, "total_users") {
			totalUsers = v
			break
		}
	}
	if totalUsers == nil {
		return 0
	}
	switch t := totalUsers.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	}
	return 0
}

// collectQueryStepIDs returns the Step number of every entry in `log` whose
// Action is "query_data" (or empty — early steps may have been written before
// the Action field was added) and whose Query carries actual SQL. The user-
// count probe is run-wide so we cite the broadest evidence available.
//
// Whitespace-only Query strings are skipped to stay consistent with
// render.isExecutableQueryStep — including such IDs would just inflate the
// list with steps that contribute no rendered evidence anyway.
func collectQueryStepIDs(log []models.ExplorationStep) []int {
	out := make([]int, 0, len(log))
	for _, s := range log {
		if s.Action != "" && s.Action != "query_data" {
			continue
		}
		if strings.TrimSpace(s.Query) == "" {
			continue
		}
		out = append(out, s.Step)
	}
	return out
}

// ValidateInsights validates affected counts in insights against total users.
// Returns ValidationResults and adjusts insight counts in-place.
func (v *UserCountValidator) ValidateInsights(
	ctx context.Context,
	insights []models.Insight,
) []models.ValidationResult {
	totalUsers, err := v.GetTotalUsers(ctx)
	if err != nil {
		applog.WithError(err).Warn("Could not fetch total users for validation")
		return nil
	}

	results := make([]models.ValidationResult, 0)

	for i, insight := range insights {
		if insight.AffectedCount <= 0 {
			continue
		}

		vr := models.ValidationResult{
			InsightID:    insight.ID,
			AnalysisArea: insight.AnalysisArea,
			ValidatedAt:  time.Now(),
			ClaimedCount: insight.AffectedCount,
			VerifiedCount: insight.AffectedCount,
		}

		if insight.AffectedCount <= totalUsers {
			vr.Status = "confirmed"
			vr.Reasoning = fmt.Sprintf("Count %d is within total users (%d)", insight.AffectedCount, totalUsers)
		} else {
			ratio := float64(insight.AffectedCount) / float64(totalUsers)

			if ratio > 10 {
				// Likely counting events/sessions, not unique users
				adjusted := totalUsers / 10
				vr.Status = "adjusted"
				vr.VerifiedCount = adjusted
				vr.Reasoning = fmt.Sprintf("Count %d is %.1fx total users (%d). Likely counting events, not unique users. Adjusted to %d.",
					insight.AffectedCount, ratio, totalUsers, adjusted)
				insights[i].AffectedCount = adjusted
			} else {
				// Slightly over, might be double-counting
				adjusted := int(float64(totalUsers) * 0.8)
				vr.Status = "adjusted"
				vr.VerifiedCount = adjusted
				vr.Reasoning = fmt.Sprintf("Count %d exceeds total users (%d). Adjusted to %d.",
					insight.AffectedCount, totalUsers, adjusted)
				insights[i].AffectedCount = adjusted
			}

			applog.WithFields(applog.Fields{
				"insight":   insight.Name,
				"area":      insight.AnalysisArea,
				"original":  insight.AffectedCount,
				"adjusted":  vr.VerifiedCount,
				"total":     totalUsers,
			}).Warn("User count adjusted")
		}

		// Store validation on insight
		insights[i].Validation = &models.InsightValidation{ //nolint:gosec // index bounded by insights slice length
			Status:        vr.Status,
			VerifiedCount: vr.VerifiedCount,
			OriginalCount: vr.ClaimedCount,
			Reasoning:     vr.Reasoning,
			ValidatedAt:   vr.ValidatedAt,
		}

		results = append(results, vr)
	}

	applog.WithFields(applog.Fields{
		"total_validations": len(results),
		"total_users":       totalUsers,
	}).Info("User count validation completed")

	return results
}

// ValidateRecommendations validates segment sizes in recommendations.
func (v *UserCountValidator) ValidateRecommendations(
	ctx context.Context,
	recommendations []models.Recommendation,
) []models.ValidationResult {
	totalUsers, err := v.GetTotalUsers(ctx)
	if err != nil {
		return nil
	}

	results := make([]models.ValidationResult, 0)

	for i, rec := range recommendations {
		if rec.SegmentSize <= 0 {
			continue
		}

		vr := models.ValidationResult{
			InsightID:    rec.ID,
			AnalysisArea: rec.Category,
			ValidatedAt:  time.Now(),
			ClaimedCount: rec.SegmentSize,
			VerifiedCount: rec.SegmentSize,
		}

		if rec.SegmentSize > totalUsers {
			adjusted := int(float64(totalUsers) * 0.8)
			vr.Status = "adjusted"
			vr.VerifiedCount = adjusted
			vr.Reasoning = fmt.Sprintf("Segment size %d exceeds total users (%d). Adjusted to %d.",
				rec.SegmentSize, totalUsers, adjusted)
			recommendations[i].SegmentSize = adjusted
		} else {
			vr.Status = "confirmed"
			vr.Reasoning = fmt.Sprintf("Segment size %d is within total users (%d)", rec.SegmentSize, totalUsers)
		}

		results = append(results, vr)
	}

	return results
}
