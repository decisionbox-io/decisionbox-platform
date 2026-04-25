package handler

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// CollectionDropper is the minimum Qdrant surface the schema-index
// handler needs for /reindex: drop the per-project collection so the
// worker can rebuild from scratch. Matches
// services/agent/internal/ai/schema_retrieve.Retriever.DropCollection
// but kept as an in-package interface so tests can inject a fake.
type CollectionDropper interface {
	DropCollection(ctx context.Context, projectID string) error
}

// IndexCanceller is the minimum worker surface the /cancel endpoint
// needs — signals the in-flight indexing run for this project to
// abort. The concrete schemaindex.Worker type satisfies it; an
// in-package interface keeps the handler test-friendly.
type IndexCanceller interface {
	Cancel(projectID string) bool
	IsRunning(projectID string) bool
}

// SchemaCacheInvalidator is the minimum repo surface the
// /invalidate-cache endpoint needs, plus the LastCachedAt query that
// /cache-info uses to render "Last cached: …" in the dashboard.
// Concrete impl is database.SchemaCacheRepository; the in-package
// interface keeps tests from depending on Mongo.
type SchemaCacheInvalidator interface {
	Invalidate(ctx context.Context, projectID string) error
	LastCachedAt(ctx context.Context, projectID string) (time.Time, error)
}

// SchemaIndexHandler serves the lifecycle endpoints the dashboard uses
// to observe and drive schema indexing. Plan §8.4.
type SchemaIndexHandler struct {
	projects   database.ProjectRepo
	progress   database.SchemaIndexProgressRepo
	dropper    CollectionDropper                  // nullable — reindex works without it if no prior index exists
	logs       *database.SchemaIndexLogRepository // nullable — log-tail endpoint returns empty when absent
	canceller  IndexCanceller                     // nullable — cancel endpoint returns 503 when worker isn't wired
	cacheRepo  SchemaCacheInvalidator             // nullable — invalidate-cache endpoint returns 503 when not wired
}

// NewSchemaIndexHandler constructs the handler. Pass a nil dropper when
// Qdrant is not wired (community smoke-test builds, e.g.); reindex then
// relies on the worker's pre-run DropCollection as the source of truth.
// canceller is also optional — when nil the /cancel endpoint returns
// 503 (service unavailable) so the UI can hide the button gracefully.
// cacheRepo is optional — when nil the /invalidate-cache endpoint
// returns 503.
func NewSchemaIndexHandler(projects database.ProjectRepo, progress database.SchemaIndexProgressRepo, dropper CollectionDropper, logs *database.SchemaIndexLogRepository, canceller IndexCanceller, cacheRepo SchemaCacheInvalidator) *SchemaIndexHandler {
	return &SchemaIndexHandler{projects: projects, progress: progress, dropper: dropper, logs: logs, canceller: canceller, cacheRepo: cacheRepo}
}

// SchemaIndexStatusResponse is the wire shape returned by GET /status.
// Kept separate from the Mongo doc so we can drop fields without
// breaking the dashboard; e.g. run_id is internal-only and not useful
// to poll against.
type SchemaIndexStatusResponse struct {
	// Status is one of pending_indexing | indexing | ready | failed,
	// or "" when the project has never been indexed.
	Status string `json:"status"`
	// Error is the most recent failure reason; empty on happy paths.
	Error string `json:"error,omitempty"`
	// UpdatedAt is the schema_index_updated_at from the project doc
	// (last ready transition). Zero when the project has not yet
	// completed an indexing run.
	UpdatedAt string `json:"updated_at,omitempty"`
	// Progress mirrors the live worker counters (nil when no progress
	// doc exists yet — e.g. a project freshly flipped to
	// pending_indexing that the worker hasn't claimed).
	Progress *SchemaIndexProgressView `json:"progress,omitempty"`
}

// SchemaIndexProgressView is the subset of
// models.SchemaIndexProgress the dashboard actually needs.
type SchemaIndexProgressView struct {
	Phase       string `json:"phase"`
	TablesTotal int    `json:"tables_total"`
	TablesDone  int    `json:"tables_done"`
	StartedAt   string `json:"started_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}

// GetStatus returns the project's schema-indexing status + progress.
// GET /api/v1/projects/{id}/schema-index/status
func (h *SchemaIndexHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}

	p, err := h.projects.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get project: "+err.Error())
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	resp := SchemaIndexStatusResponse{Status: p.SchemaIndexStatus, Error: p.SchemaIndexError}
	if p.SchemaIndexUpdatedAt != nil {
		resp.UpdatedAt = p.SchemaIndexUpdatedAt.UTC().Format("2006-01-02T15:04:05Z")
	}

	prog, err := h.progress.Get(r.Context(), id)
	if err != nil {
		apilog.WithError(err).Warn("schema-index: progress lookup failed; serving status without live counters")
	} else if prog != nil {
		resp.Progress = &SchemaIndexProgressView{
			Phase:        prog.Phase,
			TablesTotal:  prog.TablesTotal,
			TablesDone:   prog.TablesDone,
			ErrorMessage: prog.ErrorMessage,
		}
		if !prog.StartedAt.IsZero() {
			resp.Progress.StartedAt = prog.StartedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		if !prog.UpdatedAt.IsZero() {
			resp.Progress.UpdatedAt = prog.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// Retry transitions a failed project back to pending_indexing so the
// worker picks it up. Rejects any non-failed starting state so the
// user can't accidentally interrupt an in-flight run — for that they
// use POST /reindex, which explicitly forces it.
// POST /api/v1/projects/{id}/schema-index/retry
func (h *SchemaIndexHandler) Retry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}

	p, err := h.projects.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get project: "+err.Error())
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if p.SchemaIndexStatus != models.SchemaIndexStatusFailed {
		writeError(w, http.StatusConflict, "retry is only allowed from failed state; current status is \""+p.SchemaIndexStatus+"\"")
		return
	}

	if err := h.projects.SetSchemaIndexStatus(r.Context(), id, models.SchemaIndexStatusPendingIndexing, ""); err != nil {
		writeError(w, http.StatusInternalServerError, "retry: "+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": models.SchemaIndexStatusPendingIndexing})
}

// Reindex forces a full re-index. Works from any status — the
// Advanced-tab UI uses this to apply config changes that don't
// auto-reindex (plan §3.3). Drops the Qdrant collection so the worker
// cannot accidentally resume against stale vectors.
// POST /api/v1/projects/{id}/reindex
func (h *SchemaIndexHandler) Reindex(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}

	p, err := h.projects.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get project: "+err.Error())
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	// Best-effort collection drop so the next indexing run starts from
	// a clean slate. Indexer.BuildIndex also drops first, so missing
	// collections here are harmless; we only surface an error when
	// Qdrant itself is unreachable (which would eventually fail the
	// worker run anyway — better to fail fast at the API).
	if h.dropper != nil {
		if err := h.dropper.DropCollection(r.Context(), id); err != nil {
			writeError(w, http.StatusBadGateway, "drop collection: "+err.Error())
			return
		}
	}

	if err := h.projects.SetSchemaIndexStatus(r.Context(), id, models.SchemaIndexStatusPendingIndexing, ""); err != nil {
		writeError(w, http.StatusInternalServerError, "reindex: "+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": models.SchemaIndexStatusPendingIndexing})
}

// Cancel aborts the in-flight indexing run for the project. The worker
// signals the agent subprocess via context cancellation; the project
// status transitions to "cancelled" when the subprocess exits.
//
// Responses:
//   - 202 Accepted      — cancel signal delivered; final status will
//                          land once the subprocess finishes unwinding
//                          (usually <1s; MSSQL can take a few seconds).
//   - 409 Conflict      — no run is in flight right now (either never
//                          started or already completed).
//   - 503 Unavailable   — worker is not wired (Qdrant-less build).
//
// POST /api/v1/projects/{id}/schema-index/cancel
func (h *SchemaIndexHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}
	if h.canceller == nil {
		writeError(w, http.StatusServiceUnavailable, "schema-index worker is not running on this API instance")
		return
	}

	p, err := h.projects.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get project: "+err.Error())
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	// Cheap pre-check so the UI gets a clear "nothing to cancel"
	// signal even if the worker's inflight map is empty for reasons
	// other than a race (e.g. status=ready, status=failed).
	if p.SchemaIndexStatus != models.SchemaIndexStatusIndexing {
		writeError(w, http.StatusConflict, "no indexing run is in flight; current status is \""+p.SchemaIndexStatus+"\"")
		return
	}

	if !h.canceller.Cancel(id) {
		// Raced with completion: status says indexing but the worker
		// has already moved on. UI should just re-poll status.
		writeError(w, http.StatusConflict, "indexing run completed before cancel was delivered")
		return
	}
	apilog.WithField("project_id", id).Info("Cancel request delivered to schema-index worker")
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "cancelling"})
}

// InvalidateCache resets the project's schema-discovery state so the
// next indexing run rediscovers from the warehouse. Three things
// happen, in this order — status flip FIRST so discovery is locked
// out before the slower cleanup runs:
//
//  1. project.schema_index_status flips to "needs_reindex" (atomic
//     Mongo update, ~1ms). From this instant onward, TriggerDiscovery
//     returns 409 — even if a discovery request lands while the cache
//     and Qdrant cleanup is still in flight.
//  2. project_schema_cache rows for the project are deleted (Mongo
//     DeleteMany, typically <100ms even for ERP-scale).
//  3. The Qdrant collection is dropped (a metadata + segment-file
//     operation; sub-second for typical sizes, a few seconds at the
//     extreme high end).
//
// Failure semantics: if step 2 or 3 fails after step 1 succeeded, the
// project is in needs_reindex with leftover cache/Qdrant artifacts.
// That's safe — discovery is already blocked, and clicking Clear
// again is idempotent: cache delete is a no-op when empty,
// DropCollection on a missing collection is a no-op, status is
// already needs_reindex.
//
// Rejects when an indexing run is already in flight: the worker has
// the previous cache loaded in memory by the time it gets to blurb
// generation, so deleting Mongo rows mid-run would just confuse the
// next pass.
//
// Responses:
//   - 202 Accepted      — full cleanup successful.
//   - 409 Conflict      — an indexing run is in flight; cancel first.
//   - 502 Bad Gateway   — Qdrant unreachable while dropping collection
//                          (status already flipped — discovery blocked,
//                          retry is safe).
//   - 503 Unavailable   — cache repo is not wired on this build.
//
// POST /api/v1/projects/{id}/schema-index/invalidate-cache
func (h *SchemaIndexHandler) InvalidateCache(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}
	if h.cacheRepo == nil {
		writeError(w, http.StatusServiceUnavailable, "schema cache is not configured on this API instance")
		return
	}

	p, err := h.projects.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get project: "+err.Error())
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if p.SchemaIndexStatus == models.SchemaIndexStatusIndexing {
		writeError(w, http.StatusConflict, "cannot clear cache while an indexing run is in flight; cancel it first")
		return
	}

	// Step 1: lock out discovery FIRST. Even if subsequent steps fail
	// or take seconds, no concurrent /discover request can sneak past.
	if err := h.projects.SetSchemaIndexStatus(r.Context(), id, models.SchemaIndexStatusNeedsReindex, ""); err != nil {
		writeError(w, http.StatusInternalServerError, "reset status: "+err.Error())
		return
	}
	// Step 2: drop the cache. Idempotent on retry.
	if err := h.cacheRepo.Invalidate(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "invalidate cache: "+err.Error())
		return
	}
	// Step 3: drop Qdrant. Sub-second for typical sizes; on failure
	// surface 502 so the user knows the cleanup is partial — but the
	// project is already in needs_reindex from step 1, so discovery
	// stays locked out and a retry is safe.
	if h.dropper != nil {
		if err := h.dropper.DropCollection(r.Context(), id); err != nil {
			writeError(w, http.StatusBadGateway, "drop collection: "+err.Error())
			return
		}
	}
	apilog.WithField("project_id", id).Info("Schema cache invalidated by user; status set to needs_reindex (no auto-reindex)")
	writeJSON(w, http.StatusAccepted, map[string]string{"status": models.SchemaIndexStatusNeedsReindex})
}

// SchemaCacheInfoResponse is the wire shape returned by GET /cache-info.
type SchemaCacheInfoResponse struct {
	// LastCachedAt is the RFC 3339 timestamp of the most recent
	// catalog pass that landed in the cache, or empty when the cache
	// is empty for this project.
	LastCachedAt string `json:"last_cached_at,omitempty"`
	// Cached is true when the project has at least one cached row.
	// Cheaper for the UI than parsing the timestamp.
	Cached bool `json:"cached"`
}

// GetCacheInfo returns metadata about the project's schema cache so
// the Settings → Advanced section can show "Last cached: 3 hours ago"
// next to the Clear button.
//
// GET /api/v1/projects/{id}/schema-index/cache-info
func (h *SchemaIndexHandler) GetCacheInfo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}
	if h.cacheRepo == nil {
		// Same shape as a cache miss — UI just renders "No cache".
		writeJSON(w, http.StatusOK, SchemaCacheInfoResponse{Cached: false})
		return
	}

	p, err := h.projects.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get project: "+err.Error())
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	last, err := h.cacheRepo.LastCachedAt(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cache info: "+err.Error())
		return
	}
	resp := SchemaCacheInfoResponse{Cached: !last.IsZero()}
	if !last.IsZero() {
		resp.LastCachedAt = last.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

// SchemaIndexLogLine is one line the dashboard tail renders.
type SchemaIndexLogLine struct {
	RunID     string    `json:"run_id"`
	Line      string    `json:"line"`
	CreatedAt time.Time `json:"created_at"`
}

// ListLogs returns recent agent-subprocess log lines for a project,
// optionally since an RFC 3339 cursor so the dashboard's polling view
// only receives new lines.
//
// GET /api/v1/projects/{id}/schema-index/logs?since=<rfc3339>&limit=<n>
//
// When the log repository isn't wired (community smoke builds), returns
// an empty list instead of 404 — the UI's "empty tail" state is a
// perfectly fine no-op render.
func (h *SchemaIndexHandler) ListLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}
	if h.logs == nil {
		writeJSON(w, http.StatusOK, []SchemaIndexLogLine{})
		return
	}

	var since time.Time
	if s := r.URL.Query().Get("since"); s != "" {
		parsed, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			parsed, err = time.Parse(time.RFC3339, s)
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be RFC 3339: "+err.Error())
			return
		}
		since = parsed
	}

	limit := 200
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	rows, err := h.logs.List(r.Context(), id, since, limit)
	if err != nil {
		apilog.WithError(err).Warn("schema-index logs: list failed")
		writeError(w, http.StatusInternalServerError, "failed to list logs")
		return
	}
	out := make([]SchemaIndexLogLine, len(rows))
	for i, r := range rows {
		out[i] = SchemaIndexLogLine{RunID: r.RunID, Line: r.Line, CreatedAt: r.CreatedAt}
	}
	writeJSON(w, http.StatusOK, out)
}
