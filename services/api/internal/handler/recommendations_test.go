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

type mockRecommendationRepo struct {
	recs []*commonmodels.StandaloneRecommendation
}

func (m *mockRecommendationRepo) Create(_ context.Context, _ *commonmodels.StandaloneRecommendation) error {
	return nil
}
func (m *mockRecommendationRepo) CreateMany(_ context.Context, _ []*commonmodels.StandaloneRecommendation) error {
	return nil
}
func (m *mockRecommendationRepo) GetByID(_ context.Context, id string) (*commonmodels.StandaloneRecommendation, error) {
	for _, rec := range m.recs {
		if rec.ID == id {
			return rec, nil
		}
	}
	return nil, context.DeadlineExceeded
}
func (m *mockRecommendationRepo) ListByProject(_ context.Context, projectID string, limit, offset int) ([]*commonmodels.StandaloneRecommendation, error) {
	var result []*commonmodels.StandaloneRecommendation
	for _, rec := range m.recs {
		if rec.ProjectID == projectID {
			result = append(result, rec)
		}
	}
	return result, nil
}
func (m *mockRecommendationRepo) ListByDiscovery(_ context.Context, _ string) ([]*commonmodels.StandaloneRecommendation, error) {
	return nil, nil
}
func (m *mockRecommendationRepo) CountByProject(_ context.Context, _ string) (int64, error) {
	return int64(len(m.recs)), nil
}
func (m *mockRecommendationRepo) UpdateEmbedding(_ context.Context, _ string, _, _ string) error {
	return nil
}
func (m *mockRecommendationRepo) UpdateDuplicate(_ context.Context, _ string, _ string, _ float64) error {
	return nil
}

func TestRecommendationsHandler_List(t *testing.T) {
	repo := &mockRecommendationRepo{
		recs: []*commonmodels.StandaloneRecommendation{
			{
				ID:                     "11111111-1111-4111-8111-111111111111",
				ProjectID:              "proj-1",
				Title:                  "Add retry mechanics",
				RecommendationCategory: "engagement",
				Priority:               1,
				CreatedAt:              time.Now(),
			},
		},
	}

	h := NewRecommendationsHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/recommendations", nil)
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()

	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.([]interface{})
	if len(data) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(data))
	}
}

func TestRecommendationsHandler_Get(t *testing.T) {
	repo := &mockRecommendationRepo{
		recs: []*commonmodels.StandaloneRecommendation{
			{
				ID:        "11111111-1111-4111-8111-111111111111",
				ProjectID: "proj-1",
				Title:     "Add retry mechanics",
			},
		},
	}

	h := NewRecommendationsHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/recommendations/11111111-1111-4111-8111-111111111111", nil)
	req.SetPathValue("id", "proj-1")
	req.SetPathValue("recId", "11111111-1111-4111-8111-111111111111")
	w := httptest.NewRecorder()

	h.Get(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["title"] != "Add retry mechanics" {
		t.Errorf("expected title=Add retry mechanics, got %v", data["title"])
	}
}

func TestRecommendationsHandler_GetNotFound(t *testing.T) {
	repo := &mockRecommendationRepo{}
	h := NewRecommendationsHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/recommendations/nonexistent", nil)
	req.SetPathValue("id", "proj-1")
	req.SetPathValue("recId", "nonexistent")
	w := httptest.NewRecorder()

	h.Get(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}
