package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/packgen"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// stubPackgenProvider lets tests script Generate / RegenerateSection
// outcomes without spinning up the enterprise plugin.
type stubPackgenProvider struct {
	genResult *packgen.GenerateResult
	genErr    error

	regResult *packgen.RegenerateSectionResult
	regErr    error

	lastGen packgen.GenerateRequest
	lastReg packgen.RegenerateSectionRequest
}

func (s *stubPackgenProvider) Generate(_ context.Context, req packgen.GenerateRequest) (*packgen.GenerateResult, error) {
	s.lastGen = req
	return s.genResult, s.genErr
}

func (s *stubPackgenProvider) RegenerateSection(_ context.Context, req packgen.RegenerateSectionRequest) (*packgen.RegenerateSectionResult, error) {
	s.lastReg = req
	return s.regResult, s.regErr
}

// installStubProvider swaps in a stub Provider for the test and restores
// the registry on cleanup. Tests that want the no-op (community) build
// can simply skip calling this helper.
func installStubProvider(t *testing.T, p packgen.Provider) {
	t.Helper()
	packgen.ResetForTest()
	packgen.SetProviderForTest(p)
	t.Cleanup(packgen.ResetForTest)
}

// resetPackgenForTest ensures the registry is in the no-op state.
func resetPackgenForTest(t *testing.T) {
	t.Helper()
	packgen.ResetForTest()
	t.Cleanup(packgen.ResetForTest)
}

// --- Generate ---

func TestPackGenerate_Generate_NoProviderConfigured_404(t *testing.T) {
	resetPackgenForTest(t)

	repo := newMockProjectRepo()
	p := &models.Project{ID: "p-1", State: models.ProjectStatePackGenerationPending}
	_ = repo.Create(context.Background(), p)

	h := NewPackGenerateHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
}

func TestPackGenerate_Generate_MissingProjectID_400(t *testing.T) {
	resetPackgenForTest(t)
	installStubProvider(t, &stubPackgenProvider{})

	h := NewPackGenerateHandler(newMockProjectRepo())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects//pack-generate", nil)
	req.SetPathValue("id", "")
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

func TestPackGenerate_Generate_ProjectNotFound_404(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})

	h := NewPackGenerateHandler(newMockProjectRepo())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/missing/pack-generate", nil)
	req.SetPathValue("id", "missing")
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
}

func TestPackGenerate_Generate_WrongState_409(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})

	repo := newMockProjectRepo()
	p := &models.Project{ID: "p-2", State: models.ProjectStateReady}
	_ = repo.Create(context.Background(), p)

	h := NewPackGenerateHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
}

func TestPackGenerate_Generate_NoGeneratePackPayload_400(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})

	repo := newMockProjectRepo()
	p := &models.Project{ID: "p-3", State: models.ProjectStatePackGenerationPending}
	_ = repo.Create(context.Background(), p)

	h := NewPackGenerateHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

func TestPackGenerate_Generate_AsyncResult_202(t *testing.T) {
	stub := &stubPackgenProvider{genResult: &packgen.GenerateResult{RunID: "run-1", Async: true}}
	installStubProvider(t, stub)

	repo := newMockProjectRepo()
	p := &models.Project{
		ID:    "p-4",
		State: models.ProjectStatePackGenerationPending,
		GeneratePack: &models.GeneratePackConfig{
			Enabled:     true,
			PackName:    "Acme",
			PackSlug:    "acme",
			Description: "match-3 puzzle",
		},
	}
	_ = repo.Create(context.Background(), p)

	h := NewPackGenerateHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}
	if stub.lastGen.PackSlug != "acme" {
		t.Errorf("provider received PackSlug=%q, want %q", stub.lastGen.PackSlug, "acme")
	}
	if stub.lastGen.Description != "match-3 puzzle" {
		t.Errorf("provider received Description=%q", stub.lastGen.Description)
	}
}

func TestPackGenerate_Generate_SyncResult_200(t *testing.T) {
	stub := &stubPackgenProvider{genResult: &packgen.GenerateResult{RunID: "run-1", PackSlug: "acme", Attempts: 2}}
	installStubProvider(t, stub)

	repo := newMockProjectRepo()
	p := &models.Project{
		ID:    "p-5",
		State: models.ProjectStatePackGenerationPending,
		GeneratePack: &models.GeneratePackConfig{Enabled: true, PackName: "Acme", PackSlug: "acme"},
	}
	_ = repo.Create(context.Background(), p)

	h := NewPackGenerateHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data packgen.GenerateResult `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.PackSlug != "acme" || resp.Data.Attempts != 2 {
		t.Errorf("unexpected response: %+v", resp.Data)
	}
}

func TestPackGenerate_Generate_ProviderErrNotConfigured_404(t *testing.T) {
	stub := &stubPackgenProvider{genErr: packgen.ErrNotConfigured}
	installStubProvider(t, stub)

	repo := newMockProjectRepo()
	p := &models.Project{
		ID:    "p-6",
		State: models.ProjectStatePackGenerationPending,
		GeneratePack: &models.GeneratePackConfig{Enabled: true, PackName: "Acme", PackSlug: "acme"},
	}
	_ = repo.Create(context.Background(), p)

	h := NewPackGenerateHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
}

func TestPackGenerate_Generate_ProviderError_500(t *testing.T) {
	stub := &stubPackgenProvider{genErr: errors.New("LLM unavailable")}
	installStubProvider(t, stub)

	repo := newMockProjectRepo()
	p := &models.Project{
		ID:    "p-7",
		State: models.ProjectStatePackGenerationPending,
		GeneratePack: &models.GeneratePackConfig{Enabled: true, PackName: "Acme", PackSlug: "acme"},
	}
	_ = repo.Create(context.Background(), p)

	h := NewPackGenerateHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body = %s", w.Code, w.Body.String())
	}
}

// --- RegenerateSection ---

func TestPackGenerate_Regenerate_NoProviderConfigured_404(t *testing.T) {
	resetPackgenForTest(t)

	repo := newMockProjectRepo()
	p := &models.Project{ID: "p-r1", State: models.ProjectStatePackGenerationDone, Domain: "acme"}
	_ = repo.Create(context.Background(), p)

	h := NewPackGenerateHandler(repo)
	body := `{"section":"categories","feedback":"more retention"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate/regenerate", strings.NewReader(body))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.RegenerateSection(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
}

func TestPackGenerate_Regenerate_BadJSON_400(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})
	h := NewPackGenerateHandler(newMockProjectRepo())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/p/pack-generate/regenerate", strings.NewReader("not json"))
	req.SetPathValue("id", "p")
	w := httptest.NewRecorder()
	h.RegenerateSection(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

func TestPackGenerate_Regenerate_MissingFields_400(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})
	h := NewPackGenerateHandler(newMockProjectRepo())

	cases := []string{
		`{"section":"","feedback":"x"}`,
		`{"section":"x","feedback":""}`,
	}
	for _, body := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/p/pack-generate/regenerate", strings.NewReader(body))
		req.SetPathValue("id", "p")
		w := httptest.NewRecorder()
		h.RegenerateSection(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body=%s: status = %d, want 400", body, w.Code)
		}
	}
}

func TestPackGenerate_Regenerate_WrongState_409(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})

	repo := newMockProjectRepo()
	p := &models.Project{ID: "p-r2", State: models.ProjectStateReady, Domain: "acme"}
	_ = repo.Create(context.Background(), p)

	h := NewPackGenerateHandler(repo)
	body := `{"section":"categories","feedback":"more retention"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate/regenerate", strings.NewReader(body))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.RegenerateSection(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
}

func TestPackGenerate_Regenerate_Success(t *testing.T) {
	stub := &stubPackgenProvider{regResult: &packgen.RegenerateSectionResult{PackSlug: "acme", Section: "categories", Attempts: 1}}
	installStubProvider(t, stub)

	repo := newMockProjectRepo()
	p := &models.Project{ID: "p-r3", State: models.ProjectStatePackGenerationDone, Domain: "acme"}
	_ = repo.Create(context.Background(), p)

	h := NewPackGenerateHandler(repo)
	body := `{"section":"categories","feedback":"more retention focus"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate/regenerate", strings.NewReader(body))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.RegenerateSection(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if stub.lastReg.Feedback != "more retention focus" {
		t.Errorf("provider received Feedback=%q", stub.lastReg.Feedback)
	}
	if stub.lastReg.PackSlug != "acme" {
		t.Errorf("provider received PackSlug=%q (should be project.Domain)", stub.lastReg.PackSlug)
	}
}

func TestPackGenerate_Regenerate_NoPackOnProject_400(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})

	repo := newMockProjectRepo()
	p := &models.Project{ID: "p-r4", State: models.ProjectStatePackGenerationDone, Domain: ""}
	_ = repo.Create(context.Background(), p)

	h := NewPackGenerateHandler(repo)
	body := `{"section":"categories","feedback":"x"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate/regenerate", strings.NewReader(body))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.RegenerateSection(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

// --- Project create — wizard mode ---

func TestProjectsCreate_WizardMode_AcceptsEmptyDomain(t *testing.T) {
	swapChecker(t, &stubChecker{})
	repo := newMockProjectRepo()
	domainPacks := newMockDomainPackRepo()
	h := NewProjectsHandler(repo, domainPacks)

	body := map[string]interface{}{
		"name": "Acme Project",
		"generate_pack": map[string]interface{}{
			"enabled":   true,
			"pack_name": "Acme Gaming",
			"pack_slug": "acme-gaming",
		},
		"llm": map[string]string{"provider": "claude", "model": "claude-sonnet"},
	}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewReader(buf))
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data models.Project `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	created := resp.Data
	if created.State != models.ProjectStatePackGenerationPending {
		t.Errorf("State = %q, want %q", created.State, models.ProjectStatePackGenerationPending)
	}
	if created.Domain != "" {
		t.Errorf("wizard projects must have empty Domain on create, got %q", created.Domain)
	}
	// Wizard projects must not be enqueued for schema indexing: the
	// handler skips that path because the wizard hasn't supplied a
	// warehouse yet. (The mock repo defaults SchemaIndexStatus to
	// "ready" on insert; the handler-driven status would be
	// "pending_indexing" — assert the handler did not set that.)
	if created.SchemaIndexStatus == models.SchemaIndexStatusPendingIndexing {
		t.Errorf("wizard projects should NOT be enqueued for indexing; got %q", created.SchemaIndexStatus)
	}
	if created.GeneratePack == nil || !created.GeneratePack.Enabled {
		t.Error("GeneratePack should be carried through to the persisted document")
	}
}

func TestProjectsCreate_WizardMode_RejectsInvalidSlug(t *testing.T) {
	swapChecker(t, &stubChecker{})
	h := NewProjectsHandler(newMockProjectRepo(), newMockDomainPackRepo())

	body := map[string]interface{}{
		"name": "Acme",
		"generate_pack": map[string]interface{}{
			"enabled":   true,
			"pack_name": "Acme Gaming",
			"pack_slug": "Bad Slug!", // not slug-shaped
		},
	}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewReader(buf))
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

func TestProjectsCreate_WizardMode_SlugCollisionConflict(t *testing.T) {
	swapChecker(t, &stubChecker{})
	repo := newMockProjectRepo()
	domainPacks := newMockDomainPackRepo()
	// Pre-seed a pack with the slug the wizard wants.
	_ = domainPacks.Create(context.Background(), &models.DomainPack{Slug: "acme-gaming", Name: "Existing"})
	h := NewProjectsHandler(repo, domainPacks)

	body := map[string]interface{}{
		"name": "Acme",
		"generate_pack": map[string]interface{}{
			"enabled":   true,
			"pack_name": "Acme Gaming",
			"pack_slug": "acme-gaming",
		},
	}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewReader(buf))
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
}

func TestProjectsCreate_WizardMode_RequiresNameAndSlug(t *testing.T) {
	swapChecker(t, &stubChecker{})
	h := NewProjectsHandler(newMockProjectRepo(), newMockDomainPackRepo())

	cases := []map[string]interface{}{
		{"enabled": true, "pack_slug": "x"},                    // missing pack_name
		{"enabled": true, "pack_name": "X"},                    // missing pack_slug
		{"enabled": true, "pack_name": "X", "pack_slug": "a"},  // slug too short
	}
	for i, gp := range cases {
		body := map[string]interface{}{"name": "P", "generate_pack": gp}
		buf, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewReader(buf))
		w := httptest.NewRecorder()
		h.Create(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("case %d: status = %d, want 400; body = %s", i, w.Code, w.Body.String())
		}
	}
}

func TestProjectsCreate_NonWizard_DefaultsStateToReady(t *testing.T) {
	swapChecker(t, &stubChecker{})
	repo := newMockProjectRepo()
	domainPacks := newMockDomainPackRepo()
	_ = domainPacks.Create(context.Background(), &models.DomainPack{
		Slug: "gaming",
		Name: "Gaming",
		Categories: []models.PackCategory{{ID: "match-3", Name: "Match-3"}},
		Prompts: models.PackPrompts{Base: models.BasePrompts{
			Exploration:     "explore {{PROFILE}}",
			Recommendations: "recommend",
			BaseContext:     "{{PROFILE}} {{PREVIOUS_CONTEXT}}",
		}},
	})
	h := NewProjectsHandler(repo, domainPacks)

	body := map[string]interface{}{
		"name":     "Regular Project",
		"domain":   "gaming",
		"category": "match-3",
		"llm":      map[string]string{"provider": "claude", "model": "claude-sonnet"},
	}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewReader(buf))
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data models.Project `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.State != models.ProjectStateReady {
		t.Errorf("State = %q, want %q", resp.Data.State, models.ProjectStateReady)
	}
}
