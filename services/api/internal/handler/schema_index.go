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

// SchemaIndexHandler serves the lifecycle endpoints the dashboard uses
// to observe and drive schema indexing. Plan §8.4.
type SchemaIndexHandler struct {
	projects database.ProjectRepo
	progress database.SchemaIndexProgressRepo
	dropper  CollectionDropper                // nullable — reindex works without it if no prior index exists
	logs     *database.SchemaIndexLogRepository // nullable — log-tail endpoint returns empty when absent
}

// NewSchemaIndexHandler constructs the handler. Pass a nil dropper when
// Qdrant is not wired (community smoke-test builds, e.g.); reindex then
// relies on the worker's pre-run DropCollection as the source of truth.
func NewSchemaIndexHandler(projects database.ProjectRepo, progress database.SchemaIndexProgressRepo, dropper CollectionDropper, logs *database.SchemaIndexLogRepository) *SchemaIndexHandler {
	return &SchemaIndexHandler{projects: projects, progress: progress, dropper: dropper, logs: logs}
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
