package handler

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// Discovery-trigger gating on schema_index_status. Adds to the existing
// discoveries_test.go / discoveries_policy_test.go coverage for the
// new 409 branches the Phase B5 change introduces.

func TestDiscoveriesHandler_TriggerDiscovery_Gate_PendingIndexing_Returns409(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, nil, newMockRunner())

	p := &models.Project{Name: "pending", Domain: "gaming", Category: "match3", SchemaIndexStatus: models.SchemaIndexStatusPendingIndexing}
	_ = projRepo.Create(context.Background(), p)

	req := httptest.NewRequest("POST", "/api/v1/projects/"+p.ID+"/discover", strings.NewReader("{}"))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.TriggerDiscovery(w, req)

	if w.Code != 409 {
		t.Errorf("status = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "schema-index/status") {
		t.Errorf("body should hint polling: %s", w.Body.String())
	}
}

func TestDiscoveriesHandler_TriggerDiscovery_Gate_Indexing_Returns409(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, nil, newMockRunner())

	p := &models.Project{Name: "indexing", Domain: "gaming", Category: "match3", SchemaIndexStatus: models.SchemaIndexStatusIndexing}
	_ = projRepo.Create(context.Background(), p)

	req := httptest.NewRequest("POST", "/api/v1/projects/"+p.ID+"/discover", strings.NewReader("{}"))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.TriggerDiscovery(w, req)

	if w.Code != 409 {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

func TestDiscoveriesHandler_TriggerDiscovery_Gate_Failed_Returns409WithError(t *testing.T) {
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, nil, newMockRunner())

	p := &models.Project{
		Name: "failed", Domain: "gaming", Category: "match3",
		SchemaIndexStatus: models.SchemaIndexStatusFailed,
		SchemaIndexError:  "qdrant unreachable",
	}
	_ = projRepo.Create(context.Background(), p)

	req := httptest.NewRequest("POST", "/api/v1/projects/"+p.ID+"/discover", strings.NewReader("{}"))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.TriggerDiscovery(w, req)

	if w.Code != 409 {
		t.Errorf("status = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "qdrant unreachable") {
		t.Errorf("body should include the prior error: %s", w.Body.String())
	}
}

func TestDiscoveriesHandler_TriggerDiscovery_Gate_EmptyStatus_Returns409(t *testing.T) {
	// Pre-migration project with no schema_index_status at all.
	projRepo := newMockProjectRepo()
	discRepo := newMockDiscoveryRepo()
	runRepo := newMockRunRepo()
	h := NewDiscoveriesHandler(discRepo, projRepo, runRepo, nil, newMockRunner())

	p := &models.Project{ID: "legacy-1", Name: "legacy", Domain: "gaming", Category: "match3"}
	projRepo.projects[p.ID] = p // bypass Create so SchemaIndexStatus stays ""

	req := httptest.NewRequest("POST", "/api/v1/projects/"+p.ID+"/discover", strings.NewReader("{}"))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.TriggerDiscovery(w, req)

	if w.Code != 409 {
		t.Errorf("status = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "/reindex") {
		t.Errorf("body should point to reindex: %s", w.Body.String())
	}
}

// --- Create handler: pending_indexing flip ---

func TestProjectsHandler_Create_FlipsToPendingIndexing(t *testing.T) {
	projRepo := newMockProjectRepo()
	packRepo := newMockDomainPackRepo()
	packRepo.add(testDomainPack("gaming", "match3"))

	h := NewProjectsHandler(projRepo, packRepo)

	body := `{
		"name": "with-warehouse",
		"domain": "gaming",
		"category": "match3",
		"warehouse": {"provider": "bigquery", "datasets": ["d1"]},
		"llm": {"provider": "claude", "model": "claude-sonnet-4-6"}
	}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != 201 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	// Mongo-side: status should be pending_indexing.
	// The mock defaults to "ready" on Create, but our handler then calls
	// SetSchemaIndexStatus which overrides it.
	projRepo.mu.Lock()
	defer projRepo.mu.Unlock()
	var got *models.Project
	for _, p := range projRepo.projects {
		got = p
		break
	}
	if got == nil {
		t.Fatal("no project created")
	}
	if got.SchemaIndexStatus != models.SchemaIndexStatusPendingIndexing {
		t.Errorf("status = %q, want pending_indexing", got.SchemaIndexStatus)
	}
}

func TestProjectsHandler_Create_NoWarehouse_NoIndexingEnqueued(t *testing.T) {
	projRepo := newMockProjectRepo()
	packRepo := newMockDomainPackRepo()
	packRepo.add(testDomainPack("gaming", "match3"))

	h := NewProjectsHandler(projRepo, packRepo)

	body := `{
		"name": "no-warehouse",
		"domain": "gaming",
		"category": "match3",
		"llm": {"provider": "claude", "model": "claude-sonnet-4-6"}
	}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != 201 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	projRepo.mu.Lock()
	defer projRepo.mu.Unlock()
	var got *models.Project
	for _, p := range projRepo.projects {
		got = p
		break
	}
	// Mock's default-on-create is "ready"; since the handler did not call
	// SetSchemaIndexStatus (no warehouse), status stays ready per mock
	// default. In production the initial insert leaves status empty; the
	// assertion here is that we didn't *transition to pending*.
	if got.SchemaIndexStatus == models.SchemaIndexStatusPendingIndexing {
		t.Errorf("should not enqueue indexing without warehouse, got %q", got.SchemaIndexStatus)
	}
}
