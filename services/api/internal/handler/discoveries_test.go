package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
)

func TestDiscoveriesHandler_GetByDate_InvalidDate(t *testing.T) {
	h := &DiscoveriesHandler{}

	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/discoveries/not-a-date", nil)
	req.SetPathValue("id", "proj-1")
	req.SetPathValue("date", "not-a-date")
	w := httptest.NewRecorder()

	h.GetByDate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid date", w.Code)
	}

	var resp APIResponse
	decodeResponseBody(w, &resp)
	if !strings.Contains(resp.Error, "invalid date format") {
		t.Errorf("error = %q, should contain 'invalid date format'", resp.Error)
	}
}

func TestDiscoveriesHandler_GetByDate_WrongFormat(t *testing.T) {
	h := &DiscoveriesHandler{}

	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/discoveries/03-15-2026", nil)
	req.SetPathValue("id", "proj-1")
	req.SetPathValue("date", "03-15-2026")
	w := httptest.NewRecorder()

	h.GetByDate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for MM-DD-YYYY format", w.Code)
	}
}

func TestNewDiscoveriesHandler(t *testing.T) {
	h := NewDiscoveriesHandler(nil, nil, nil, nil)
	if h == nil {
		t.Fatal("NewDiscoveriesHandler returned nil")
	}
}

func TestGetEnvOrDefault(t *testing.T) {
	// Test the getEnvOrDefault helper used by discoveries handler
	// This is a package-level function in discoveries.go

	// Test with unset env var
	val := getEnvOrDefault("NONEXISTENT_TEST_VAR_12345", "fallback")
	if val != "fallback" {
		t.Errorf("got %q, want %q", val, "fallback")
	}

	// Test with set env var
	t.Setenv("TEST_GETENV_VAR", "custom")
	val = getEnvOrDefault("TEST_GETENV_VAR", "fallback")
	if val != "custom" {
		t.Errorf("got %q, want %q", val, "custom")
	}
}

// decodeResponseBody is a helper for tests in this file.
func decodeResponseBody(w *httptest.ResponseRecorder, resp *APIResponse) {
	_ = decodeJSON(httptest.NewRequest("POST", "/", w.Body), resp)
}

// --- Mock-based unit tests ---

func TestDiscoveriesHandler_List_Success_MockRepo(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, newMockRunner())

	// Create a project
	p := &models.Project{Name: "Test", Domain: "gaming", Category: "match3"}
	projRepo.Create(context.Background(), p)

	// Add discoveries
	discRepo.add(&models.DiscoveryResult{
		ID:            "disc-1",
		ProjectID:     p.ID,
		DiscoveryDate: time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC),
		TotalSteps:    50,
		Insights:      []models.Insight{{ID: "i-1", Name: "Churn spike"}},
	})
	discRepo.add(&models.DiscoveryResult{
		ID:            "disc-2",
		ProjectID:     p.ID,
		DiscoveryDate: time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC),
		TotalSteps:    75,
	})

	req := httptest.NewRequest("GET", "/api/v1/projects/"+p.ID+"/discoveries", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()

	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	results, ok := resp.Data.([]interface{})
	if !ok {
		t.Fatal("response data should be an array")
	}
	if len(results) != 2 {
		t.Errorf("discovery count = %d, want 2", len(results))
	}
}

func TestDiscoveriesHandler_List_ProjectNotFound_MockRepo(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, newMockRunner())

	req := httptest.NewRequest("GET", "/api/v1/projects/nonexistent/discoveries", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()

	h.List(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestDiscoveriesHandler_GetLatest_Success_MockRepo(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, newMockRunner())

	discRepo.add(&models.DiscoveryResult{
		ID:            "disc-old",
		ProjectID:     "proj-1",
		DiscoveryDate: time.Date(2026, 3, 19, 0, 0, 0, 0, time.UTC),
		TotalSteps:    30,
	})
	discRepo.add(&models.DiscoveryResult{
		ID:            "disc-new",
		ProjectID:     "proj-1",
		DiscoveryDate: time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC),
		TotalSteps:    50,
		Insights:      []models.Insight{{ID: "i-1", Name: "Latest insight"}},
	})

	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/discoveries/latest", nil)
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()

	h.GetLatest(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["id"] != "disc-new" {
		t.Errorf("id = %v, want 'disc-new' (latest)", data["id"])
	}
}

func TestDiscoveriesHandler_GetLatest_NotFound_MockRepo(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, newMockRunner())

	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/discoveries/latest", nil)
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()

	h.GetLatest(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp.Error, "no discoveries found") {
		t.Errorf("error = %q, want 'no discoveries found'", resp.Error)
	}
}

func TestDiscoveriesHandler_GetByDate_Success_MockRepo(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, newMockRunner())

	discRepo.add(&models.DiscoveryResult{
		ID:            "disc-march20",
		ProjectID:     "proj-1",
		DiscoveryDate: time.Date(2026, 3, 20, 10, 30, 0, 0, time.UTC),
		TotalSteps:    40,
	})

	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/discoveries/2026-03-20", nil)
	req.SetPathValue("id", "proj-1")
	req.SetPathValue("date", "2026-03-20")
	w := httptest.NewRecorder()

	h.GetByDate(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["id"] != "disc-march20" {
		t.Errorf("id = %v, want 'disc-march20'", data["id"])
	}
}

func TestDiscoveriesHandler_GetByDate_NoMatch_MockRepo(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, newMockRunner())

	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/discoveries/2026-01-01", nil)
	req.SetPathValue("id", "proj-1")
	req.SetPathValue("date", "2026-01-01")
	w := httptest.NewRecorder()

	h.GetByDate(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestDiscoveriesHandler_GetDiscoveryByID_Success_MockRepo(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, newMockRunner())

	discRepo.add(&models.DiscoveryResult{
		ID:            "disc-abc",
		ProjectID:     "proj-1",
		DiscoveryDate: time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC),
		TotalSteps:    60,
		Insights: []models.Insight{
			{ID: "i-1", Name: "Test insight", Severity: "high"},
		},
	})

	req := httptest.NewRequest("GET", "/api/v1/discoveries/disc-abc", nil)
	req.SetPathValue("id", "disc-abc")
	w := httptest.NewRecorder()

	h.GetDiscoveryByID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["id"] != "disc-abc" {
		t.Errorf("id = %v, want 'disc-abc'", data["id"])
	}
}

func TestDiscoveriesHandler_GetDiscoveryByID_NotFound_MockRepo(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, newMockRunner())

	req := httptest.NewRequest("GET", "/api/v1/discoveries/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()

	h.GetDiscoveryByID(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestDiscoveriesHandler_GetStatus_Success_MockRepo(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, newMockRunner())

	// Create a project
	p := &models.Project{Name: "Test", Domain: "gaming", Category: "match3"}
	projRepo.Create(context.Background(), p)

	// Add a run
	runRepo.addRun(&models.DiscoveryRun{
		ID:        "run-1",
		ProjectID: p.ID,
		Status:    "running",
		Phase:     "exploration",
		StartedAt: time.Now(),
	})

	// Add a completed discovery
	discRepo.add(&models.DiscoveryResult{
		ID:            "disc-1",
		ProjectID:     p.ID,
		DiscoveryDate: time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC),
		TotalSteps:    50,
		Insights:      []models.Insight{{ID: "i-1"}, {ID: "i-2"}},
	})

	req := httptest.NewRequest("GET", "/api/v1/projects/"+p.ID+"/status", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()

	h.GetStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["project_id"] != p.ID {
		t.Errorf("project_id = %v, want %q", data["project_id"], p.ID)
	}
	if data["run"] == nil {
		t.Error("status should include run info")
	}
	if data["last_discovery"] == nil {
		t.Error("status should include last_discovery info")
	}
	ld := data["last_discovery"].(map[string]interface{})
	insightsCount := ld["insights_count"].(float64)
	if insightsCount != 2 {
		t.Errorf("insights_count = %v, want 2", insightsCount)
	}
}

func TestDiscoveriesHandler_GetStatus_ProjectNotFound_MockRepo(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, newMockRunner())

	req := httptest.NewRequest("GET", "/api/v1/projects/nonexistent/status", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()

	h.GetStatus(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestDiscoveriesHandler_GetStatus_NoRunsOrDiscoveries_MockRepo(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, newMockRunner())

	p := &models.Project{Name: "Test", Domain: "gaming", Category: "match3"}
	projRepo.Create(context.Background(), p)

	req := httptest.NewRequest("GET", "/api/v1/projects/"+p.ID+"/status", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()

	h.GetStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["run"] != nil {
		t.Error("run should be nil when no runs exist")
	}
	if data["last_discovery"] != nil {
		t.Error("last_discovery should be nil when no discoveries exist")
	}
}

func TestDiscoveriesHandler_TriggerDiscovery_Success_MockRepo(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	mockRun := newMockRunner()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, mockRun)

	p := &models.Project{Name: "Test", Domain: "gaming", Category: "match3"}
	projRepo.Create(context.Background(), p)

	req := httptest.NewRequest("POST", "/api/v1/projects/"+p.ID+"/discover",
		strings.NewReader(`{"areas":["churn"],"max_steps":50}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()

	h.TriggerDiscovery(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["status"] != "started" {
		t.Errorf("status = %v, want 'started'", data["status"])
	}
	if data["run_id"] == nil || data["run_id"] == "" {
		t.Error("should have run_id")
	}

	// Verify runner was called
	if len(mockRun.runCalls) != 1 {
		t.Fatalf("runner should have been called once, got %d", len(mockRun.runCalls))
	}
	if mockRun.runCalls[0].ProjectID != p.ID {
		t.Errorf("runner projectID = %q, want %q", mockRun.runCalls[0].ProjectID, p.ID)
	}

	// Verify run was created in repo
	if len(runRepo.runs) != 1 {
		t.Errorf("runRepo should have 1 run, got %d", len(runRepo.runs))
	}
}

func TestDiscoveriesHandler_TriggerDiscovery_ProjectNotFound_MockRepo(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, newMockRunner())

	req := httptest.NewRequest("POST", "/api/v1/projects/nonexistent/discover", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()

	h.TriggerDiscovery(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestDiscoveriesHandler_TriggerDiscovery_AlreadyRunning_MockRepo(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, newMockRunner())

	p := &models.Project{Name: "Test", Domain: "gaming", Category: "match3"}
	projRepo.Create(context.Background(), p)

	// Add a running run for this project
	runRepo.addRun(&models.DiscoveryRun{
		ID:        "existing-run",
		ProjectID: p.ID,
		Status:    "running",
		Phase:     "exploration",
		StartedAt: time.Now(),
	})

	req := httptest.NewRequest("POST", "/api/v1/projects/"+p.ID+"/discover", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()

	h.TriggerDiscovery(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["status"] != "already_running" {
		t.Errorf("status = %v, want 'already_running'", data["status"])
	}
	if data["run_id"] != "existing-run" {
		t.Errorf("run_id = %v, want 'existing-run'", data["run_id"])
	}
}

func TestDiscoveriesHandler_TriggerDiscovery_RunnerFails_MockRepo(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	mockRun := newMockRunner()
	mockRun.runErr = fmt.Errorf("binary not found")
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, mockRun)

	p := &models.Project{Name: "Test", Domain: "gaming", Category: "match3"}
	projRepo.Create(context.Background(), p)

	req := httptest.NewRequest("POST", "/api/v1/projects/"+p.ID+"/discover", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()

	h.TriggerDiscovery(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp.Error, "binary not found") {
		t.Errorf("error = %q, should contain runner error", resp.Error)
	}

	// Verify the run was marked as failed
	for _, run := range runRepo.runs {
		if run.Status != "failed" {
			t.Errorf("run status = %q, want 'failed'", run.Status)
		}
	}
}

func TestDiscoveriesHandler_GetRun_Success_MockRepo(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, newMockRunner())

	runRepo.addRun(&models.DiscoveryRun{
		ID:        "run-abc",
		ProjectID: "proj-1",
		Status:    "running",
		Phase:     "analysis",
		Progress:  60,
		StartedAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/api/v1/runs/run-abc", nil)
	req.SetPathValue("runId", "run-abc")
	w := httptest.NewRecorder()

	h.GetRun(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["id"] != "run-abc" {
		t.Errorf("id = %v, want 'run-abc'", data["id"])
	}
	if data["status"] != "running" {
		t.Errorf("status = %v, want 'running'", data["status"])
	}
}

func TestDiscoveriesHandler_GetRun_NotFound_MockRepo(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, newMockRunner())

	req := httptest.NewRequest("GET", "/api/v1/runs/nonexistent", nil)
	req.SetPathValue("runId", "nonexistent")
	w := httptest.NewRecorder()

	h.GetRun(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestDiscoveriesHandler_CancelRun_Success_MockRepo(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	mockRun := newMockRunner()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, mockRun)

	runRepo.addRun(&models.DiscoveryRun{
		ID:        "run-to-cancel",
		ProjectID: "proj-1",
		Status:    "running",
		Phase:     "exploration",
		StartedAt: time.Now(),
	})

	req := httptest.NewRequest("DELETE", "/api/v1/runs/run-to-cancel", nil)
	req.SetPathValue("runId", "run-to-cancel")
	w := httptest.NewRecorder()

	h.CancelRun(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["status"] != "cancelled" {
		t.Errorf("status = %v, want 'cancelled'", data["status"])
	}

	// Verify runner.Cancel was called
	if len(mockRun.canceled) != 1 {
		t.Fatalf("runner cancel should have been called once, got %d", len(mockRun.canceled))
	}
	if mockRun.canceled[0] != "run-to-cancel" {
		t.Errorf("canceled run = %q, want 'run-to-cancel'", mockRun.canceled[0])
	}

	// Verify run status was updated
	run := runRepo.runs["run-to-cancel"]
	if run.Status != "cancelled" {
		t.Errorf("run status = %q, want 'cancelled'", run.Status)
	}
}

func TestDiscoveriesHandler_CancelRun_NotFound_MockRepo(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, newMockRunner())

	req := httptest.NewRequest("DELETE", "/api/v1/runs/nonexistent", nil)
	req.SetPathValue("runId", "nonexistent")
	w := httptest.NewRecorder()

	h.CancelRun(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestDiscoveriesHandler_CancelRun_NotActive_MockRepo(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, newMockRunner())

	// Add a completed run
	now := time.Now()
	runRepo.addRun(&models.DiscoveryRun{
		ID:          "run-done",
		ProjectID:   "proj-1",
		Status:      "completed",
		Phase:       "done",
		StartedAt:   now,
		CompletedAt: &now,
	})

	req := httptest.NewRequest("DELETE", "/api/v1/runs/run-done", nil)
	req.SetPathValue("runId", "run-done")
	w := httptest.NewRecorder()

	h.CancelRun(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp.Error, "not active") {
		t.Errorf("error = %q, should contain 'not active'", resp.Error)
	}
}

func TestDiscoveriesHandler_CancelRun_PendingStatus_MockRepo(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	mockRun := newMockRunner()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, mockRun)

	// Pending runs should also be cancellable
	runRepo.addRun(&models.DiscoveryRun{
		ID:        "run-pending",
		ProjectID: "proj-1",
		Status:    "pending",
		Phase:     "starting",
		StartedAt: time.Now(),
	})

	req := httptest.NewRequest("DELETE", "/api/v1/runs/run-pending", nil)
	req.SetPathValue("runId", "run-pending")
	w := httptest.NewRecorder()

	h.CancelRun(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (pending runs are active)", w.Code)
	}
}
