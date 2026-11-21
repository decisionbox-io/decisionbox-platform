package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	"github.com/decisionbox-io/decisionbox/services/api/internal/discoverytrigger"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// A selective discovery run names analysis areas, and the areas a run can
// execute are the project's OWN enabled ones — the agent builds its list from
// prompts.analysis_areas and drops everything switched off. Requesting ids that
// match none of them used to start a run that analysed nothing and then
// reported "completed".
//
// These cover the trigger's half of #573; the picker that stopped offering
// unrunnable ids in the first place is covered in the dashboard suite.

// playbookProject builds a ready, LLM-configured project whose playbook holds
// the given areas. A false value is an area switched off on the Playbook page.
func playbookProject(areas map[string]bool) *models.Project {
	cfg := make(map[string]models.AnalysisAreaConfig, len(areas))
	priority := 0
	for id, enabled := range areas {
		priority++
		cfg[id] = models.AnalysisAreaConfig{Name: id, Enabled: enabled, Priority: priority}
	}
	return &models.Project{
		Name: "Test", Domain: "gaming", Category: "match3",
		LLMConfigVersion: llmlayer.ConfigVersion,
		Prompts:          &models.ProjectPrompts{AnalysisAreas: cfg},
	}
}

// triggerAreas POSTs a selective run for the given area ids.
func triggerAreas(t *testing.T, h *DiscoveriesHandler, projectID string, areas ...string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{"areas": areas})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/v1/projects/"+projectID+"/discover", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", projectID)
	w := httptest.NewRecorder()
	h.TriggerDiscovery(w, req)
	return w
}

func errorBody(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var resp APIResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp.Error
}

func TestTriggerDiscovery_AreasThatNameOnlyDisabledAreasIsRejected(t *testing.T) {
	projRepo, runRepo, mockRun := newMockProjectRepo(), newMockRunRepo(), newMockRunner()
	h := NewDiscoveriesHandler(newMockDiscoveryRepo(), projRepo, runRepo, nil, nil, nil, mockRun)

	p := playbookProject(map[string]bool{"churn": true, "levels": false})
	if err := projRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("create project: %v", err)
	}

	w := triggerAreas(t, h, p.ID, "levels")

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — the only requested area is switched off", w.Code)
	}
	// The message names what the project CAN run, which is the list the caller
	// has to pick from.
	if got := errorBody(t, w); !strings.Contains(got, "churn") {
		t.Errorf("error = %q, should name the enabled area the project does offer", got)
	}
	// Nothing may be left behind: a rejected request must not reserve a run
	// document (which would hold the project's concurrency slot) or spawn an
	// agent.
	if len(runRepo.createdParams) != 0 {
		t.Errorf("Create was called %d times, want 0 — a rejected request must reserve no run", len(runRepo.createdParams))
	}
	if len(mockRun.runCalls) != 0 {
		t.Errorf("runner was called %d times, want 0", len(mockRun.runCalls))
	}
}

func TestTriggerDiscovery_AreasThatNameNothingTheProjectHasIsRejected(t *testing.T) {
	// The pack-area case: an id that was never on this project's playbook, or
	// a custom area that has since been deleted.
	projRepo, runRepo, mockRun := newMockProjectRepo(), newMockRunRepo(), newMockRunner()
	h := NewDiscoveriesHandler(newMockDiscoveryRepo(), projRepo, runRepo, nil, nil, nil, mockRun)

	p := playbookProject(map[string]bool{"churn": true})
	if err := projRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("create project: %v", err)
	}

	w := triggerAreas(t, h, p.ID, "boosters")

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if len(mockRun.runCalls) != 0 {
		t.Errorf("runner was called %d times, want 0", len(mockRun.runCalls))
	}
}

func TestTriggerDiscovery_ProjectWithNoPlaybookRejectsASelectiveRun(t *testing.T) {
	// Prompts are seeded at creation, but a project that predates them has
	// none at all — so there is no area for a selective run to execute, and
	// the message has no list to offer.
	projRepo, runRepo := newMockProjectRepo(), newMockRunRepo()
	h := NewDiscoveriesHandler(newMockDiscoveryRepo(), projRepo, runRepo, nil, nil, nil, newMockRunner())

	p := &models.Project{Name: "legacy", Domain: "gaming", Category: "match3", LLMConfigVersion: llmlayer.ConfigVersion}
	if err := projRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("create project: %v", err)
	}

	w := triggerAreas(t, h, p.ID, "churn")

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if got := errorBody(t, w); !strings.Contains(got, "no analysis area enabled") {
		t.Errorf("error = %q, should say the project has no enabled area", got)
	}
}

func TestTriggerDiscovery_ACustomAreaOnTheProjectRuns(t *testing.T) {
	// The bug this issue is about: a custom area exists only on the project,
	// never in its domain pack. It must be runnable.
	projRepo, runRepo, mockRun := newMockProjectRepo(), newMockRunRepo(), newMockRunner()
	h := NewDiscoveriesHandler(newMockDiscoveryRepo(), projRepo, runRepo, nil, nil, nil, mockRun)

	p := playbookProject(map[string]bool{"churn": true, "cart_abandonment": true})
	if err := projRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("create project: %v", err)
	}

	w := triggerAreas(t, h, p.ID, "cart_abandonment")

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", w.Code, w.Body.String())
	}
	if len(mockRun.runCalls) != 1 {
		t.Fatalf("runner should have been called once, got %d", len(mockRun.runCalls))
	}
	if got := mockRun.runCalls[0].Areas; len(got) != 1 || got[0] != "cart_abandonment" {
		t.Errorf("RunOptions.Areas = %v, want [cart_abandonment]", got)
	}
}

func TestTriggerDiscovery_AreasThatPartlyMatchStillRun(t *testing.T) {
	// A request naming one enabled area and one disabled one has work to do,
	// so it runs; the agent narrows the ids to the areas it resolved. Refusing
	// it would be a stricter contract than the endpoint has ever had, and the
	// ids are forwarded unchanged.
	projRepo, runRepo, mockRun := newMockProjectRepo(), newMockRunRepo(), newMockRunner()
	h := NewDiscoveriesHandler(newMockDiscoveryRepo(), projRepo, runRepo, nil, nil, nil, mockRun)

	p := playbookProject(map[string]bool{"churn": true, "levels": false})
	if err := projRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("create project: %v", err)
	}

	w := triggerAreas(t, h, p.ID, "levels", "churn")

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", w.Code, w.Body.String())
	}
	if got := mockRun.runCalls[0].Areas; len(got) != 2 {
		t.Errorf("RunOptions.Areas = %v, want both ids forwarded for the agent to narrow", got)
	}
}

func TestTriggerDiscovery_OmittedAreasRunEveryEnabledAreaAndAreNotChecked(t *testing.T) {
	// An empty request is not selective — it means "every enabled area" — so
	// it is not refused even when the project has nothing enabled. Whether a
	// run with no areas is worth starting is a separate question from whether
	// the caller named the wrong ones, and this change does not answer it.
	projRepo, runRepo, mockRun := newMockProjectRepo(), newMockRunRepo(), newMockRunner()
	h := NewDiscoveriesHandler(newMockDiscoveryRepo(), projRepo, runRepo, nil, nil, nil, mockRun)

	p := playbookProject(map[string]bool{"churn": false})
	if err := projRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("create project: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/v1/projects/"+p.ID+"/discover", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.TriggerDiscovery(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", w.Code, w.Body.String())
	}
	if len(mockRun.runCalls) != 1 {
		t.Fatalf("runner should have been called once, got %d", len(mockRun.runCalls))
	}
}

func TestStartRun_RejectsUnrunnableAreasWithATypedInvalidParamsError(t *testing.T) {
	// The in-process seam (apiserver.TriggerDiscovery, which a scheduler
	// plugin reaches) gets the same typed error the HTTP adapter renders as
	// 400, rather than a generic failure it would have to parse.
	projRepo := newMockProjectRepo()
	p := playbookProject(map[string]bool{"churn": true})
	if err := projRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("create project: %v", err)
	}
	h := NewDiscoveriesHandler(newMockDiscoveryRepo(), projRepo, newMockRunRepo(), nil, nil, nil, newMockRunner())

	_, err := h.StartRun(context.Background(), discoverytrigger.Options{
		ProjectID: p.ID, Areas: []string{"levels"}, Source: "schedule:s1",
	})

	var invalid *discoverytrigger.InvalidParamsError
	if !errors.As(err, &invalid) {
		t.Fatalf("StartRun error = %v (%T), want *discoverytrigger.InvalidParamsError", err, err)
	}
}
