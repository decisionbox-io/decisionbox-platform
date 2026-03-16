package validation

import (
	"context"
	"fmt"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/debug"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// UserCountValidator validates user counts in discovery insights against warehouse totals.
type UserCountValidator struct {
	warehouse   gowarehouse.Provider
	debugLogger *debug.Logger
	dataset     string
	filter      string // e.g., "WHERE app_id = 'xyz'" or ""

	totalUsers       int
	totalUsersCached bool
}

// UserCountValidatorOptions configures the validator.
type UserCountValidatorOptions struct {
	Warehouse   gowarehouse.Provider
	DebugLogger *debug.Logger
	Dataset     string
	Filter      string
}

// NewUserCountValidator creates a new user count validator.
func NewUserCountValidator(opts UserCountValidatorOptions) *UserCountValidator {
	return &UserCountValidator{
		warehouse:   opts.Warehouse,
		debugLogger: opts.DebugLogger,
		dataset:     opts.Dataset,
		filter:      opts.Filter,
	}
}

// GetTotalUsers fetches the total unique users from the warehouse.
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

	var lastErr error
	for _, query := range queries {
		results, err := v.warehouse.Query(ctx, query, nil)
		if err != nil {
			lastErr = err
			continue
		}

		if len(results.Rows) > 0 {
			if totalUsers, ok := results.Rows[0]["total_users"]; ok {
				var count int
				switch t := totalUsers.(type) {
				case int:
					count = t
				case int64:
					count = int(t)
				case float64:
					count = int(t)
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
		}
	}

	if lastErr != nil {
		return 0, fmt.Errorf("failed to fetch total users: %w", lastErr)
	}

	return 0, fmt.Errorf("could not determine total users")
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
