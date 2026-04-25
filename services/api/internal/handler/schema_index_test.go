package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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
	h := NewSchemaIndexHandler(projRepo, prog, drop, nil, nil, nil)
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
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, nil, nil)
	w := httptest.NewRecorder()
	h.GetStatus(w, newReq("GET", "/schema-index/status", "nope", ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d", w.Code)
	}
}

func TestSchemaIndex_GetStatus_EmptyProjectID(t *testing.T) {
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, nil, nil)
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
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, nil, nil)
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
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, nil)

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
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), &mockDropper{}, nil, nil, nil)
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
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, mc, nil)

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
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, nil)

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
			h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, mc, nil)

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
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, mc, nil)

	w := httptest.NewRecorder()
	h.Cancel(w, newReq("POST", "/schema-index/cancel", p.ID, ""))
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

func TestSchemaIndex_Cancel_MissingProject_404(t *testing.T) {
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, &mockCanceller{}, nil)
	w := httptest.NewRecorder()
	h.Cancel(w, newReq("POST", "/schema-index/cancel", "nope", ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestSchemaIndex_Cancel_EmptyProjectID_400(t *testing.T) {
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, &mockCanceller{}, nil)
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
	called       []string
	err          error
	lastCachedAt time.Time
	lastErr      error
}

func (m *mockCacheInvalidator) Invalidate(_ context.Context, projectID string) error {
	m.called = append(m.called, projectID)
	return m.err
}

func (m *mockCacheInvalidator) LastCachedAt(_ context.Context, _ string) (time.Time, error) {
	return m.lastCachedAt, m.lastErr
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
			h := NewSchemaIndexHandler(projRepo, newMockProgress(), drop, nil, nil, ci)

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
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci)

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
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), drop, nil, nil, ci)

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
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), &mockDropper{}, nil, nil, ci)

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
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, nil)

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
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), drop, nil, nil, ci)

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
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, nil, ci)
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
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, nil, ci)
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
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci)

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
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci)

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
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci)

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
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, nil)

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
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, nil, ci)
	w := httptest.NewRecorder()
	h.GetCacheInfo(w, newReq("GET", "/schema-index/cache-info", "nope", ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestSchemaIndex_GetCacheInfo_EmptyProjectID_400(t *testing.T) {
	ci := &mockCacheInvalidator{}
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil, nil, nil, ci)
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
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil, nil, nil, ci)

	w := httptest.NewRecorder()
	h.GetCacheInfo(w, newReq("GET", "/schema-index/cache-info", p.ID, ""))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}
