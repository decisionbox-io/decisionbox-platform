package handler

import (
	"net/http"

	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// ReadsHandler owns the /projects/{id}/reads routes.
type ReadsHandler struct {
	repo database.ReadMarkRepo
}

func NewReadsHandler(repo database.ReadMarkRepo) *ReadsHandler {
	return &ReadsHandler{repo: repo}
}

// MarkRead — POST /api/v1/projects/{id}/reads
// Idempotent: repeated calls for the same target refresh read_at without
// creating duplicates (enforced by the unique compound index on read_marks).
func (h *ReadsHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	uid, ok := userID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var body struct {
		TargetType string `json:"target_type"`
		TargetID   string `json:"target_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.TargetID == "" || !models.IsValidTargetType(body.TargetType) {
		writeError(w, http.StatusBadRequest, "target_type and target_id are required")
		return
	}

	mark := &models.ReadMark{
		ProjectID:  projectID,
		UserID:     uid,
		TargetType: body.TargetType,
		TargetID:   body.TargetID,
	}
	if err := h.repo.Upsert(r.Context(), mark); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark read: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, mark)
}

// MarkUnread — DELETE /api/v1/projects/{id}/reads
// Idempotent: returns 204 whether or not a mark existed.
func (h *ReadsHandler) MarkUnread(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	uid, ok := userID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var body struct {
		TargetType string `json:"target_type"`
		TargetID   string `json:"target_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.TargetID == "" || !models.IsValidTargetType(body.TargetType) {
		writeError(w, http.StatusBadRequest, "target_type and target_id are required")
		return
	}

	if err := h.repo.Delete(r.Context(), projectID, uid, body.TargetType, body.TargetID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark unread: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListReadIDs — GET /api/v1/projects/{id}/reads?target_type=insight
// Returns just the target_ids the caller has read, so list pages can apply
// greyed styling without fetching full mark documents.
func (h *ReadsHandler) ListReadIDs(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	uid, ok := userID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	targetType := r.URL.Query().Get("target_type")
	if !models.IsValidTargetType(targetType) {
		writeError(w, http.StatusBadRequest, "target_type query param is required")
		return
	}

	ids, err := h.repo.ListReadIDs(r.Context(), projectID, uid, targetType)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list read marks: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ids)
}
