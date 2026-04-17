package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

const maxListNameLen = 200

// ListsHandler owns all /projects/{id}/lists and /bookmarks routes.
type ListsHandler struct {
	lists           database.BookmarkListRepo
	bookmarks       database.BookmarkRepo
	insights        database.InsightRepo
	recommendations database.RecommendationRepo
}

func NewListsHandler(
	lists database.BookmarkListRepo,
	bookmarks database.BookmarkRepo,
	insights database.InsightRepo,
	recommendations database.RecommendationRepo,
) *ListsHandler {
	return &ListsHandler{
		lists:           lists,
		bookmarks:       bookmarks,
		insights:        insights,
		recommendations: recommendations,
	}
}

// userID resolves the caller's identity via the auth context. In community
// mode (NoAuth provider) this returns "anonymous"; in enterprise mode it
// returns the OIDC sub claim. Handlers must not proceed without a user.
func userID(r *http.Request) (string, bool) {
	user, ok := auth.FromContext(r.Context())
	if !ok || user == nil || user.Sub == "" {
		return "", false
	}
	return user.Sub, true
}

// Create — POST /api/v1/projects/{id}/lists
func (h *ListsHandler) Create(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	uid, ok := userID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Color       string `json:"color"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(body.Name) > maxListNameLen {
		writeError(w, http.StatusBadRequest, "name exceeds maximum length")
		return
	}

	list := &models.BookmarkList{
		ProjectID:   projectID,
		UserID:      uid,
		Name:        body.Name,
		Description: body.Description,
		Color:       body.Color,
	}
	if err := h.lists.Create(r.Context(), list); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create list: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, list)
}

// List — GET /api/v1/projects/{id}/lists
func (h *ListsHandler) List(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	uid, ok := userID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	lists, err := h.lists.List(r.Context(), projectID, uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list lists: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, lists)
}

// Get — GET /api/v1/projects/{id}/lists/{listId}
// Resolves bookmarks against insights/recommendations so the dashboard
// does not need N follow-up requests. Orphaned bookmarks (source deleted)
// are returned with Deleted=true rather than filtered out.
func (h *ListsHandler) Get(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	listID := r.PathValue("listId")
	uid, ok := userID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	list, err := h.lists.GetByID(r.Context(), projectID, uid, listID)
	if err != nil {
		if errors.Is(err, database.ErrBookmarkListNotFound) {
			writeError(w, http.StatusNotFound, "list not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get list: "+err.Error())
		return
	}

	bms, err := h.bookmarks.ListByList(r.Context(), list.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list bookmarks: "+err.Error())
		return
	}

	items := make([]models.BookmarkItem, 0, len(bms))
	for _, bm := range bms {
		items = append(items, h.resolveBookmark(r, bm))
	}

	writeJSON(w, http.StatusOK, struct {
		*models.BookmarkList
		Items []models.BookmarkItem `json:"items"`
	}{list, items})
}

func (h *ListsHandler) resolveBookmark(r *http.Request, bm *models.Bookmark) models.BookmarkItem {
	switch bm.TargetType {
	case models.TargetTypeInsight:
		ins, err := h.insights.GetByID(r.Context(), bm.TargetID)
		if err != nil || ins == nil {
			return models.BookmarkItem{Bookmark: bm, Deleted: true}
		}
		return models.BookmarkItem{Bookmark: bm, Target: ins}
	case models.TargetTypeRecommendation:
		rec, err := h.recommendations.GetByID(r.Context(), bm.TargetID)
		if err != nil || rec == nil {
			return models.BookmarkItem{Bookmark: bm, Deleted: true}
		}
		return models.BookmarkItem{Bookmark: bm, Target: rec}
	}
	return models.BookmarkItem{Bookmark: bm, Deleted: true}
}

// Update — PATCH /api/v1/projects/{id}/lists/{listId}
func (h *ListsHandler) Update(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	listID := r.PathValue("listId")
	uid, ok := userID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Color       *string `json:"color"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Name != nil {
		trimmed := strings.TrimSpace(*body.Name)
		if trimmed == "" {
			writeError(w, http.StatusBadRequest, "name cannot be empty")
			return
		}
		if len(trimmed) > maxListNameLen {
			writeError(w, http.StatusBadRequest, "name exceeds maximum length")
			return
		}
		body.Name = &trimmed
	}

	list, err := h.lists.Update(r.Context(), projectID, uid, listID, database.UpdateFields{
		Name:        body.Name,
		Description: body.Description,
		Color:       body.Color,
	})
	if err != nil {
		if errors.Is(err, database.ErrBookmarkListNotFound) {
			writeError(w, http.StatusNotFound, "list not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update list: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// Delete — DELETE /api/v1/projects/{id}/lists/{listId}
func (h *ListsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	listID := r.PathValue("listId")
	uid, ok := userID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	if err := h.lists.Delete(r.Context(), projectID, uid, listID); err != nil {
		if errors.Is(err, database.ErrBookmarkListNotFound) {
			writeError(w, http.StatusNotFound, "list not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete list: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AddBookmark — POST /api/v1/projects/{id}/lists/{listId}/items
// Validates the list exists and is owned by the caller, validates the target
// exists in its source collection, then inserts (or returns existing on dup).
func (h *ListsHandler) AddBookmark(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	listID := r.PathValue("listId")
	uid, ok := userID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var body struct {
		DiscoveryID string `json:"discovery_id"`
		TargetType  string `json:"target_type"`
		TargetID    string `json:"target_id"`
		Note        string `json:"note"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.TargetID == "" {
		writeError(w, http.StatusBadRequest, "target_id is required")
		return
	}
	if !models.IsValidTargetType(body.TargetType) {
		writeError(w, http.StatusBadRequest, "target_type must be 'insight' or 'recommendation'")
		return
	}

	list, err := h.lists.GetByID(r.Context(), projectID, uid, listID)
	if err != nil {
		if errors.Is(err, database.ErrBookmarkListNotFound) {
			writeError(w, http.StatusNotFound, "list not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get list: "+err.Error())
		return
	}

	if !h.targetExists(r, body.TargetType, body.TargetID) {
		writeError(w, http.StatusNotFound, "target not found")
		return
	}

	bm := &models.Bookmark{
		ListID:      list.ID,
		ProjectID:   projectID,
		UserID:      uid,
		DiscoveryID: body.DiscoveryID,
		TargetType:  body.TargetType,
		TargetID:    body.TargetID,
		Note:        body.Note,
	}
	saved, err := h.bookmarks.Add(r.Context(), bm)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to add bookmark: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, saved)
}

func (h *ListsHandler) targetExists(r *http.Request, targetType, targetID string) bool {
	switch targetType {
	case models.TargetTypeInsight:
		ins, err := h.insights.GetByID(r.Context(), targetID)
		return err == nil && ins != nil
	case models.TargetTypeRecommendation:
		rec, err := h.recommendations.GetByID(r.Context(), targetID)
		return err == nil && rec != nil
	}
	return false
}

// RemoveBookmark — DELETE /api/v1/projects/{id}/lists/{listId}/items/{bookmarkId}
func (h *ListsHandler) RemoveBookmark(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	listID := r.PathValue("listId")
	bookmarkID := r.PathValue("bookmarkId")
	uid, ok := userID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	if err := h.bookmarks.Delete(r.Context(), projectID, uid, listID, bookmarkID); err != nil {
		if errors.Is(err, database.ErrBookmarkNotFound) {
			writeError(w, http.StatusNotFound, "bookmark not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete bookmark: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListsContaining — GET /api/v1/projects/{id}/bookmarks?target_type=...&target_id=...
// Returns the list IDs that contain a bookmark for the target, for the caller.
// Used by BookmarkButton to know whether an item is bookmarked anywhere.
func (h *ListsHandler) ListsContaining(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	uid, ok := userID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	targetType := r.URL.Query().Get("target_type")
	targetID := r.URL.Query().Get("target_id")
	if targetID == "" || !models.IsValidTargetType(targetType) {
		writeError(w, http.StatusBadRequest, "target_type and target_id query params are required")
		return
	}

	ids, err := h.bookmarks.ListsContaining(r.Context(), projectID, uid, targetType, targetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to query bookmarks: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ids)
}
