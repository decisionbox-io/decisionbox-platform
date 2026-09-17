package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/mongo"
)

// Schema-editor list bounds. A big warehouse (ERP-scale) can have thousands of
// tables; DefaultSchemaEditorLimit keeps the list response bounded when the
// caller doesn't refine with ?search=, MaxSchemaEditorLimit caps a crafted
// ?limit=. The editor's search box narrows the set for anything past the cap.
const (
	DefaultSchemaEditorLimit = 200
	MaxSchemaEditorLimit     = 1000
)

// SchemaEditorCache is the schema-cache surface the editor needs: read a
// datasource's cached tables (full schema), read one table, and apply a
// single-row column removal or table deletion. Concrete impl is
// *database.SchemaCacheRepository; the in-package interface keeps tests off
// Mongo. Nullable — when nil the editor endpoints return 503.
type SchemaEditorCache interface {
	ListEntries(ctx context.Context, projectID, warehouseID string) ([]database.SchemaCacheEntry, error)
	GetEntry(ctx context.Context, projectID, warehouseID, schemaKey string) (*database.SchemaCacheEntry, error)
	UpdateColumns(ctx context.Context, projectID, warehouseID, schemaKey string, columns []models.ColumnInfo, keyColumns, metrics, dimensions []string, sampleData []map[string]interface{}) error
	DeleteTable(ctx context.Context, projectID, warehouseID, schemaKey string) error
	LastCachedAt(ctx context.Context, projectID string) (time.Time, error)
}

// SchemaEditRecorder is the audit-trail surface: append an edit and read the
// trail (+ the count since the last index for the pre-reset warning). Concrete
// impl is *database.SchemaEditRepository.
type SchemaEditRecorder interface {
	Record(ctx context.Context, edit models.SchemaEdit) error
	List(ctx context.Context, projectID, datasourceID string, limit int) ([]models.SchemaEdit, error)
	CountSince(ctx context.Context, projectID string, since time.Time) (int, error)
}

// SchemaVectorEditor is the Qdrant surface the editor needs to keep blurbs in
// sync: read current payloads, replace a point on a blurb rewrite (re-embed),
// patch payload on a keyword/column change, and delete a point on removal.
// Concrete impl is *qdrant.Provider. Nullable — when nil (Qdrant not wired) the
// editor endpoints return 503.
type SchemaVectorEditor interface {
	GetSchemaPoints(ctx context.Context, projectID, warehouseID string, tables []string) (map[string]vectorstore.SchemaPoint, error)
	UpsertSchemaPoint(ctx context.Context, projectID, warehouseID, table string, vector []float64, payload map[string]interface{}) error
	SetSchemaPayload(ctx context.Context, projectID, warehouseID, table string, fields map[string]interface{}) error
	DeleteSchemaPoint(ctx context.Context, projectID, warehouseID, table string) error
}

// SchemaEditorHandler serves the advanced schema editor: browse a datasource's
// indexed tables, rewrite a table's blurb (re-embedding the vector), remove
// columns or a whole table, and read the audit trail. Manual edits take effect
// immediately (discovery + ask read the schema cache directly) and are recorded
// so a user can review + re-apply what they changed; a full rebuild
// (Clear schema cache → re-discover) restores the warehouse's schema.
type SchemaEditorHandler struct {
	projects       database.ProjectRepo
	cache          SchemaEditorCache    // nullable — endpoints 503 when nil
	edits          SchemaEditRecorder   // nullable — audit trail unavailable when nil
	vectors        SchemaVectorEditor   // nullable — endpoints 503 when nil
	runs           SchemaIndexRunLister // nullable — used to date "edits since last index"
	secretProvider gosecrets.Provider
}

// NewSchemaEditorHandler constructs the handler. Pass nil cache/vectors in
// builds without Mongo/Qdrant wired — the endpoints then return 503 so the UI
// can hide the editor. runs is optional — when set, "edits since last index"
// is dated from the latest schema-index run (refreshed on every re-index, even
// a cache-hit one) instead of the schema cache timestamp (which a cache-hit
// re-index does not refresh).
func NewSchemaEditorHandler(projects database.ProjectRepo, cache SchemaEditorCache, edits SchemaEditRecorder, vectors SchemaVectorEditor, runs SchemaIndexRunLister, secretProvider gosecrets.Provider) *SchemaEditorHandler {
	return &SchemaEditorHandler{projects: projects, cache: cache, edits: edits, vectors: vectors, runs: runs, secretProvider: secretProvider}
}

// --- wire types ---

type schemaEditorColumnView struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
	Category string `json:"category,omitempty"`
}

type schemaEditorTableView struct {
	Table          string                   `json:"table"` // qualified schema_key, e.g. "dbo.orders"
	RowCount       int64                    `json:"row_count"`
	Columns        []schemaEditorColumnView `json:"columns"`
	KeyColumns     []string                 `json:"key_columns,omitempty"`
	Metrics        []string                 `json:"metrics,omitempty"`
	Dimensions     []string                 `json:"dimensions,omitempty"`
	Blurb          string                   `json:"blurb"`
	Keywords       []string                 `json:"keywords,omitempty"`
	HasBlurb       bool                     `json:"has_blurb"`
	BlurbModel     string                   `json:"blurb_model,omitempty"`
	EmbeddingModel string                   `json:"embedding_model,omitempty"`
}

type schemaEditorTablesResponse struct {
	Tables       []schemaEditorTableView `json:"tables"`
	Total        int                     `json:"total"`
	Truncated    bool                    `json:"truncated"`
	DatasourceID string                  `json:"datasource_id"`
}

// updateTableRequest carries only the fields being changed — a nil pointer
// means "leave this alone". Columns is the set to KEEP (removal only): the
// server intersects it by name with the existing columns, so a client can't
// inject a fabricated column, type, or category.
type updateTableRequest struct {
	Blurb    *string              `json:"blurb,omitempty"`
	Keywords *[]string            `json:"keywords,omitempty"`
	Columns  *[]models.ColumnInfo `json:"columns,omitempty"`
}

type schemaEditsResponse struct {
	Edits          []models.SchemaEdit `json:"edits"`
	SinceLastIndex int                 `json:"since_last_index"`
	DatasourceID   string              `json:"datasource_id,omitempty"`
}

// ListTables returns a datasource's indexed tables — columns from the Mongo
// schema cache joined with the blurb + keywords from Qdrant. Supports a
// ?search= substring filter (table name) and ?limit=; sample data is
// deliberately omitted (not needed, potentially large / sensitive).
//
// GET /api/v1/projects/{id}/schema-editor/tables?datasource_id=&search=&limit=
func (h *SchemaEditorHandler) ListTables(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}
	if h.cache == nil || h.vectors == nil {
		writeError(w, http.StatusServiceUnavailable, "schema editor is not available (schema cache / vector store not configured)")
		return
	}
	ctx := r.Context()
	datasourceID := h.resolveDatasourceID(ctx, projectID, r.URL.Query().Get("datasource_id"))
	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("search")))
	limit := parseLimit(r.URL.Query().Get("limit"), DefaultSchemaEditorLimit, MaxSchemaEditorLimit)

	entries, err := h.cache.ListEntries(ctx, projectID, datasourceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list schema: "+err.Error())
		return
	}

	// Filter by the search term before the Qdrant round-trip so we only fetch
	// blurbs for the tables we'll return.
	filtered := entries
	if search != "" {
		filtered = filtered[:0:0]
		for _, e := range entries {
			if strings.Contains(strings.ToLower(e.SchemaKey), search) {
				filtered = append(filtered, e)
			}
		}
	}
	total := len(filtered)
	truncated := false
	if len(filtered) > limit {
		filtered = filtered[:limit]
		truncated = true
	}

	tables := make([]string, 0, len(filtered))
	for _, e := range filtered {
		tables = append(tables, e.SchemaKey)
	}
	points, err := h.vectors.GetSchemaPoints(ctx, projectID, datasourceID, tables)
	if err != nil {
		writeError(w, http.StatusBadGateway, "read blurbs: "+err.Error())
		return
	}

	views := make([]schemaEditorTableView, 0, len(filtered))
	for _, e := range filtered {
		views = append(views, buildTableView(e, points[e.SchemaKey]))
	}
	writeJSON(w, http.StatusOK, schemaEditorTablesResponse{
		Tables:       views,
		Total:        total,
		Truncated:    truncated,
		DatasourceID: datasourceID,
	})
}

// UpdateTable applies a manual edit to one table: a blurb rewrite (re-embedded
// into Qdrant), a keyword change, and/or a column removal (Mongo cache + Qdrant
// column-count). Each applied change is recorded in the audit trail.
//
// PUT /api/v1/projects/{id}/schema-editor/tables?datasource_id=&table=
func (h *SchemaEditorHandler) UpdateTable(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}
	if h.cache == nil || h.vectors == nil {
		writeError(w, http.StatusServiceUnavailable, "schema editor is not available (schema cache / vector store not configured)")
		return
	}
	table := r.URL.Query().Get("table")
	if table == "" {
		writeError(w, http.StatusBadRequest, "table is required")
		return
	}
	datasourceID := h.resolveDatasourceID(r.Context(), projectID, r.URL.Query().Get("datasource_id"))

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB — a blurb + column list
	var req updateTableRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Blurb == nil && req.Keywords == nil && req.Columns == nil {
		writeError(w, http.StatusBadRequest, "no changes: provide blurb, keywords, and/or columns")
		return
	}

	ctx := r.Context()
	entry, err := h.cache.GetEntry(ctx, projectID, datasourceID, table)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get table: "+err.Error())
		return
	}
	if entry == nil {
		writeError(w, http.StatusNotFound, "table not found in the schema index")
		return
	}
	actor := actorEmail(r)

	// Column removal first: it's the Mongo-side change and never needs the LLM,
	// so a bad embedding config can't block it. Blurb / keyword changes follow.
	if req.Columns != nil {
		if err := h.applyColumnEdit(ctx, projectID, datasourceID, entry, *req.Columns, actor); err != nil {
			writeApplyError(w, err)
			return
		}
		// Re-read so a subsequent blurb rebuild sees the trimmed column count.
		entry, err = h.cache.GetEntry(ctx, projectID, datasourceID, table)
		if err != nil || entry == nil {
			writeError(w, http.StatusInternalServerError, "reload table after column edit")
			return
		}
	}

	// Blurb before keywords: a blurb rewrite upserts (creates) the Qdrant point,
	// so a keyword patch that follows lands on an existing point. The reverse
	// order would silently drop keywords for a table that had no blurb point yet
	// (SetSchemaPayload is a no-op on a missing point).
	if req.Blurb != nil {
		if err := h.applyBlurbEdit(ctx, projectID, datasourceID, entry, *req.Blurb, actor); err != nil {
			writeApplyError(w, err)
			return
		}
	}

	if req.Keywords != nil {
		if err := h.applyKeywordEdit(ctx, projectID, datasourceID, entry, *req.Keywords, actor); err != nil {
			writeApplyError(w, err)
			return
		}
	}

	// Return the fresh view (re-read the point so the response reflects the edit).
	points, err := h.vectors.GetSchemaPoints(ctx, projectID, datasourceID, []string{entry.SchemaKey})
	if err != nil {
		writeError(w, http.StatusBadGateway, "read blurb after edit: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, buildTableView(*entry, points[entry.SchemaKey]))
}

// DeleteTable removes a table from the schema index — its Qdrant blurb point and
// its Mongo cache row — so it stops surfacing in discovery / retrieval. The
// removal is recorded in the audit trail.
//
// DELETE /api/v1/projects/{id}/schema-editor/tables?datasource_id=&table=
func (h *SchemaEditorHandler) DeleteTable(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}
	if h.cache == nil || h.vectors == nil {
		writeError(w, http.StatusServiceUnavailable, "schema editor is not available (schema cache / vector store not configured)")
		return
	}
	table := r.URL.Query().Get("table")
	if table == "" {
		writeError(w, http.StatusBadRequest, "table is required")
		return
	}
	ctx := r.Context()
	datasourceID := h.resolveDatasourceID(ctx, projectID, r.URL.Query().Get("datasource_id"))
	entry, err := h.cache.GetEntry(ctx, projectID, datasourceID, table)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get table: "+err.Error())
		return
	}
	if entry == nil {
		writeError(w, http.StatusNotFound, "table not found in the schema index")
		return
	}

	// Capture the blurb for the audit record before we drop the point.
	points, _ := h.vectors.GetSchemaPoints(ctx, projectID, datasourceID, []string{table})
	before := payloadString(points[table].Payload, "blurb")
	if before == "" {
		before = strconv.Itoa(len(entry.Schema.Columns)) + " columns, " + strconv.FormatInt(entry.Schema.RowCount, 10) + " rows"
	}

	if err := h.vectors.DeleteSchemaPoint(ctx, projectID, datasourceID, table); err != nil {
		writeError(w, http.StatusBadGateway, "delete blurb: "+err.Error())
		return
	}
	if err := h.cache.DeleteTable(ctx, projectID, datasourceID, table); err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		writeError(w, http.StatusInternalServerError, "delete table: "+err.Error())
		return
	}
	h.record(ctx, projectID, datasourceID, table, models.SchemaEditActionDelete, before, "", actorEmail(r))
	writeJSON(w, http.StatusOK, map[string]interface{}{"deleted": table})
}

// ListEdits returns the manual-edit audit trail (newest first) plus the count
// of edits made since the last successful index — the number the dashboard
// shows in the "N manual edits will be lost" warning before a re-index / cache
// clear.
//
// GET /api/v1/projects/{id}/schema-editor/edits?datasource_id=&limit=
func (h *SchemaEditorHandler) ListEdits(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}
	if h.edits == nil {
		// No audit repo wired: report an empty trail rather than an error so the
		// warning path degrades gracefully.
		writeJSON(w, http.StatusOK, schemaEditsResponse{Edits: []models.SchemaEdit{}})
		return
	}
	ctx := r.Context()
	datasourceID := r.URL.Query().Get("datasource_id")
	limit := parseLimit(r.URL.Query().Get("limit"), database.DefaultSchemaEditLimit, database.MaxSchemaEditLimit)

	edits, err := h.edits.List(ctx, projectID, datasourceID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list edits: "+err.Error())
		return
	}

	// "Since last index" = edits after the last indexing run. Prefer the latest
	// schema-index run's finish time (stamped on every re-index, including a
	// cache-hit one) over the schema cache timestamp, which a cache-hit re-index
	// does not refresh. Fall back to the cache timestamp when no run records
	// exist (pre-feature projects). Best-effort — a count failure shouldn't fail
	// the list.
	sinceCount := 0
	if lastIndex, ok := h.lastIndexTime(ctx, projectID); ok {
		if n, nErr := h.edits.CountSince(ctx, projectID, lastIndex); nErr == nil {
			sinceCount = n
		}
	}
	writeJSON(w, http.StatusOK, schemaEditsResponse{
		Edits:          edits,
		SinceLastIndex: sinceCount,
		DatasourceID:   datasourceID,
	})
}

// --- edit appliers ---

// applyColumnEdit intersects the requested keep-set (by name) with the existing
// columns — removal only — updates the Mongo cache row (and the derived
// key/metric/dimension lists) and patches the Qdrant column_count. A no-op when
// nothing is actually removed.
func (h *SchemaEditorHandler) applyColumnEdit(ctx context.Context, projectID, datasourceID string, entry *database.SchemaCacheEntry, keep []models.ColumnInfo, actor string) error {
	existing := entry.Schema.Columns
	keepNames := make(map[string]struct{}, len(keep))
	for _, c := range keep {
		keepNames[c.Name] = struct{}{}
	}
	kept := make([]models.ColumnInfo, 0, len(existing))
	removed := make([]string, 0)
	for _, c := range existing {
		if _, ok := keepNames[c.Name]; ok {
			kept = append(kept, c) // preserve the stored definition (no client spoofing)
		} else {
			removed = append(removed, c.Name)
		}
	}
	if len(kept) == 0 {
		return &applyError{status: http.StatusBadRequest, msg: "cannot remove every column"}
	}
	if len(removed) == 0 {
		return nil // nothing changed
	}
	keptNameSet := make(map[string]struct{}, len(kept))
	for _, c := range kept {
		keptNameSet[c.Name] = struct{}{}
	}
	keyCols := filterToSet(entry.Schema.KeyColumns, keptNameSet)
	metrics := filterToSet(entry.Schema.Metrics, keptNameSet)
	dims := filterToSet(entry.Schema.Dimensions, keptNameSet)
	// Strip the removed columns' *values* from the cached sample rows too — the
	// schema provider surfaces SampleData to the agent, so leaving them would
	// keep leaking a removed column's data until the cache is rebuilt.
	samples := filterSampleRows(entry.Schema.SampleData, keptNameSet)

	if err := h.cache.UpdateColumns(ctx, projectID, datasourceID, entry.SchemaKey, kept, keyCols, metrics, dims, samples); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return &applyError{status: http.StatusNotFound, msg: "table not found in the schema index"}
		}
		return &applyError{status: http.StatusInternalServerError, msg: "update columns: " + err.Error()}
	}
	// Keep the blurb point's column_count metadata honest (best-effort — the
	// Mongo change is the source of truth for what discovery reads).
	if err := h.vectors.SetSchemaPayload(ctx, projectID, datasourceID, entry.SchemaKey, map[string]interface{}{
		"column_count": int64(len(kept)),
	}); err != nil {
		apilog.WithError(err).Warn("schema editor: column_count payload patch failed (non-fatal)")
	}
	h.record(ctx, projectID, datasourceID, entry.SchemaKey, models.SchemaEditActionColumns,
		strings.Join(columnNames(existing), ", "), strings.Join(columnNames(kept), ", "), actor)
	return nil
}

// applyKeywordEdit patches the blurb point's keywords (sparse-rerank terms) —
// payload-only, no re-embed. A no-op when the keyword set is unchanged.
func (h *SchemaEditorHandler) applyKeywordEdit(ctx context.Context, projectID, datasourceID string, entry *database.SchemaCacheEntry, keywords []string, actor string) error {
	points, err := h.vectors.GetSchemaPoints(ctx, projectID, datasourceID, []string{entry.SchemaKey})
	if err != nil {
		return &applyError{status: http.StatusBadGateway, msg: "read blurb: " + err.Error()}
	}
	point, hasPoint := points[entry.SchemaKey]
	if !hasPoint {
		// Keywords live on the blurb point; there's nowhere to store them for a
		// table with no blurb yet. Skip silently (and don't record a phantom
		// edit) — the user must write a blurb first, which the combined
		// blurb+keywords path handles by upserting the point before this runs.
		return nil
	}
	current := payloadStringSlice(point.Payload, "keywords")
	cleaned := cleanStrings(keywords)
	if equalStrings(current, cleaned) {
		return nil
	}
	if err := h.vectors.SetSchemaPayload(ctx, projectID, datasourceID, entry.SchemaKey, map[string]interface{}{
		"keywords": toInterfaceSlice(cleaned),
	}); err != nil {
		return &applyError{status: http.StatusBadGateway, msg: "update keywords: " + err.Error()}
	}
	h.record(ctx, projectID, datasourceID, entry.SchemaKey, models.SchemaEditActionKeywords,
		strings.Join(current, ", "), strings.Join(cleaned, ", "), actor)
	return nil
}

// applyBlurbEdit re-embeds the new blurb and replaces the table's Qdrant point
// (preserving the rest of the payload). A no-op when the text is unchanged.
func (h *SchemaEditorHandler) applyBlurbEdit(ctx context.Context, projectID, datasourceID string, entry *database.SchemaCacheEntry, blurb, actor string) error {
	newBlurb := strings.TrimSpace(blurb)
	if newBlurb == "" {
		return &applyError{status: http.StatusBadRequest, msg: "blurb cannot be empty"}
	}
	points, err := h.vectors.GetSchemaPoints(ctx, projectID, datasourceID, []string{entry.SchemaKey})
	if err != nil {
		return &applyError{status: http.StatusBadGateway, msg: "read blurb: " + err.Error()}
	}
	existing := points[entry.SchemaKey].Payload
	if payloadString(existing, "blurb") == newBlurb {
		return nil // unchanged
	}

	project, err := h.projects.GetByID(ctx, projectID)
	if err != nil || project == nil {
		return &applyError{status: http.StatusNotFound, msg: "project not found"}
	}
	if project.Embedding.Provider == "" {
		return &applyError{status: http.StatusPreconditionFailed, msg: "embedding provider not configured for this project"}
	}
	emb, err := newProjectEmbeddingProvider(ctx, h.secretProvider, projectID, project.Embedding.Provider, project.Embedding.Model, project.Embedding.Config)
	if err != nil {
		return &applyError{status: http.StatusInternalServerError, msg: "create embedding provider"}
	}
	vectors, err := emb.Embed(ctx, []string{newBlurb})
	if err != nil || len(vectors) != 1 {
		return &applyError{status: http.StatusBadGateway, msg: "embed blurb"}
	}

	payload := buildBlurbPayload(projectID, datasourceID, entry, existing, newBlurb, emb.ModelName())
	if err := h.vectors.UpsertSchemaPoint(ctx, projectID, datasourceID, entry.SchemaKey, vectors[0], payload); err != nil {
		return &applyError{status: http.StatusBadGateway, msg: "write blurb: " + err.Error()}
	}
	h.record(ctx, projectID, datasourceID, entry.SchemaKey, models.SchemaEditActionBlurb,
		payloadString(existing, "blurb"), newBlurb, actor)
	return nil
}

// record appends an audit entry best-effort — the edit already succeeded, so a
// trail-write failure is logged, not surfaced.
func (h *SchemaEditorHandler) record(ctx context.Context, projectID, datasourceID, table, action, before, after, actor string) {
	if h.edits == nil {
		return
	}
	if err := h.edits.Record(ctx, models.SchemaEdit{
		ProjectID:    projectID,
		DatasourceID: datasourceID,
		Table:        table,
		Action:       action,
		Before:       before,
		After:        after,
		Actor:        actor,
		At:           time.Now().UTC(),
	}); err != nil {
		apilog.WithError(err).Warn("schema editor: audit record failed (non-fatal)")
	}
}

// --- helpers ---

// applyError carries an HTTP status alongside the message so the appliers can
// signal the right code back through UpdateTable.
type applyError struct {
	status int
	msg    string
}

func (e *applyError) Error() string { return e.msg }

func writeApplyError(w http.ResponseWriter, err error) {
	var ae *applyError
	if errors.As(err, &ae) {
		writeError(w, ae.status, ae.msg)
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

// buildTableView joins a cache entry (structure) with its blurb point (blurb +
// keywords). Sample data is intentionally excluded.
func buildTableView(e database.SchemaCacheEntry, point vectorstore.SchemaPoint) schemaEditorTableView {
	cols := make([]schemaEditorColumnView, 0, len(e.Schema.Columns))
	for _, c := range e.Schema.Columns {
		cols = append(cols, schemaEditorColumnView{Name: c.Name, Type: c.Type, Nullable: c.Nullable, Category: c.Category})
	}
	return schemaEditorTableView{
		Table:          e.SchemaKey,
		RowCount:       e.Schema.RowCount,
		Columns:        cols,
		KeyColumns:     e.Schema.KeyColumns,
		Metrics:        e.Schema.Metrics,
		Dimensions:     e.Schema.Dimensions,
		Blurb:          payloadString(point.Payload, "blurb"),
		Keywords:       payloadStringSlice(point.Payload, "keywords"),
		HasBlurb:       point.Payload != nil,
		BlurbModel:     payloadString(point.Payload, "blurb_model"),
		EmbeddingModel: payloadString(point.Payload, "embedding_model"),
	}
}

// buildBlurbPayload constructs the full Qdrant payload for a blurb rewrite,
// preserving the existing point's fields where present and falling back to the
// cache entry when the table had no prior blurb point. The keys MUST match the
// agent's schema indexer (services/agent/internal/ai/schema_retrieve) — an
// upsert replaces the whole payload, so an omitted key would be lost on read.
func buildBlurbPayload(projectID, datasourceID string, entry *database.SchemaCacheEntry, existing map[string]interface{}, blurb, embeddingModel string) map[string]interface{} {
	warehouseID := datasourceID
	if warehouseID == "" {
		warehouseID = models.DefaultWarehouseID
	}
	dataset := datasetFromQualified(entry.SchemaKey)
	bareTable := entry.SchemaKey
	if dataset != "" {
		bareTable = strings.TrimPrefix(entry.SchemaKey, dataset+".")
	}
	// Preserve keywords + blurb_model from the prior point when present.
	keywords := payloadStringSlice(existing, "keywords")
	blurbModel := payloadString(existing, "blurb_model")
	return map[string]interface{}{
		"project_id":      projectID,
		"warehouse_id":    warehouseID,
		"table":           bareTable,
		"dataset":         dataset,
		"blurb":           blurb,
		"keywords":        toInterfaceSlice(keywords),
		"row_count":       entry.Schema.RowCount,
		"column_count":    int64(len(entry.Schema.Columns)),
		"blurb_model":     blurbModel,
		"embedding_model": embeddingModel,
	}
}

// datasetFromQualified extracts the dataset component from a qualified schema
// key ("dataset.table" or "dataproject.dataset.table") — the segment between
// the last two dots, or before the only dot; "" for a dot-less name. Mirrors
// the agent's datasetFromQualified in services/agent/internal/discovery.
func datasetFromQualified(s string) string {
	last, prev := -1, -1
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			prev, last = last, i
		}
	}
	if last < 0 {
		return ""
	}
	return s[prev+1 : last]
}

// lastIndexTime returns the timestamp of the project's most recent schema-index
// run, used to date "edits since last index". It prefers the latest run record
// (refreshed on every re-index, including a cache-hit one, via a fresh
// finished_at) and falls back to the schema cache's last-write time for
// pre-feature projects that have no run records. Returns ok=false when neither
// source is available (never indexed) — the caller then counts every edit.
func (h *SchemaEditorHandler) lastIndexTime(ctx context.Context, projectID string) (time.Time, bool) {
	if h.runs != nil {
		if runs, err := h.runs.List(ctx, projectID, "", 1); err == nil && len(runs) > 0 {
			if !runs[0].FinishedAt.IsZero() {
				return runs[0].FinishedAt, true
			}
		}
	}
	if h.cache != nil {
		if t, err := h.cache.LastCachedAt(ctx, projectID); err == nil && !t.IsZero() {
			return t, true
		}
	}
	return time.Time{}, false
}

// resolveDatasourceID returns the requested datasource id, or the project's
// primary warehouse id when the caller omitted it — mirroring the frontend's
// resolvePrimaryDatasourceId and models.PrimaryWarehouse so an omitted
// datasource_id targets the configured primary, not the legacy "default", on a
// multi-warehouse project. Falls back to the raw value (empty → default) when
// the project can't be loaded or has no warehouse.
func (h *SchemaEditorHandler) resolveDatasourceID(ctx context.Context, projectID, raw string) string {
	if raw != "" {
		return raw
	}
	p, err := h.projects.GetByID(ctx, projectID)
	if err != nil || p == nil {
		return raw
	}
	wh := p.PrimaryWarehouse()
	if wh.ID != "" {
		return wh.ID
	}
	if wh.Provider != "" {
		// A legacy / id-less primary warehouse resolves to the reserved default.
		return models.DefaultWarehouseID
	}
	return raw
}

func actorEmail(r *http.Request) string {
	if u, ok := auth.FromContext(r.Context()); ok && u != nil {
		return u.Email
	}
	return ""
}

func parseLimit(raw string, def, max int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func payloadString(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func payloadStringSlice(m map[string]interface{}, key string) []string {
	if m == nil {
		return nil
	}
	raw, ok := m[key].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func columnNames(cols []models.ColumnInfo) []string {
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		out = append(out, c.Name)
	}
	return out
}

// filterSampleRows drops every key not in keep from each cached sample row, so
// a removed column's values no longer reach the agent through SampleData.
// Returns nil for empty input (leaves the field unset rather than writing []).
func filterSampleRows(rows []map[string]interface{}, keep map[string]struct{}) []map[string]interface{} {
	if len(rows) == 0 {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		trimmed := make(map[string]interface{}, len(keep))
		for k, v := range row {
			if _, ok := keep[k]; ok {
				trimmed[k] = v
			}
		}
		out = append(out, trimmed)
	}
	return out
}

// filterToSet keeps only the names present in keep, preserving order.
func filterToSet(names []string, keep map[string]struct{}) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if _, ok := keep[n]; ok {
			out = append(out, n)
		}
	}
	return out
}

// cleanStrings trims and drops empty entries, preserving order.
func cleanStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func toInterfaceSlice(in []string) []interface{} {
	out := make([]interface{}, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// Compile-time checks that the concrete repos satisfy the editor interfaces.
var (
	_ SchemaEditorCache  = (*database.SchemaCacheRepository)(nil)
	_ SchemaEditRecorder = (*database.SchemaEditRepository)(nil)
)
