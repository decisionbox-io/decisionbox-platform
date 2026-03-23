package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/internal/models"
)

func TestFeedbackHandler_Submit_MissingFields(t *testing.T) {
	h := NewFeedbackHandler(nil)

	// Empty body
	req := httptest.NewRequest("POST", "/api/v1/discoveries/run1/feedback",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("runId", "run1")
	w := httptest.NewRecorder()

	h.Submit(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestFeedbackHandler_Submit_InvalidRating(t *testing.T) {
	h := NewFeedbackHandler(nil)

	req := httptest.NewRequest("POST", "/api/v1/discoveries/run1/feedback",
		strings.NewReader(`{"target_type":"insight","target_id":"0","rating":"meh"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("runId", "run1")
	w := httptest.NewRecorder()

	h.Submit(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error == "" {
		t.Error("should have error message")
	}
}

func TestFeedbackHandler_Submit_InvalidTargetType(t *testing.T) {
	h := NewFeedbackHandler(nil)

	req := httptest.NewRequest("POST", "/api/v1/discoveries/run1/feedback",
		strings.NewReader(`{"target_type":"sql_query","target_id":"0","rating":"like"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("runId", "run1")
	w := httptest.NewRecorder()

	h.Submit(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestFeedbackHandler_Submit_InvalidBody(t *testing.T) {
	h := NewFeedbackHandler(nil)

	req := httptest.NewRequest("POST", "/api/v1/discoveries/run1/feedback",
		strings.NewReader(`not json`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("runId", "run1")
	w := httptest.NewRecorder()

	h.Submit(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid body", w.Code)
	}
}

func TestFeedbackHandler_Submit_EmptyRating(t *testing.T) {
	h := NewFeedbackHandler(nil)

	req := httptest.NewRequest("POST", "/api/v1/discoveries/run1/feedback",
		strings.NewReader(`{"target_type":"insight","target_id":"0","rating":""}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("runId", "run1")
	w := httptest.NewRecorder()

	h.Submit(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("empty rating: status = %d, want 400", w.Code)
	}
}

func TestFeedbackHandler_Submit_EmptyTargetID(t *testing.T) {
	h := NewFeedbackHandler(nil)

	req := httptest.NewRequest("POST", "/api/v1/discoveries/run1/feedback",
		strings.NewReader(`{"target_type":"insight","target_id":"","rating":"like"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("runId", "run1")
	w := httptest.NewRecorder()

	h.Submit(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("empty target_id: status = %d, want 400", w.Code)
	}
}

// --- Mock-based unit tests ---

func TestFeedbackHandler_Submit_Success_MockRepo(t *testing.T) {
	repo := newMockFeedbackRepo()
	h := NewFeedbackHandler(repo)

	body := `{"project_id":"proj-1","target_type":"insight","target_id":"i-0","rating":"like","comment":"great insight"}`
	req := httptest.NewRequest("POST", "/api/v1/discoveries/disc-1/feedback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("runId", "disc-1")
	w := httptest.NewRecorder()

	h.Submit(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["id"] == nil || data["id"] == "" {
		t.Error("feedback should have an id")
	}
	if data["rating"] != "like" {
		t.Errorf("rating = %v, want 'like'", data["rating"])
	}
	if data["target_type"] != "insight" {
		t.Errorf("target_type = %v, want 'insight'", data["target_type"])
	}
	if data["discovery_id"] != "disc-1" {
		t.Errorf("discovery_id = %v, want 'disc-1'", data["discovery_id"])
	}

	// Verify stored
	if len(repo.items) != 1 {
		t.Errorf("repo should have 1 feedback, got %d", len(repo.items))
	}
}

func TestFeedbackHandler_Submit_Upsert_MockRepo(t *testing.T) {
	repo := newMockFeedbackRepo()
	h := NewFeedbackHandler(repo)

	// Submit initial feedback
	body := `{"project_id":"proj-1","target_type":"insight","target_id":"i-0","rating":"like"}`
	req := httptest.NewRequest("POST", "/api/v1/discoveries/disc-1/feedback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("runId", "disc-1")
	w := httptest.NewRecorder()
	h.Submit(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("first submit: status = %d, want 200", w.Code)
	}

	// Submit again with different rating (should upsert, not create new)
	body = `{"project_id":"proj-1","target_type":"insight","target_id":"i-0","rating":"dislike"}`
	req = httptest.NewRequest("POST", "/api/v1/discoveries/disc-1/feedback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("runId", "disc-1")
	w = httptest.NewRecorder()
	h.Submit(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("second submit: status = %d, want 200", w.Code)
	}

	// Should still be 1 item (upserted)
	if len(repo.items) != 1 {
		t.Errorf("repo should have 1 feedback after upsert, got %d", len(repo.items))
	}
	if repo.items[0].Rating != "dislike" {
		t.Errorf("rating = %q, want 'dislike' after upsert", repo.items[0].Rating)
	}
}

func TestFeedbackHandler_Submit_RecommendationType_MockRepo(t *testing.T) {
	repo := newMockFeedbackRepo()
	h := NewFeedbackHandler(repo)

	body := `{"project_id":"proj-1","target_type":"recommendation","target_id":"r-0","rating":"dislike"}`
	req := httptest.NewRequest("POST", "/api/v1/discoveries/disc-1/feedback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("runId", "disc-1")
	w := httptest.NewRecorder()

	h.Submit(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestFeedbackHandler_Submit_RepoError_MockRepo(t *testing.T) {
	repo := newMockFeedbackRepo()
	repo.upsertErr = fmt.Errorf("write conflict")
	h := NewFeedbackHandler(repo)

	body := `{"project_id":"proj-1","target_type":"insight","target_id":"i-0","rating":"like"}`
	req := httptest.NewRequest("POST", "/api/v1/discoveries/disc-1/feedback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("runId", "disc-1")
	w := httptest.NewRecorder()

	h.Submit(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestFeedbackHandler_List_Success_MockRepo(t *testing.T) {
	repo := newMockFeedbackRepo()
	h := NewFeedbackHandler(repo)

	// Add some feedback items
	repo.Upsert(context.Background(), &models.Feedback{
		DiscoveryID: "disc-1",
		TargetType:  "insight",
		TargetID:    "i-0",
		Rating:      "like",
	})
	repo.Upsert(context.Background(), &models.Feedback{
		DiscoveryID: "disc-1",
		TargetType:  "recommendation",
		TargetID:    "r-0",
		Rating:      "dislike",
	})
	repo.Upsert(context.Background(), &models.Feedback{
		DiscoveryID: "disc-2",
		TargetType:  "insight",
		TargetID:    "i-1",
		Rating:      "like",
	})

	req := httptest.NewRequest("GET", "/api/v1/discoveries/disc-1/feedback", nil)
	req.SetPathValue("runId", "disc-1")
	w := httptest.NewRecorder()

	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	items, ok := resp.Data.([]interface{})
	if !ok {
		t.Fatal("response data should be an array")
	}
	if len(items) != 2 {
		t.Errorf("feedback count = %d, want 2 (only disc-1)", len(items))
	}
}

func TestFeedbackHandler_List_Empty_MockRepo(t *testing.T) {
	repo := newMockFeedbackRepo()
	h := NewFeedbackHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/discoveries/disc-no-feedback/feedback", nil)
	req.SetPathValue("runId", "disc-no-feedback")
	w := httptest.NewRecorder()

	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestFeedbackHandler_List_RepoError_MockRepo(t *testing.T) {
	repo := newMockFeedbackRepo()
	repo.listErr = fmt.Errorf("database error")
	h := NewFeedbackHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/discoveries/disc-1/feedback", nil)
	req.SetPathValue("runId", "disc-1")
	w := httptest.NewRecorder()

	h.List(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestFeedbackHandler_Delete_Success_MockRepo(t *testing.T) {
	repo := newMockFeedbackRepo()
	h := NewFeedbackHandler(repo)

	// Create a feedback item
	fb, _ := repo.Upsert(context.Background(), &models.Feedback{
		DiscoveryID: "disc-1",
		TargetType:  "insight",
		TargetID:    "i-0",
		Rating:      "like",
	})

	req := httptest.NewRequest("DELETE", "/api/v1/feedback/"+fb.ID, nil)
	req.SetPathValue("id", fb.ID)
	w := httptest.NewRecorder()

	h.Delete(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["status"] != "deleted" {
		t.Errorf("status = %v, want 'deleted'", data["status"])
	}

	// Verify it's gone
	if len(repo.items) != 0 {
		t.Errorf("repo should have 0 items after delete, got %d", len(repo.items))
	}
}

func TestFeedbackHandler_Delete_NotFound_MockRepo(t *testing.T) {
	repo := newMockFeedbackRepo()
	h := NewFeedbackHandler(repo)

	req := httptest.NewRequest("DELETE", "/api/v1/feedback/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()

	h.Delete(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (repo returns error for missing)", w.Code)
	}
}

func TestFeedbackHandler_Delete_RepoError_MockRepo(t *testing.T) {
	repo := newMockFeedbackRepo()
	repo.deleteErr = fmt.Errorf("permission denied")
	h := NewFeedbackHandler(repo)

	// Create an item, but deleteErr will override
	fb, _ := repo.Upsert(context.Background(), &models.Feedback{
		DiscoveryID: "disc-1",
		TargetType:  "insight",
		TargetID:    "i-0",
		Rating:      "like",
	})

	req := httptest.NewRequest("DELETE", "/api/v1/feedback/"+fb.ID, nil)
	req.SetPathValue("id", fb.ID)
	w := httptest.NewRecorder()

	h.Delete(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}
