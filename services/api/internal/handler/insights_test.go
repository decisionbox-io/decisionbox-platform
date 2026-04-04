package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

type mockInsightRepo struct {
	insights []*commonmodels.StandaloneInsight
}

func (m *mockInsightRepo) Create(_ context.Context, _ *commonmodels.StandaloneInsight) error {
	return nil
}
func (m *mockInsightRepo) CreateMany(_ context.Context, _ []*commonmodels.StandaloneInsight) error {
	return nil
}
func (m *mockInsightRepo) GetByID(_ context.Context, id string) (*commonmodels.StandaloneInsight, error) {
	for _, ins := range m.insights {
		if ins.ID == id {
			return ins, nil
		}
	}
	return nil, context.DeadlineExceeded // simulate not found
}
func (m *mockInsightRepo) ListByProject(_ context.Context, projectID string, limit, offset int) ([]*commonmodels.StandaloneInsight, error) {
	var result []*commonmodels.StandaloneInsight
	for _, ins := range m.insights {
		if ins.ProjectID == projectID {
			result = append(result, ins)
		}
	}
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}
func (m *mockInsightRepo) ListByDiscovery(_ context.Context, _ string) ([]*commonmodels.StandaloneInsight, error) {
	return nil, nil
}
func (m *mockInsightRepo) CountByProject(_ context.Context, _ string) (int64, error) {
	return int64(len(m.insights)), nil
}
func (m *mockInsightRepo) UpdateEmbedding(_ context.Context, _ string, _, _ string) error {
	return nil
}
func (m *mockInsightRepo) UpdateDuplicate(_ context.Context, _ string, _ string, _ float64) error {
	return nil
}
func (m *mockInsightRepo) GetLatestEmbeddingModel(_ context.Context, _ string) (string, error) {
	return "", nil
}

func TestInsightsHandler_List(t *testing.T) {
	repo := &mockInsightRepo{
		insights: []*commonmodels.StandaloneInsight{
			{
				ID:           "11111111-1111-4111-8111-111111111111",
				ProjectID:    "proj-1",
				Name:         "High churn",
				Severity:     "high",
				AnalysisArea: "churn",
				CreatedAt:    time.Now(),
			},
			{
				ID:           "22222222-2222-4222-8222-222222222222",
				ProjectID:    "proj-1",
				Name:         "Low engagement",
				Severity:     "medium",
				AnalysisArea: "engagement",
				CreatedAt:    time.Now(),
			},
		},
	}

	h := NewInsightsHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/insights", nil)
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()

	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.([]interface{})
	if len(data) != 2 {
		t.Fatalf("expected 2 insights, got %d", len(data))
	}
}

func TestInsightsHandler_Get(t *testing.T) {
	repo := &mockInsightRepo{
		insights: []*commonmodels.StandaloneInsight{
			{
				ID:        "11111111-1111-4111-8111-111111111111",
				ProjectID: "proj-1",
				Name:      "High churn",
			},
		},
	}

	h := NewInsightsHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/insights/11111111-1111-4111-8111-111111111111", nil)
	req.SetPathValue("id", "proj-1")
	req.SetPathValue("insightId", "11111111-1111-4111-8111-111111111111")
	w := httptest.NewRecorder()

	h.Get(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["name"] != "High churn" {
		t.Errorf("expected name=High churn, got %v", data["name"])
	}
}

func TestInsightsHandler_GetNotFound(t *testing.T) {
	repo := &mockInsightRepo{}
	h := NewInsightsHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/insights/nonexistent", nil)
	req.SetPathValue("id", "proj-1")
	req.SetPathValue("insightId", "nonexistent")
	w := httptest.NewRecorder()

	h.Get(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}
