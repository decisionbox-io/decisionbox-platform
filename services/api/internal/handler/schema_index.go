package handler

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/models"
	"golang.org/x/sync/singleflight"
)

// liveTableCacheTTL bounds how often ListCachedTables spawns a live
// --list-tables agent run for a given (project, datasource-config). Within the
// TTL a prior successful result is reused so repeated picker polls don't each
// spawn an agent job. liveTableFailTTL is shorter so a warehouse that was
// unreachable (bad credentials, VPN down) is retried soon after the operator
// fixes it, while still bounding the spawn rate on a persistent failure.
const (
	liveTableCacheTTL = 60 * time.Second
	liveTableFailTTL  = 15 * time.Second
)

type liveTableEntry struct {
	tables []string
	failed bool
	at     time.Time
}

// ttl returns the entry's effective lifetime — shorter for a failed listing so
// a fixed credential/connection recovers quickly.
func (e liveTableEntry) ttl() time.Duration {
	if e.failed {
		return liveTableFailTTL
	}
	return liveTableCacheTTL
}

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
// /cache-info uses to render "Last cached: …" in the dashboard, and
// the ListTables query that the dashboard's discovery-scope picker
// uses to render the warehouse table list.
// Concrete impl is database.SchemaCacheRepository; the in-package
// interface keeps tests from depending on Mongo.
type SchemaCacheInvalidator interface {
	Invalidate(ctx context.Context, projectID string) error
	LastCachedAt(ctx context.Context, projectID string) (time.Time, error)
	ListTables(ctx context.Context, projectID, warehouseID string) ([]string, error)
}

// SchemaIndexLogLister is the minimum repo surface the /logs endpoint
// needs. Concrete impl is *database.SchemaIndexLogRepository, which
// satisfies it without changes — the in-package interface lets tests
// inject a fake without standing up Mongo.
type SchemaIndexLogLister interface {
	List(ctx context.Context, projectID string, since time.Time, limit int) ([]database.SchemaIndexLog, error)
}

// SchemaIndexRunLister is the minimum repo surface the /runs endpoint needs:
// the durable per-datasource run history the agent stamps on completion.
// Concrete impl is *database.SchemaIndexRunRepository; nullable — when unset
// (a build without the repo wired) /runs returns an empty list.
type SchemaIndexRunLister interface {
	List(ctx context.Context, projectID, datasourceID string, limit int) ([]models.SchemaIndexRun, error)
	// LatestByDatasource returns one (most recent) run per datasource — the
	// project-page roll-up's source, so no datasource is dropped by paging.
	LatestByDatasource(ctx context.Context, projectID string) ([]models.SchemaIndexRun, error)
}

// WarehouseTableLister lists a warehouse's qualified table names live (by
// running the agent's --list-tables mode). It backs the discovery-scope table
// picker before the first index exists — at that point project_schema_cache is
// empty, so ListCachedTables falls back to this to enumerate the warehouse's
// tables cheaply (names only, no schema/blurb/embed). Concrete impl is
// AgentTableLister over runner.Runner; nullable — when unset (e.g. a build
// without an agent runner) ListCachedTables serves only the indexed cache.
type WarehouseTableLister interface {
	ListWarehouseTables(ctx context.Context, projectID, warehouseID string) ([]string, error)
}

// SchemaIndexHandler serves the lifecycle endpoints the dashboard uses
// to observe and drive schema indexing. Plan §8.4.
type SchemaIndexHandler struct {
	projects   database.ProjectRepo
	progress   database.SchemaIndexProgressRepo
	dropper    CollectionDropper      // nullable — reindex works without it if no prior index exists
	logs       SchemaIndexLogLister   // nullable — log-tail endpoint returns empty when absent
	canceller  IndexCanceller         // nullable — cancel endpoint returns 503 when worker isn't wired
	cacheRepo  SchemaCacheInvalidator // nullable — invalidate-cache endpoint returns 503 when not wired
	runs       SchemaIndexRunLister   // nullable — /runs endpoint returns empty list when absent
	lister     WarehouseTableLister   // nullable — pre-index live table listing for the scope picker

	liveMu    sync.Mutex                // guards liveCache
	liveCache map[string]liveTableEntry // per-project TTL cache of live table listings (bounds agent spawns)
	liveSF    singleflight.Group        // coalesces concurrent live listings per project
}

// getLiveTables returns a cached live listing for the cache key when it is still
// within its TTL (a failed listing is cached too, on a shorter TTL, so a
// persistent failure backs off instead of re-spawning). The key encodes the
// datasource config so a warehouse/dataset change yields a fresh listing.
func (h *SchemaIndexHandler) getLiveTables(key string) ([]string, bool) {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	e, ok := h.liveCache[key]
	if !ok || time.Since(e.at) > e.ttl() {
		return nil, false
	}
	return e.tables, true
}

// putLiveTables records a live-listing result (success, or failure→failed=true)
// with the current time so subsequent polls within the TTL reuse it.
func (h *SchemaIndexHandler) putLiveTables(key string, tables []string, failed bool) {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	if h.liveCache == nil {
		h.liveCache = make(map[string]liveTableEntry)
	}
	h.liveCache[key] = liveTableEntry{tables: tables, failed: failed, at: time.Now()}
}

// liveTableCacheKey identifies a (project, primary-datasource config) so the
// cached listing is invalidated the moment the datasource or its datasets
// change (via PUT /projects/{id}). Credentials live outside the project doc, so
// a credential fix is covered by the shorter failed-entry TTL instead.
func liveTableCacheKey(projectID string, wh models.WarehouseConfig) string {
	var b strings.Builder
	b.WriteString(projectID)
	b.WriteString("|id=")
	b.WriteString(wh.ID)
	b.WriteString("|prov=")
	b.WriteString(wh.Provider)
	// Top-level connection fields the provider factory reads directly (e.g.
	// BigQuery's data project + location), so editing them invalidates the key
	// even when provider/datasets/config are unchanged.
	b.WriteString("|proj=")
	b.WriteString(wh.ProjectID)
	b.WriteString("|loc=")
	b.WriteString(wh.Location)
	ds := append([]string(nil), wh.Datasets...)
	sort.Strings(ds)
	b.WriteString("|ds=")
	b.WriteString(strings.Join(ds, ","))
	cfgKeys := make([]string, 0, len(wh.Config))
	for k := range wh.Config {
		cfgKeys = append(cfgKeys, k)
	}
	sort.Strings(cfgKeys)
	b.WriteString("|cfg=")
	for _, k := range cfgKeys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(wh.Config[k])
		b.WriteByte(';')
	}
	return b.String()
}

// SetTableLister installs the live warehouse-table lister used by
// ListCachedTables to populate the discovery-scope picker before the first
// index exists. Optional and wired once at startup; when unset, ListCachedTables
// serves only the indexed schema cache (its prior behaviour).
func (h *SchemaIndexHandler) SetTableLister(l WarehouseTableLister) { h.lister = l }

// NewSchemaIndexHandler constructs the handler. Pass a nil dropper when
// Qdrant is not wired (community smoke-test builds, e.g.); reindex then
// relies on the worker's pre-run DropCollection as the source of truth.
// canceller is also optional — when nil the /cancel endpoint returns
// 503 (service unavailable) so the UI can hide the button gracefully.
// cacheRepo is optional — when nil the /invalidate-cache endpoint
// returns 503. runs is optional — when nil /runs returns an empty list.
func NewSchemaIndexHandler(projects database.ProjectRepo, progress database.SchemaIndexProgressRepo, dropper CollectionDropper, logs SchemaIndexLogLister, canceller IndexCanceller, cacheRepo SchemaCacheInvalidator, runs SchemaIndexRunLister) *SchemaIndexHandler {
	return &SchemaIndexHandler{projects: projects, progress: progress, dropper: dropper, logs: logs, canceller: canceller, cacheRepo: cacheRepo, runs: runs}
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

// SchemaIndexRunView is the wire shape of one durable per-datasource run
// record returned by GET /schema-index/runs. Kept separate from the Mongo doc
// so timestamps are explicit RFC 3339 strings and the project_id (implied by
// the path) is dropped.
type SchemaIndexRunView struct {
	DatasourceID    string           `json:"datasource_id"`
	DatasourceName  string           `json:"datasource_name,omitempty"`
	RunID           string           `json:"run_id"`
	Kind            string           `json:"kind"`
	ObjectsIndexed  int              `json:"objects_indexed"`
	BlurbsGenerated int              `json:"blurbs_generated"`
	Status          string           `json:"status"`
	Error           string           `json:"error,omitempty"`
	PhaseDurations  map[string]int64 `json:"phase_durations,omitempty"`
	TokensIn        int              `json:"tokens_in,omitempty"`
	TokensOut       int              `json:"tokens_out,omitempty"`
	StartedAt       string           `json:"started_at,omitempty"`
	FinishedAt      string           `json:"finished_at,omitempty"`
}

// ListRuns returns a project's per-datasource schema-index run history, newest
// finished_at first, optionally filtered to one datasource. This is the
// durable audit record — it survives the next run's progress Reset.
//
// GET /api/v1/projects/{id}/schema-index/runs?datasource_id=<id>&limit=<n>&latest=<0|1>
//
// latest=1 returns just the most recent run per datasource (the project-page
// roll-up's source; datasource_id + limit are ignored in that mode). When the
// run repo isn't wired (smoke builds), returns an empty list.
func (h *SchemaIndexHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}
	type response struct {
		Runs []SchemaIndexRunView `json:"runs"`
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
	if h.runs == nil {
		writeJSON(w, http.StatusOK, response{Runs: []SchemaIndexRunView{}})
		return
	}

	var runs []models.SchemaIndexRun
	if r.URL.Query().Get("latest") == "1" || r.URL.Query().Get("latest") == "true" {
		runs, err = h.runs.LatestByDatasource(r.Context(), id)
	} else {
		datasourceID := r.URL.Query().Get("datasource_id")
		limit := 0 // 0 → repo default; an out-of-range value is clamped there
		if l := r.URL.Query().Get("limit"); l != "" {
			if n, perr := strconv.Atoi(l); perr == nil && n > 0 {
				limit = n
			}
		}
		runs, err = h.runs.List(r.Context(), id, datasourceID, limit)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list runs: "+err.Error())
		return
	}
	out := make([]SchemaIndexRunView, len(runs))
	for i, run := range runs {
		v := SchemaIndexRunView{
			DatasourceID:    run.DatasourceID,
			DatasourceName:  run.DatasourceName,
			RunID:           run.RunID,
			Kind:            run.Kind,
			ObjectsIndexed:  run.ObjectsIndexed,
			BlurbsGenerated: run.BlurbsGenerated,
			Status:          run.Status,
			Error:           run.Error,
			PhaseDurations:  run.PhaseDurations,
			TokensIn:        run.TokensIn,
			TokensOut:       run.TokensOut,
		}
		if !run.StartedAt.IsZero() {
			v.StartedAt = run.StartedAt.UTC().Format(time.RFC3339)
		}
		if !run.FinishedAt.IsZero() {
			v.FinishedAt = run.FinishedAt.UTC().Format(time.RFC3339)
		}
		out[i] = v
	}
	writeJSON(w, http.StatusOK, response{Runs: out})
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

// ListCachedTables returns the distinct cached schema_key values
// the agent has stored for a project — one entry per qualified table
// (the exact form is provider-dependent: e.g. "<dataset>.<table>"
// for BigQuery, "<schema>.<table>" for Postgres / Snowflake /
// Databricks, "dbo.orders" for MSSQL). Used by the discovery-scope
// page's table picker so the user picks from what the agent actually
// sees, not a free-form text input.
//
// GET /api/v1/projects/{id}/schema-cache/tables
//
// When the schema cache repository isn't wired, or no rows exist yet,
// returns an empty list — the UI's empty-state render is fine.
func (h *SchemaIndexHandler) ListCachedTables(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}
	type response struct {
		Tables []string `json:"tables"`
	}
	if h.cacheRepo == nil {
		writeJSON(w, http.StatusOK, response{Tables: []string{}})
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
	// Resolve which datasource's tables to list. Empty ?warehouse_id= means the
	// project's primary — the legacy single-warehouse behaviour and the scope
	// page's default. An explicit id must name a real warehouse of THIS project,
	// so a bad/foreign id can't spawn a live agent listing against an arbitrary
	// datasource. All warehouses share the project_schema_cache keyed by warehouse
	// id, so the picker can offer any datasource's tables, each scoped to its own
	// datasource (the enforcement filter is per-warehouse too).
	whID := r.URL.Query().Get("warehouse_id")
	var wh models.WarehouseConfig
	if whID == "" {
		// PrimaryWarehouse().ID matches the id the shipped single-warehouse path
		// used (and normalises a legacy default to "default"), so the empty case is
		// unchanged. A project with no warehouse yields a zero config + empty id;
		// the live-fallback guard below skips the doomed listing.
		wh = p.PrimaryWarehouse()
		whID = wh.ID
	} else {
		var ok bool
		if wh, ok = p.WarehouseByID(whID); !ok {
			writeError(w, http.StatusNotFound, "warehouse not found on project")
			return
		}
	}
	tables, err := h.cacheRepo.ListTables(r.Context(), id, whID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list cached tables: "+err.Error())
		return
	}
	// Before the first index the schema cache is empty, so fall back to a live
	// enumeration of the warehouse's table names (cheap — names only, via the
	// agent's --list-tables mode) so the discovery-scope picker is populated and
	// the operator can restrict the table set BEFORE paying to index it. Only
	// when a lister is wired, the cache truly has nothing, AND the project has a
	// warehouse configured — a blank project with no datasource would otherwise
	// spawn a doomed agent run on every poll. A live-listing failure degrades to
	// the empty list (the picker's empty state) rather than erroring the page.
	if len(tables) == 0 && h.lister != nil && len(p.EffectiveWarehouses()) > 0 {
		// Cache key encodes the datasource config so a warehouse/dataset change
		// invalidates the cached listing immediately (not just after the TTL).
		key := liveTableCacheKey(id, wh)
		if cached, ok := h.getLiveTables(key); ok {
			// Fresh cached result (possibly empty) — reuse it; don't re-spawn.
			tables = cached
		} else {
			// Coalesce concurrent picker polls for the same key through
			// singleflight so parallel requests share ONE agent run instead of
			// each spawning its own; the TTL cache then bounds sequential polls.
			v, _, _ := h.liveSF.Do(key, func() (interface{}, error) {
				// Re-check the cache inside the flight: a just-finished leader may
				// have populated it while this call was queued behind the lock.
				if cached, ok := h.getLiveTables(key); ok {
					return cached, nil
				}
				live, lerr := h.lister.ListWarehouseTables(r.Context(), id, whID)
				if lerr != nil {
					apilog.WithField("project_id", id).
						Warn("schema-cache tables: live warehouse enumeration failed; serving empty list: " + lerr.Error())
					// Negative-cache the failure (short TTL) so a persistent problem
					// (bad creds, VPN down) backs off instead of spawning a doomed
					// run per poll, but a fixed connection is retried soon.
					h.putLiveTables(key, nil, true)
					return []string(nil), nil
				}
				h.putLiveTables(key, live, false)
				return live, nil
			})
			if v != nil {
				tables, _ = v.([]string)
			}
		}
	}
	if tables == nil {
		tables = []string{}
	}
	writeJSON(w, http.StatusOK, response{Tables: tables})
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
