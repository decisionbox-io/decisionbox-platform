package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// --- mockProgress: in-memory SchemaIndexProgressRepo ---

type mockProgress struct {
	mu   sync.Mutex
	docs map[string]*models.SchemaIndexProgress
	err  error
}

func newMockProgress() *mockProgress {
	return &mockProgress{docs: make(map[string]*models.SchemaIndexProgress)}
}

func (m *mockProgress) Reset(_ context.Context, projectID, runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	now := time.Now()
	m.docs[projectID] = &models.SchemaIndexProgress{
		ProjectID: projectID,
		RunID:     runID,
		Phase:     models.SchemaIndexPhaseListingTables,
		StartedAt: now,
		UpdatedAt: now,
	}
	return nil
}
func (m *mockProgress) SetPhase(_ context.Context, projectID, phase string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if doc, ok := m.docs[projectID]; ok {
		doc.Phase = phase
	}
	return nil
}
func (m *mockProgress) UpdateTables(_ context.Context, projectID string, total, done int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if doc, ok := m.docs[projectID]; ok {
		doc.TablesTotal = total
		doc.TablesDone = done
	}
	return nil
}
func (m *mockProgress) IncrementDone(_ context.Context, projectID string, delta int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if doc, ok := m.docs[projectID]; ok {
		doc.TablesDone += delta
	}
	return nil
}
func (m *mockProgress) RecordError(_ context.Context, projectID, msg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if doc, ok := m.docs[projectID]; ok {
		doc.ErrorMessage = msg
	}
	return nil
}
func (m *mockProgress) Get(_ context.Context, projectID string) (*models.SchemaIndexProgress, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	doc, ok := m.docs[projectID]
	if !ok {
		return nil, nil
	}
	cp := *doc
	return &cp, nil
}
func (m *mockProgress) Delete(_ context.Context, projectID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.docs, projectID)
	return nil
}

// --- mockDropper ---

type mockDropper struct {
	calls []string
	err   error
}

func (m *mockDropper) DropCollection(_ context.Context, projectID string) error {
	m.calls = append(m.calls, projectID)
	return m.err
}

// --- helpers ---

func makeHandlerWithProject(t *testing.T, p *models.Project) (*SchemaIndexHandler, *mockProjectRepo, *mockProgress, *mockDropper) {
	t.Helper()
	projRepo := newMockProjectRepo()
	if err := projRepo.Create(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	prog := newMockProgress()
	drop := &mockDropper{}
	h := NewSchemaIndexHandler(projRepo, prog, drop, nil, nil, nil, nil)
	return h, projRepo, prog, drop
}

func newReq(method, url, projectID string, body string) *http.Request {
	r := httptest.NewRequest(method, url, strings.NewReader(body))
	r.SetPathValue("id", projectID)
	return r
}

// --- GetStatus ---

func TestSchemaIndex_GetStatus_HappyPath(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	p := &models.Project{
		Name:                 "t",
		Domain:               "gaming",
		Category:             "match3",
		SchemaIndexStatus:    models.SchemaIndexStatusReady,
		SchemaIndexUpdatedAt: &now,
	}
	h, proj, prog, _ := makeHandlerWithProject(t, p)

	// Seed progress doc via the mock (simulates worker in-flight output).
	_ = prog.Reset(context.Background(), p.ID, "run-1")
	_ = prog.SetPhase(context.Background(), p.ID, models.SchemaIndexPhaseEmbedding)
	_ = prog.UpdateTables(context.Background(), p.ID, 100, 42)

	w := httptest.NewRecorder()
	h.GetStatus(w, newReq("GET", "/schema-index/status", p.ID, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	resp := decodeStatus(t, w)
	if resp.Status != "ready" {
		t.Errorf("status = %q", resp.Status)
	}
	if resp.UpdatedAt == "" {
		t.Error("updated_at missing")
	}
	if resp.Progress == nil {
		t.Fatal("progress missing")
	}
	if resp.Progress.Phase != "embedding" {
		t.Errorf("progress.phase = %q", resp.Progress.Phase)
	}
	if resp.Progress.TablesTotal != 100 || resp.Progress.TablesDone != 42 {
		t.Errorf("progress counters = %d/%d", resp.Progress.TablesDone, resp.Progress.TablesTotal)
	}
	_ = proj
}

// decodeStatus unwraps the {"data": {...}} envelope that writeJSON uses.
func decodeStatus(t *testing.T, w *httptest.ResponseRecorder) SchemaIndexStatusResponse {
	t.Helper()
	var env struct {
		Data SchemaIndexStatusResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	return env.Data
}

func TestSchemaIndex_GetStatus_NoProgressDoc(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3"}
	h, _, _, _ := makeHandlerWithProject(t, p)

	w := httptest.NewRecorder()
	h.GetStatus(w, newReq("GET", "/schema-index/status", p.ID, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	resp := decodeStatus(t, w)
	if resp.Progress != nil {
		t.Errorf("progress should be nil, got %+v", resp.Progress)
	}
}

func TestSchemaIndex_GetStatus_MissingProject(t *testing.T) {
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, nil, nil, nil)
	w := httptest.NewRecorder()
	h.GetStatus(w, newReq("GET", "/schema-index/status", "nope", ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d", w.Code)
	}
}

func TestSchemaIndex_GetStatus_EmptyProjectID(t *testing.T) {
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, nil, nil, nil)
	w := httptest.NewRecorder()
	h.GetStatus(w, newReq("GET", "/schema-index/status", "", ""))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d", w.Code)
	}
}

// --- Retry ---

func TestSchemaIndex_Retry_FromFailed_TransitionsToPending(t *testing.T) {
	p := &models.Project{
		Name:              "t",
		Domain:            "gaming",
		Category:          "match3",
		SchemaIndexStatus: models.SchemaIndexStatusFailed,
		SchemaIndexError:  "boom",
	}
	h, proj, _, _ := makeHandlerWithProject(t, p)

	w := httptest.NewRecorder()
	h.Retry(w, newReq("POST", "/schema-index/retry", p.ID, ""))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d", w.Code)
	}
	got, _ := proj.GetByID(context.Background(), p.ID)
	if got.SchemaIndexStatus != "pending_indexing" {
		t.Errorf("status = %q", got.SchemaIndexStatus)
	}
	if got.SchemaIndexError != "" {
		t.Errorf("error should be cleared, got %q", got.SchemaIndexError)
	}
}

func TestSchemaIndex_Retry_FromReady_409(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3", SchemaIndexStatus: models.SchemaIndexStatusReady}
	h, _, _, _ := makeHandlerWithProject(t, p)

	w := httptest.NewRecorder()
	h.Retry(w, newReq("POST", "/schema-index/retry", p.ID, ""))
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

func TestSchemaIndex_Retry_FromIndexing_409(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3", SchemaIndexStatus: models.SchemaIndexStatusIndexing}
	h, _, _, _ := makeHandlerWithProject(t, p)

	w := httptest.NewRecorder()
	h.Retry(w, newReq("POST", "/schema-index/retry", p.ID, ""))
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

func TestSchemaIndex_Retry_MissingProject(t *testing.T) {
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, nil, nil, nil)
	w := httptest.NewRecorder()
	h.Retry(w, newReq("POST", "/schema-index/retry", "nope", ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d", w.Code)
	}
}

// --- Reindex ---

func TestSchemaIndex_Reindex_FromReady_DropsAndTransitions(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3", SchemaIndexStatus: models.SchemaIndexStatusReady}
	h, proj, _, drop := makeHandlerWithProject(t, p)

	w := httptest.NewRecorder()
	h.Reindex(w, newReq("POST", "/reindex", p.ID, ""))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d", w.Code)
	}
	if len(drop.calls) != 1 || drop.calls[0] != p.ID {
		t.Errorf("DropCollection called with %v", drop.calls)
	}
	got, _ := proj.GetByID(context.Background(), p.ID)
	if got.SchemaIndexStatus != "pending_indexing" {
		t.Errorf("status = %q", got.SchemaIndexStatus)
	}
}

func TestSchemaIndex_Reindex_FromFailed_Allowed(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3", SchemaIndexStatus: models.SchemaIndexStatusFailed, SchemaIndexError: "prev err"}
	h, proj, _, _ := makeHandlerWithProject(t, p)

	w := httptest.NewRecorder()
	h.Reindex(w, newReq("POST", "/reindex", p.ID, ""))
	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d", w.Code)
	}
	got, _ := proj.GetByID(context.Background(), p.ID)
	if got.SchemaIndexError != "" {
		t.Errorf("reindex should clear prior error, got %q", got.SchemaIndexError)
	}
}

func TestSchemaIndex_Reindex_DropperErrorPropagated(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3"}
	h, proj, _, drop := makeHandlerWithProject(t, p)
	drop.err = errors.New("qdrant down")

	w := httptest.NewRecorder()
	h.Reindex(w, newReq("POST", "/reindex", p.ID, ""))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
	// Status must NOT have transitioned — we bail before the repo call.
	got, _ := proj.GetByID(context.Background(), p.ID)
	if got.SchemaIndexStatus == "pending_indexing" {
		t.Errorf("status flipped despite dropper failure: %q", got.SchemaIndexStatus)
	}
}

func TestSchemaIndex_Reindex_NilDropperSkipsDropStep(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3"}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	h.Reindex(w, newReq("POST", "/reindex", p.ID, ""))
	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d", w.Code)
	}
	got, _ := projRepo.GetByID(context.Background(), p.ID)
	if got.SchemaIndexStatus != "pending_indexing" {
		t.Errorf("status = %q", got.SchemaIndexStatus)
	}
}

func TestSchemaIndex_Reindex_MissingProject(t *testing.T) {
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), &mockDropper{}, nil, nil, nil, nil)
	w := httptest.NewRecorder()
	h.Reindex(w, newReq("POST", "/reindex", "nope", ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d", w.Code)
	}
}

// --- Cancel ---

// mockCanceller captures Cancel calls so tests can assert the handler
// only signals when its preconditions hold (status==indexing,
// canceller wired, project exists).
type mockCanceller struct {
	cancelCalled []string
	cancelReturn bool
	runningReturn bool
}

func (m *mockCanceller) Cancel(projectID string) bool {
	m.cancelCalled = append(m.cancelCalled, projectID)
	return m.cancelReturn
}
func (m *mockCanceller) IsRunning(string) bool { return m.runningReturn }

func TestSchemaIndex_Cancel_HappyPath(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3", SchemaIndexStatus: models.SchemaIndexStatusIndexing}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	mc := &mockCanceller{cancelReturn: true}
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, mc, nil, nil)

	w := httptest.NewRecorder()
	h.Cancel(w, newReq("POST", "/schema-index/cancel", p.ID, ""))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d", w.Code)
	}
	if len(mc.cancelCalled) != 1 || mc.cancelCalled[0] != p.ID {
		t.Errorf("Cancel called with %v", mc.cancelCalled)
	}
}

func TestSchemaIndex_Cancel_NoCanceller_503(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3", SchemaIndexStatus: models.SchemaIndexStatusIndexing}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	h.Cancel(w, newReq("POST", "/schema-index/cancel", p.ID, ""))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestSchemaIndex_Cancel_NotIndexing_409(t *testing.T) {
	for _, status := range []string{
		models.SchemaIndexStatusReady,
		models.SchemaIndexStatusFailed,
		models.SchemaIndexStatusPendingIndexing,
		"",
	} {
		t.Run("status="+status, func(t *testing.T) {
			p := &models.Project{Name: "t", Domain: "gaming", Category: "match3", SchemaIndexStatus: status}
			projRepo := newMockProjectRepo()
			_ = projRepo.Create(context.Background(), p)
			mc := &mockCanceller{cancelReturn: true}
			h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, mc, nil, nil)

			w := httptest.NewRecorder()
			h.Cancel(w, newReq("POST", "/schema-index/cancel", p.ID, ""))
			if w.Code != http.StatusConflict {
				t.Errorf("status = %d, want 409", w.Code)
			}
			if len(mc.cancelCalled) != 0 {
				t.Errorf("Cancel must not be called when status=%q, got %v", status, mc.cancelCalled)
			}
		})
	}
}

func TestSchemaIndex_Cancel_RaceWithCompletion_409(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3", SchemaIndexStatus: models.SchemaIndexStatusIndexing}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	// Worker has already finished by the time we try to cancel: returns
	// false from Cancel, handler maps that to 409.
	mc := &mockCanceller{cancelReturn: false}
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, mc, nil, nil)

	w := httptest.NewRecorder()
	h.Cancel(w, newReq("POST", "/schema-index/cancel", p.ID, ""))
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

func TestSchemaIndex_Cancel_MissingProject_404(t *testing.T) {
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, &mockCanceller{}, nil, nil)
	w := httptest.NewRecorder()
	h.Cancel(w, newReq("POST", "/schema-index/cancel", "nope", ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestSchemaIndex_Cancel_EmptyProjectID_400(t *testing.T) {
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, &mockCanceller{}, nil, nil)
	w := httptest.NewRecorder()
	h.Cancel(w, newReq("POST", "/schema-index/cancel", "", ""))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// --- InvalidateCache ---

// mockCacheInvalidator records each Invalidate call so tests can assert
// the handler only fires it when preconditions hold. lastCachedAt /
// lastErr drive the GetCacheInfo path; called/err drive the
// InvalidateCache path.
type mockCacheInvalidator struct {
	called                []string
	err                   error
	lastCachedAt          time.Time
	lastErr               error
	tables                []string
	tablesErr             error
	listTablesWarehouseID string // captures the warehouse id ListTables was scoped to
}

func (m *mockCacheInvalidator) Invalidate(_ context.Context, projectID string) error {
	m.called = append(m.called, projectID)
	return m.err
}

func (m *mockCacheInvalidator) LastCachedAt(_ context.Context, _ string) (time.Time, error) {
	return m.lastCachedAt, m.lastErr
}

func (m *mockCacheInvalidator) ListTables(_ context.Context, _, warehouseID string) ([]string, error) {
	m.listTablesWarehouseID = warehouseID
	return m.tables, m.tablesErr
}

func TestSchemaIndex_InvalidateCache_HappyPath(t *testing.T) {
	for _, status := range []string{
		models.SchemaIndexStatusReady,
		models.SchemaIndexStatusFailed,
		models.SchemaIndexStatusPendingIndexing,
		models.SchemaIndexStatusCancelled,
		"", // never indexed
	} {
		t.Run("status="+status, func(t *testing.T) {
			p := &models.Project{Name: "t", Domain: "gaming", Category: "match3", SchemaIndexStatus: status, SchemaIndexError: "prev"}
			projRepo := newMockProjectRepo()
			_ = projRepo.Create(context.Background(), p)
			ci := &mockCacheInvalidator{}
			drop := &mockDropper{}
			h := NewSchemaIndexHandler(projRepo, newMockProgress(), drop, nil, nil, ci, nil)

			w := httptest.NewRecorder()
			h.InvalidateCache(w, newReq("POST", "/schema-index/invalidate-cache", p.ID, ""))
			if w.Code != http.StatusAccepted {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			if len(ci.called) != 1 || ci.called[0] != p.ID {
				t.Errorf("Invalidate called with %v, want [%s]", ci.called, p.ID)
			}
			if len(drop.calls) != 1 || drop.calls[0] != p.ID {
				t.Errorf("DropCollection called with %v, want [%s]", drop.calls, p.ID)
			}
			got, _ := projRepo.GetByID(context.Background(), p.ID)
			if got.SchemaIndexStatus != models.SchemaIndexStatusNeedsReindex {
				t.Errorf("status after invalidate = %q, want needs_reindex", got.SchemaIndexStatus)
			}
			if got.SchemaIndexError != "" {
				t.Errorf("error should be cleared on invalidate, got %q", got.SchemaIndexError)
			}
		})
	}
}

func TestSchemaIndex_InvalidateCache_NilDropperOK(t *testing.T) {
	// On builds without Qdrant, the dropper is nil. Invalidate-cache
	// must still succeed (cache cleared, status flipped) — the next
	// reindex will rebuild whatever exists.
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3", SchemaIndexStatus: models.SchemaIndexStatusReady}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	ci := &mockCacheInvalidator{}
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci, nil)

	w := httptest.NewRecorder()
	h.InvalidateCache(w, newReq("POST", "/schema-index/invalidate-cache", p.ID, ""))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	got, _ := projRepo.GetByID(context.Background(), p.ID)
	if got.SchemaIndexStatus != models.SchemaIndexStatusNeedsReindex {
		t.Errorf("status = %q, want needs_reindex", got.SchemaIndexStatus)
	}
}

func TestSchemaIndex_InvalidateCache_DropperError_502(t *testing.T) {
	// Qdrant unreachable while dropping the collection. Status was
	// flipped FIRST (step 1), then cache cleared (step 2), then drop
	// failed (step 3). 502 surfaces the partial cleanup; status stays
	// at needs_reindex so discovery is still blocked and the user can
	// safely retry.
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3", SchemaIndexStatus: models.SchemaIndexStatusReady}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	ci := &mockCacheInvalidator{}
	drop := &mockDropper{err: errors.New("qdrant down")}
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), drop, nil, nil, ci, nil)

	w := httptest.NewRecorder()
	h.InvalidateCache(w, newReq("POST", "/schema-index/invalidate-cache", p.ID, ""))
	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}
	// Cache cleared (step 2 ran before the dropper).
	if len(ci.called) != 1 {
		t.Errorf("Invalidate called %d times, want 1", len(ci.called))
	}
	// Status WAS flipped (step 1). Discovery is locked out even
	// though Qdrant cleanup failed — that's the whole point of doing
	// status-flip first.
	got, _ := projRepo.GetByID(context.Background(), p.ID)
	if got.SchemaIndexStatus != models.SchemaIndexStatusNeedsReindex {
		t.Errorf("status = %q after partial failure, want needs_reindex (must lock out discovery even when cleanup fails)", got.SchemaIndexStatus)
	}
}

func TestSchemaIndex_InvalidateCache_StatusFlippedBeforeCacheDelete(t *testing.T) {
	// Defense-in-depth: if cache-delete itself fails, status must
	// already be needs_reindex so a concurrent /discover request
	// can't sneak through. The whole point of step-1-first ordering.
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3", SchemaIndexStatus: models.SchemaIndexStatusReady}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	ci := &mockCacheInvalidator{err: errors.New("mongo blip")}
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), &mockDropper{}, nil, nil, ci, nil)

	w := httptest.NewRecorder()
	h.InvalidateCache(w, newReq("POST", "/schema-index/invalidate-cache", p.ID, ""))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	got, _ := projRepo.GetByID(context.Background(), p.ID)
	if got.SchemaIndexStatus != models.SchemaIndexStatusNeedsReindex {
		t.Errorf("status = %q after cache-delete failure, want needs_reindex (lock-out must hold even on partial failure)", got.SchemaIndexStatus)
	}
}

func TestSchemaIndex_InvalidateCache_NoRepo_503(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3", SchemaIndexStatus: models.SchemaIndexStatusReady}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	h.InvalidateCache(w, newReq("POST", "/schema-index/invalidate-cache", p.ID, ""))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestSchemaIndex_InvalidateCache_WhileIndexing_409(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3", SchemaIndexStatus: models.SchemaIndexStatusIndexing}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	ci := &mockCacheInvalidator{}
	drop := &mockDropper{}
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), drop, nil, nil, ci, nil)

	w := httptest.NewRecorder()
	h.InvalidateCache(w, newReq("POST", "/schema-index/invalidate-cache", p.ID, ""))
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
	if len(ci.called) != 0 {
		t.Errorf("Invalidate must NOT be called while indexing, got %v", ci.called)
	}
	if len(drop.calls) != 0 {
		t.Errorf("DropCollection must NOT be called while indexing, got %v", drop.calls)
	}
}

func TestSchemaIndex_InvalidateCache_MissingProject_404(t *testing.T) {
	ci := &mockCacheInvalidator{}
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, nil, ci, nil)
	w := httptest.NewRecorder()
	h.InvalidateCache(w, newReq("POST", "/schema-index/invalidate-cache", "nope", ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
	if len(ci.called) != 0 {
		t.Errorf("Invalidate must NOT be called when project missing")
	}
}

func TestSchemaIndex_InvalidateCache_EmptyProjectID_400(t *testing.T) {
	ci := &mockCacheInvalidator{}
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, nil, ci, nil)
	w := httptest.NewRecorder()
	h.InvalidateCache(w, newReq("POST", "/schema-index/invalidate-cache", "", ""))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSchemaIndex_InvalidateCache_RepoError_500(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3", SchemaIndexStatus: models.SchemaIndexStatusReady}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	ci := &mockCacheInvalidator{err: errors.New("mongo down")}
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci, nil)

	w := httptest.NewRecorder()
	h.InvalidateCache(w, newReq("POST", "/schema-index/invalidate-cache", p.ID, ""))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

// --- GetCacheInfo ---

// decodeCacheInfo unwraps the {"data": {...}} envelope.
func decodeCacheInfo(t *testing.T, w *httptest.ResponseRecorder) SchemaCacheInfoResponse {
	t.Helper()
	var env struct {
		Data SchemaCacheInfoResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	return env.Data
}

func TestSchemaIndex_GetCacheInfo_HappyPath(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3"}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	when := time.Date(2026, 4, 25, 10, 30, 0, 0, time.UTC)
	ci := &mockCacheInvalidator{lastCachedAt: when}
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci, nil)

	w := httptest.NewRecorder()
	h.GetCacheInfo(w, newReq("GET", "/schema-index/cache-info", p.ID, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	got := decodeCacheInfo(t, w)
	if !got.Cached {
		t.Errorf("cached = false, want true")
	}
	if got.LastCachedAt != "2026-04-25T10:30:00Z" {
		t.Errorf("last_cached_at = %q", got.LastCachedAt)
	}
}

func TestSchemaIndex_GetCacheInfo_EmptyCache(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3"}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	ci := &mockCacheInvalidator{} // zero time → empty cache
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci, nil)

	w := httptest.NewRecorder()
	h.GetCacheInfo(w, newReq("GET", "/schema-index/cache-info", p.ID, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	got := decodeCacheInfo(t, w)
	if got.Cached {
		t.Errorf("cached = true on empty cache")
	}
	if got.LastCachedAt != "" {
		t.Errorf("last_cached_at = %q on empty cache", got.LastCachedAt)
	}
}

func TestSchemaIndex_GetCacheInfo_NoRepo_OK_Empty(t *testing.T) {
	// Builds without the cache repo wired return the same empty shape
	// instead of 503 — the UI can render "No cache" without special-
	// casing.
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3"}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	h.GetCacheInfo(w, newReq("GET", "/schema-index/cache-info", p.ID, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	got := decodeCacheInfo(t, w)
	if got.Cached {
		t.Errorf("cached = true when repo not wired")
	}
}

func TestSchemaIndex_GetCacheInfo_MissingProject_404(t *testing.T) {
	ci := &mockCacheInvalidator{}
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, nil, ci, nil)
	w := httptest.NewRecorder()
	h.GetCacheInfo(w, newReq("GET", "/schema-index/cache-info", "nope", ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestSchemaIndex_GetCacheInfo_EmptyProjectID_400(t *testing.T) {
	ci := &mockCacheInvalidator{}
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, nil, ci, nil)
	w := httptest.NewRecorder()
	h.GetCacheInfo(w, newReq("GET", "/schema-index/cache-info", "", ""))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSchemaIndex_GetCacheInfo_RepoError_500(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3"}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	ci := &mockCacheInvalidator{lastErr: errors.New("mongo down")}
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci, nil)

	w := httptest.NewRecorder()
	h.GetCacheInfo(w, newReq("GET", "/schema-index/cache-info", p.ID, ""))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

// --- ListCachedTables ---

// decodeListCachedTables decodes the {"data": {"tables": [...]}}
// response envelope returned by the ListCachedTables handler. The
// outer "data" wrap is added by writeJSON.
func decodeListCachedTables(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	var got struct {
		Data struct {
			Tables []string `json:"tables"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	return got.Data.Tables
}

func TestSchemaIndex_ListCachedTables_HappyPath(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3"}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	ci := &mockCacheInvalidator{tables: []string{"a.x", "a.y", "b.z"}}
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci, nil)

	w := httptest.NewRecorder()
	h.ListCachedTables(w, newReq("GET", "/schema-cache/tables", p.ID, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	got := decodeListCachedTables(t, w)
	if len(got) != 3 || got[0] != "a.x" || got[1] != "a.y" || got[2] != "b.z" {
		t.Errorf("tables = %v, want [a.x a.y b.z]", got)
	}
}

func TestSchemaIndex_ListCachedTables_EmptyCache(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3"}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	ci := &mockCacheInvalidator{} // tables nil → empty list, not null
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci, nil)

	w := httptest.NewRecorder()
	h.ListCachedTables(w, newReq("GET", "/schema-cache/tables", p.ID, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	// JSON must serialise an empty list as `[]`, not `null`, so the
	// dashboard's mapping helpers don't need to special-case nil.
	if !strings.Contains(w.Body.String(), `"tables":[]`) {
		t.Errorf("body = %s, want tables:[]", w.Body.String())
	}
	got := decodeListCachedTables(t, w)
	if len(got) != 0 {
		t.Errorf("tables = %v, want empty", got)
	}
}

// fakeTableLister is a WarehouseTableLister test double: it records whether it
// was called, which warehouse ids it was asked for, and returns a canned list
// (or a per-warehouse list via byWarehouse) or an error.
type fakeTableLister struct {
	tables       []string
	byWarehouse  map[string][]string // optional per-warehouse result; falls back to tables
	err          error
	called       bool
	calls        int
	gotWarehouse []string // warehouse ids seen, in call order
}

func (f *fakeTableLister) ListWarehouseTables(_ context.Context, _, warehouseID string) ([]string, error) {
	f.called = true
	f.calls++
	f.gotWarehouse = append(f.gotWarehouse, warehouseID)
	if f.byWarehouse != nil {
		if v, ok := f.byWarehouse[warehouseID]; ok {
			return v, f.err
		}
	}
	return f.tables, f.err
}

func TestSchemaIndex_ListCachedTables_LiveFallback_WhenCacheEmpty(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3",
		Warehouse: models.WarehouseConfig{Provider: "postgres", Datasets: []string{"public"}}}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	ci := &mockCacheInvalidator{} // empty cache → triggers the live fallback
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci, nil)
	lister := &fakeTableLister{tables: []string{"dbo.orders", "dbo.customers"}}
	h.SetTableLister(lister)

	w := httptest.NewRecorder()
	h.ListCachedTables(w, newReq("GET", "/schema-cache/tables", p.ID, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if !lister.called {
		t.Error("expected live lister to be called when the cache is empty")
	}
	got := decodeListCachedTables(t, w)
	if len(got) != 2 || got[0] != "dbo.orders" || got[1] != "dbo.customers" {
		t.Errorf("tables = %v, want the live-enumerated set", got)
	}
}

func TestSchemaIndex_ListCachedTables_LiveFallback_SkippedWhenCacheNonEmpty(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3"}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	ci := &mockCacheInvalidator{tables: []string{"a.x"}} // cache has rows → indexed set wins
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci, nil)
	lister := &fakeTableLister{tables: []string{"dbo.should_not_appear"}}
	h.SetTableLister(lister)

	w := httptest.NewRecorder()
	h.ListCachedTables(w, newReq("GET", "/schema-cache/tables", p.ID, ""))
	if lister.called {
		t.Error("live lister must not be called when the schema cache already has tables")
	}
	got := decodeListCachedTables(t, w)
	if len(got) != 1 || got[0] != "a.x" {
		t.Errorf("tables = %v, want the indexed cache set [a.x]", got)
	}
}

func TestSchemaIndex_ListCachedTables_LiveFallback_CachedAcrossPolls(t *testing.T) {
	// A second poll within the TTL must reuse the first result and NOT spawn
	// another agent run (bounds doomed spawns on a flaky/unreachable warehouse).
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3",
		Warehouse: models.WarehouseConfig{Provider: "postgres", Datasets: []string{"public"}}}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	ci := &mockCacheInvalidator{} // empty cache
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci, nil)
	lister := &fakeTableLister{tables: []string{"dbo.orders"}}
	h.SetTableLister(lister)

	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		h.ListCachedTables(w, newReq("GET", "/schema-cache/tables", p.ID, ""))
		if got := decodeListCachedTables(t, w); len(got) != 1 || got[0] != "dbo.orders" {
			t.Fatalf("poll %d: tables = %v, want [dbo.orders]", i, got)
		}
	}
	if lister.calls != 1 {
		t.Errorf("lister spawned %d times across 3 polls, want 1 (TTL-cached)", lister.calls)
	}
}

func TestSchemaIndex_ListCachedTables_LiveFallback_SkippedWithoutWarehouse(t *testing.T) {
	// A blank project with no datasource must NOT spawn a doomed --list-tables
	// agent run on every poll.
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3"} // no warehouse
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	ci := &mockCacheInvalidator{} // empty cache
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci, nil)
	lister := &fakeTableLister{tables: []string{"dbo.should_not_appear"}}
	h.SetTableLister(lister)

	w := httptest.NewRecorder()
	h.ListCachedTables(w, newReq("GET", "/schema-cache/tables", p.ID, ""))
	if lister.called {
		t.Error("live lister must not be called for a project with no warehouse")
	}
	if got := decodeListCachedTables(t, w); len(got) != 0 {
		t.Errorf("tables = %v, want empty (no warehouse, no cache)", got)
	}
}

func TestSchemaIndex_ListCachedTables_LiveFallback_ErrorDegradesToEmpty(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3",
		Warehouse: models.WarehouseConfig{Provider: "postgres", Datasets: []string{"public"}}}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	ci := &mockCacheInvalidator{} // empty cache
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci, nil)
	h.SetTableLister(&fakeTableLister{err: errors.New("warehouse unreachable")})

	w := httptest.NewRecorder()
	h.ListCachedTables(w, newReq("GET", "/schema-cache/tables", p.ID, ""))
	// A live-listing failure must not error the page — the picker's empty
	// state is a fine render.
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (live-list failure degrades to empty)", w.Code)
	}
	got := decodeListCachedTables(t, w)
	if len(got) != 0 {
		t.Errorf("tables = %v, want empty on live-list error", got)
	}
}

// twoWarehouseProject builds a managed multi-warehouse project: primary
// wh_mssql (dbo) + secondary wh_pg (public). Used by the per-datasource
// discovery-scope picker tests.
func twoWarehouseProject() *models.Project {
	return &models.Project{
		Name: "t", Domain: "gaming", Category: "match3",
		Warehouses: []models.WarehouseConfig{
			{ID: "wh_mssql", Provider: "sqlserver", Datasets: []string{"dbo"}},
			{ID: "wh_pg", Provider: "postgres", Datasets: []string{"public"}},
		},
		PrimaryWarehouseID: "wh_mssql",
	}
}

func TestSchemaIndex_ListCachedTables_WarehouseID_ScopesCacheToDatasource(t *testing.T) {
	// An explicit ?warehouse_id= must scope the cached-table lookup to THAT
	// datasource, so the picker shows the secondary's tables — not the primary's.
	p := twoWarehouseProject()
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	ci := &mockCacheInvalidator{tables: []string{"public.customer"}} // cache hit → no live fallback
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci, nil)

	w := httptest.NewRecorder()
	h.ListCachedTables(w, newReq("GET", "/schema-cache/tables?warehouse_id=wh_pg", p.ID, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if ci.listTablesWarehouseID != "wh_pg" {
		t.Errorf("ListTables scoped to %q, want wh_pg", ci.listTablesWarehouseID)
	}
}

func TestSchemaIndex_ListCachedTables_WarehouseID_EmptyResolvesToPrimary(t *testing.T) {
	// No ?warehouse_id= must resolve to the primary — the shipped single-warehouse
	// behaviour, preserved for a multi-warehouse project's default view.
	p := twoWarehouseProject()
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	ci := &mockCacheInvalidator{tables: []string{"dbo.orders"}}
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci, nil)

	w := httptest.NewRecorder()
	h.ListCachedTables(w, newReq("GET", "/schema-cache/tables", p.ID, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if ci.listTablesWarehouseID != "wh_mssql" {
		t.Errorf("ListTables scoped to %q, want the primary wh_mssql", ci.listTablesWarehouseID)
	}
}

func TestSchemaIndex_ListCachedTables_WarehouseID_Unknown_404(t *testing.T) {
	// A warehouse id that isn't on the project must 404 — and must NOT spawn a
	// live --list-tables agent run against an arbitrary/foreign datasource.
	p := twoWarehouseProject()
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	ci := &mockCacheInvalidator{} // empty cache — would trigger the live fallback if we got that far
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci, nil)
	lister := &fakeTableLister{tables: []string{"dbo.should_not_appear"}}
	h.SetTableLister(lister)

	w := httptest.NewRecorder()
	h.ListCachedTables(w, newReq("GET", "/schema-cache/tables?warehouse_id=wh_nope", p.ID, ""))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown warehouse id", w.Code)
	}
	if lister.called {
		t.Error("live lister must not be called for an unknown warehouse id")
	}
}

func TestSchemaIndex_ListCachedTables_WarehouseID_LiveFallback_PerDatasource(t *testing.T) {
	// The live-listing TTL cache key must be per-datasource: polling two
	// warehouses must return each one's own tables (not the first's cached
	// result), proving the cache key isn't primary-only.
	p := twoWarehouseProject()
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	ci := &mockCacheInvalidator{} // empty cache → live fallback for both
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci, nil)
	lister := &fakeTableLister{byWarehouse: map[string][]string{
		"wh_mssql": {"dbo.orders"},
		"wh_pg":    {"public.customer"},
	}}
	h.SetTableLister(lister)

	// Primary (empty id → wh_mssql).
	w1 := httptest.NewRecorder()
	h.ListCachedTables(w1, newReq("GET", "/schema-cache/tables", p.ID, ""))
	if got := decodeListCachedTables(t, w1); len(got) != 1 || got[0] != "dbo.orders" {
		t.Fatalf("primary tables = %v, want [dbo.orders]", got)
	}
	// Secondary — must NOT return the primary's cached result.
	w2 := httptest.NewRecorder()
	h.ListCachedTables(w2, newReq("GET", "/schema-cache/tables?warehouse_id=wh_pg", p.ID, ""))
	if got := decodeListCachedTables(t, w2); len(got) != 1 || got[0] != "public.customer" {
		t.Fatalf("secondary tables = %v, want [public.customer] (per-datasource cache key)", got)
	}
	if lister.calls != 2 {
		t.Errorf("lister spawned %d times, want 2 (one per distinct datasource key)", lister.calls)
	}
	if len(lister.gotWarehouse) != 2 || lister.gotWarehouse[0] != "wh_mssql" || lister.gotWarehouse[1] != "wh_pg" {
		t.Errorf("lister saw warehouse ids %v, want [wh_mssql wh_pg]", lister.gotWarehouse)
	}
}

func TestSchemaIndex_ListCachedTables_NoRepo_OK_Empty(t *testing.T) {
	// Smoke build without the cache repo wired returns the empty shape
	// instead of 503 — same contract as GetCacheInfo.
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3"}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	h.ListCachedTables(w, newReq("GET", "/schema-cache/tables", p.ID, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	got := decodeListCachedTables(t, w)
	if len(got) != 0 {
		t.Errorf("tables = %v, want empty when repo not wired", got)
	}
}

func TestSchemaIndex_ListCachedTables_MissingProject_404(t *testing.T) {
	ci := &mockCacheInvalidator{}
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, nil, ci, nil)
	w := httptest.NewRecorder()
	h.ListCachedTables(w, newReq("GET", "/schema-cache/tables", "nope", ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestSchemaIndex_ListCachedTables_EmptyProjectID_400(t *testing.T) {
	ci := &mockCacheInvalidator{}
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, nil, ci, nil)
	w := httptest.NewRecorder()
	h.ListCachedTables(w, newReq("GET", "/schema-cache/tables", "", ""))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSchemaIndex_ListCachedTables_RepoError_500(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3"}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	ci := &mockCacheInvalidator{tablesErr: errors.New("mongo down")}
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci, nil)

	w := httptest.NewRecorder()
	h.ListCachedTables(w, newReq("GET", "/schema-cache/tables", p.ID, ""))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

// --- GetStatus error branches ---

func TestSchemaIndex_GetStatus_ProjectGetError_500(t *testing.T) {
	projRepo := newMockProjectRepo()
	projRepo.getErr = errors.New("mongo down")
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	h.GetStatus(w, newReq("GET", "/schema-index/status", "any", ""))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

// Progress lookup failure is non-fatal: the handler logs a warning and
// returns the project's status without live progress counters.
func TestSchemaIndex_GetStatus_ProgressErrorDegradesGracefully(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3", SchemaIndexStatus: models.SchemaIndexStatusReady}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	prog := newMockProgress()
	prog.err = errors.New("mongo blip")
	h := NewSchemaIndexHandler(projRepo, prog, nil, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	h.GetStatus(w, newReq("GET", "/schema-index/status", p.ID, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (progress error must not fail the request)", w.Code)
	}
	resp := decodeStatus(t, w)
	if resp.Status != "ready" {
		t.Errorf("status = %q, want ready", resp.Status)
	}
	if resp.Progress != nil {
		t.Errorf("progress should be nil when repo errors, got %+v", resp.Progress)
	}
}

// --- Retry error branches ---

func TestSchemaIndex_Retry_EmptyProjectID_400(t *testing.T) {
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, nil, nil, nil)
	w := httptest.NewRecorder()
	h.Retry(w, newReq("POST", "/schema-index/retry", "", ""))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSchemaIndex_Retry_ProjectGetError_500(t *testing.T) {
	projRepo := newMockProjectRepo()
	projRepo.getErr = errors.New("mongo down")
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	h.Retry(w, newReq("POST", "/schema-index/retry", "any", ""))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

// --- Reindex error branches ---

func TestSchemaIndex_Reindex_EmptyProjectID_400(t *testing.T) {
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, nil, nil, nil)
	w := httptest.NewRecorder()
	h.Reindex(w, newReq("POST", "/reindex", "", ""))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSchemaIndex_Reindex_ProjectGetError_500(t *testing.T) {
	projRepo := newMockProjectRepo()
	projRepo.getErr = errors.New("mongo down")
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), &mockDropper{}, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	h.Reindex(w, newReq("POST", "/reindex", "any", ""))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

// --- Cancel projects.GetByID error branch ---

func TestSchemaIndex_Cancel_ProjectGetError_500(t *testing.T) {
	projRepo := newMockProjectRepo()
	projRepo.getErr = errors.New("mongo down")
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, &mockCanceller{}, nil, nil)

	w := httptest.NewRecorder()
	h.Cancel(w, newReq("POST", "/schema-index/cancel", "any", ""))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

// --- ListLogs ---

// mockLogLister implements SchemaIndexLogLister with a canned set of
// rows + optional error injection. Tracks calls so tests can assert
// the handler forwards `since` and `limit` correctly.
type mockLogLister struct {
	mu    sync.Mutex
	rows  []database.SchemaIndexLog
	err   error
	calls []listLogCall
}

type listLogCall struct {
	projectID string
	since     time.Time
	limit     int
}

func (m *mockLogLister) List(_ context.Context, projectID string, since time.Time, limit int) ([]database.SchemaIndexLog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, listLogCall{projectID: projectID, since: since, limit: limit})
	if m.err != nil {
		return nil, m.err
	}
	return m.rows, nil
}

// decodeLogList unwraps {"data": [...]}.
func decodeLogList(t *testing.T, w *httptest.ResponseRecorder) []SchemaIndexLogLine {
	t.Helper()
	var env struct {
		Data []SchemaIndexLogLine `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	return env.Data
}

func TestSchemaIndex_ListLogs_HappyPath(t *testing.T) {
	when := time.Date(2026, 4, 25, 10, 30, 0, 0, time.UTC)
	lister := &mockLogLister{rows: []database.SchemaIndexLog{
		{ProjectID: "p1", RunID: "r1", Line: "first", CreatedAt: when},
		{ProjectID: "p1", RunID: "r1", Line: "second", CreatedAt: when.Add(time.Second)},
	}}
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, lister, nil, nil, nil)

	w := httptest.NewRecorder()
	h.ListLogs(w, newReq("GET", "/schema-index/logs", "p1", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	got := decodeLogList(t, w)
	if len(got) != 2 || got[0].Line != "first" || got[1].Line != "second" {
		t.Errorf("rows = %+v", got)
	}
	if len(lister.calls) != 1 || lister.calls[0].projectID != "p1" {
		t.Errorf("List called with %+v", lister.calls)
	}
	if lister.calls[0].limit != 200 {
		t.Errorf("default limit = %d, want 200", lister.calls[0].limit)
	}
	if !lister.calls[0].since.IsZero() {
		t.Errorf("since should be zero when query missing, got %v", lister.calls[0].since)
	}
}

func TestSchemaIndex_ListLogs_NilRepo_OK_Empty(t *testing.T) {
	// Builds without the log repo wired return an empty list (not 503)
	// so the dashboard tail just shows "no logs yet" without special-
	// casing.
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, nil, nil, nil)
	w := httptest.NewRecorder()
	h.ListLogs(w, newReq("GET", "/schema-index/logs", "p1", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	got := decodeLogList(t, w)
	if len(got) != 0 {
		t.Errorf("rows = %+v, want empty", got)
	}
}

func TestSchemaIndex_ListLogs_EmptyProjectID_400(t *testing.T) {
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, &mockLogLister{}, nil, nil, nil)
	w := httptest.NewRecorder()
	h.ListLogs(w, newReq("GET", "/schema-index/logs", "", ""))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSchemaIndex_ListLogs_ParsesSinceQuery(t *testing.T) {
	lister := &mockLogLister{}
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, lister, nil, nil, nil)

	since := "2026-04-25T10:30:00.500Z" // RFC 3339Nano
	r := httptest.NewRequest("GET", "/schema-index/logs?since="+url.QueryEscape(since), nil)
	r.SetPathValue("id", "p1")
	w := httptest.NewRecorder()
	h.ListLogs(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(lister.calls) != 1 {
		t.Fatalf("List call count = %d", len(lister.calls))
	}
	want, _ := time.Parse(time.RFC3339Nano, since)
	if !lister.calls[0].since.Equal(want) {
		t.Errorf("since = %v, want %v", lister.calls[0].since, want)
	}
}

func TestSchemaIndex_ListLogs_FallsBackToRFC3339(t *testing.T) {
	// Plain RFC 3339 (no fractional seconds) must also be accepted —
	// the parser tries Nano first then falls back.
	lister := &mockLogLister{}
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, lister, nil, nil, nil)

	since := "2026-04-25T10:30:00Z"
	r := httptest.NewRequest("GET", "/schema-index/logs?since="+url.QueryEscape(since), nil)
	r.SetPathValue("id", "p1")
	w := httptest.NewRecorder()
	h.ListLogs(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	want, _ := time.Parse(time.RFC3339, since)
	if !lister.calls[0].since.Equal(want) {
		t.Errorf("since = %v, want %v", lister.calls[0].since, want)
	}
}

func TestSchemaIndex_ListLogs_BadSince_400(t *testing.T) {
	lister := &mockLogLister{}
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, lister, nil, nil, nil)
	r := httptest.NewRequest("GET", "/schema-index/logs?since=notadate", nil)
	r.SetPathValue("id", "p1")
	w := httptest.NewRecorder()
	h.ListLogs(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if len(lister.calls) != 0 {
		t.Errorf("List must not be called on bad since, got %+v", lister.calls)
	}
}

func TestSchemaIndex_ListLogs_HonoursLimitQuery(t *testing.T) {
	lister := &mockLogLister{}
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, lister, nil, nil, nil)
	r := httptest.NewRequest("GET", "/schema-index/logs?limit=42", nil)
	r.SetPathValue("id", "p1")
	w := httptest.NewRecorder()
	h.ListLogs(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if lister.calls[0].limit != 42 {
		t.Errorf("limit = %d, want 42", lister.calls[0].limit)
	}
}

func TestSchemaIndex_ListLogs_BadLimitFallsBackToDefault(t *testing.T) {
	// A non-numeric or zero/negative limit must NOT fail the request —
	// the handler silently drops back to the default of 200.
	lister := &mockLogLister{}
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, lister, nil, nil, nil)
	r := httptest.NewRequest("GET", "/schema-index/logs?limit=abc", nil)
	r.SetPathValue("id", "p1")
	w := httptest.NewRecorder()
	h.ListLogs(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if lister.calls[0].limit != 200 {
		t.Errorf("limit = %d, want default 200", lister.calls[0].limit)
	}
}

func TestSchemaIndex_ListLogs_RepoError_500(t *testing.T) {
	lister := &mockLogLister{err: errors.New("mongo down")}
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, lister, nil, nil, nil)
	w := httptest.NewRecorder()
	h.ListLogs(w, newReq("GET", "/schema-index/logs", "p1", ""))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

// --- SetSchemaIndexStatus error branches (the final write step that
// transitions the project's lifecycle) ---

func TestSchemaIndex_Retry_SetStatusError_500(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3", SchemaIndexStatus: models.SchemaIndexStatusFailed}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	projRepo.setStatusErr = errors.New("mongo write failed")
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	h.Retry(w, newReq("POST", "/schema-index/retry", p.ID, ""))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestSchemaIndex_Reindex_SetStatusError_500(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3", SchemaIndexStatus: models.SchemaIndexStatusReady}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	projRepo.setStatusErr = errors.New("mongo write failed")
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), &mockDropper{}, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	h.Reindex(w, newReq("POST", "/reindex", p.ID, ""))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestSchemaIndex_InvalidateCache_SetStatusError_500(t *testing.T) {
	// Step 1 of invalidate-cache (SetSchemaIndexStatus) fails. Cache
	// delete must NOT have run — we bail before stepping into Mongo
	// writes for the cache rows.
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3", SchemaIndexStatus: models.SchemaIndexStatusReady}
	projRepo := newMockProjectRepo()
	_ = projRepo.Create(context.Background(), p)
	projRepo.setStatusErr = errors.New("mongo write failed")
	ci := &mockCacheInvalidator{}
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), &mockDropper{}, nil, nil, ci, nil)

	w := httptest.NewRecorder()
	h.InvalidateCache(w, newReq("POST", "/schema-index/invalidate-cache", p.ID, ""))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	if len(ci.called) != 0 {
		t.Errorf("Invalidate must NOT run when status flip fails, got %v", ci.called)
	}
}

// --- ListRuns ---

type mockRunLister struct {
	runs         []models.SchemaIndexRun
	err          error
	gotProjectID string
	gotDSID      string
	gotLimit     int
}

func (m *mockRunLister) List(_ context.Context, projectID, datasourceID string, limit int) ([]models.SchemaIndexRun, error) {
	m.gotProjectID, m.gotDSID, m.gotLimit = projectID, datasourceID, limit
	if m.err != nil {
		return nil, m.err
	}
	return m.runs, nil
}

func makeRunsHandler(t *testing.T, p *models.Project, lister SchemaIndexRunLister) *SchemaIndexHandler {
	t.Helper()
	projRepo := newMockProjectRepo()
	if p != nil {
		if err := projRepo.Create(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	return NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, nil, lister)
}

func decodeRuns(t *testing.T, w *httptest.ResponseRecorder) []SchemaIndexRunView {
	t.Helper()
	var env struct {
		Data struct {
			Runs []SchemaIndexRunView `json:"runs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	return env.Data.Runs
}

func TestSchemaIndex_ListRuns_HappyPath(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3"}
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	fin := time.Now().UTC().Truncate(time.Second)
	lister := &mockRunLister{runs: []models.SchemaIndexRun{
		{DatasourceID: "wh_a", DatasourceName: "Redshift", RunID: "r2", Kind: "tables",
			ObjectsIndexed: 42, BlurbsGenerated: 40, Status: models.SchemaIndexStatusReady,
			PhaseDurations: map[string]int64{"schema_discovery": 3000}, TokensIn: 10, TokensOut: 20,
			StartedAt: start, FinishedAt: fin},
		{DatasourceID: "wh_a", RunID: "r1", Kind: "tables", Status: models.SchemaIndexStatusFailed, Error: "boom", FinishedAt: start},
	}}
	h := makeRunsHandler(t, p, lister)

	w := httptest.NewRecorder()
	h.ListRuns(w, newReq("GET", "/schema-index/runs", p.ID, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	runs := decodeRuns(t, w)
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if runs[0].RunID != "r2" || runs[0].ObjectsIndexed != 42 || runs[0].BlurbsGenerated != 40 {
		t.Errorf("run[0] mapped wrong: %+v", runs[0])
	}
	if runs[0].Status != "ready" || runs[0].DatasourceName != "Redshift" {
		t.Errorf("run[0] status/name wrong: %+v", runs[0])
	}
	if runs[0].StartedAt == "" || runs[0].FinishedAt == "" {
		t.Errorf("run[0] timestamps not formatted: %+v", runs[0])
	}
	if runs[0].PhaseDurations["schema_discovery"] != 3000 {
		t.Errorf("run[0] phase_durations = %+v", runs[0].PhaseDurations)
	}
	if runs[1].Status != "failed" || runs[1].Error != "boom" {
		t.Errorf("run[1] failure mapped wrong: %+v", runs[1])
	}
}

func TestSchemaIndex_ListRuns_DatasourceAndLimitPassthrough(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3"}
	lister := &mockRunLister{}
	h := makeRunsHandler(t, p, lister)

	w := httptest.NewRecorder()
	h.ListRuns(w, newReq("GET", "/schema-index/runs?datasource_id=wh_a&limit=5", p.ID, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if lister.gotDSID != "wh_a" {
		t.Errorf("datasource_id passthrough = %q", lister.gotDSID)
	}
	if lister.gotLimit != 5 {
		t.Errorf("limit passthrough = %d, want 5", lister.gotLimit)
	}
	if lister.gotProjectID != p.ID {
		t.Errorf("project_id passthrough = %q", lister.gotProjectID)
	}
}

func TestSchemaIndex_ListRuns_BadLimitIgnored(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3"}
	lister := &mockRunLister{}
	h := makeRunsHandler(t, p, lister)

	w := httptest.NewRecorder()
	h.ListRuns(w, newReq("GET", "/schema-index/runs?limit=notanumber", p.ID, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (malformed limit should not 400)", w.Code)
	}
	if lister.gotLimit != 0 {
		t.Errorf("malformed limit should pass 0 (repo default), got %d", lister.gotLimit)
	}
}

func TestSchemaIndex_ListRuns_MissingProject(t *testing.T) {
	h := makeRunsHandler(t, nil, &mockRunLister{})
	w := httptest.NewRecorder()
	h.ListRuns(w, newReq("GET", "/schema-index/runs", "nope", ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestSchemaIndex_ListRuns_NilListerReturnsEmpty(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3"}
	h := makeRunsHandler(t, p, nil) // no run lister wired

	w := httptest.NewRecorder()
	h.ListRuns(w, newReq("GET", "/schema-index/runs", p.ID, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if runs := decodeRuns(t, w); len(runs) != 0 {
		t.Errorf("nil lister should yield empty list, got %+v", runs)
	}
}

func TestSchemaIndex_ListRuns_ListerError(t *testing.T) {
	p := &models.Project{Name: "t", Domain: "gaming", Category: "match3"}
	h := makeRunsHandler(t, p, &mockRunLister{err: errors.New("mongo down")})

	w := httptest.NewRecorder()
	h.ListRuns(w, newReq("GET", "/schema-index/runs", p.ID, ""))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}
