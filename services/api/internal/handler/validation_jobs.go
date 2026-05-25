// Package handler — validation-job HTTP surface.
//
// The four routes registered in server.go:
//
//	POST /api/v1/discoveries/{did}/insights/{iid}/validate
//	POST /api/v1/discoveries/{did}/recommendations/{rid}/validate
//	POST /api/v1/validation-jobs/{jid}/cancel
//	GET  /api/v1/discoveries/{did}/validation-jobs?doc_kind=...&doc_id=...
//
// The Enqueue handlers:
//   - 404 if the parent discovery doesn't exist OR the cited insight /
//     recommendation isn't in its embedded array.
//   - 403 if the project's ValidationEnabled toggle is currently OFF.
//   - 409 if a non-terminal job already exists for this doc — checked
//     in two layers: a fast pre-check via CountActiveByDoc, then the
//     partial-unique-on-active Mongo index as the durable defence
//     against the concurrent-click race.
//   - 200 on success with `{ job_id }` so the dashboard can start
//     polling immediately.
//
// The Cancel handler accepts any non-terminal job and writes
// cancelled_at + status=cancelled. The worker sees the flip on its
// next heartbeat write and aborts the agent subprocess via the
// runner's cancel func (wired up in I.3).
//
// The List handler returns up to 20 jobs (most-recent first) for the
// given (discovery, doc). The dashboard's router uses the
// most-recent-non-terminal entry to drive the in-flight progress card.
package handler

import (
	"errors"
	"net/http"

	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/models"
	"github.com/google/uuid"
)

// ValidationJobsHandler bundles the dependencies the four routes
// share — the job repo (writes + reads), the discovery repo (404
// preflight), and the project repo (validation_enabled gate). All
// three are interface-typed so handler tests can inject in-memory
// mocks (see validation_jobs_test.go).
type ValidationJobsHandler struct {
	jobs        database.ValidationJobRepo
	discoveries database.DiscoveryRepo
	projects    database.ProjectRepo
}

// NewValidationJobsHandler wires the handler against its repos.
func NewValidationJobsHandler(
	jobs database.ValidationJobRepo,
	discoveries database.DiscoveryRepo,
	projects database.ProjectRepo,
) *ValidationJobsHandler {
	return &ValidationJobsHandler{
		jobs:        jobs,
		discoveries: discoveries,
		projects:    projects,
	}
}

// ValidateInsight enqueues a manual-validation job for one insight.
// POST /api/v1/discoveries/{did}/insights/{iid}/validate
func (h *ValidationJobsHandler) ValidateInsight(w http.ResponseWriter, r *http.Request) {
	h.enqueue(w, r, models.ValidationJobDocKindInsight,
		r.PathValue("did"), r.PathValue("iid"))
}

// ValidateRecommendation enqueues a manual-validation job for one rec.
// POST /api/v1/discoveries/{did}/recommendations/{rid}/validate
func (h *ValidationJobsHandler) ValidateRecommendation(w http.ResponseWriter, r *http.Request) {
	h.enqueue(w, r, models.ValidationJobDocKindRecommendation,
		r.PathValue("did"), r.PathValue("rid"))
}

func (h *ValidationJobsHandler) enqueue(w http.ResponseWriter, r *http.Request, docKind, discoveryID, docID string) {
	if discoveryID == "" || docID == "" {
		writeError(w, http.StatusBadRequest, "discovery_id and doc_id are required")
		return
	}

	disc, err := h.discoveries.GetByID(r.Context(), discoveryID)
	if err != nil || disc == nil {
		writeError(w, http.StatusNotFound, "discovery not found")
		return
	}

	// Confirm the cited doc actually lives in this discovery. Doing
	// this here means downstream code paths can assume the (discovery,
	// doc) pair is valid.
	if !discoveryHasDoc(disc, docKind, docID) {
		writeError(w, http.StatusNotFound, docKind+" not found in discovery")
		return
	}

	// Resolve the project + check the toggle. A discovery's
	// project_id is the source of truth — we don't trust a
	// client-supplied project id.
	proj, err := h.projects.GetByID(r.Context(), disc.ProjectID)
	if err != nil || proj == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if !proj.EffectiveValidationEnabled() {
		writeError(w, http.StatusForbidden, "validation is disabled for this project")
		return
	}

	// Fast pre-check for the common "user double-clicked" case so we
	// can return a clean 409 without rolling back from a duplicate
	// E11000. The partial-unique-on-active index is the durable
	// defence against the racing-clicks path.
	count, err := h.jobs.CountActiveByDoc(r.Context(), discoveryID, docKind, docID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check active jobs")
		return
	}
	if count > 0 {
		writeError(w, http.StatusConflict, "a validation job is already active for this "+docKind)
		return
	}

	job := &models.ValidationJob{
		ID:          uuid.New().String(),
		ProjectID:   disc.ProjectID,
		DiscoveryID: discoveryID,
		DocKind:     docKind,
		DocID:       docID,
		RequestedBy: requesterFromContext(r),
	}
	if err := h.jobs.Enqueue(r.Context(), job); err != nil {
		if errors.Is(err, database.ErrDuplicateActiveJob) {
			writeError(w, http.StatusConflict, "a validation job is already active for this "+docKind)
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to enqueue validation job")
		return
	}

	writeJSON(w, http.StatusOK, job)
}

// Cancel marks a non-terminal job as cancelled.
// POST /api/v1/validation-jobs/{jid}/cancel
func (h *ValidationJobsHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("jid")
	if id == "" {
		writeError(w, http.StatusBadRequest, "job id is required")
		return
	}
	err := h.jobs.Cancel(r.Context(), id)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, database.ErrNotFound):
		writeError(w, http.StatusNotFound, "validation job not found")
	case errors.Is(err, database.ErrAlreadyTerminal):
		writeError(w, http.StatusConflict, "validation job is already in a terminal state")
	default:
		writeError(w, http.StatusInternalServerError, "failed to cancel validation job")
	}
}

// ListByDoc returns up to 20 jobs (most-recent first) for one
// (discovery, doc) pair so the dashboard can decide whether to show
// the in-flight progress card or the "Run validation" empty state.
// GET /api/v1/discoveries/{did}/validation-jobs?doc_kind=insight&doc_id=...
func (h *ValidationJobsHandler) ListByDoc(w http.ResponseWriter, r *http.Request) {
	discoveryID := r.PathValue("did")
	docKind := r.URL.Query().Get("doc_kind")
	docID := r.URL.Query().Get("doc_id")
	if discoveryID == "" || docKind == "" || docID == "" {
		writeError(w, http.StatusBadRequest, "discovery_id (path), doc_kind, doc_id (query) are required")
		return
	}
	if docKind != models.ValidationJobDocKindInsight && docKind != models.ValidationJobDocKindRecommendation {
		writeError(w, http.StatusBadRequest, "doc_kind must be insight or recommendation")
		return
	}
	jobs, err := h.jobs.ListByDoc(r.Context(), discoveryID, docKind, docID, 20)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list validation jobs")
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

// discoveryHasDoc returns true when the (kind, id) pair matches an
// entry in the discovery's embedded insights / recommendations array.
// The agent's writers persist these arrays; we don't read across into
// the standalone collections here because the validation pipeline
// operates on the embedded copies.
func discoveryHasDoc(disc *models.DiscoveryResult, kind, id string) bool {
	switch kind {
	case models.ValidationJobDocKindInsight:
		for i := range disc.Insights {
			if disc.Insights[i].ID == id {
				return true
			}
		}
	case models.ValidationJobDocKindRecommendation:
		for i := range disc.Recommendations {
			if disc.Recommendations[i].ID == id {
				return true
			}
		}
	}
	return false
}

// requesterFromContext extracts the authenticated user id when auth is
// wired. Returns "" in dev / unauthenticated mode. We accept whichever
// of the auth-related context keys is present; the value is stamped
// onto ValidationJob.RequestedBy for audit logs.
func requesterFromContext(r *http.Request) string {
	for _, key := range []string{"user_id", "userID", "auth_user_id"} {
		if v, ok := r.Context().Value(contextKey(key)).(string); ok && v != "" {
			return v
		}
	}
	return ""
}

type contextKey string
