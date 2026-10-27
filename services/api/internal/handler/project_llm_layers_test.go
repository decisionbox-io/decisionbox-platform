package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	"github.com/decisionbox-io/decisionbox/libs/go-common/policy"
	"github.com/decisionbox-io/decisionbox/services/api/internal/runner"
	"github.com/decisionbox-io/decisionbox/services/api/managedai"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// --- the configuration gate (D9) ---

func TestStartRun_RefusesAnUnconfiguredProjectAndNamesSettings(t *testing.T) {
	projRepo := newMockProjectRepo()
	h := NewDiscoveriesHandler(newMockDiscoveryRepo(), projRepo, newMockRunRepo(), nil, nil, nil, newMockRunner())

	p := &models.Project{
		Name:              "legacy",
		Domain:            "gaming",
		Category:          "match3",
		SchemaIndexStatus: models.SchemaIndexStatusReady,
	}
	p.LLM = models.LLMConfig{Provider: "bedrock", Model: "opus"}
	projRepo.Create(context.Background(), p)

	req := httptest.NewRequest("POST", "/api/v1/projects/"+p.ID+"/discover", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.TriggerDiscovery(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	// A scheduled run leaves this message in the schedule's last_error, and
	// nobody is present to read a redirect there — so the message itself has
	// to say where to go.
	if !strings.Contains(body, "settings#models") {
		t.Errorf("conflict message %q must name the settings route", body)
	}
}

func TestStartRun_AcceptsAConfiguredProject(t *testing.T) {
	projRepo := newMockProjectRepo()
	runner := newMockRunner()
	h := NewDiscoveriesHandler(newMockDiscoveryRepo(), projRepo, newMockRunRepo(), nil, nil, nil, runner)

	p := &models.Project{
		Name:              "configured",
		Domain:            "gaming",
		Category:          "match3",
		SchemaIndexStatus: models.SchemaIndexStatusReady,
		LLMConfigVersion:  llmlayer.ConfigVersion,
	}
	projRepo.Create(context.Background(), p)

	req := httptest.NewRequest("POST", "/api/v1/projects/"+p.ID+"/discover", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.TriggerDiscovery(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", w.Code, w.Body.String())
	}
}

func TestStartRun_NotGatedOnAManagedDeployment(t *testing.T) {
	// Cloud has no AI configuration surface, so there is nothing for a user
	// to define and nowhere to send them. Belt and braces beside
	// managedai.Apply stamping the version on every write: an old cloud
	// project carries no version until something writes it.
	// Load() caches the config, and other tests in this package call it, so
	// the env var alone is not enough.
	t.Cleanup(managedai.Load)
	t.Setenv(managedai.EnvGatewayURL, "https://gateway.example/v1")
	t.Setenv(managedai.EnvAnalysisModel, "decisionbox-analysis-model")
	t.Setenv(managedai.EnvBlurbModel, "decisionbox-blurb-model")
	t.Setenv(managedai.EnvEmbedModel, "decisionbox-embed-model")
	managedai.Load()
	projRepo := newMockProjectRepo()
	h := NewDiscoveriesHandler(newMockDiscoveryRepo(), projRepo, newMockRunRepo(), nil, nil, nil, newMockRunner())

	p := &models.Project{
		Name:              "cloud",
		Domain:            "gaming",
		Category:          "match3",
		SchemaIndexStatus: models.SchemaIndexStatusReady,
		// Deliberately version 0.
	}
	projRepo.Create(context.Background(), p)

	req := httptest.NewRequest("POST", "/api/v1/projects/"+p.ID+"/discover", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.TriggerDiscovery(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 — a managed project must never be gated: %s", w.Code, w.Body.String())
	}
}

func TestStartRun_NotReGatedWhenALayerIsAddedLater(t *testing.T) {
	// The bump rule, enforced: adding a layer does not bump ConfigVersion, so
	// a project configured at the current version keeps running.
	projRepo := newMockProjectRepo()
	h := NewDiscoveriesHandler(newMockDiscoveryRepo(), projRepo, newMockRunRepo(), nil, nil, nil, newMockRunner())

	p := &models.Project{
		Name:              "configured-at-1",
		Domain:            "gaming",
		Category:          "match3",
		SchemaIndexStatus: models.SchemaIndexStatusReady,
		LLMProviders:      []llmlayer.ProviderEntry{{ID: llmlayer.DefaultProviderID, Provider: "bedrock"}},
		LayerModels:       map[string]llmlayer.LayerModel{llmlayer.DefaultLayer: {Model: "opus"}},
		LLMConfigVersion:  llmlayer.ConfigVersion,
	}
	projRepo.Create(context.Background(), p)

	// A layer with no row of its own resolves to the default row.
	spec, err := p.LayerSpec("a_layer_from_a_future_release")
	if err != nil {
		t.Fatalf("an unknown layer must inherit the default row, got %v", err)
	}
	if spec.Model != "opus" {
		t.Errorf("inherited model = %q, want opus", spec.Model)
	}

	req := httptest.NewRequest("POST", "/api/v1/projects/"+p.ID+"/discover", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.TriggerDiscovery(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", w.Code, w.Body.String())
	}
}

func TestStartRun_UnindexedProjectKeepsItsOwnMessage(t *testing.T) {
	// The new gate is last, so a project that is both un-indexed and
	// unconfigured still gets the more specific, long-standing message.
	projRepo := newMockProjectRepo()
	h := NewDiscoveriesHandler(newMockDiscoveryRepo(), projRepo, newMockRunRepo(), nil, nil, nil, newMockRunner())

	// The mock defaults a blank status to ready on insert, so state it.
	p := &models.Project{
		Name:              "unindexed",
		Domain:            "gaming",
		Category:          "match3",
		SchemaIndexStatus: models.SchemaIndexStatusPendingIndexing,
	}
	projRepo.Create(context.Background(), p)

	req := httptest.NewRequest("POST", "/api/v1/projects/"+p.ID+"/discover", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.TriggerDiscovery(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "schema index is not ready yet") {
		t.Errorf("body = %q, want the schema-index message", w.Body.String())
	}
}

// --- the read-time flag ---

func TestGetProject_StampsLLMConfigRequired(t *testing.T) {
	projRepo := newMockProjectRepo()
	h := NewProjectsHandler(projRepo, newMockDomainPackRepo())

	p := &models.Project{Name: "legacy", Domain: "gaming", Category: "match3"}
	p.LLM = models.LLMConfig{Provider: "bedrock", Model: "opus"}
	projRepo.Create(context.Background(), p)

	req := httptest.NewRequest("GET", "/api/v1/projects/"+p.ID, nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Get(w, req)

	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := env.Data
	if got["llm_config_required"] != true {
		t.Errorf("llm_config_required = %v, want true so the dashboard guard needs no second request", got["llm_config_required"])
	}
}

func TestGetProject_NeverRequiresConfigOnAManagedDeployment(t *testing.T) {
	// Load() caches the config, and other tests in this package call it, so
	// the env var alone is not enough.
	t.Cleanup(managedai.Load)
	t.Setenv(managedai.EnvGatewayURL, "https://gateway.example/v1")
	t.Setenv(managedai.EnvAnalysisModel, "decisionbox-analysis-model")
	t.Setenv(managedai.EnvBlurbModel, "decisionbox-blurb-model")
	t.Setenv(managedai.EnvEmbedModel, "decisionbox-embed-model")
	managedai.Load()
	projRepo := newMockProjectRepo()
	h := NewProjectsHandler(projRepo, newMockDomainPackRepo())

	p := &models.Project{Name: "cloud", Domain: "gaming", Category: "match3"}
	projRepo.Create(context.Background(), p)

	req := httptest.NewRequest("GET", "/api/v1/projects/"+p.ID, nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Get(w, req)

	if strings.Contains(w.Body.String(), `"llm_config_required":true`) {
		t.Errorf("a managed project must never be flagged as needing configuration: %s", w.Body.String())
	}
}

// --- GET /api/v1/llm/layers ---

func TestListLLMLayers(t *testing.T) {
	h := NewProvidersHandler()
	w := httptest.NewRecorder()
	h.ListLLMLayers(w, httptest.NewRequest("GET", "/api/v1/llm/layers", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var env struct {
		Data struct {
			Version         int    `json:"version"`
			DefaultLayerKey string `json:"default_layer_key"`
			Layers          []struct {
				Key                    string `json:"key"`
				Label                  string `json:"label"`
				Group                  string `json:"group"`
				RefusesReasoningModels bool   `json:"refuses_reasoning_models"`
			} `json:"layers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := env.Data
	if got.Version != llmlayer.ConfigVersion {
		t.Errorf("version = %d, want %d", got.Version, llmlayer.ConfigVersion)
	}
	if got.DefaultLayerKey != llmlayer.DefaultLayer {
		t.Errorf("default_layer_key = %q, want %q", got.DefaultLayerKey, llmlayer.DefaultLayer)
	}
	if len(got.Layers) != 17 {
		t.Fatalf("layers = %d, want 17", len(got.Layers))
	}
	// The rule has to reach the UI as data, or the model picker cannot warn
	// before a save that the API will reject.
	var sawRule bool
	for _, l := range got.Layers {
		if l.Key == llmlayer.LayerTableDescriptions {
			sawRule = l.RefusesReasoningModels
		}
		if l.Label == "" || l.Group == "" {
			t.Errorf("layer %q is missing its label or group", l.Key)
		}
	}
	if !sawRule {
		t.Error("table_descriptions must advertise refuses_reasoning_models")
	}
}

// --- GET /api/v1/projects/{id}/llm/config ---

func TestGetProjectLLMConfig_SeedsAnUnconfiguredProject(t *testing.T) {
	projRepo := newMockProjectRepo()
	h := NewProjectsHandler(projRepo, newMockDomainPackRepo())

	yes := true
	p := &models.Project{Name: "legacy", Domain: "gaming", Category: "match3", ReasoningEnabled: &yes}
	p.LLM = models.LLMConfig{Provider: "bedrock", Model: "opus", Config: map[string]string{"region": "us-east-1"}}
	p.BlurbLLM = &models.BlurbLLMConfig{Provider: "litellm", Model: "qwen3-32b"}
	projRepo.Create(context.Background(), p)

	req := httptest.NewRequest("GET", "/api/v1/projects/"+p.ID+"/llm/config", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.GetProjectLLMConfig(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			Configured  bool                           `json:"configured"`
			Seeded      bool                           `json:"seeded"`
			Providers   []llmlayer.ProviderEntry       `json:"providers"`
			LayerModels map[string]llmlayer.LayerModel `json:"layer_models"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := env.Data
	if got.Configured {
		t.Error("configured = true on a project that has never been through settings")
	}
	if !got.Seeded {
		t.Error("seeded must be true so the UI can say these values are derived and not yet saved")
	}
	if len(got.Providers) != 2 {
		t.Fatalf("providers = %+v, want the default and blurb entries", got.Providers)
	}
	def, ok := got.LayerModels[llmlayer.DefaultLayer]
	if !ok || def.Model != "opus" {
		t.Errorf("default row = %+v, want the analysis pair", def)
	}
	if def.Config[gollm.ReasoningEnabledKey] != "true" {
		t.Error("the project's reasoning toggle must be carried onto the seeded default row")
	}
	if td := got.LayerModels[llmlayer.LayerTableDescriptions]; td.Model != "qwen3-32b" {
		t.Errorf("table-description row = %+v, want the blurb pair", td)
	}
}

func TestGetProjectLLMConfig_ReturnsTheStoredConfigurationOnceSaved(t *testing.T) {
	projRepo := newMockProjectRepo()
	h := NewProjectsHandler(projRepo, newMockDomainPackRepo())

	p := &models.Project{
		Name: "configured", Domain: "gaming", Category: "match3",
		LLMProviders:     []llmlayer.ProviderEntry{{ID: llmlayer.DefaultProviderID, Provider: "bedrock"}},
		LayerModels:      map[string]llmlayer.LayerModel{llmlayer.DefaultLayer: {Model: "haiku"}},
		LLMConfigVersion: llmlayer.ConfigVersion,
	}
	projRepo.Create(context.Background(), p)

	req := httptest.NewRequest("GET", "/api/v1/projects/"+p.ID+"/llm/config", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.GetProjectLLMConfig(w, req)

	var env struct {
		Data struct {
			Configured  bool                           `json:"configured"`
			Seeded      bool                           `json:"seeded"`
			LayerModels map[string]llmlayer.LayerModel `json:"layer_models"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := env.Data
	if !got.Configured || got.Seeded {
		t.Errorf("configured=%t seeded=%t, want true/false", got.Configured, got.Seeded)
	}
	if got.LayerModels[llmlayer.DefaultLayer].Model != "haiku" {
		t.Errorf("stored configuration not returned: %+v", got.LayerModels)
	}
}

// --- the per-layer connection test ---

func TestTestLLM_LayerArgConstruction(t *testing.T) {
	cases := []struct {
		name     string
		query    string
		wantArgs string
		wantCode int
	}{
		{
			// No shipped agent image has a --layer flag, so the default must
			// degrade to today's exact invocation rather than die on an
			// unknown flag.
			name: "no layer is today's invocation", query: "",
			wantArgs: "--test-connection llm", wantCode: http.StatusOK,
		},
		{
			name: "the default layer omits the flag", query: "?layer=" + llmlayer.DefaultLayer,
			wantArgs: "--test-connection llm", wantCode: http.StatusOK,
		},
		{
			name: "a real layer passes it", query: "?layer=" + llmlayer.LayerExploration,
			wantArgs: "--test-connection llm --layer exploration", wantCode: http.StatusOK,
		},
		{
			name: "an unknown layer is refused here, not by the agent", query: "?layer=validation",
			wantCode: http.StatusBadRequest,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := &stubProjectRepo{project: testProjectForConnTest()}
			rs := &runnerStub{}
			h := NewTestConnectionHandler(repo, rs)

			req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/test/llm"+c.query, nil)
			req.SetPathValue("id", "proj-1")
			w := httptest.NewRecorder()
			h.TestLLM(w, req)

			if w.Code != c.wantCode {
				t.Fatalf("status = %d, want %d: %s", w.Code, c.wantCode, w.Body.String())
			}
			if c.wantCode != http.StatusOK {
				if len(rs.calls) != 0 {
					t.Errorf("the agent must not be invoked for an unknown layer (got %d calls)", len(rs.calls))
				}
				return
			}
			if len(rs.calls) != 1 {
				t.Fatalf("RunSync calls = %d, want 1", len(rs.calls))
			}
			if got := strings.Join(rs.calls[0].Args, " "); got != c.wantArgs {
				t.Errorf("args = %q, want %q", got, c.wantArgs)
			}
		})
	}
}

func TestTestBlurbLLM_NeverCarriesALayerFlag(t *testing.T) {
	repo := &stubProjectRepo{project: testProjectForConnTest()}
	rs := &runnerStub{}
	h := NewTestConnectionHandler(repo, rs)

	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/test/blurb-llm?layer=exploration", nil)
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.TestBlurbLLM(w, req)

	if got := strings.Join(rs.calls[0].Args, " "); got != "--test-connection blurb-llm" {
		t.Errorf("args = %q; the blurb-llm target keeps its exact current form for older agent images", got)
	}
}

// --- CRUD: merge, guards, dual-write ---

// perProviderChecker denies one named provider and records every provider it
// was asked about, so the per-ENTRY allow-list can be told apart from the
// legacy single-provider check.
type perProviderChecker struct {
	policy.NoopChecker
	deny  string
	asked []string
}

func (c *perProviderChecker) CheckLLMProviderAllowed(_ context.Context, _ string, provider string) error {
	c.asked = append(c.asked, provider)
	if provider == c.deny {
		return &policy.PolicyError{Kind: "feature", Feature: "llm_provider", PlanID: "starter_t1", Allowed: []string{"bedrock"}}
	}
	return nil
}

func putProject(t *testing.T, h *ProjectsHandler, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PUT", "/api/v1/projects/"+id, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", id)
	w := httptest.NewRecorder()
	h.Update(w, req)
	return w
}

func configuredProject(t *testing.T, repo *mockProjectRepo) *models.Project {
	t.Helper()
	p := &models.Project{Name: "p", Domain: "gaming", Category: "match3"}
	p.LLM = models.LLMConfig{Provider: "bedrock", Model: "opus"}
	p.LLMProviders = []llmlayer.ProviderEntry{{ID: llmlayer.DefaultProviderID, Provider: "bedrock"}}
	p.LayerModels = map[string]llmlayer.LayerModel{
		llmlayer.DefaultLayer: {ProviderID: llmlayer.DefaultProviderID, Model: "opus"},
	}
	p.LLMConfigVersion = llmlayer.ConfigVersion
	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return p
}

func TestUpdate_PerEntryProviderAllowList(t *testing.T) {
	// Without the per-entry loop a tenant adds a disallowed provider as a
	// SECONDARY entry and uses it for every layer. (The loop also closes a
	// gap that predates this feature: blurb_llm.provider was never checked.)
	ck := &perProviderChecker{deny: "litellm"}
	swapChecker(t, ck)

	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, newMockDomainPackRepo())
	p := configuredProject(t, repo)

	w := putProject(t, h, p.ID, `{
	  "llm_providers": [
	    {"id": "default", "provider": "bedrock"},
	    {"id": "cheap", "provider": "litellm"}
	  ],
	  "layer_models": {"default": {"provider_id": "default", "model": "opus"}}
	}`)

	if w.Code == http.StatusOK {
		t.Fatalf("a disallowed secondary provider was accepted: %s", w.Body.String())
	}
	var sawSecondary bool
	for _, provider := range ck.asked {
		if provider == "litellm" {
			sawSecondary = true
		}
	}
	if !sawSecondary {
		t.Errorf("the allow-list was asked about %v — it must run for every entry, not just the legacy llm.provider", ck.asked)
	}
}

func TestUpdate_RefusesRemovingAProviderStillInUseAndNamesTheLayers(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, newMockDomainPackRepo())
	// The mock snapshots on insert, so the fixture is complete before Create.
	p := &models.Project{Name: "p", Domain: "gaming", Category: "match3"}
	p.LLM = models.LLMConfig{Provider: "bedrock", Model: "opus"}
	p.LLMProviders = []llmlayer.ProviderEntry{
		{ID: llmlayer.DefaultProviderID, Provider: "bedrock"},
		{ID: "deepinfra", Provider: "litellm"},
	}
	p.LayerModels = map[string]llmlayer.LayerModel{
		llmlayer.DefaultLayer:     {ProviderID: llmlayer.DefaultProviderID, Model: "opus"},
		llmlayer.LayerExploration: {ProviderID: "deepinfra", Model: "deepseek"},
		llmlayer.LayerQuestions:   {ProviderID: "deepinfra", Model: "deepseek"},
	}
	p.LLMConfigVersion = llmlayer.ConfigVersion
	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	w := putProject(t, h, p.ID, `{
	  "llm_providers": [{"id": "default", "provider": "bedrock"}]
	}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"deepinfra", llmlayer.LayerExploration, llmlayer.LayerQuestions} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal %q must name %q so the user knows what to reassign", body, want)
		}
	}
}

func TestUpdate_AllowsRemovingAProviderOnceItsLayersAreReassigned(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, newMockDomainPackRepo())
	p := &models.Project{Name: "p", Domain: "gaming", Category: "match3"}
	p.LLM = models.LLMConfig{Provider: "bedrock", Model: "opus"}
	p.LLMProviders = []llmlayer.ProviderEntry{
		{ID: llmlayer.DefaultProviderID, Provider: "bedrock"},
		{ID: "deepinfra", Provider: "litellm"},
	}
	p.LayerModels = map[string]llmlayer.LayerModel{
		llmlayer.DefaultLayer:     {ProviderID: llmlayer.DefaultProviderID, Model: "opus"},
		llmlayer.LayerExploration: {ProviderID: "deepinfra", Model: "deepseek"},
	}
	p.LLMConfigVersion = llmlayer.ConfigVersion
	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	w := putProject(t, h, p.ID, `{
	  "llm_providers": [{"id": "default", "provider": "bedrock"}],
	  "layer_models": {"default": {"provider_id": "default", "model": "opus"}}
	}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	stored, _ := repo.GetByID(context.Background(), p.ID)
	if len(stored.LLMProviders) != 1 {
		t.Errorf("provider entries = %+v, want only the default", stored.LLMProviders)
	}
	if _, still := stored.LayerModels[llmlayer.LayerExploration]; still {
		t.Error("the reassigned layer row should be gone")
	}
}

func TestUpdate_RejectsUnknownLayerAndUnknownProviderID(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{
			"unknown layer key",
			`{"llm_providers":[{"id":"default","provider":"bedrock"}],"layer_models":{"default":{"model":"opus"},"validation":{"model":"opus"}}}`,
			"unknown layer",
		},
		{
			"layer naming a provider that does not exist",
			`{"llm_providers":[{"id":"default","provider":"bedrock"}],"layer_models":{"default":{"model":"opus"},"exploration":{"provider_id":"ghost","model":"x"}}}`,
			"no provider entry with id",
		},
		{
			"no default provider entry",
			`{"llm_providers":[{"id":"cheap","provider":"bedrock"}],"layer_models":{"default":{"provider_id":"cheap","model":"opus"}}}`,
			"reserved id",
		},
		{
			"no default layer row",
			`{"llm_providers":[{"id":"default","provider":"bedrock"}],"layer_models":{"exploration":{"model":"opus"}}}`,
			"row is required",
		},
		{
			"duplicate provider ids",
			`{"llm_providers":[{"id":"default","provider":"bedrock"},{"provider":"claude"}],"layer_models":{"default":{"model":"opus"}}}`,
			"duplicate provider id",
		},
		{
			"an over-long provider id the secret backends could not store",
			`{"llm_providers":[{"id":"default","provider":"bedrock"},{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","provider":"bedrock"}],"layer_models":{"default":{"model":"opus"}}}`,
			"too long",
		},
		{
			"a reasoning model on table descriptions",
			`{"llm_providers":[{"id":"default","provider":"bedrock"}],"layer_models":{"default":{"model":"opus"},"table_descriptions":{"model":"gpt-5"}}}`,
			"reasoning-class",
		},
		{
			// Apply-to-all with a reasoning model would otherwise sneak a
			// refused model into table descriptions through the default row.
			"a reasoning model as the default, with no table-description override",
			`{"llm_providers":[{"id":"default","provider":"bedrock"}],"layer_models":{"default":{"model":"o3"}}}`,
			"reasoning-class",
		},
		{
			"a malformed wire_override on a provider entry",
			`{"llm_providers":[{"id":"default","provider":"bedrock","config":{"wire_override":"antropik"}}],"layer_models":{"default":{"model":"opus"}}}`,
			"wire_override",
		},
		{
			"a malformed wire_override on a layer row",
			`{"llm_providers":[{"id":"default","provider":"bedrock"}],"layer_models":{"default":{"model":"opus"},"ask":{"model":"opus","config":{"wire_override":"antropik"}}}}`,
			"wire_override",
		},
		{
			"a layer with no model and no endpoint",
			`{"llm_providers":[{"id":"default","provider":"bedrock"}],"layer_models":{"default":{"model":"opus"},"ask":{"model":""}}}`,
			"no model configured",
		},
		{
			// The invariant dualWriteLegacyLLM depends on. The mirror copies
			// the default row's endpoint into project.llm while the
			// credential an older agent reads for it is fixed at
			// llm-credentials — the reserved entry's key.
			"the default row pointed at a secondary entry",
			`{"llm_providers":[{"id":"default","provider":"bedrock"},{"id":"cheap","provider":"bedrock"}],"layer_models":{"default":{"provider_id":"cheap","model":"opus"}}}`,
			`must use the \"default\" provider entry`,
		},
		{
			// A project member picks this name; the deployment decides which
			// variables are nameable.
			"an entry naming an environment variable the deployment did not export",
			`{"llm_providers":[{"id":"default","provider":"bedrock"},{"id":"exfil","provider":"openai","config":{"base_url":"https://attacker.example/v1","env_var":"MONGODB_URI"}}],"layer_models":{"default":{"model":"opus"}}}`,
			"is not a variable a project may read",
		},
		{
			"a layer row naming an environment variable the deployment did not export",
			`{"llm_providers":[{"id":"default","provider":"bedrock"}],"layer_models":{"default":{"model":"opus"},"ask":{"model":"opus","config":{"env_var":"SECRET_ENCRYPTION_KEY"}}}}`,
			"is not a variable a project may read",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := newMockProjectRepo()
			h := NewProjectsHandler(repo, newMockDomainPackRepo())
			p := configuredProject(t, repo)

			w := putProject(t, h, p.ID, c.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), c.want) {
				t.Errorf("message %q must mention %q", w.Body.String(), c.want)
			}
		})
	}
}

func TestUpdate_AcceptsAnEndpointBackedLayerWithNoModel(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, newMockDomainPackRepo())
	p := configuredProject(t, repo)

	w := putProject(t, h, p.ID, `{
	  "llm_providers": [
	    {"id": "default", "provider": "bedrock"},
	    {"id": "onprem", "provider": "litellm", "config": {"endpoint_id": "ep-7"}}
	  ],
	  "layer_models": {
	    "default": {"provider_id": "default", "model": "opus"},
	    "table_descriptions": {"provider_id": "onprem", "model": ""}
	  }
	}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — an endpoint serves its own model: %s", w.Code, w.Body.String())
	}
}

func TestUpdate_DualWritesTheLegacyMirror(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, newMockDomainPackRepo())
	p := configuredProject(t, repo)

	// The table-description layer is on the RESERVED blurb entry, whose
	// credential already lives under the key an older image reads
	// (blurb-llm-credentials) — so the mirror is safe to write.
	w := putProject(t, h, p.ID, `{
	  "llm_providers": [
	    {"id": "default", "provider": "bedrock", "config": {"region": "us-east-1"}},
	    {"id": "blurb", "provider": "litellm", "config": {"base_url": "http://cheap"}}
	  ],
	  "layer_models": {
	    "default": {"provider_id": "default", "model": "opus", "config": {"reasoning_enabled": "true"}},
	    "table_descriptions": {"provider_id": "blurb", "model": "qwen3-32b"}
	  }
	}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	stored, _ := repo.GetByID(context.Background(), p.ID)

	// An agent, sources-worker or fileingestion-worker image older than this
	// release reads these fields and must see a truthful single-provider
	// setup.
	if stored.LLM.Provider != "bedrock" || stored.LLM.Model != "opus" {
		t.Errorf("llm = %+v, want the default layer's pair", stored.LLM)
	}
	if stored.LLM.Config["region"] != "us-east-1" {
		t.Errorf("llm.config = %+v, want the default entry's connection fields", stored.LLM.Config)
	}
	if stored.ReasoningEnabled == nil || !*stored.ReasoningEnabled {
		t.Error("reasoning_enabled must mirror the default layer, so an older image honours the operator's intent")
	}
	if stored.PromptCacheEnabled == nil || !*stored.PromptCacheEnabled {
		t.Error("prompt_cache_enabled must mirror the default layer (absent means on)")
	}
	if stored.BlurbLLM == nil || stored.BlurbLLM.Provider != "litellm" || stored.BlurbLLM.Model != "qwen3-32b" {
		t.Errorf("blurb_llm = %+v, want the table-description layer", stored.BlurbLLM)
	}
}

func TestUpdate_ClearsTheLegacyBlurbMirrorWhenTableDescriptionsMatchTheDefault(t *testing.T) {
	// Leaving a stale override would make an older image index with the wrong
	// model — the exact failure the dual-write exists to prevent.
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, newMockDomainPackRepo())
	p := &models.Project{Name: "p", Domain: "gaming", Category: "match3"}
	p.LLM = models.LLMConfig{Provider: "bedrock", Model: "opus"}
	p.BlurbLLM = &models.BlurbLLMConfig{Provider: "litellm", Model: "stale"}
	p.LLMProviders = []llmlayer.ProviderEntry{{ID: llmlayer.DefaultProviderID, Provider: "bedrock"}}
	p.LayerModels = map[string]llmlayer.LayerModel{
		llmlayer.DefaultLayer: {ProviderID: llmlayer.DefaultProviderID, Model: "opus"},
	}
	p.LLMConfigVersion = llmlayer.ConfigVersion
	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	w := putProject(t, h, p.ID, `{
	  "llm_providers": [{"id": "default", "provider": "bedrock"}],
	  "layer_models": {"default": {"provider_id": "default", "model": "opus"}}
	}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	stored, _ := repo.GetByID(context.Background(), p.ID)
	if stored.BlurbLLM != nil {
		t.Errorf("blurb_llm = %+v, want nil once table descriptions equal the default", stored.BlurbLLM)
	}
}

func TestUpdate_AnUnrelatedEditDoesNotConfigureTheProject(t *testing.T) {
	// The forced review is the point. A client that GETs a project, changes
	// its name and PUTs it back must not silently stamp the version.
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, newMockDomainPackRepo())

	p := &models.Project{Name: "legacy", Domain: "gaming", Category: "match3"}
	p.LLM = models.LLMConfig{Provider: "bedrock", Model: "opus"}
	repo.Create(context.Background(), p)

	w := putProject(t, h, p.ID, `{"name": "renamed"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	stored, _ := repo.GetByID(context.Background(), p.ID)
	if stored.LLMConfigVersion != 0 {
		t.Errorf("llm_config_version = %d, want 0 — only saving the configuration clears the gate", stored.LLMConfigVersion)
	}
}

func TestCreate_IsBornConfiguredFromTheLegacyFieldsAlone(t *testing.T) {
	// A create is an explicit configuration act, and a client that only knows
	// the legacy shape must still produce a runnable project rather than one
	// born gated.
	repo := newMockProjectRepo()
	packs := newMockDomainPackRepo()
	packs.add(&models.DomainPack{Slug: "gaming", Name: "Gaming"})
	h := NewProjectsHandler(repo, packs)

	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(`{
	  "name": "new", "domain": "gaming", "category": "match3",
	  "llm": {"provider": "bedrock", "model": "opus"}
	}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data models.Project `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.LLMConfigVersion != llmlayer.ConfigVersion {
		t.Errorf("llm_config_version = %d, want %d", env.Data.LLMConfigVersion, llmlayer.ConfigVersion)
	}
	if got := env.Data.LayerModels[llmlayer.DefaultLayer].Model; got != "opus" {
		t.Errorf("seeded default row model = %q, want opus", got)
	}
	spec, err := env.Data.LayerSpec(llmlayer.LayerExploration)
	if err != nil {
		t.Fatalf("a created project must resolve every layer: %v", err)
	}
	if spec.Provider != "bedrock" || spec.Model != "opus" {
		t.Errorf("exploration resolved to %s/%s, want bedrock/opus", spec.Provider, spec.Model)
	}
}

// A client that only knows the legacy single-provider shape — an older
// dashboard build, a script, a curl — must still change what the product
// reads. On a configured project the legacy block is only a mirror, so moving
// it alone would leave the user with a 200 and every layer on the old model.
func TestUpdate_ALegacyLLMWriteMovesTheDefaultLayer(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, newMockDomainPackRepo())
	p := configuredProject(t, repo)

	w := putProject(t, h, p.ID, `{"llm": {"provider": "claude", "model": "claude-haiku-4-5", "config": {"max_output_tokens": "4096"}}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	stored, _ := repo.GetByID(context.Background(), p.ID)

	spec, err := stored.LayerSpec(llmlayer.LayerAnalysis)
	if err != nil {
		t.Fatalf("LayerSpec: %v", err)
	}
	if spec.Provider != "claude" || spec.Model != "claude-haiku-4-5" {
		t.Errorf("analysis resolved to %s/%s, want the model the legacy write asked for", spec.Provider, spec.Model)
	}
	if stored.LLMConfigVersion != llmlayer.ConfigVersion {
		t.Errorf("llm_config_version = %d; a legacy write must not un-configure the project", stored.LLMConfigVersion)
	}
}

// A model-scoped key from a legacy config must not land on the provider ENTRY,
// where it would apply to every layer sharing that connection.
func TestUpdate_ALegacyWriteKeepsModelScopedKeysOffTheEntry(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, newMockDomainPackRepo())
	p := configuredProject(t, repo)

	w := putProject(t, h, p.ID, `{"llm": {"provider": "claude", "model": "m", "config": {"region": "eu-west-1", "max_output_tokens": "4096", "wire_override": "anthropic"}}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	stored, _ := repo.GetByID(context.Background(), p.ID)
	entry, ok := stored.LLMProviderByID(llmlayer.DefaultProviderID)
	if !ok {
		t.Fatal("the default entry is missing")
	}
	if entry.Config["region"] != "eu-west-1" {
		t.Errorf("the connection field did not reach the entry: %+v", entry.Config)
	}
	for _, key := range []string{"max_output_tokens", "wire_override"} {
		if _, present := entry.Config[key]; present {
			t.Errorf("model-scoped key %q landed on the provider entry", key)
		}
	}
	// Off the entry is only half of it: they have to land on the default ROW,
	// or a legacy client's budget and wire override are accepted with a 200
	// and silently discarded.
	row := stored.LayerModels[llmlayer.DefaultLayer]
	if row.Config["max_output_tokens"] != "4096" || row.Config["wire_override"] != "anthropic" {
		t.Errorf("default row config = %+v, want the model-scoped keys from the legacy write", row.Config)
	}
	if _, present := row.Config["region"]; present {
		t.Error("a connection field landed on the layer row, where it would apply to only one layer")
	}
	// And they have to survive the round trip back out to an older image.
	spec, err := stored.LayerSpecUnchecked(llmlayer.DefaultLayer)
	if err != nil {
		t.Fatalf("LayerSpecUnchecked: %v", err)
	}
	if spec.Config["max_output_tokens"] != "4096" || spec.Config["region"] != "eu-west-1" {
		t.Errorf("resolved default spec config = %+v, want the connection and model settings merged", spec.Config)
	}
}

// An UNCONFIGURED project is untouched by a legacy write: the forced review is
// the point, and stamping it here would skip it.
func TestUpdate_ALegacyWriteDoesNotConfigureAnUnconfiguredProject(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, newMockDomainPackRepo())

	p := &models.Project{Name: "legacy", Domain: "gaming", Category: "match3"}
	p.LLM = models.LLMConfig{Provider: "bedrock", Model: "opus"}
	repo.Create(context.Background(), p)

	w := putProject(t, h, p.ID, `{"llm": {"provider": "claude", "model": "haiku"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	stored, _ := repo.GetByID(context.Background(), p.ID)
	if stored.LLMConfigVersion != 0 {
		t.Errorf("llm_config_version = %d, want 0", stored.LLMConfigVersion)
	}
	if len(stored.LayerModels) != 0 {
		t.Errorf("layer_models = %+v, want none", stored.LayerModels)
	}
	if stored.LLM.Model != "haiku" {
		t.Errorf("the legacy write itself must still land: llm = %+v", stored.LLM)
	}
}

// The Ask read path makes the same managed exemption StartRun does.
//
// Without it an existing cloud project — gateway-configured, version 0
// because managedai.Apply stamps only on write — answers 412 "configure your
// models" on a deployment that has no AI configuration surface at all. The
// discovery half was already covered; this is the half that broke Ask.
func TestLayerSpecForRequest_NotGatedOnAManagedDeployment(t *testing.T) {
	p := &models.Project{Name: "cloud", Domain: "gaming"}
	p.LLM = models.LLMConfig{
		Provider: "openai",
		Model:    "decisionbox-analysis-model",
		Config:   map[string]string{"base_url": "https://gateway.example/v1"},
	}
	// Deliberately version 0: what every project written before #407 carries.

	if _, err := layerSpecForRequest(p, llmlayer.LayerAsk); !llmlayer.IsNotConfigured(err) {
		t.Fatalf("self-hosted: err = %v, want the configuration gate to refuse", err)
	}

	t.Cleanup(managedai.Load)
	t.Setenv(managedai.EnvGatewayURL, "https://gateway.example/v1")
	t.Setenv(managedai.EnvAnalysisModel, "decisionbox-analysis-model")
	t.Setenv(managedai.EnvBlurbModel, "decisionbox-blurb-model")
	t.Setenv(managedai.EnvEmbedModel, "decisionbox-embed-model")
	managedai.Load()

	spec, err := layerSpecForRequest(p, llmlayer.LayerAsk)
	if err != nil {
		t.Fatalf("managed: err = %v, want the gateway configuration to resolve", err)
	}
	if spec.Model != "decisionbox-analysis-model" {
		t.Errorf("managed ask model = %q, want the stored gateway alias", spec.Model)
	}
}

// The positive half of the env_var rule: a name the operator exported for
// this purpose is accepted, so the restriction does not remove the feature.
func TestUpdate_AcceptsAPrefixedEnvVarFallback(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, newMockDomainPackRepo())
	p := configuredProject(t, repo)

	body := `{"llm_providers":[{"id":"default","provider":"bedrock"},{"id":"deepinfra","provider":"openai","config":{"base_url":"https://api.deepinfra.com/v1/openai","env_var":"DBX_LLM_DEEPINFRA_KEY"}}],` +
		`"layer_models":{"default":{"model":"opus"},"exploration":{"provider_id":"deepinfra","model":"deepseek-v3"}}}`
	w := putProject(t, h, p.ID, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
}

// The legacy blurb mirror is NOT written when table descriptions are on a
// secondary entry.
//
// The mirror would point an older agent, sources-worker or
// fileingestion-worker image at that entry's endpoint while the credential it
// reads for blurb_llm is fixed: blurb-llm-credentials, then llm-credentials.
// It has never heard of a namespaced key. So the write would hand a
// third-party endpoint the primary credential — authentication failure if we
// are lucky, disclosure if we are not.
//
// Clearing it is the honest degradation: the older image falls back to the
// analysis slot, which is the wrong MODEL but an authorised call. Current
// images read the layer rows and are unaffected either way.
func TestUpdate_RefusesToMirrorASecondaryEntryIntoTheBlurbSlot(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, newMockDomainPackRepo())
	p := configuredProject(t, repo)

	// Something stale in the mirror first, so the test proves a CLEAR rather
	// than an absence.
	stored, _ := repo.GetByID(context.Background(), p.ID)
	stored.BlurbLLM = &models.BlurbLLMConfig{Provider: "bedrock", Model: "haiku"}
	if err := repo.Update(context.Background(), p.ID, stored); err != nil {
		t.Fatalf("seed the stale mirror: %v", err)
	}

	w := putProject(t, h, p.ID, `{
	  "llm_providers": [
	    {"id": "default", "provider": "bedrock", "config": {"region": "us-east-1"}},
	    {"id": "cheap", "provider": "litellm", "config": {"base_url": "http://cheap"}}
	  ],
	  "layer_models": {
	    "default": {"provider_id": "default", "model": "opus"},
	    "table_descriptions": {"provider_id": "cheap", "model": "qwen3-32b"}
	  }
	}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a secondary blurb entry is a supported choice: %s", w.Code, w.Body.String())
	}
	stored, _ = repo.GetByID(context.Background(), p.ID)
	if stored.BlurbLLM != nil {
		t.Errorf("blurb_llm = %+v, want it cleared: an older image cannot read that entry's credential", stored.BlurbLLM)
	}
	// The layer row itself is untouched — this is about the mirror only.
	row := stored.LayerModels[llmlayer.LayerTableDescriptions]
	if row.ProviderID != "cheap" || row.Model != "qwen3-32b" {
		t.Errorf("table_descriptions row = %+v, want the user's choice kept", row)
	}
}

// Two rows can name the same provider and model and still be different
// deployments — a Vertex endpoint_id override is the real case. Clearing the
// mirror on a provider+model match alone pointed an older image at the
// analysis endpoint instead of the one chosen for table descriptions.
func TestUpdate_KeepsTheBlurbMirrorWhenOnlyTheEndpointDiffers(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, newMockDomainPackRepo())
	p := configuredProject(t, repo)

	w := putProject(t, h, p.ID, `{
	  "llm_providers": [
	    {"id": "default", "provider": "vertex-ai", "config": {"project_id": "gcp", "location": "us-central1", "endpoint_id": "ep-analysis"}}
	  ],
	  "layer_models": {
	    "default": {"provider_id": "default", "model": "gemini-2.5-pro"},
	    "table_descriptions": {"provider_id": "default", "model": "gemini-2.5-pro", "config": {"endpoint_id": "ep-blurb"}}
	  }
	}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	stored, _ := repo.GetByID(context.Background(), p.ID)
	if stored.BlurbLLM == nil {
		t.Fatal("blurb_llm was cleared, so an older image would index against the analysis endpoint")
	}
	if stored.BlurbLLM.Config["endpoint_id"] != "ep-blurb" {
		t.Errorf("blurb_llm.config = %+v, want the table-description endpoint", stored.BlurbLLM.Config)
	}
}

// A project created with a reasoning-class analysis model is NOT born
// configured.
//
// The seed is derived: it would put that model on table descriptions, whose
// own rule refuses it, and the first schema index would fail with the
// settings page as the only cure. The project is created and gated instead,
// so the user is sent there with everything pre-filled and one choice to
// make — rather than finding out from a failed index.
func TestCreate_IsNotBornConfiguredWhenTheSeedWouldBeRefused(t *testing.T) {
	repo := newMockProjectRepo()
	packRepo := newMockDomainPackRepo()
	packRepo.add(testDomainPack("gaming", "match3"))
	h := NewProjectsHandler(repo, packRepo)

	body := `{"name":"Reasoning","domain":"gaming","category":"match3","llm":{"provider":"openai","model":"gpt-5"},` +
		`"embedding":{"provider":"openai","model":"text-embedding-3-small"}}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 — a reasoning analysis model is a legitimate choice: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	stored, _ := repo.GetByID(context.Background(), resp.Data.ID)
	if stored.LLMConfigured() {
		t.Errorf("project is configured with layer_models=%+v; the seed its own rule refuses must not be stamped", stored.LayerModels)
	}
	if len(stored.LayerModels) != 0 {
		t.Errorf("layer_models = %+v, want none — settings re-seeds and asks for the one missing choice", stored.LayerModels)
	}
	// The project is otherwise intact: the user's analysis model survives.
	if stored.LLM.Model != "gpt-5" {
		t.Errorf("llm.model = %q, want the model the user chose", stored.LLM.Model)
	}
}

// Every legacy field a client can still send has to move the row that
// actually drives the product, not just its own mirror.
//
// `llm` was handled when the mirror went in; blurb_llm and the two toggles
// were not. An older dashboard build, a script or a curl setting one of them
// got a 200 while every layer kept running the old value — and an older
// agent then read a configuration that disagreed with the one current agents
// read.
func TestUpdate_LegacyOnlyWritesMoveTheirLayerRows(t *testing.T) {
	t.Run("blurb_llm moves the table-description row", func(t *testing.T) {
		repo := newMockProjectRepo()
		h := NewProjectsHandler(repo, newMockDomainPackRepo())
		p := configuredProject(t, repo)

		w := putProject(t, h, p.ID, `{"blurb_llm":{"provider":"bedrock","model":"haiku","config":{"region":"us-east-1","max_output_tokens":"1024"}}}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		stored, _ := repo.GetByID(context.Background(), p.ID)
		row, ok := stored.LayerModels[llmlayer.LayerTableDescriptions]
		if !ok {
			t.Fatalf("no table_descriptions row after a legacy blurb write: %+v", stored.LayerModels)
		}
		if row.Model != "haiku" {
			t.Errorf("table_descriptions model = %q, want the model the client sent", row.Model)
		}
		if row.ProviderID != llmlayer.BlurbProviderID {
			t.Errorf("table_descriptions provider_id = %q, want the reserved blurb entry", row.ProviderID)
		}
		if row.Config["max_output_tokens"] != "1024" {
			t.Errorf("row config = %+v, want the model-scoped setting", row.Config)
		}
		entry, found := stored.LLMProviderByID(llmlayer.BlurbProviderID)
		if !found || entry.Config["region"] != "us-east-1" {
			t.Errorf("blurb entry = %+v, want its connection fields", entry)
		}
		// And the layer really resolves to it.
		spec, err := stored.LayerSpecUnchecked(llmlayer.LayerTableDescriptions)
		if err != nil {
			t.Fatalf("LayerSpecUnchecked: %v", err)
		}
		if spec.Model != "haiku" {
			t.Errorf("resolved table-description model = %q", spec.Model)
		}
	})

	// A blank model on a non-endpoint blurb override INHERITS the analysis
	// model — the legacy resolver has always done that, and it is a shape
	// clients still send. Writing the blank through produced a row with no
	// model and a 400 on a request that used to work.
	t.Run("a blurb write with no model inherits the analysis model", func(t *testing.T) {
		repo := newMockProjectRepo()
		h := NewProjectsHandler(repo, newMockDomainPackRepo())
		p := configuredProject(t, repo)

		w := putProject(t, h, p.ID, `{"blurb_llm":{"provider":"bedrock","model":""}}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		stored, _ := repo.GetByID(context.Background(), p.ID)
		row := stored.LayerModels[llmlayer.LayerTableDescriptions]
		if row.Model != stored.LLM.Model {
			t.Errorf("table_descriptions model = %q, want the analysis model %q", row.Model, stored.LLM.Model)
		}
	})

	// An ENDPOINT-backed override legitimately carries no model: the
	// deployment serves its own, so inheriting one would be wrong.
	t.Run("an endpoint-backed blurb write keeps its empty model", func(t *testing.T) {
		repo := newMockProjectRepo()
		h := NewProjectsHandler(repo, newMockDomainPackRepo())
		p := configuredProject(t, repo)

		w := putProject(t, h, p.ID, `{"blurb_llm":{"provider":"vertex-ai","model":"","config":{"project_id":"gcp","location":"us-central1","endpoint_id":"ep-7"}}}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		stored, _ := repo.GetByID(context.Background(), p.ID)
		if row := stored.LayerModels[llmlayer.LayerTableDescriptions]; row.Model != "" {
			t.Errorf("table_descriptions model = %q, want it left empty for the endpoint", row.Model)
		}
	})

	t.Run("an empty blurb provider clears the row", func(t *testing.T) {
		repo := newMockProjectRepo()
		h := NewProjectsHandler(repo, newMockDomainPackRepo())
		p := configuredProject(t, repo)
		if w := putProject(t, h, p.ID, `{"blurb_llm":{"provider":"bedrock","model":"haiku"}}`); w.Code != http.StatusOK {
			t.Fatalf("seed: %s", w.Body.String())
		}

		w := putProject(t, h, p.ID, `{"blurb_llm":{"provider":"","model":""}}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		stored, _ := repo.GetByID(context.Background(), p.ID)
		if _, ok := stored.LayerModels[llmlayer.LayerTableDescriptions]; ok {
			t.Error("the table-description row survived the documented clear")
		}
	})

	// A project can be configured on its ROWS while its entries were never
	// stored: the resolver synthesises them from the legacy fields. Appending
	// a blurb entry to that empty slice produced a one-entry project with no
	// reserved default, and a 400 naming an id the user never typed.
	t.Run("a project whose entries were never stored still accepts one", func(t *testing.T) {
		repo := newMockProjectRepo()
		h := NewProjectsHandler(repo, newMockDomainPackRepo())
		p := configuredProject(t, repo)

		stored, _ := repo.GetByID(context.Background(), p.ID)
		stored.LLMProviders = nil
		stored.LayerModels = map[string]llmlayer.LayerModel{
			llmlayer.DefaultLayer: {ProviderID: llmlayer.DefaultProviderID, Model: "opus"},
		}
		stored.LLMConfigVersion = llmlayer.ConfigVersion
		if err := repo.Update(context.Background(), p.ID, stored); err != nil {
			t.Fatalf("seed: %v", err)
		}

		w := putProject(t, h, p.ID, `{"blurb_llm":{"provider":"bedrock","model":"haiku"}}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		got, _ := repo.GetByID(context.Background(), p.ID)
		if _, ok := got.LLMProviderByID(llmlayer.DefaultProviderID); !ok {
			t.Errorf("no default entry after the write: %+v", got.LLMProviders)
		}
		if got.LayerModels[llmlayer.LayerTableDescriptions].Model != "haiku" {
			t.Errorf("table_descriptions row = %+v", got.LayerModels[llmlayer.LayerTableDescriptions])
		}
	})

	t.Run("the toggles move the default row", func(t *testing.T) {
		repo := newMockProjectRepo()
		h := NewProjectsHandler(repo, newMockDomainPackRepo())
		p := configuredProject(t, repo)

		w := putProject(t, h, p.ID, `{"prompt_cache_enabled":false,"reasoning_enabled":true}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		stored, _ := repo.GetByID(context.Background(), p.ID)
		spec, err := stored.LayerSpecUnchecked(llmlayer.DefaultLayer)
		if err != nil {
			t.Fatalf("LayerSpecUnchecked: %v", err)
		}
		if spec.PromptCache {
			t.Error("prompt caching is still on after a legacy write turning it off")
		}
		if !spec.Reasoning {
			t.Error("reasoning is still off after a legacy write turning it on")
		}
	})
}

// Ask's per-layer controls have to reach the request, not just the provider.
//
// Reasoning is requested PER CALL — putting reasoning_enabled in a provider
// config does not ask for it, Ollama reads the request field — and an output
// ceiling below Ask's own reservation is a hard limit, not a preference: the
// provider rejects a 2,048-token request from a model capped at 512.
func TestAskAppliesItsLayersResolvedRequestSettings(t *testing.T) {
	spec := llmlayer.LayerSpec{
		Layer: llmlayer.LayerAsk, ProviderID: llmlayer.DefaultProviderID,
		Provider: "openai", Model: "gpt-4o",
		Config: map[string]string{"max_output_tokens": "512"},
	}
	if got := askLayerOutputTokens(spec); got != 512 {
		t.Errorf("output tokens = %d, want the layer's own 512", got)
	}
	if got := askReasoningEffort(spec); got != gollm.ReasoningEffortDefault {
		t.Errorf("reasoning effort = %q, want the default for a layer that did not opt in", got)
	}

	spec.Reasoning = true
	if got := askReasoningEffort(spec); got != gollm.ReasoningEffortOn {
		t.Errorf("reasoning effort = %q, want it requested", got)
	}

	// A roomier model does not make answers longer: the reservation is a
	// product decision, and only a LOWER ceiling overrides it.
	roomy := llmlayer.LayerSpec{
		Layer: llmlayer.LayerAsk, ProviderID: llmlayer.DefaultProviderID,
		Provider: "openai", Model: "gpt-4o",
		Config: map[string]string{"max_output_tokens": "32000"},
	}
	if got := askLayerOutputTokens(roomy); got != askMaxOutputTokens {
		t.Errorf("output tokens = %d, want Ask's own %d", got, askMaxOutputTokens)
	}
}

// The legacy compatibility path rewrites provider ENTRIES, so the
// deployment's allow-list applies to it like any other write.
//
// Without the check it was a way around the list entirely: a blurb_llm
// update rewrites the shared reserved blurb entry, so every layer pointing
// at that entry moved to a forbidden provider with only a shape check in
// the way.
func TestUpdate_LegacyBlurbWriteCannotBypassTheProviderAllowList(t *testing.T) {
	ck := &perProviderChecker{deny: "litellm"}
	swapChecker(t, ck)

	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, newMockDomainPackRepo())
	p := configuredProject(t, repo)

	w := putProject(t, h, p.ID, `{"blurb_llm":{"provider":"litellm","model":"qwen3-32b","config":{"base_url":"http://cheap"}}}`)
	if w.Code == http.StatusOK {
		t.Fatalf("a forbidden provider was accepted through the legacy path: %s", w.Body.String())
	}

	var asked bool
	for _, provider := range ck.asked {
		if provider == "litellm" {
			asked = true
		}
	}
	if !asked {
		t.Errorf("the allow-list was asked about %v; the rewritten entry must be checked", ck.asked)
	}

	// And nothing was written: a refused request leaves the project alone.
	stored, _ := repo.GetByID(context.Background(), p.ID)
	if stored.BlurbLLM != nil && stored.BlurbLLM.Provider == "litellm" {
		t.Error("the forbidden provider was persisted anyway")
	}
}

// A legacy project sending only layer_models validates against entries
// SYNTHESISED from llm and blurb_llm — and those entries are real to the
// resolver, so a row may name the synthesised "blurb" one. Nothing had
// persisted them, and the mirror is then rewritten over the very fields
// they came from: the update succeeded and the layer silently fell back to
// the default provider.
func TestUpdate_LayersOnlyWriteMaterialisesTheEntriesItWasValidatedAgainst(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, newMockDomainPackRepo())
	p := configuredProject(t, repo)

	// A legacy-shaped project: both legacy blocks, no stored entries.
	stored, _ := repo.GetByID(context.Background(), p.ID)
	stored.LLMProviders = nil
	stored.LayerModels = nil
	stored.LLMConfigVersion = 0
	stored.LLM = models.LLMConfig{Provider: "bedrock", Model: "opus"}
	stored.BlurbLLM = &models.BlurbLLMConfig{Provider: "litellm", Model: "qwen3-32b", Config: map[string]string{"base_url": "http://cheap"}}
	if err := repo.Update(context.Background(), p.ID, stored); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Put ANALYSIS on the synthesised blurb entry and leave table
	// descriptions on the default — so the mirror clears blurb_llm.
	w := putProject(t, h, p.ID, `{"layer_models":{
	  "default":{"provider_id":"default","model":"opus"},
	  "analysis":{"provider_id":"blurb","model":"qwen3-32b"}
	}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	got, _ := repo.GetByID(context.Background(), p.ID)
	entry, ok := got.LLMProviderByID(llmlayer.BlurbProviderID)
	if !ok {
		t.Fatalf("the entry the analysis row names was not persisted: %+v", got.LLMProviders)
	}
	if entry.Provider != "litellm" {
		t.Errorf("blurb entry provider = %q, want litellm", entry.Provider)
	}
	// And analysis still resolves to it rather than falling back.
	spec, err := got.LayerSpecUnchecked(llmlayer.LayerAnalysis)
	if err != nil {
		t.Fatalf("LayerSpecUnchecked: %v", err)
	}
	if spec.Provider != "litellm" || spec.FellBackFromProviderID != "" {
		t.Errorf("analysis resolved to %q (fell back from %q), want the entry it names",
			spec.Provider, spec.FellBackFromProviderID)
	}
}

// An agent image older than the API does not know --layer and dies in
// flag.Parse, which reads as an unrelated connection failure. Retrying
// without the flag would be worse than any message: it would test the
// DEFAULT layer and report the answer as if it were this layer's.
func TestTestLLM_AnOlderAgentIsReportedAsSuch(t *testing.T) {
	older := &runner.RunSyncResult{
		Error:  "flag provided but not defined: -layer\nUsage of decisionbox-agent:",
		Output: nil,
	}
	if !mentionsUnknownLayerFlag(older) {
		t.Error("an older agent's flag-parse failure was not recognised")
	}

	// A real connection failure keeps its own message.
	real := &runner.RunSyncResult{Error: "dial tcp 10.0.0.1:443: connect: connection refused"}
	if mentionsUnknownLayerFlag(real) {
		t.Error("a genuine connection failure was mistaken for an old agent")
	}
	if mentionsUnknownLayerFlag(nil) {
		t.Error("a nil result must not be read as an old agent")
	}
	// A different unknown flag is not this.
	other := &runner.RunSyncResult{Error: "flag provided but not defined: -wombat"}
	if mentionsUnknownLayerFlag(other) {
		t.Error("an unrelated unknown flag was reported as the layer problem")
	}
}

// The legacy mirror has to be resolved from the project AS UPDATED.
//
// dualWriteLegacyLLM replaces p.PromptCacheEnabled with what the DEFAULT
// ROW resolves to, and a table-description row that sets its own model but
// no caching entry inherits exactly that. Resolving it from the snapshot
// taken at the top of the function used the pre-update pointer — so the
// mirror carried the old value and an older agent image indexed with a
// different caching setting from the one a current image resolves for the
// same project.
func TestUpdate_TheBlurbMirrorReflectsTheDefaultRowJustSaved(t *testing.T) {
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, newMockDomainPackRepo())
	p := configuredProject(t, repo)

	// The project field says caching is ON; the default ROW being saved
	// turns it off, and table descriptions inherit that.
	on := true
	stored, _ := repo.GetByID(context.Background(), p.ID)
	stored.PromptCacheEnabled = &on
	if err := repo.Update(context.Background(), p.ID, stored); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := putProject(t, h, p.ID, `{
	  "llm_providers": [{"id": "default", "provider": "bedrock"}],
	  "layer_models": {
	    "default": {"provider_id": "default", "model": "opus", "config": {"prompt_cache": "false"}},
	    "table_descriptions": {"provider_id": "default", "model": "haiku"}
	  }
	}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	got, _ := repo.GetByID(context.Background(), p.ID)
	spec, err := got.LayerSpecUnchecked(llmlayer.LayerTableDescriptions)
	if err != nil {
		t.Fatalf("LayerSpecUnchecked: %v", err)
	}
	if spec.PromptCache {
		t.Error("the table-description layer resolves caching ON; it inherits a default row that turned it off")
	}
	if got.BlurbLLM == nil {
		t.Fatal("the blurb mirror was cleared; this layer has its own model")
	}
	if cached := got.BlurbLLM.Config["prompt_cache"]; cached != "false" {
		t.Errorf("blurb mirror prompt_cache = %q, want false — an older image must not index with the pre-update value", cached)
	}
}
