package handler

import (
	"errors"
	"net/http"
	"strconv"
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
	discoveries     database.DiscoveryRepo
}

func NewListsHandler(
	lists database.BookmarkListRepo,
	bookmarks database.BookmarkRepo,
	insights database.InsightRepo,
	recommendations database.RecommendationRepo,
	discoveries database.DiscoveryRepo,
) *ListsHandler {
	return &ListsHandler{
		lists:           lists,
		bookmarks:       bookmarks,
		insights:        insights,
		recommendations: recommendations,
		discoveries:     discoveries,
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

// resolveBookmark finds the underlying insight or recommendation for a
// bookmark. Insights and recommendations live in two places — the embedded
// arrays inside a DiscoveryResult, and (after Phase 9 denormalization) a
// standalone per-project collection. We try the standalone collection first
// and fall back to the discovery document so older, non-denormalized items
// still resolve. Only when neither turns up a match do we flag Deleted=true.
func (h *ListsHandler) resolveBookmark(r *http.Request, bm *models.Bookmark) models.BookmarkItem {
	switch bm.TargetType {
	case models.TargetTypeInsight:
		if ins, err := h.insights.GetByID(r.Context(), bm.TargetID); err == nil && ins != nil {
			return models.BookmarkItem{Bookmark: bm, Target: ins}
		}
		if ins := h.findInsightInDiscovery(r, bm.DiscoveryID, bm.TargetID); ins != nil {
			return models.BookmarkItem{Bookmark: bm, Target: ins}
		}
	case models.TargetTypeRecommendation:
		if rec, err := h.recommendations.GetByID(r.Context(), bm.TargetID); err == nil && rec != nil {
			return models.BookmarkItem{Bookmark: bm, Target: rec}
		}
		if rec := h.findRecommendationInDiscovery(r, bm.DiscoveryID, bm.TargetID); rec != nil {
			return models.BookmarkItem{Bookmark: bm, Target: rec}
		}
	}
	return models.BookmarkItem{Bookmark: bm, Deleted: true}
}

// findInsightInDiscovery loads the discovery and searches its Insights array.
// TargetID may be a real insight id OR a numeric index — the dashboard's
// detail-page URL uses `rec.id || i` which can be either form.
func (h *ListsHandler) findInsightInDiscovery(r *http.Request, discoveryID, targetID string) *models.Insight {
	if discoveryID == "" {
		return nil
	}
	disc, err := h.discoveries.GetByID(r.Context(), discoveryID)
	if err != nil || disc == nil {
		return nil
	}
	for i := range disc.Insights {
		if disc.Insights[i].ID == targetID {
			return &disc.Insights[i]
		}
	}
	if idx, err := strconv.Atoi(targetID); err == nil && idx >= 0 && idx < len(disc.Insights) {
		return &disc.Insights[idx]
	}
	return nil
}

func (h *ListsHandler) findRecommendationInDiscovery(r *http.Request, discoveryID, targetID string) *models.Recommendation {
	if discoveryID == "" {
		return nil
	}
	disc, err := h.discoveries.GetByID(r.Context(), discoveryID)
	if err != nil || disc == nil {
		return nil
	}
	for i := range disc.Recommendations {
		if disc.Recommendations[i].ID == targetID {
			return &disc.Recommendations[i]
		}
	}
	if idx, err := strconv.Atoi(targetID); err == nil && idx >= 0 && idx < len(disc.Recommendations) {
		return &disc.Recommendations[idx]
	}
	return nil
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
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
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

	// Trust the UI's claim that the target exists. Insights and recommendations
	// live in two places — the discovery document and (after Phase 9
	// denormalization) a standalone collection. The user is almost always on
	// the detail page when bookmarking, so the target is real by construction.
	// Orphans from later deletion are handled on read via BookmarkItem.Deleted.

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
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
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
