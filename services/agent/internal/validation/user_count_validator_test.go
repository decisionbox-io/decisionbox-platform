package validation

import (
	"context"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

func TestGetTotalUsers(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	wh.QueryResults["COUNT(DISTINCT user_id)"] = &gowarehouse.QueryResult{
		Columns: []string{"total_users"},
		Rows:    []map[string]interface{}{{"total_users": int64(5000)}},
	}

	v := NewUserCountValidator(UserCountValidatorOptions{
		Warehouse: wh,
		Dataset:   "test_dataset",
	})

	total, err := v.GetTotalUsers(context.Background())
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if total != 5000 {
		t.Errorf("total = %d, want 5000", total)
	}

	// Second call should use cache
	total2, _ := v.GetTotalUsers(context.Background())
	if total2 != 5000 {
		t.Error("should return cached value")
	}
}

func TestValidateInsightsConfirmed(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"total_users"},
		Rows:    []map[string]interface{}{{"total_users": int64(10000)}},
	}

	v := NewUserCountValidator(UserCountValidatorOptions{
		Warehouse: wh,
		Dataset:   "test_dataset",
	})

	insights := []models.Insight{
		{ID: "1", Name: "Test", AffectedCount: 500, AnalysisArea: "churn"},
		{ID: "2", Name: "Test 2", AffectedCount: 2000, AnalysisArea: "engagement"},
	}

	results := v.ValidateInsights(context.Background(), insights)

	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	for _, r := range results {
		if r.Status != "confirmed" {
			t.Errorf("insight %s should be confirmed, got %q", r.InsightID, r.Status)
		}
	}
}

func TestValidateInsightsAdjusted(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"total_users"},
		Rows:    []map[string]interface{}{{"total_users": int64(1000)}},
	}

	v := NewUserCountValidator(UserCountValidatorOptions{
		Warehouse: wh,
		Dataset:   "test_dataset",
	})

	insights := []models.Insight{
		{ID: "1", Name: "Overcounted", AffectedCount: 50000, AnalysisArea: "churn"}, // 50x total
	}

	results := v.ValidateInsights(context.Background(), insights)

	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].Status != "adjusted" {
		t.Errorf("should be adjusted, got %q", results[0].Status)
	}
	if insights[0].AffectedCount >= 50000 {
		t.Error("count should be adjusted down")
	}
	if insights[0].Validation == nil {
		t.Error("Validation should be set on insight")
	}
}

func TestValidateInsightsSlightlyOver(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"total_users"},
		Rows:    []map[string]interface{}{{"total_users": int64(1000)}},
	}

	v := NewUserCountValidator(UserCountValidatorOptions{
		Warehouse: wh,
		Dataset:   "test_dataset",
	})

	insights := []models.Insight{
		{ID: "1", Name: "Slightly over", AffectedCount: 1500, AnalysisArea: "churn"}, // 1.5x
	}

	results := v.ValidateInsights(context.Background(), insights)

	if results[0].Status != "adjusted" {
		t.Errorf("should be adjusted, got %q", results[0].Status)
	}
	// Should be adjusted to 80% of total
	if insights[0].AffectedCount != 800 {
		t.Errorf("adjusted count = %d, want 800", insights[0].AffectedCount)
	}
}

func TestValidateInsightsZeroCount(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"total_users"},
		Rows:    []map[string]interface{}{{"total_users": int64(1000)}},
	}

	v := NewUserCountValidator(UserCountValidatorOptions{
		Warehouse: wh,
		Dataset:   "test_dataset",
	})

	insights := []models.Insight{
		{ID: "1", Name: "Zero", AffectedCount: 0, AnalysisArea: "churn"},
	}

	results := v.ValidateInsights(context.Background(), insights)

	// Zero count insights should be skipped
	if len(results) != 0 {
		t.Errorf("should skip zero count insights, got %d results", len(results))
	}
}

func TestValidateRecommendations(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"total_users"},
		Rows:    []map[string]interface{}{{"total_users": int64(5000)}},
	}

	v := NewUserCountValidator(UserCountValidatorOptions{
		Warehouse: wh,
		Dataset:   "test_dataset",
	})

	recs := []models.Recommendation{
		{ID: "r1", Title: "Fix churn", SegmentSize: 3000, Category: "churn"},
		{ID: "r2", Title: "Revenue boost", SegmentSize: 10000, Category: "monetization"},
	}

	results := v.ValidateRecommendations(context.Background(), recs)

	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	if results[0].Status != "confirmed" {
		t.Error("3000 should be confirmed (within 5000)")
	}
	if results[1].Status != "adjusted" {
		t.Error("10000 should be adjusted (exceeds 5000)")
	}
}
