package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// newAuthedRequest wraps httptest.NewRequest so every test request carries a
// UserPrincipal — matching what the auth middleware sets in production.
// Returns the request and recorder so the caller can set path values.
func newAuthedRequest(method, url, body, userSub string) (*http.Request, *httptest.ResponseRecorder) {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, url, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, url, nil)
	}
	if userSub != "" {
		ctx := auth.WithUser(r.Context(), &auth.UserPrincipal{Sub: userSub})
		r = r.WithContext(ctx)
	}
	return r, httptest.NewRecorder()
}

func newListsHandler(lists *mockBookmarkListRepo, bookmarks *mockBookmarkRepo) (*ListsHandler, *mockInsightRepo, *mockRecommendationRepo) {
	ins := &mockInsightRepo{}
	rec := &mockRecommendationRepo{}
	return NewListsHandler(lists, bookmarks, ins, rec), ins, rec
}

// --- Create ---

func TestLists_Create_Success(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	h, _, _ := newListsHandler(lists, bms)

	req, w := newAuthedRequest("POST", "/api/v1/projects/p1/lists",
		`{"name":"Retention ideas","description":"churn + activation","color":"#2b7"}`, "anonymous")
	req.SetPathValue("id", "p1")

	h.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", w.Code, w.Body.String())
	}
	var resp APIResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["name"] != "Retention ideas" {
		t.Errorf("name = %v", data["name"])
	}
	if data["user_id"] != "anonymous" {
		t.Errorf("user_id = %v, want anonymous", data["user_id"])
	}
	if data["project_id"] != "p1" {
		t.Errorf("project_id = %v, want p1", data["project_id"])
	}
}

func TestLists_Create_PersistsEnterpriseSub(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	h, _, _ := newListsHandler(lists, bms)

	req, w := newAuthedRequest("POST", "/api/v1/projects/p1/lists", `{"name":"Mine"}`, "oidc|alice@acme")
	req.SetPathValue("id", "p1")
	h.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d", w.Code)
	}
	for _, l := range lists.lists {
		if l.UserID != "oidc|alice@acme" {
			t.Errorf("user_id = %q, want enterprise sub passthrough", l.UserID)
		}
	}
}

func TestLists_Create_MissingName(t *testing.T) {
	h, _, _ := newListsHandler(newMockBookmarkListRepo(), newMockBookmarkRepo())

	req, w := newAuthedRequest("POST", "/api/v1/projects/p1/lists", `{}`, "anonymous")
	req.SetPathValue("id", "p1")
	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestLists_Create_WhitespaceName(t *testing.T) {
	h, _, _ := newListsHandler(newMockBookmarkListRepo(), newMockBookmarkRepo())

	req, w := newAuthedRequest("POST", "/api/v1/projects/p1/lists", `{"name":"   "}`, "anonymous")
	req.SetPathValue("id", "p1")
	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestLists_Create_NameTooLong(t *testing.T) {
	h, _, _ := newListsHandler(newMockBookmarkListRepo(), newMockBookmarkRepo())

	huge := strings.Repeat("x", maxListNameLen+1)
	req, w := newAuthedRequest("POST", "/api/v1/projects/p1/lists",
		`{"name":"`+huge+`"}`, "anonymous")
	req.SetPathValue("id", "p1")
	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestLists_Create_InvalidJSON(t *testing.T) {
	h, _, _ := newListsHandler(newMockBookmarkListRepo(), newMockBookmarkRepo())

	req, w := newAuthedRequest("POST", "/api/v1/projects/p1/lists", `not json`, "anonymous")
	req.SetPathValue("id", "p1")
	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestLists_Create_Unauthenticated(t *testing.T) {
	h, _, _ := newListsHandler(newMockBookmarkListRepo(), newMockBookmarkRepo())

	req, w := newAuthedRequest("POST", "/api/v1/projects/p1/lists", `{"name":"x"}`, "")
	req.SetPathValue("id", "p1")
	h.Create(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// --- List ---

func TestLists_List_ReturnsOnlyCurrentUserLists(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	h, _, _ := newListsHandler(lists, bms)

	// alice's lists in project p1
	_ = lists.Create(context.Background(), &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "A1"})
	_ = lists.Create(context.Background(), &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "A2"})
	// bob's list in the same project (must not leak to alice)
	_ = lists.Create(context.Background(), &models.BookmarkList{ProjectID: "p1", UserID: "bob", Name: "B1"})
	// alice's list in a different project (must not leak to p1 queries)
	_ = lists.Create(context.Background(), &models.BookmarkList{ProjectID: "p2", UserID: "alice", Name: "A3"})

	req, w := newAuthedRequest("GET", "/api/v1/projects/p1/lists", "", "alice")
	req.SetPathValue("id", "p1")
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp APIResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	items := resp.Data.([]interface{})
	if len(items) != 2 {
		t.Fatalf("len = %d, want 2 (only alice's p1 lists)", len(items))
	}
}

func TestLists_List_Empty(t *testing.T) {
	h, _, _ := newListsHandler(newMockBookmarkListRepo(), newMockBookmarkRepo())

	req, w := newAuthedRequest("GET", "/api/v1/projects/p1/lists", "", "alice")
	req.SetPathValue("id", "p1")
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
}

// --- Get (list detail with resolved items) ---

func TestLists_Get_ResolvesItems(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	h, ins, rec := newListsHandler(lists, bms)

	ins.insights = append(ins.insights, &commonmodels.StandaloneInsight{ID: "i1", Name: "High churn"})
	rec.recs = append(rec.recs, &commonmodels.StandaloneRecommendation{ID: "r1", Title: "Add onboarding hint"})

	list := &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "L"}
	_ = lists.Create(context.Background(), list)
	_, _ = bms.Add(context.Background(), &models.Bookmark{ListID: list.ID, ProjectID: "p1", UserID: "alice", TargetType: "insight", TargetID: "i1"})
	_, _ = bms.Add(context.Background(), &models.Bookmark{ListID: list.ID, ProjectID: "p1", UserID: "alice", TargetType: "recommendation", TargetID: "r1"})

	req, w := newAuthedRequest("GET", "/api/v1/projects/p1/lists/"+list.ID, "", "alice")
	req.SetPathValue("id", "p1")
	req.SetPathValue("listId", list.ID)
	h.Get(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp APIResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	items := data["items"].([]interface{})
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2", len(items))
	}
	for _, it := range items {
		m := it.(map[string]interface{})
		if m["target"] == nil && m["deleted"] != true {
			t.Errorf("item missing both target and deleted flag: %v", m)
		}
	}
}

func TestLists_Get_OrphanBookmarksReturnDeleted(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	h, _, _ := newListsHandler(lists, bms)

	list := &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "L"}
	_ = lists.Create(context.Background(), list)
	// Bookmark points to an insight that does not exist in the mock.
	_, _ = bms.Add(context.Background(), &models.Bookmark{ListID: list.ID, ProjectID: "p1", UserID: "alice", TargetType: "insight", TargetID: "ghost"})

	req, w := newAuthedRequest("GET", "/api/v1/projects/p1/lists/"+list.ID, "", "alice")
	req.SetPathValue("id", "p1")
	req.SetPathValue("listId", list.ID)
	h.Get(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp APIResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	items := resp.Data.(map[string]interface{})["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	m := items[0].(map[string]interface{})
	if m["deleted"] != true {
		t.Errorf("expected deleted=true for orphan bookmark, got %v", m)
	}
}

func TestLists_Get_WrongUser_404(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	h, _, _ := newListsHandler(lists, bms)

	list := &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "L"}
	_ = lists.Create(context.Background(), list)

	req, w := newAuthedRequest("GET", "/api/v1/projects/p1/lists/"+list.ID, "", "bob")
	req.SetPathValue("id", "p1")
	req.SetPathValue("listId", list.ID)
	h.Get(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (cross-user isolation)", w.Code)
	}
}

func TestLists_Get_WrongProject_404(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	h, _, _ := newListsHandler(lists, bms)

	list := &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "L"}
	_ = lists.Create(context.Background(), list)

	req, w := newAuthedRequest("GET", "/api/v1/projects/p2/lists/"+list.ID, "", "alice")
	req.SetPathValue("id", "p2")
	req.SetPathValue("listId", list.ID)
	h.Get(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (cross-project isolation)", w.Code)
	}
}

// --- Update ---

func TestLists_Update_PartialRename(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	h, _, _ := newListsHandler(lists, bms)

	list := &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "Old", Description: "keep me"}
	_ = lists.Create(context.Background(), list)

	req, w := newAuthedRequest("PATCH", "/api/v1/projects/p1/lists/"+list.ID, `{"name":"New"}`, "alice")
	req.SetPathValue("id", "p1")
	req.SetPathValue("listId", list.ID)
	h.Update(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if lists.lists[list.ID].Name != "New" {
		t.Errorf("name = %q", lists.lists[list.ID].Name)
	}
	if lists.lists[list.ID].Description != "keep me" {
		t.Errorf("description should be preserved, got %q", lists.lists[list.ID].Description)
	}
}

func TestLists_Update_EmptyName_400(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	h, _, _ := newListsHandler(lists, bms)

	list := &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "Old"}
	_ = lists.Create(context.Background(), list)

	req, w := newAuthedRequest("PATCH", "/api/v1/projects/p1/lists/"+list.ID, `{"name":"   "}`, "alice")
	req.SetPathValue("id", "p1")
	req.SetPathValue("listId", list.ID)
	h.Update(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestLists_Update_WrongUser_404(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	h, _, _ := newListsHandler(lists, bms)

	list := &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "Old"}
	_ = lists.Create(context.Background(), list)

	req, w := newAuthedRequest("PATCH", "/api/v1/projects/p1/lists/"+list.ID, `{"name":"New"}`, "bob")
	req.SetPathValue("id", "p1")
	req.SetPathValue("listId", list.ID)
	h.Update(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// --- Delete (with cascade) ---

func TestLists_Delete_CascadesBookmarks(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	bms.cascadeTo(lists)
	h, _, _ := newListsHandler(lists, bms)

	list := &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "L"}
	_ = lists.Create(context.Background(), list)
	_, _ = bms.Add(context.Background(), &models.Bookmark{ListID: list.ID, ProjectID: "p1", UserID: "alice", TargetType: "insight", TargetID: "i1"})
	_, _ = bms.Add(context.Background(), &models.Bookmark{ListID: list.ID, ProjectID: "p1", UserID: "alice", TargetType: "insight", TargetID: "i2"})

	req, w := newAuthedRequest("DELETE", "/api/v1/projects/p1/lists/"+list.ID, "", "alice")
	req.SetPathValue("id", "p1")
	req.SetPathValue("listId", list.ID)
	h.Delete(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if _, ok := lists.lists[list.ID]; ok {
		t.Error("list should be deleted")
	}
	if len(bms.items) != 0 {
		t.Errorf("bookmarks should cascade-delete, got %d remaining", len(bms.items))
	}
}

func TestLists_Delete_WrongUser_404(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	h, _, _ := newListsHandler(lists, bms)

	list := &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "L"}
	_ = lists.Create(context.Background(), list)

	req, w := newAuthedRequest("DELETE", "/api/v1/projects/p1/lists/"+list.ID, "", "bob")
	req.SetPathValue("id", "p1")
	req.SetPathValue("listId", list.ID)
	h.Delete(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
	if _, ok := lists.lists[list.ID]; !ok {
		t.Error("list should NOT have been deleted by the wrong user")
	}
}

// --- AddBookmark ---

func TestLists_AddBookmark_Success(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	h, ins, _ := newListsHandler(lists, bms)

	ins.insights = append(ins.insights, &commonmodels.StandaloneInsight{ID: "i1", Name: "X"})
	list := &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "L"}
	_ = lists.Create(context.Background(), list)

	body := `{"discovery_id":"d1","target_type":"insight","target_id":"i1","note":"nice"}`
	req, w := newAuthedRequest("POST", "/api/v1/projects/p1/lists/"+list.ID+"/items", body, "alice")
	req.SetPathValue("id", "p1")
	req.SetPathValue("listId", list.ID)
	h.AddBookmark(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if len(bms.items) != 1 {
		t.Errorf("expected 1 bookmark, got %d", len(bms.items))
	}
}

func TestLists_AddBookmark_Idempotent(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	h, ins, _ := newListsHandler(lists, bms)

	ins.insights = append(ins.insights, &commonmodels.StandaloneInsight{ID: "i1", Name: "X"})
	list := &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "L"}
	_ = lists.Create(context.Background(), list)

	body := `{"target_type":"insight","target_id":"i1"}`
	for i := 0; i < 3; i++ {
		req, w := newAuthedRequest("POST", "/api/v1/projects/p1/lists/"+list.ID+"/items", body, "alice")
		req.SetPathValue("id", "p1")
		req.SetPathValue("listId", list.ID)
		h.AddBookmark(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("iter %d status = %d", i, w.Code)
		}
	}
	if len(bms.items) != 1 {
		t.Errorf("expected 1 bookmark after 3 idempotent adds, got %d", len(bms.items))
	}
	if bms.addCount != 1 {
		t.Errorf("repo should have seen 1 real insert, saw %d", bms.addCount)
	}
}

func TestLists_AddBookmark_InvalidTargetType(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	h, _, _ := newListsHandler(lists, bms)

	list := &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "L"}
	_ = lists.Create(context.Background(), list)

	body := `{"target_type":"sql_query","target_id":"q1"}`
	req, w := newAuthedRequest("POST", "/api/v1/projects/p1/lists/"+list.ID+"/items", body, "alice")
	req.SetPathValue("id", "p1")
	req.SetPathValue("listId", list.ID)
	h.AddBookmark(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestLists_AddBookmark_MissingTargetID(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	h, _, _ := newListsHandler(lists, bms)

	list := &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "L"}
	_ = lists.Create(context.Background(), list)

	body := `{"target_type":"insight"}`
	req, w := newAuthedRequest("POST", "/api/v1/projects/p1/lists/"+list.ID+"/items", body, "alice")
	req.SetPathValue("id", "p1")
	req.SetPathValue("listId", list.ID)
	h.AddBookmark(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestLists_AddBookmark_ListWrongUser_404(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	h, ins, _ := newListsHandler(lists, bms)

	ins.insights = append(ins.insights, &commonmodels.StandaloneInsight{ID: "i1", Name: "X"})
	list := &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "L"}
	_ = lists.Create(context.Background(), list)

	body := `{"target_type":"insight","target_id":"i1"}`
	req, w := newAuthedRequest("POST", "/api/v1/projects/p1/lists/"+list.ID+"/items", body, "bob")
	req.SetPathValue("id", "p1")
	req.SetPathValue("listId", list.ID)
	h.AddBookmark(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestLists_AddBookmark_TargetNotFound_404(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	h, _, _ := newListsHandler(lists, bms)

	list := &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "L"}
	_ = lists.Create(context.Background(), list)

	body := `{"target_type":"insight","target_id":"ghost"}`
	req, w := newAuthedRequest("POST", "/api/v1/projects/p1/lists/"+list.ID+"/items", body, "alice")
	req.SetPathValue("id", "p1")
	req.SetPathValue("listId", list.ID)
	h.AddBookmark(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for missing target", w.Code)
	}
}

// --- RemoveBookmark ---

func TestLists_RemoveBookmark_Success(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	h, _, _ := newListsHandler(lists, bms)

	list := &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "L"}
	_ = lists.Create(context.Background(), list)
	bm, _ := bms.Add(context.Background(), &models.Bookmark{ListID: list.ID, ProjectID: "p1", UserID: "alice", TargetType: "insight", TargetID: "i1"})

	req, w := newAuthedRequest("DELETE",
		"/api/v1/projects/p1/lists/"+list.ID+"/items/"+bm.ID, "", "alice")
	req.SetPathValue("id", "p1")
	req.SetPathValue("listId", list.ID)
	req.SetPathValue("bookmarkId", bm.ID)
	h.RemoveBookmark(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
}

func TestLists_RemoveBookmark_WrongUser_404(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	h, _, _ := newListsHandler(lists, bms)

	list := &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "L"}
	_ = lists.Create(context.Background(), list)
	bm, _ := bms.Add(context.Background(), &models.Bookmark{ListID: list.ID, ProjectID: "p1", UserID: "alice", TargetType: "insight", TargetID: "i1"})

	req, w := newAuthedRequest("DELETE",
		"/api/v1/projects/p1/lists/"+list.ID+"/items/"+bm.ID, "", "bob")
	req.SetPathValue("id", "p1")
	req.SetPathValue("listId", list.ID)
	req.SetPathValue("bookmarkId", bm.ID)
	h.RemoveBookmark(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestLists_RemoveBookmark_WrongList_404(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	h, _, _ := newListsHandler(lists, bms)

	list1 := &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "L1"}
	list2 := &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "L2"}
	_ = lists.Create(context.Background(), list1)
	_ = lists.Create(context.Background(), list2)
	bm, _ := bms.Add(context.Background(), &models.Bookmark{ListID: list1.ID, ProjectID: "p1", UserID: "alice", TargetType: "insight", TargetID: "i1"})

	// Try to delete via list2 — different list for the same (project,user).
	req, w := newAuthedRequest("DELETE",
		"/api/v1/projects/p1/lists/"+list2.ID+"/items/"+bm.ID, "", "alice")
	req.SetPathValue("id", "p1")
	req.SetPathValue("listId", list2.ID)
	req.SetPathValue("bookmarkId", bm.ID)
	h.RemoveBookmark(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// --- ListsContaining ---

func TestLists_ListsContaining_MultipleLists(t *testing.T) {
	lists := newMockBookmarkListRepo()
	bms := newMockBookmarkRepo()
	h, _, _ := newListsHandler(lists, bms)

	la := &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "A"}
	lb := &models.BookmarkList{ProjectID: "p1", UserID: "alice", Name: "B"}
	_ = lists.Create(context.Background(), la)
	_ = lists.Create(context.Background(), lb)
	_, _ = bms.Add(context.Background(), &models.Bookmark{ListID: la.ID, ProjectID: "p1", UserID: "alice", TargetType: "insight", TargetID: "i1"})
	_, _ = bms.Add(context.Background(), &models.Bookmark{ListID: lb.ID, ProjectID: "p1", UserID: "alice", TargetType: "insight", TargetID: "i1"})
	// Red herring: different target, bob, different project
	_, _ = bms.Add(context.Background(), &models.Bookmark{ListID: la.ID, ProjectID: "p1", UserID: "alice", TargetType: "insight", TargetID: "i2"})
	_, _ = bms.Add(context.Background(), &models.Bookmark{ListID: la.ID, ProjectID: "p1", UserID: "bob", TargetType: "insight", TargetID: "i1"})

	req, w := newAuthedRequest("GET", "/api/v1/projects/p1/bookmarks?target_type=insight&target_id=i1", "", "alice")
	req.SetPathValue("id", "p1")
	h.ListsContaining(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp APIResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	ids := resp.Data.([]interface{})
	if len(ids) != 2 {
		t.Errorf("got %d lists, want 2", len(ids))
	}
}

func TestLists_ListsContaining_MissingParams(t *testing.T) {
	h, _, _ := newListsHandler(newMockBookmarkListRepo(), newMockBookmarkRepo())

	req, w := newAuthedRequest("GET", "/api/v1/projects/p1/bookmarks?target_type=insight", "", "alice")
	req.SetPathValue("id", "p1")
	h.ListsContaining(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestLists_ListsContaining_InvalidTargetType(t *testing.T) {
	h, _, _ := newListsHandler(newMockBookmarkListRepo(), newMockBookmarkRepo())

	req, w := newAuthedRequest("GET", "/api/v1/projects/p1/bookmarks?target_type=junk&target_id=x", "", "alice")
	req.SetPathValue("id", "p1")
	h.ListsContaining(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// --- Repo error passthrough ---

func TestLists_List_RepoError_500(t *testing.T) {
	lists := newMockBookmarkListRepo()
	lists.listErr = errSentinel("boom")
	h, _, _ := newListsHandler(lists, newMockBookmarkRepo())

	req, w := newAuthedRequest("GET", "/api/v1/projects/p1/lists", "", "alice")
	req.SetPathValue("id", "p1")
	h.List(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}
