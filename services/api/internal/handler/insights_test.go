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

type mockInsightRepo struct {
	insights []*commonmodels.StandaloneInsight
	// lastFilter is what the handler asked for, so a test can assert the
	// query parameter reached the repository rather than only that the
	// response looked right.
	lastFilter database.ListFilter
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
func (m *mockInsightRepo) ListByProject(ctx context.Context, projectID string, limit, offset int) ([]*commonmodels.StandaloneInsight, error) {
	return m.ListByProjectFiltered(ctx, projectID, limit, offset, database.ListFilter{})
}

// ListByProjectFiltered mirrors the repository: marked duplicates are dropped
// unless the caller opted in. Modelling it rather than ignoring it is what
// makes the handler tests prove the flag is threaded all the way down.
func (m *mockInsightRepo) ListByProjectFiltered(_ context.Context, projectID string, limit, offset int, f database.ListFilter) ([]*commonmodels.StandaloneInsight, error) {
	m.lastFilter = f
	var result []*commonmodels.StandaloneInsight
	for _, ins := range m.insights {
		if ins.ProjectID != projectID {
			continue
		}
		if !f.IncludeDuplicates && ins.DuplicateOf != "" {
			continue
		}
		result = append(result, ins)
	}
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}
func (m *mockInsightRepo) ListByDiscovery(_ context.Context, _ string) ([]*commonmodels.StandaloneInsight, error) {
	return nil, nil
}
func (m *mockInsightRepo) ListByDiscoveryFiltered(_ context.Context, _ string, f database.ListFilter) ([]*commonmodels.StandaloneInsight, error) {
	m.lastFilter = f
	return nil, nil
}
func (m *mockInsightRepo) CountByProject(ctx context.Context, projectID string) (int64, error) {
	return m.CountByProjectFiltered(ctx, projectID, database.ListFilter{})
}
func (m *mockInsightRepo) CountByProjectFiltered(_ context.Context, _ string, f database.ListFilter) (int64, error) {
	m.lastFilter = f
	n := 0
	for _, ins := range m.insights {
		if !f.IncludeDuplicates && ins.DuplicateOf != "" {
			continue
		}
		n++
	}
	return int64(n), nil
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

// --- include_duplicates (acceptance criterion 6) ---

func TestInsightsHandler_HidesMarkedDuplicatesByDefault(t *testing.T) {
	repo := &mockInsightRepo{insights: []*commonmodels.StandaloneInsight{
		{ID: "a", ProjectID: "p1", Name: "original"},
		{ID: "b", ProjectID: "p1", Name: "restatement", DuplicateOf: "a", SimilarityScore: 0.93},
		{ID: "c", ProjectID: "p1", Name: "also fine"},
	}}
	h := NewInsightsHandler(repo)

	list := func(query string) []*commonmodels.StandaloneInsight {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/p1/insights"+query, nil)
		req.SetPathValue("id", "p1")
		w := httptest.NewRecorder()
		h.List(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("query %q: status %d", query, w.Code)
		}
		// The handlers wrap their payload in APIResponse, so the list has to
		// be decoded out of Data rather than off the body.
		var resp struct {
			Data []*commonmodels.StandaloneInsight `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("query %q: decode: %v", query, err)
		}
		return resp.Data
	}

	t.Run("default excludes", func(t *testing.T) {
		out := list("")
		if len(out) != 2 {
			t.Fatalf("got %d insights, want 2 (the marked duplicate hidden)", len(out))
		}
		for _, i := range out {
			if i.ID == "b" {
				t.Error("the marked duplicate must not be returned by default")
			}
		}
		if repo.lastFilter.IncludeDuplicates {
			t.Error("no parameter must reach the repository as IncludeDuplicates=false")
		}
	})

	t.Run("opt-in includes", func(t *testing.T) {
		for _, q := range []string{"?include_duplicates=1", "?include_duplicates=true"} {
			if out := list(q); len(out) != 3 {
				t.Errorf("%s returned %d insights, want all 3", q, len(out))
			}
			if !repo.lastFilter.IncludeDuplicates {
				t.Errorf("%s did not reach the repository", q)
			}
		}
	})

	t.Run("anything else is off", func(t *testing.T) {
		// These widen a response rather than change what it means, so the safe
		// reading of a value nobody meant is the default — not a 400.
		for _, q := range []string{
			"?include_duplicates=0",
			"?include_duplicates=false",
			"?include_duplicates=",
			"?include_duplicates=yes",
			"?include_duplicates=TRUE",
			"?include_duplicates=please",
		} {
			if out := list(q); len(out) != 2 {
				t.Errorf("%s returned %d insights, want 2", q, len(out))
			}
			if repo.lastFilter.IncludeDuplicates {
				t.Errorf("%s must not be read as an opt-in", q)
			}
		}
	})

	t.Run("the parameter composes with paging", func(t *testing.T) {
		if out := list("?include_duplicates=1&limit=1"); len(out) != 1 {
			t.Errorf("got %d, want 1", len(out))
		}
		if !repo.lastFilter.IncludeDuplicates {
			t.Error("limit must not clear the flag")
		}
	})
}
