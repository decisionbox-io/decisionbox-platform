package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/models"
)

func TestReads_MarkRead_Success(t *testing.T) {
	repo := newMockReadMarkRepo()
	h := NewReadsHandler(repo)

	req, w := newAuthedRequest("POST", "/api/v1/projects/p1/reads", `{"target_type":"insight","target_id":"i1"}`, "alice")
	req.SetPathValue("id", "p1")
	h.MarkRead(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if len(repo.items) != 1 {
		t.Errorf("expected 1 mark stored, got %d", len(repo.items))
	}
}

func TestReads_MarkRead_Idempotent(t *testing.T) {
	repo := newMockReadMarkRepo()
	h := NewReadsHandler(repo)

	for i := 0; i < 5; i++ {
		req, w := newAuthedRequest("POST", "/api/v1/projects/p1/reads", `{"target_type":"insight","target_id":"i1"}`, "alice")
		req.SetPathValue("id", "p1")
		h.MarkRead(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("iter %d status = %d", i, w.Code)
		}
	}
	if len(repo.items) != 1 {
		t.Errorf("expected 1 mark after repeated upserts, got %d", len(repo.items))
	}
}

func TestReads_MarkRead_InvalidTargetType(t *testing.T) {
	h := NewReadsHandler(newMockReadMarkRepo())

	req, w := newAuthedRequest("POST", "/api/v1/projects/p1/reads", `{"target_type":"step","target_id":"1"}`, "alice")
	req.SetPathValue("id", "p1")
	h.MarkRead(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestReads_MarkRead_MissingTargetID(t *testing.T) {
	h := NewReadsHandler(newMockReadMarkRepo())

	req, w := newAuthedRequest("POST", "/api/v1/projects/p1/reads", `{"target_type":"insight"}`, "alice")
	req.SetPathValue("id", "p1")
	h.MarkRead(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestReads_MarkRead_InvalidJSON(t *testing.T) {
	h := NewReadsHandler(newMockReadMarkRepo())

	req, w := newAuthedRequest("POST", "/api/v1/projects/p1/reads", `nope`, "alice")
	req.SetPathValue("id", "p1")
	h.MarkRead(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestReads_MarkRead_Unauthenticated(t *testing.T) {
	h := NewReadsHandler(newMockReadMarkRepo())

	req, w := newAuthedRequest("POST", "/api/v1/projects/p1/reads", `{"target_type":"insight","target_id":"i1"}`, "")
	req.SetPathValue("id", "p1")
	h.MarkRead(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestReads_MarkUnread_IdempotentWhenMissing(t *testing.T) {
	h := NewReadsHandler(newMockReadMarkRepo())

	req, w := newAuthedRequest("DELETE", "/api/v1/projects/p1/reads", `{"target_type":"insight","target_id":"never-read"}`, "alice")
	req.SetPathValue("id", "p1")
	h.MarkUnread(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204 (idempotent)", w.Code)
	}
}

func TestReads_MarkUnread_RemovesMark(t *testing.T) {
	repo := newMockReadMarkRepo()
	h := NewReadsHandler(repo)
	_ = repo.Upsert(context.Background(), &models.ReadMark{ProjectID: "p1", UserID: "alice", TargetType: "insight", TargetID: "i1"})

	req, w := newAuthedRequest("DELETE", "/api/v1/projects/p1/reads", `{"target_type":"insight","target_id":"i1"}`, "alice")
	req.SetPathValue("id", "p1")
	h.MarkUnread(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d", w.Code)
	}
	if len(repo.items) != 0 {
		t.Errorf("mark should be gone, got %d", len(repo.items))
	}
}

func TestReads_ListReadIDs_ScopedByUser(t *testing.T) {
	repo := newMockReadMarkRepo()
	ctx := context.Background()
	_ = repo.Upsert(ctx, &models.ReadMark{ProjectID: "p1", UserID: "alice", TargetType: "insight", TargetID: "i1"})
	_ = repo.Upsert(ctx, &models.ReadMark{ProjectID: "p1", UserID: "alice", TargetType: "insight", TargetID: "i2"})
	_ = repo.Upsert(ctx, &models.ReadMark{ProjectID: "p1", UserID: "alice", TargetType: "recommendation", TargetID: "r1"})
	_ = repo.Upsert(ctx, &models.ReadMark{ProjectID: "p1", UserID: "bob", TargetType: "insight", TargetID: "i1"})
	_ = repo.Upsert(ctx, &models.ReadMark{ProjectID: "p2", UserID: "alice", TargetType: "insight", TargetID: "i1"})

	h := NewReadsHandler(repo)

	req, w := newAuthedRequest("GET", "/api/v1/projects/p1/reads?target_type=insight", "", "alice")
	req.SetPathValue("id", "p1")
	h.ListReadIDs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp APIResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	ids := resp.Data.([]interface{})
	if len(ids) != 2 {
		t.Errorf("len = %d, want 2 (only alice's p1 insights)", len(ids))
	}
}

func TestReads_ListReadIDs_MissingTargetType(t *testing.T) {
	h := NewReadsHandler(newMockReadMarkRepo())

	req, w := newAuthedRequest("GET", "/api/v1/projects/p1/reads", "", "alice")
	req.SetPathValue("id", "p1")
	h.ListReadIDs(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestReads_ListReadIDs_Empty(t *testing.T) {
	h := NewReadsHandler(newMockReadMarkRepo())

	req, w := newAuthedRequest("GET", "/api/v1/projects/p1/reads?target_type=insight", "", "alice")
	req.SetPathValue("id", "p1")
	h.ListReadIDs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp APIResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	ids := resp.Data.([]interface{})
	if len(ids) != 0 {
		t.Errorf("len = %d, want 0", len(ids))
	}
}

func TestReads_RepoError_500(t *testing.T) {
	repo := newMockReadMarkRepo()
	repo.upsertErr = errSentinel("boom")
	h := NewReadsHandler(repo)

	req, w := newAuthedRequest("POST", "/api/v1/projects/p1/reads", `{"target_type":"insight","target_id":"i1"}`, "alice")
	req.SetPathValue("id", "p1")
	h.MarkRead(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}
