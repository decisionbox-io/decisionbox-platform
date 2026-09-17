package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/mongo"
)

// A uniquely-named test embedding provider so it can't collide with the
// "test-embedding" that search_test.go registers in the same package.
var registerEditorEmbedOnce sync.Once

func ensureEditorTestEmbed() {
	registerEditorEmbedOnce.Do(func() {
		goembedding.RegisterWithMeta("schema-editor-embed",
			func(_ goembedding.ProviderConfig) (goembedding.Provider, error) {
				return &testEmbeddingProvider{}, nil
			},
			goembedding.ProviderMeta{ID: "schema-editor-embed", Name: "Schema Editor Test Embed"},
		)
	})
}

// --- fakes ---

type fakeEditorCache struct {
	mu             sync.Mutex
	entries        map[string]database.SchemaCacheEntry // schema_key -> entry
	lastCached     time.Time
	seenDatasource string // last warehouse/datasource id a read was scoped to
	updateCall     *struct {
		key                          string
		columns                      []models.ColumnInfo
		keyCols, metrics, dimensions []string
	}
	deleted []string
}

func newFakeEditorCache() *fakeEditorCache {
	return &fakeEditorCache{entries: map[string]database.SchemaCacheEntry{}}
}

func (f *fakeEditorCache) ListEntries(_ context.Context, _, warehouseID string) ([]database.SchemaCacheEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seenDatasource = warehouseID
	out := make([]database.SchemaCacheEntry, 0, len(f.entries))
	for _, e := range f.entries {
		out = append(out, e)
	}
	return out, nil
}

func (f *fakeEditorCache) GetEntry(_ context.Context, _, warehouseID, key string) (*database.SchemaCacheEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seenDatasource = warehouseID
	e, ok := f.entries[key]
	if !ok {
		return nil, nil
	}
	cp := e
	return &cp, nil
}

func (f *fakeEditorCache) UpdateColumns(_ context.Context, _, _, key string, cols []models.ColumnInfo, keyCols, metrics, dims []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.entries[key]
	if !ok {
		return mongo.ErrNoDocuments
	}
	e.Schema.Columns = cols
	e.Schema.KeyColumns = keyCols
	e.Schema.Metrics = metrics
	e.Schema.Dimensions = dims
	f.entries[key] = e
	f.updateCall = &struct {
		key                          string
		columns                      []models.ColumnInfo
		keyCols, metrics, dimensions []string
	}{key, cols, keyCols, metrics, dims}
	return nil
}

func (f *fakeEditorCache) DeleteTable(_ context.Context, _, _, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.entries[key]; !ok {
		return mongo.ErrNoDocuments
	}
	delete(f.entries, key)
	f.deleted = append(f.deleted, key)
	return nil
}

func (f *fakeEditorCache) LastCachedAt(_ context.Context, _ string) (time.Time, error) {
	return f.lastCached, nil
}

type fakeEditRecorder struct {
	mu         sync.Mutex
	records    []models.SchemaEdit
	sinceCount int
	sinceArg   time.Time // captures the cutoff CountSince was last called with
}

func (f *fakeEditRecorder) Record(_ context.Context, e models.SchemaEdit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, e)
	return nil
}

func (f *fakeEditRecorder) List(_ context.Context, _, _ string, _ int) ([]models.SchemaEdit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]models.SchemaEdit(nil), f.records...), nil
}

func (f *fakeEditRecorder) CountSince(_ context.Context, _ string, since time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sinceArg = since
	return f.sinceCount, nil
}

type fakeVectorEditor struct {
	mu       sync.Mutex
	points   map[string]vectorstore.SchemaPoint // table -> point
	upserts  map[string][]float64               // table -> vector last upserted
	upPayl   map[string]map[string]interface{}  // table -> payload last upserted
	setCalls map[string]map[string]interface{}  // table -> fields last set
	deletes  []string
}

func newFakeVectorEditor() *fakeVectorEditor {
	return &fakeVectorEditor{
		points:   map[string]vectorstore.SchemaPoint{},
		upserts:  map[string][]float64{},
		upPayl:   map[string]map[string]interface{}{},
		setCalls: map[string]map[string]interface{}{},
	}
}

func (f *fakeVectorEditor) GetSchemaPoints(_ context.Context, _, _ string, tables []string) (map[string]vectorstore.SchemaPoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]vectorstore.SchemaPoint{}
	for _, t := range tables {
		if p, ok := f.points[t]; ok {
			out[t] = p
		}
	}
	return out, nil
}

func (f *fakeVectorEditor) UpsertSchemaPoint(_ context.Context, _, _, table string, vector []float64, payload map[string]interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts[table] = vector
	f.upPayl[table] = payload
	f.points[table] = vectorstore.SchemaPoint{ID: table, Payload: payload}
	return nil
}

func (f *fakeVectorEditor) SetSchemaPayload(_ context.Context, _, _, table string, fields map[string]interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setCalls[table] = fields
	p, ok := f.points[table]
	if !ok {
		return nil // Qdrant no-op on a missing point
	}
	if p.Payload == nil {
		p.Payload = map[string]interface{}{}
	}
	for k, v := range fields {
		p.Payload[k] = v
	}
	f.points[table] = p
	return nil
}

func (f *fakeVectorEditor) DeleteSchemaPoint(_ context.Context, _, _, table string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.points, table)
	f.deletes = append(f.deletes, table)
	return nil
}

// --- helpers ---

func seedEntry(cache *fakeEditorCache, key string, cols ...models.ColumnInfo) {
	cache.entries[key] = database.SchemaCacheEntry{
		ProjectID:   "p1",
		WarehouseID: "default",
		SchemaKey:   key,
		Schema: models.TableSchema{
			TableName:  key,
			RowCount:   100,
			Columns:    cols,
			KeyColumns: []string{"id"},
			Metrics:    []string{"amount"},
			Dimensions: []string{"status"},
		},
	}
}

func seedPoint(vec *fakeVectorEditor, table, blurb string, keywords ...string) {
	kw := make([]interface{}, len(keywords))
	for i, k := range keywords {
		kw[i] = k
	}
	vec.points[table] = vectorstore.SchemaPoint{ID: table, Payload: map[string]interface{}{
		"blurb":        blurb,
		"keywords":     kw,
		"blurb_model":  "prior/model",
		"column_count": int64(2),
	}}
}

func editorRequest(method, target, projectID, body string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.SetPathValue("id", projectID)
	r = r.WithContext(auth.WithUser(r.Context(), &auth.UserPrincipal{Email: "jale@decisionbox.io"}))
	return r
}

func decodeData(t *testing.T, body []byte, into interface{}) {
	t.Helper()
	var wrap struct {
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		t.Fatalf("decode wrapper: %v (body=%s)", err, body)
	}
	if wrap.Error != "" {
		t.Fatalf("unexpected error response: %s", wrap.Error)
	}
	if into != nil {
		if err := json.Unmarshal(wrap.Data, into); err != nil {
			t.Fatalf("decode data: %v (data=%s)", err, wrap.Data)
		}
	}
}

// fakeRunsLister implements SchemaIndexRunLister for the "since last index"
// timestamp. Only List (limit 1 → newest run) is exercised by the editor.
type fakeRunsLister struct {
	latest []models.SchemaIndexRun
}

func (f *fakeRunsLister) List(_ context.Context, _, _ string, _ int) ([]models.SchemaIndexRun, error) {
	return f.latest, nil
}
func (f *fakeRunsLister) LatestByDatasource(_ context.Context, _ string) ([]models.SchemaIndexRun, error) {
	return f.latest, nil
}

func newEditorHandler(cache SchemaEditorCache, edits SchemaEditRecorder, vec SchemaVectorEditor) (*SchemaEditorHandler, *mockProjectRepo) {
	proj := newMockProjectRepo()
	p := &models.Project{ID: "p1", Name: "P1", Embedding: goembedding.ProjectConfig{Provider: "schema-editor-embed", Model: "test-model"}}
	_ = proj.Create(context.Background(), p) // assigns proj-1; overwrite id
	proj.projects["p1"] = p
	return NewSchemaEditorHandler(proj, cache, edits, vec, nil, nil), proj
}

// --- tests ---

func TestSchemaEditor_ListTables(t *testing.T) {
	cache := newFakeEditorCache()
	seedEntry(cache, "dbo.orders", models.ColumnInfo{Name: "id", Type: "int"}, models.ColumnInfo{Name: "total", Type: "numeric"})
	seedEntry(cache, "dbo.customers", models.ColumnInfo{Name: "id", Type: "int"})
	vec := newFakeVectorEditor()
	seedPoint(vec, "dbo.orders", "All orders.", "orders", "revenue")
	h, _ := newEditorHandler(cache, &fakeEditRecorder{}, vec)

	w := httptest.NewRecorder()
	h.ListTables(w, editorRequest(http.MethodGet, "/x?datasource_id=default", "p1", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp schemaEditorTablesResponse
	decodeData(t, w.Body.Bytes(), &resp)
	if resp.Total != 2 || len(resp.Tables) != 2 {
		t.Fatalf("expected 2 tables, got total=%d len=%d", resp.Total, len(resp.Tables))
	}
	// The orders table must carry its blurb + keywords + has_blurb.
	var orders *schemaEditorTableView
	for i := range resp.Tables {
		if resp.Tables[i].Table == "dbo.orders" {
			orders = &resp.Tables[i]
		}
	}
	if orders == nil {
		t.Fatal("dbo.orders missing from response")
	}
	if orders.Blurb != "All orders." || !orders.HasBlurb || len(orders.Keywords) != 2 {
		t.Fatalf("orders view wrong: %+v", orders)
	}
}

func TestSchemaEditor_ListTables_Search(t *testing.T) {
	cache := newFakeEditorCache()
	seedEntry(cache, "dbo.orders")
	seedEntry(cache, "dbo.customers")
	h, _ := newEditorHandler(cache, &fakeEditRecorder{}, newFakeVectorEditor())

	w := httptest.NewRecorder()
	h.ListTables(w, editorRequest(http.MethodGet, "/x?search=cust", "p1", ""))
	var resp schemaEditorTablesResponse
	decodeData(t, w.Body.Bytes(), &resp)
	if resp.Total != 1 || resp.Tables[0].Table != "dbo.customers" {
		t.Fatalf("search filter wrong: %+v", resp)
	}
}

func TestSchemaEditor_ListTables_ServiceUnavailable(t *testing.T) {
	h := NewSchemaEditorHandler(newMockProjectRepo(), nil, nil, nil, nil, nil)
	w := httptest.NewRecorder()
	h.ListTables(w, editorRequest(http.MethodGet, "/x", "p1", ""))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestSchemaEditor_UpdateBlurb(t *testing.T) {
	ensureEditorTestEmbed()
	cache := newFakeEditorCache()
	seedEntry(cache, "dbo.orders", models.ColumnInfo{Name: "id", Type: "int"}, models.ColumnInfo{Name: "total", Type: "numeric"})
	vec := newFakeVectorEditor()
	seedPoint(vec, "dbo.orders", "old blurb", "orders")
	edits := &fakeEditRecorder{}
	h, _ := newEditorHandler(cache, edits, vec)

	w := httptest.NewRecorder()
	h.UpdateTable(w, editorRequest(http.MethodPut, "/x?datasource_id=default&table=dbo.orders", "p1", `{"blurb":"new and improved"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	// Re-embedded → point upserted with a fresh vector + new blurb, keywords preserved.
	if _, ok := vec.upserts["dbo.orders"]; !ok {
		t.Fatal("expected an upsert for the re-embedded blurb")
	}
	pay := vec.upPayl["dbo.orders"]
	if pay["blurb"] != "new and improved" {
		t.Fatalf("payload blurb = %v", pay["blurb"])
	}
	if pay["embedding_model"] != "test-model" {
		t.Fatalf("embedding_model = %v, want test-model", pay["embedding_model"])
	}
	kws, _ := pay["keywords"].([]interface{})
	if len(kws) != 1 || kws[0] != "orders" {
		t.Fatalf("keywords not preserved: %v", pay["keywords"])
	}
	// Audit recorded with before/after + actor.
	if len(edits.records) != 1 {
		t.Fatalf("expected 1 audit record, got %d", len(edits.records))
	}
	rec := edits.records[0]
	if rec.Action != models.SchemaEditActionBlurb || rec.Before != "old blurb" || rec.After != "new and improved" || rec.Actor != "jale@decisionbox.io" {
		t.Fatalf("audit record wrong: %+v", rec)
	}
}

func TestSchemaEditor_UpdateBlurb_UnchangedNoOp(t *testing.T) {
	ensureEditorTestEmbed()
	cache := newFakeEditorCache()
	seedEntry(cache, "dbo.orders", models.ColumnInfo{Name: "id", Type: "int"})
	vec := newFakeVectorEditor()
	seedPoint(vec, "dbo.orders", "same", "orders")
	edits := &fakeEditRecorder{}
	h, _ := newEditorHandler(cache, edits, vec)

	w := httptest.NewRecorder()
	h.UpdateTable(w, editorRequest(http.MethodPut, "/x?table=dbo.orders", "p1", `{"blurb":"same"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if len(vec.upserts) != 0 || len(edits.records) != 0 {
		t.Fatalf("unchanged blurb should be a no-op: upserts=%d records=%d", len(vec.upserts), len(edits.records))
	}
}

func TestSchemaEditor_BlurbAndKeywords_NoBlurbTable_PreservesBoth(t *testing.T) {
	// Codex P2: a table with no blurb point yet, saving blurb + keywords in one
	// request. The blurb upsert must run first (create the point) so the
	// keyword patch that follows lands on it — neither is dropped.
	ensureEditorTestEmbed()
	cache := newFakeEditorCache()
	seedEntry(cache, "dbo.orders", models.ColumnInfo{Name: "id", Type: "int"})
	vec := newFakeVectorEditor() // NO seeded point → has_blurb=false
	edits := &fakeEditRecorder{}
	h, _ := newEditorHandler(cache, edits, vec)

	w := httptest.NewRecorder()
	h.UpdateTable(w, editorRequest(http.MethodPut, "/x?table=dbo.orders", "p1",
		`{"blurb":"Fresh blurb.","keywords":["orders","sales"]}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	p, ok := vec.points["dbo.orders"]
	if !ok {
		t.Fatal("blurb upsert should have created the point")
	}
	if p.Payload["blurb"] != "Fresh blurb." {
		t.Fatalf("blurb = %v", p.Payload["blurb"])
	}
	kws, _ := p.Payload["keywords"].([]interface{})
	if len(kws) != 2 {
		t.Fatalf("keywords dropped on a no-blurb table: %v", p.Payload["keywords"])
	}
}

func TestSchemaEditor_KeywordsOnly_NoBlurbTable_NoOp(t *testing.T) {
	// Keywords have nowhere to live without a blurb point — skip silently and
	// don't record a phantom edit.
	cache := newFakeEditorCache()
	seedEntry(cache, "dbo.orders", models.ColumnInfo{Name: "id", Type: "int"})
	vec := newFakeVectorEditor() // no point
	edits := &fakeEditRecorder{}
	h, _ := newEditorHandler(cache, edits, vec)

	w := httptest.NewRecorder()
	h.UpdateTable(w, editorRequest(http.MethodPut, "/x?table=dbo.orders", "p1", `{"keywords":["x"]}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if len(vec.setCalls) != 0 {
		t.Fatalf("no SetPayload expected on a missing point, got %v", vec.setCalls)
	}
	if len(edits.records) != 0 {
		t.Fatalf("no phantom audit record expected, got %+v", edits.records)
	}
}

func TestSchemaEditor_OmittedDatasource_ResolvesToPrimary(t *testing.T) {
	// Codex P2: an omitted datasource_id must resolve to the project's primary
	// warehouse, not the legacy "default".
	cache := newFakeEditorCache()
	seedEntry(cache, "dbo.orders", models.ColumnInfo{Name: "id", Type: "int"})
	h, proj := newEditorHandler(cache, &fakeEditRecorder{}, newFakeVectorEditor())
	// Make it a multi-warehouse project whose primary is NOT "default".
	proj.projects["p1"].PrimaryWarehouseID = "wh_b"
	proj.projects["p1"].Warehouses = []models.WarehouseConfig{{ID: "wh_a"}, {ID: "wh_b"}}

	w := httptest.NewRecorder()
	h.ListTables(w, editorRequest(http.MethodGet, "/x", "p1", "")) // no datasource_id
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if cache.seenDatasource != "wh_b" {
		t.Fatalf("omitted datasource resolved to %q, want primary wh_b", cache.seenDatasource)
	}
}

func TestSchemaEditor_UpdateKeywords(t *testing.T) {
	cache := newFakeEditorCache()
	seedEntry(cache, "dbo.orders", models.ColumnInfo{Name: "id", Type: "int"})
	vec := newFakeVectorEditor()
	seedPoint(vec, "dbo.orders", "blurb", "orders")
	edits := &fakeEditRecorder{}
	h, _ := newEditorHandler(cache, edits, vec)

	w := httptest.NewRecorder()
	h.UpdateTable(w, editorRequest(http.MethodPut, "/x?table=dbo.orders", "p1", `{"keywords":["orders","sales"]}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	// Keywords patched via SetPayload (no re-embed / upsert).
	if len(vec.upserts) != 0 {
		t.Fatalf("keyword edit must not re-embed")
	}
	set := vec.setCalls["dbo.orders"]
	kws, _ := set["keywords"].([]interface{})
	if len(kws) != 2 {
		t.Fatalf("keywords set wrong: %v", set["keywords"])
	}
	if len(edits.records) != 1 || edits.records[0].Action != models.SchemaEditActionKeywords {
		t.Fatalf("keyword audit wrong: %+v", edits.records)
	}
}

func TestSchemaEditor_RemoveColumns(t *testing.T) {
	cache := newFakeEditorCache()
	seedEntry(cache, "dbo.orders",
		models.ColumnInfo{Name: "id", Type: "int"},
		models.ColumnInfo{Name: "total", Type: "numeric"},
		models.ColumnInfo{Name: "amount", Type: "numeric"},
	)
	vec := newFakeVectorEditor()
	seedPoint(vec, "dbo.orders", "blurb", "orders")
	edits := &fakeEditRecorder{}
	h, _ := newEditorHandler(cache, edits, vec)

	// Keep only id + total (remove amount, which is also a metric).
	w := httptest.NewRecorder()
	h.UpdateTable(w, editorRequest(http.MethodPut, "/x?table=dbo.orders", "p1",
		`{"columns":[{"name":"id","type":"int"},{"name":"total","type":"numeric"}]}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if cache.updateCall == nil || len(cache.updateCall.columns) != 2 {
		t.Fatalf("UpdateColumns not called with 2 kept columns: %+v", cache.updateCall)
	}
	// "amount" was a metric — it must be filtered out of the metrics list.
	if len(cache.updateCall.metrics) != 0 {
		t.Fatalf("removed column should drop from metrics, got %v", cache.updateCall.metrics)
	}
	// column_count patched in Qdrant.
	if set := vec.setCalls["dbo.orders"]; set["column_count"] != int64(2) {
		t.Fatalf("column_count patch wrong: %v", set["column_count"])
	}
	if len(edits.records) != 1 || edits.records[0].Action != models.SchemaEditActionColumns {
		t.Fatalf("column audit wrong: %+v", edits.records)
	}
}

func TestSchemaEditor_RemoveAllColumns_400(t *testing.T) {
	cache := newFakeEditorCache()
	seedEntry(cache, "dbo.orders", models.ColumnInfo{Name: "id", Type: "int"})
	h, _ := newEditorHandler(cache, &fakeEditRecorder{}, newFakeVectorEditor())

	w := httptest.NewRecorder()
	h.UpdateTable(w, editorRequest(http.MethodPut, "/x?table=dbo.orders", "p1", `{"columns":[]}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestSchemaEditor_Update_NoChanges_400(t *testing.T) {
	cache := newFakeEditorCache()
	seedEntry(cache, "dbo.orders", models.ColumnInfo{Name: "id", Type: "int"})
	h, _ := newEditorHandler(cache, &fakeEditRecorder{}, newFakeVectorEditor())

	w := httptest.NewRecorder()
	h.UpdateTable(w, editorRequest(http.MethodPut, "/x?table=dbo.orders", "p1", `{}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestSchemaEditor_Update_NotFound_404(t *testing.T) {
	h, _ := newEditorHandler(newFakeEditorCache(), &fakeEditRecorder{}, newFakeVectorEditor())
	w := httptest.NewRecorder()
	h.UpdateTable(w, editorRequest(http.MethodPut, "/x?table=missing", "p1", `{"blurb":"x"}`))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestSchemaEditor_Update_MissingTableParam_400(t *testing.T) {
	h, _ := newEditorHandler(newFakeEditorCache(), &fakeEditRecorder{}, newFakeVectorEditor())
	w := httptest.NewRecorder()
	h.UpdateTable(w, editorRequest(http.MethodPut, "/x", "p1", `{"blurb":"x"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestSchemaEditor_DeleteTable(t *testing.T) {
	cache := newFakeEditorCache()
	seedEntry(cache, "dbo.orders", models.ColumnInfo{Name: "id", Type: "int"})
	vec := newFakeVectorEditor()
	seedPoint(vec, "dbo.orders", "the orders table", "orders")
	edits := &fakeEditRecorder{}
	h, _ := newEditorHandler(cache, edits, vec)

	w := httptest.NewRecorder()
	h.DeleteTable(w, editorRequest(http.MethodDelete, "/x?table=dbo.orders", "p1", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if len(vec.deletes) != 1 || vec.deletes[0] != "dbo.orders" {
		t.Fatalf("point not deleted: %v", vec.deletes)
	}
	if len(cache.deleted) != 1 || cache.deleted[0] != "dbo.orders" {
		t.Fatalf("cache row not deleted: %v", cache.deleted)
	}
	if len(edits.records) != 1 || edits.records[0].Action != models.SchemaEditActionDelete || edits.records[0].Before != "the orders table" {
		t.Fatalf("delete audit wrong: %+v", edits.records)
	}
}

func TestSchemaEditor_DeleteTable_NotFound_404(t *testing.T) {
	h, _ := newEditorHandler(newFakeEditorCache(), &fakeEditRecorder{}, newFakeVectorEditor())
	w := httptest.NewRecorder()
	h.DeleteTable(w, editorRequest(http.MethodDelete, "/x?table=missing", "p1", ""))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestSchemaEditor_ListEdits(t *testing.T) {
	cache := newFakeEditorCache()
	cache.lastCached = time.Now()
	edits := &fakeEditRecorder{sinceCount: 3}
	_ = edits.Record(context.Background(), models.SchemaEdit{ProjectID: "p1", Table: "dbo.orders", Action: models.SchemaEditActionBlurb})
	h, _ := newEditorHandler(cache, edits, newFakeVectorEditor())

	w := httptest.NewRecorder()
	h.ListEdits(w, editorRequest(http.MethodGet, "/x", "p1", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp schemaEditsResponse
	decodeData(t, w.Body.Bytes(), &resp)
	if len(resp.Edits) != 1 || resp.SinceLastIndex != 3 {
		t.Fatalf("edits response wrong: %+v", resp)
	}
}

func TestSchemaEditor_ListEdits_DatesFromLatestRun(t *testing.T) {
	// Codex R2 P2: "since last index" must be dated from the latest schema-index
	// run (refreshed on every re-index), not the schema cache timestamp (which a
	// cache-hit re-index doesn't refresh). When both exist, the run wins.
	runTime := time.Now().UTC()
	cacheTime := runTime.Add(-48 * time.Hour) // stale cache timestamp
	cache := newFakeEditorCache()
	cache.lastCached = cacheTime
	edits := &fakeEditRecorder{sinceCount: 0}
	runs := &fakeRunsLister{latest: []models.SchemaIndexRun{{ProjectID: "p1", FinishedAt: runTime}}}
	h, _ := newEditorHandler(cache, edits, newFakeVectorEditor())
	h.runs = runs

	w := httptest.NewRecorder()
	h.ListEdits(w, editorRequest(http.MethodGet, "/x", "p1", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !edits.sinceArg.Equal(runTime) {
		t.Fatalf("CountSince cutoff = %v, want latest run time %v (not stale cache %v)", edits.sinceArg, runTime, cacheTime)
	}
}

func TestSchemaEditor_ListEdits_FallsBackToCacheTimeWithoutRuns(t *testing.T) {
	// Pre-feature project: no run records → fall back to the cache timestamp.
	cacheTime := time.Now().UTC().Add(-time.Hour)
	cache := newFakeEditorCache()
	cache.lastCached = cacheTime
	edits := &fakeEditRecorder{}
	runs := &fakeRunsLister{latest: nil} // no runs
	h, _ := newEditorHandler(cache, edits, newFakeVectorEditor())
	h.runs = runs

	w := httptest.NewRecorder()
	h.ListEdits(w, editorRequest(http.MethodGet, "/x", "p1", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !edits.sinceArg.Equal(cacheTime) {
		t.Fatalf("CountSince cutoff = %v, want cache time %v", edits.sinceArg, cacheTime)
	}
}

func TestSchemaEditor_ListEdits_NoRepo(t *testing.T) {
	h := NewSchemaEditorHandler(newMockProjectRepo(), newFakeEditorCache(), nil, newFakeVectorEditor(), nil, nil)
	w := httptest.NewRecorder()
	h.ListEdits(w, editorRequest(http.MethodGet, "/x", "p1", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp schemaEditsResponse
	decodeData(t, w.Body.Bytes(), &resp)
	if len(resp.Edits) != 0 {
		t.Fatalf("expected empty edits, got %+v", resp.Edits)
	}
}
