package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/api/database"
)

type mockRecommendationRepo struct {
	recs []*commonmodels.StandaloneRecommendation
	// lastFilter is what the handler asked for.
	lastFilter database.ListFilter
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
func (m *mockRecommendationRepo) ListByProject(ctx context.Context, projectID string, limit, offset int) ([]*commonmodels.StandaloneRecommendation, error) {
	return m.ListByProjectFiltered(ctx, projectID, limit, offset, database.ListFilter{})
}

// ListByProjectFiltered mirrors the repository: marked duplicates are dropped
// unless the caller opted in.
func (m *mockRecommendationRepo) ListByProjectFiltered(_ context.Context, projectID string, _, _ int, f database.ListFilter) ([]*commonmodels.StandaloneRecommendation, error) {
	m.lastFilter = f
	var result []*commonmodels.StandaloneRecommendation
	for _, rec := range m.recs {
		if rec.ProjectID != projectID {
			continue
		}
		if !f.IncludeDuplicates && rec.DuplicateOf != "" {
			continue
		}
		result = append(result, rec)
	}
	return result, nil
}
func (m *mockRecommendationRepo) ListByDiscovery(_ context.Context, _ string) ([]*commonmodels.StandaloneRecommendation, error) {
	return nil, nil
}
func (m *mockRecommendationRepo) ListByDiscoveryFiltered(_ context.Context, _ string, f database.ListFilter) ([]*commonmodels.StandaloneRecommendation, error) {
	m.lastFilter = f
	return nil, nil
}
func (m *mockRecommendationRepo) CountByProject(ctx context.Context, projectID string) (int64, error) {
	return m.CountByProjectFiltered(ctx, projectID, database.ListFilter{})
}
func (m *mockRecommendationRepo) CountByProjectFiltered(_ context.Context, _ string, f database.ListFilter) (int64, error) {
	m.lastFilter = f
	n := 0
	for _, rec := range m.recs {
		if !f.IncludeDuplicates && rec.DuplicateOf != "" {
			continue
		}
		n++
	}
	return int64(n), nil
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

func TestRecommendationsHandler_HidesMarkedDuplicatesByDefault(t *testing.T) {
	repo := &mockRecommendationRepo{recs: []*commonmodels.StandaloneRecommendation{
		{ID: "a", ProjectID: "p1", Title: "original"},
		{ID: "b", ProjectID: "p1", Title: "restatement", DuplicateOf: "a", SimilarityScore: 0.91},
	}}
	h := NewRecommendationsHandler(repo)

	list := func(query string) []*commonmodels.StandaloneRecommendation {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/p1/recommendations"+query, nil)
		req.SetPathValue("id", "p1")
		w := httptest.NewRecorder()
		h.List(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("query %q: status %d", query, w.Code)
		}
		// The handlers wrap their payload in APIResponse, so the list has to
		// be decoded out of Data rather than off the body.
		var resp struct {
			Data []*commonmodels.StandaloneRecommendation `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("query %q: decode: %v", query, err)
		}
		return resp.Data
	}

	if out := list(""); len(out) != 1 || out[0].ID != "a" {
		t.Errorf("default listing = %+v, want only the original", out)
	}
	if out := list("?include_duplicates=true"); len(out) != 2 {
		t.Errorf("opt-in returned %d, want 2", len(out))
	}
	if !repo.lastFilter.IncludeDuplicates {
		t.Error("the opt-in did not reach the repository")
	}
}
