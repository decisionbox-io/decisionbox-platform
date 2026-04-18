//go:build integration

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// End-to-end HTTP tests against a real MongoDB testcontainer. These exercise
// the full handler → repo pipeline that the handler unit tests only mock.
// The direct motivation was two production bugs that unit tests couldn't
// catch: insert-time target validation failing when insights only live in
// the discovery document (not the standalone collection), and list-detail
// returning Deleted=true for the same reason.

var testDB *database.DB

func TestMain(m *testing.M) {
	ctx := context.Background()
	container, err := tcmongo.Run(ctx, "mongo:7.0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "MongoDB start failed: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = container.Terminate(ctx) }()

	uri, _ := container.ConnectionString(ctx)
	cfg := gomongo.DefaultConfig()
	cfg.URI = uri
	cfg.Database = "lists_http_integ_test"

	client, err := gomongo.NewClient(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "MongoDB connect failed: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = client.Disconnect(ctx) }()

	testDB = database.New(client)
	if err := database.InitDatabase(ctx, testDB); err != nil {
		fmt.Fprintf(os.Stderr, "InitDatabase failed: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// seedDiscoveryWithInsight inserts a discovery document with a single insight
// embedded in its Insights array — mirroring what the agent emits and what
// the dashboard renders on the detail page. Returns discoveryID + insightID.
func seedDiscoveryWithInsight(t *testing.T, projectID, insightName string) (string, string) {
	t.Helper()
	discID := primitive.NewObjectID()
	insID := primitive.NewObjectID().Hex()
	disc := bson.M{
		"_id":            discID,
		"project_id":     projectID,
		"discovery_date": time.Now().UTC(),
		"insights": []bson.M{
			{
				"id":             insID,
				"name":           insightName,
				"description":    "seeded for http integration test",
				"severity":       "high",
				"analysis_area":  "churn",
				"affected_count": 1234,
				"confidence":     0.9,
				"discovered_at":  time.Now().UTC(),
			},
		},
		"recommendations": []bson.M{},
		"created_at":      time.Now().UTC(),
	}
	if _, err := testDB.Collection("discoveries").InsertOne(context.Background(), disc); err != nil {
		t.Fatalf("insert discovery: %v", err)
	}
	return discID.Hex(), insID
}

func newHandler() *ListsHandler {
	return NewListsHandler(
		database.NewBookmarkListRepository(testDB),
		database.NewBookmarkRepository(testDB),
		database.NewInsightRepository(testDB),
		database.NewRecommendationRepository(testDB),
		database.NewDiscoveryRepository(testDB),
	)
}

func authedCtx(userID string) context.Context {
	return auth.WithUser(context.Background(), &auth.UserPrincipal{Sub: userID})
}

func postJSON(h *ListsHandler, fn func(http.ResponseWriter, *http.Request), path string, body any, userID string, pathVals map[string]string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", path, bytes.NewReader(b)).WithContext(authedCtx(userID))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range pathVals {
		r.SetPathValue(k, v)
	}
	w := httptest.NewRecorder()
	fn(w, r)
	return w
}

func getJSON(fn func(http.ResponseWriter, *http.Request), path, userID string, pathVals map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil).WithContext(authedCtx(userID))
	for k, v := range pathVals {
		r.SetPathValue(k, v)
	}
	w := httptest.NewRecorder()
	fn(w, r)
	return w
}

func decodeData(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp struct {
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	return resp.Data
}

// TestHTTPInteg_BookmarkFromDiscoveryOnly verifies the full flow for an
// insight that only exists inside a DiscoveryResult document — no standalone
// row, no embedding. Both the insert and the subsequent list-detail resolve
// must succeed; the item must come back with `target` populated and without
// the Deleted flag.
func TestHTTPInteg_BookmarkFromDiscoveryOnly(t *testing.T) {
	h := newHandler()
	projectID := "p-http-1"
	userID := "anonymous"
	discID, insID := seedDiscoveryWithInsight(t, projectID, "Low retention at level 15")

	// 1. Create a list.
	w := postJSON(h, h.Create, "/api/v1/projects/"+projectID+"/lists",
		map[string]string{"name": "Retention ideas"}, userID, map[string]string{"id": projectID})
	if w.Code != http.StatusCreated {
		t.Fatalf("Create list: status=%d body=%s", w.Code, w.Body.String())
	}
	listID := decodeData(t, w)["id"].(string)

	// 2. Bookmark the insight. This was returning 404 before the fix because
	// the handler validated against the standalone insights collection.
	w = postJSON(h, h.AddBookmark, "/api/v1/projects/"+projectID+"/lists/"+listID+"/items",
		map[string]string{
			"discovery_id": discID,
			"target_type":  "insight",
			"target_id":    insID,
		}, userID, map[string]string{"id": projectID, "listId": listID})
	if w.Code != http.StatusCreated {
		t.Fatalf("AddBookmark: status=%d body=%s", w.Code, w.Body.String())
	}

	// 3. Load the list detail. The item must resolve to the embedded insight,
	// not come back as Deleted=true (which is what used to happen).
	w = getJSON(h.Get, "/api/v1/projects/"+projectID+"/lists/"+listID,
		userID, map[string]string{"id": projectID, "listId": listID})
	if w.Code != http.StatusOK {
		t.Fatalf("Get list: status=%d body=%s", w.Code, w.Body.String())
	}
	data := decodeData(t, w)
	items := data["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items len=%d, want 1", len(items))
	}
	item := items[0].(map[string]any)
	if deleted, _ := item["deleted"].(bool); deleted {
		t.Fatal("item marked as deleted — discovery-doc fallback did not kick in")
	}
	target, ok := item["target"].(map[string]any)
	if !ok || target == nil {
		t.Fatalf("item.target missing: %v", item)
	}
	if target["name"] != "Low retention at level 15" {
		t.Errorf("target.name=%v, want seeded insight name", target["name"])
	}
}

// TestHTTPInteg_BookmarkByIndex covers the dashboard's older URL pattern
// where the path segment is a numeric index (e.g. "0") into the discovery's
// insights array, not a real id. resolveBookmark must fall back to index
// lookup and still produce a non-deleted item.
func TestHTTPInteg_BookmarkByIndex(t *testing.T) {
	h := newHandler()
	projectID := "p-http-idx"
	userID := "anonymous"
	discID, _ := seedDiscoveryWithInsight(t, projectID, "Onboarding drop-off")

	w := postJSON(h, h.Create, "/api/v1/projects/"+projectID+"/lists",
		map[string]string{"name": "Onboarding"}, userID, map[string]string{"id": projectID})
	listID := decodeData(t, w)["id"].(string)

	// Bookmark using "0" (the index) instead of the real id.
	w = postJSON(h, h.AddBookmark, "/api/v1/projects/"+projectID+"/lists/"+listID+"/items",
		map[string]string{
			"discovery_id": discID,
			"target_type":  "insight",
			"target_id":    "0",
		}, userID, map[string]string{"id": projectID, "listId": listID})
	if w.Code != http.StatusCreated {
		t.Fatalf("AddBookmark: status=%d body=%s", w.Code, w.Body.String())
	}

	w = getJSON(h.Get, "/api/v1/projects/"+projectID+"/lists/"+listID,
		userID, map[string]string{"id": projectID, "listId": listID})
	items := decodeData(t, w)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items len=%d", len(items))
	}
	item := items[0].(map[string]any)
	if deleted, _ := item["deleted"].(bool); deleted {
		t.Fatal("index-based bookmark resolved as deleted")
	}
	target := item["target"].(map[string]any)
	if target["name"] != "Onboarding drop-off" {
		t.Errorf("target.name=%v", target["name"])
	}
}

// patchJSON is like postJSON but for PATCH.
func patchJSON(fn func(http.ResponseWriter, *http.Request), path string, body any, userID string, pathVals map[string]string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("PATCH", path, bytes.NewReader(b)).WithContext(authedCtx(userID))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range pathVals {
		r.SetPathValue(k, v)
	}
	w := httptest.NewRecorder()
	fn(w, r)
	return w
}

// deleteJSON supports both request-body DELETE (reads) and no-body DELETE.
func deleteJSON(fn func(http.ResponseWriter, *http.Request), path string, body any, userID string, pathVals map[string]string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest("DELETE", path, bytes.NewReader(b)).WithContext(authedCtx(userID))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest("DELETE", path, nil).WithContext(authedCtx(userID))
	}
	for k, v := range pathVals {
		r.SetPathValue(k, v)
	}
	w := httptest.NewRecorder()
	fn(w, r)
	return w
}

func decodeArray(t *testing.T, w *httptest.ResponseRecorder) []any {
	t.Helper()
	var resp struct {
		Data []any `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	return resp.Data
}

// TestHTTPInteg_List_ScopedToCallerAndProject verifies GET /lists never leaks
// another user's or another project's lists. Seeds 4 lists across the matrix
// (alice/p1, alice/p2, bob/p1, bob/p2) and asserts each GET returns exactly 1.
func TestHTTPInteg_List_ScopedToCallerAndProject(t *testing.T) {
	h := newHandler()

	for _, cell := range []struct{ project, user, name string }{
		{"p-list-1", "alice", "alice-p1"},
		{"p-list-1", "bob", "bob-p1"},
		{"p-list-2", "alice", "alice-p2"},
		{"p-list-2", "bob", "bob-p2"},
	} {
		_ = postJSON(h, h.Create, "/api/v1/projects/"+cell.project+"/lists",
			map[string]string{"name": cell.name}, cell.user, map[string]string{"id": cell.project})
	}

	for _, cell := range []struct{ project, user, expected string }{
		{"p-list-1", "alice", "alice-p1"},
		{"p-list-1", "bob", "bob-p1"},
		{"p-list-2", "alice", "alice-p2"},
		{"p-list-2", "bob", "bob-p2"},
	} {
		w := getJSON(h.List, "/api/v1/projects/"+cell.project+"/lists",
			cell.user, map[string]string{"id": cell.project})
		if w.Code != http.StatusOK {
			t.Fatalf("List %s/%s: status=%d", cell.project, cell.user, w.Code)
		}
		items := decodeArray(t, w)
		if len(items) != 1 {
			t.Errorf("%s/%s: got %d lists, want 1", cell.project, cell.user, len(items))
		}
		got := items[0].(map[string]any)["name"]
		if got != cell.expected {
			t.Errorf("%s/%s: name=%v, want %q", cell.project, cell.user, got, cell.expected)
		}
	}
}

// TestHTTPInteg_Update_PartialAndScoping covers PATCH. Partial fields update
// in place; wrong user gets 404 and the list is untouched.
func TestHTTPInteg_Update_PartialAndScoping(t *testing.T) {
	h := newHandler()
	projectID := "p-upd"

	w := postJSON(h, h.Create, "/api/v1/projects/"+projectID+"/lists",
		map[string]string{"name": "old", "description": "keep"}, "alice",
		map[string]string{"id": projectID})
	listID := decodeData(t, w)["id"].(string)

	// Partial: rename without touching description.
	w = patchJSON(h.Update, "/api/v1/projects/"+projectID+"/lists/"+listID,
		map[string]string{"name": "new"}, "alice",
		map[string]string{"id": projectID, "listId": listID})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH: status=%d body=%s", w.Code, w.Body.String())
	}
	got := decodeData(t, w)
	if got["name"] != "new" {
		t.Errorf("name=%v, want new", got["name"])
	}
	if got["description"] != "keep" {
		t.Errorf("description=%v, want keep (partial update should preserve)", got["description"])
	}

	// Empty name → 400, no mutation.
	w = patchJSON(h.Update, "/api/v1/projects/"+projectID+"/lists/"+listID,
		map[string]string{"name": "   "}, "alice",
		map[string]string{"id": projectID, "listId": listID})
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty name: status=%d, want 400", w.Code)
	}

	// Wrong user → 404, name unchanged.
	w = patchJSON(h.Update, "/api/v1/projects/"+projectID+"/lists/"+listID,
		map[string]string{"name": "hijack"}, "bob",
		map[string]string{"id": projectID, "listId": listID})
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-user: status=%d, want 404", w.Code)
	}
	w = getJSON(h.Get, "/api/v1/projects/"+projectID+"/lists/"+listID,
		"alice", map[string]string{"id": projectID, "listId": listID})
	if decodeData(t, w)["name"] != "new" {
		t.Error("name was mutated by wrong-user PATCH")
	}
}

// TestHTTPInteg_Delete_CascadesAndScopes ensures deleting a list removes its
// bookmarks (cascade) and that wrong users cannot delete another user's list.
func TestHTTPInteg_Delete_CascadesAndScopes(t *testing.T) {
	h := newHandler()
	projectID := "p-del"
	discID, insID := seedDiscoveryWithInsight(t, projectID, "cascade check")

	w := postJSON(h, h.Create, "/api/v1/projects/"+projectID+"/lists",
		map[string]string{"name": "L"}, "alice", map[string]string{"id": projectID})
	listID := decodeData(t, w)["id"].(string)

	for i := 0; i < 3; i++ {
		_ = postJSON(h, h.AddBookmark, "/api/v1/projects/"+projectID+"/lists/"+listID+"/items",
			map[string]string{"discovery_id": discID, "target_type": "insight", "target_id": insID + "-" + fmt.Sprint(i)},
			"alice", map[string]string{"id": projectID, "listId": listID})
	}

	// Wrong user: 404 + list still exists.
	w = deleteJSON(h.Delete, "/api/v1/projects/"+projectID+"/lists/"+listID, nil, "bob",
		map[string]string{"id": projectID, "listId": listID})
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-user DELETE: status=%d, want 404", w.Code)
	}

	// Right user.
	w = deleteJSON(h.Delete, "/api/v1/projects/"+projectID+"/lists/"+listID, nil, "alice",
		map[string]string{"id": projectID, "listId": listID})
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE: status=%d body=%s", w.Code, w.Body.String())
	}

	// List is gone.
	w = getJSON(h.Get, "/api/v1/projects/"+projectID+"/lists/"+listID,
		"alice", map[string]string{"id": projectID, "listId": listID})
	if w.Code != http.StatusNotFound {
		t.Errorf("Get after delete: status=%d, want 404", w.Code)
	}

	// Cascade: no bookmarks for this list_id in the collection.
	count, err := testDB.Collection("bookmarks").CountDocuments(context.Background(), bson.M{"list_id": listID})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("cascade did not remove bookmarks: %d remaining", count)
	}
}

// TestHTTPInteg_AddBookmark_Idempotent verifies repeated POSTs with the same
// (list, target_type, target_id) do not create duplicates — the unique index
// catches the race and the handler returns the existing bookmark.
func TestHTTPInteg_AddBookmark_Idempotent(t *testing.T) {
	h := newHandler()
	projectID := "p-idem"
	discID, insID := seedDiscoveryWithInsight(t, projectID, "dup test")

	w := postJSON(h, h.Create, "/api/v1/projects/"+projectID+"/lists",
		map[string]string{"name": "L"}, "alice", map[string]string{"id": projectID})
	listID := decodeData(t, w)["id"].(string)

	var ids []string
	for i := 0; i < 3; i++ {
		w = postJSON(h, h.AddBookmark, "/api/v1/projects/"+projectID+"/lists/"+listID+"/items",
			map[string]string{"discovery_id": discID, "target_type": "insight", "target_id": insID},
			"alice", map[string]string{"id": projectID, "listId": listID})
		if w.Code != http.StatusCreated {
			t.Fatalf("AddBookmark iter %d: status=%d", i, w.Code)
		}
		ids = append(ids, decodeData(t, w)["id"].(string))
	}
	if ids[0] != ids[1] || ids[1] != ids[2] {
		t.Errorf("idempotent add produced different ids: %v", ids)
	}

	// Confirm a single document in the DB.
	count, _ := testDB.Collection("bookmarks").CountDocuments(context.Background(), bson.M{
		"list_id": listID, "target_type": "insight", "target_id": insID,
	})
	if count != 1 {
		t.Errorf("expected 1 bookmark, got %d", count)
	}
}

// TestHTTPInteg_RemoveBookmark covers the full add → remove cycle and the
// wrong-list / wrong-user 404s.
func TestHTTPInteg_RemoveBookmark(t *testing.T) {
	h := newHandler()
	projectID := "p-rm"
	discID, insID := seedDiscoveryWithInsight(t, projectID, "rm test")

	w := postJSON(h, h.Create, "/api/v1/projects/"+projectID+"/lists",
		map[string]string{"name": "L1"}, "alice", map[string]string{"id": projectID})
	listID := decodeData(t, w)["id"].(string)

	w = postJSON(h, h.AddBookmark, "/api/v1/projects/"+projectID+"/lists/"+listID+"/items",
		map[string]string{"discovery_id": discID, "target_type": "insight", "target_id": insID},
		"alice", map[string]string{"id": projectID, "listId": listID})
	bookmarkID := decodeData(t, w)["id"].(string)

	// Wrong user.
	w = deleteJSON(h.RemoveBookmark, "/api/v1/projects/"+projectID+"/lists/"+listID+"/items/"+bookmarkID, nil,
		"bob", map[string]string{"id": projectID, "listId": listID, "bookmarkId": bookmarkID})
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-user: status=%d, want 404", w.Code)
	}

	// Right user.
	w = deleteJSON(h.RemoveBookmark, "/api/v1/projects/"+projectID+"/lists/"+listID+"/items/"+bookmarkID, nil,
		"alice", map[string]string{"id": projectID, "listId": listID, "bookmarkId": bookmarkID})
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE bookmark: status=%d", w.Code)
	}

	// Second remove → 404.
	w = deleteJSON(h.RemoveBookmark, "/api/v1/projects/"+projectID+"/lists/"+listID+"/items/"+bookmarkID, nil,
		"alice", map[string]string{"id": projectID, "listId": listID, "bookmarkId": bookmarkID})
	if w.Code != http.StatusNotFound {
		t.Errorf("double delete: status=%d, want 404", w.Code)
	}
}

// TestHTTPInteg_ListsContaining_ScopedLookup verifies the reverse lookup that
// powers BookmarkButton's fill state — it must return only the caller's lists
// that contain the target, and must respect project + user scoping.
func TestHTTPInteg_ListsContaining_ScopedLookup(t *testing.T) {
	h := newHandler()
	projectID := "p-contains"
	discID, insID := seedDiscoveryWithInsight(t, projectID, "contains test")

	// Alice bookmarks the insight in two lists.
	w := postJSON(h, h.Create, "/api/v1/projects/"+projectID+"/lists",
		map[string]string{"name": "A1"}, "alice", map[string]string{"id": projectID})
	a1 := decodeData(t, w)["id"].(string)
	w = postJSON(h, h.Create, "/api/v1/projects/"+projectID+"/lists",
		map[string]string{"name": "A2"}, "alice", map[string]string{"id": projectID})
	a2 := decodeData(t, w)["id"].(string)
	for _, lid := range []string{a1, a2} {
		_ = postJSON(h, h.AddBookmark, "/api/v1/projects/"+projectID+"/lists/"+lid+"/items",
			map[string]string{"discovery_id": discID, "target_type": "insight", "target_id": insID},
			"alice", map[string]string{"id": projectID, "listId": lid})
	}

	// Bob bookmarks the same insight in his own list — must not leak to alice.
	w = postJSON(h, h.Create, "/api/v1/projects/"+projectID+"/lists",
		map[string]string{"name": "B1"}, "bob", map[string]string{"id": projectID})
	b1 := decodeData(t, w)["id"].(string)
	_ = postJSON(h, h.AddBookmark, "/api/v1/projects/"+projectID+"/lists/"+b1+"/items",
		map[string]string{"discovery_id": discID, "target_type": "insight", "target_id": insID},
		"bob", map[string]string{"id": projectID, "listId": b1})

	// Alice's reverse lookup: exactly A1 and A2.
	w = getJSON(h.ListsContaining,
		"/api/v1/projects/"+projectID+"/bookmarks?target_type=insight&target_id="+insID,
		"alice", map[string]string{"id": projectID})
	if w.Code != http.StatusOK {
		t.Fatalf("ListsContaining: status=%d", w.Code)
	}
	items := decodeArray(t, w)
	if len(items) != 2 {
		t.Errorf("alice got %d lists, want 2 (A1+A2, not B1)", len(items))
	}

	// Bob's reverse lookup: exactly B1.
	w = getJSON(h.ListsContaining,
		"/api/v1/projects/"+projectID+"/bookmarks?target_type=insight&target_id="+insID,
		"bob", map[string]string{"id": projectID})
	if len(decodeArray(t, w)) != 1 {
		t.Errorf("bob got %d lists, want 1", len(decodeArray(t, w)))
	}
}

// --- /reads endpoints ---

// TestHTTPInteg_Reads_UpsertAndList exercises POST /reads, GET /reads, and the
// idempotent + scoping properties. Seeds marks across (project, user, type)
// and asserts GET returns only the caller's target_ids for the given type.
func TestHTTPInteg_Reads_UpsertAndList(t *testing.T) {
	reads := NewReadsHandler(database.NewReadMarkRepository(testDB))

	projectID := "p-reads"
	// Alice reads two insights and one recommendation in p-reads.
	for _, seed := range []struct{ tt, tid, user, project string }{
		{"insight", "i1", "alice", projectID},
		{"insight", "i2", "alice", projectID},
		{"recommendation", "r1", "alice", projectID},
		// Red herrings: bob's read + alice's read in a different project.
		{"insight", "i1", "bob", projectID},
		{"insight", "i1", "alice", "p-other"},
	} {
		w := postJSON(nil, reads.MarkRead, "/api/v1/projects/"+seed.project+"/reads",
			map[string]string{"target_type": seed.tt, "target_id": seed.tid},
			seed.user, map[string]string{"id": seed.project})
		if w.Code != http.StatusOK {
			t.Fatalf("seed %+v: status=%d", seed, w.Code)
		}
	}

	// Idempotent: call MarkRead twice more for (alice, i1) — GET must still return 2 ids.
	for i := 0; i < 2; i++ {
		_ = postJSON(nil, reads.MarkRead, "/api/v1/projects/"+projectID+"/reads",
			map[string]string{"target_type": "insight", "target_id": "i1"},
			"alice", map[string]string{"id": projectID})
	}

	// Alice's insight reads in p-reads: exactly [i1, i2], scoped by user+project+type.
	w := getJSON(reads.ListReadIDs,
		"/api/v1/projects/"+projectID+"/reads?target_type=insight",
		"alice", map[string]string{"id": projectID})
	if w.Code != http.StatusOK {
		t.Fatalf("List: status=%d", w.Code)
	}
	ids := decodeArray(t, w)
	if len(ids) != 2 {
		t.Errorf("alice/insight got %d, want 2 (i1+i2). body=%s", len(ids), w.Body.String())
	}

	// Alice's recommendation reads: exactly [r1].
	w = getJSON(reads.ListReadIDs,
		"/api/v1/projects/"+projectID+"/reads?target_type=recommendation",
		"alice", map[string]string{"id": projectID})
	if len(decodeArray(t, w)) != 1 {
		t.Errorf("alice/recommendation got %d, want 1", len(decodeArray(t, w)))
	}

	// Confirm DB has exactly 5 marks (the 5 seeds; the 2 idempotent repeats of
	// alice/insight/i1 must not create duplicates).
	count, _ := testDB.Collection("read_marks").CountDocuments(context.Background(), bson.M{})
	if count != 5 {
		t.Errorf("read_marks count=%d, want 5 (idempotent)", count)
	}
}

// TestHTTPInteg_Reads_MarkUnread covers the DELETE path: removes the mark,
// is idempotent for missing keys, and stays scoped.
func TestHTTPInteg_Reads_MarkUnread(t *testing.T) {
	reads := NewReadsHandler(database.NewReadMarkRepository(testDB))
	projectID := "p-unread"

	// Mark then unmark — mark gone.
	_ = postJSON(nil, reads.MarkRead, "/api/v1/projects/"+projectID+"/reads",
		map[string]string{"target_type": "insight", "target_id": "i1"},
		"alice", map[string]string{"id": projectID})
	w := deleteJSON(reads.MarkUnread, "/api/v1/projects/"+projectID+"/reads",
		map[string]string{"target_type": "insight", "target_id": "i1"},
		"alice", map[string]string{"id": projectID})
	if w.Code != http.StatusOK {
		t.Fatalf("MarkUnread: status=%d body=%s", w.Code, w.Body.String())
	}

	// Idempotent: delete a mark that doesn't exist.
	w = deleteJSON(reads.MarkUnread, "/api/v1/projects/"+projectID+"/reads",
		map[string]string{"target_type": "insight", "target_id": "ghost"},
		"alice", map[string]string{"id": projectID})
	if w.Code != http.StatusOK {
		t.Errorf("idempotent MarkUnread: status=%d", w.Code)
	}

	// Cross-user delete has no effect on another user's mark.
	_ = postJSON(nil, reads.MarkRead, "/api/v1/projects/"+projectID+"/reads",
		map[string]string{"target_type": "insight", "target_id": "shared"},
		"bob", map[string]string{"id": projectID})
	w = deleteJSON(reads.MarkUnread, "/api/v1/projects/"+projectID+"/reads",
		map[string]string{"target_type": "insight", "target_id": "shared"},
		"alice", map[string]string{"id": projectID})
	if w.Code != http.StatusOK {
		t.Fatalf("cross-user MarkUnread: status=%d", w.Code)
	}
	// Bob's mark still there.
	w = getJSON(reads.ListReadIDs,
		"/api/v1/projects/"+projectID+"/reads?target_type=insight",
		"bob", map[string]string{"id": projectID})
	if len(decodeArray(t, w)) != 1 {
		t.Errorf("bob's mark was incorrectly deleted by alice's call")
	}
}

// TestHTTPInteg_TrulyOrphanedBookmarkStaysDeleted proves the Deleted flag
// still fires when the underlying insight is really gone. This is the
// intentional design — we should not silently hide orphans, we should flag
// them so the UI can render "[removed]".
func TestHTTPInteg_TrulyOrphanedBookmarkStaysDeleted(t *testing.T) {
	h := newHandler()
	projectID := "p-http-orphan"
	userID := "anonymous"

	// Seed a discovery (so the discovery_id is real) but bookmark a
	// target_id that doesn't exist in it.
	discID, _ := seedDiscoveryWithInsight(t, projectID, "Some insight")

	w := postJSON(h, h.Create, "/api/v1/projects/"+projectID+"/lists",
		map[string]string{"name": "L"}, userID, map[string]string{"id": projectID})
	listID := decodeData(t, w)["id"].(string)

	w = postJSON(h, h.AddBookmark, "/api/v1/projects/"+projectID+"/lists/"+listID+"/items",
		map[string]string{
			"discovery_id": discID,
			"target_type":  "insight",
			"target_id":    "ghost-id",
		}, userID, map[string]string{"id": projectID, "listId": listID})
	if w.Code != http.StatusCreated {
		t.Fatalf("AddBookmark: status=%d body=%s", w.Code, w.Body.String())
	}

	w = getJSON(h.Get, "/api/v1/projects/"+projectID+"/lists/"+listID,
		userID, map[string]string{"id": projectID, "listId": listID})
	items := decodeData(t, w)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items len=%d", len(items))
	}
	item := items[0].(map[string]any)
	if deleted, _ := item["deleted"].(bool); !deleted {
		t.Errorf("truly orphan bookmark should be flagged deleted: %v", item)
	}
}
