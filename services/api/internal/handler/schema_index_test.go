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
	h := NewSchemaIndexHandler(projRepo, prog, drop)
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
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil)
	w := httptest.NewRecorder()
	h.GetStatus(w, newReq("GET", "/schema-index/status", "nope", ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d", w.Code)
	}
}

func TestSchemaIndex_GetStatus_EmptyProjectID(t *testing.T) {
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil)
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
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), nil)
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
	h := NewSchemaIndexHandler(projRepo, newMockProgress(), nil)

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
	h := NewSchemaIndexHandler(newMockProjectRepo(), newMockProgress(), &mockDropper{})
	w := httptest.NewRecorder()
	h.Reindex(w, newReq("POST", "/reindex", "nope", ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d", w.Code)
	}
}
