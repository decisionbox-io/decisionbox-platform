package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/services/api/models"

	// Register real providers so GetProviderMeta finds them.
	_ "github.com/decisionbox-io/decisionbox/providers/llm/claude"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/ollama"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/openai"
)

// stubSecretProvider records which keys were requested so a test can
// assert the slot-routing logic picks the right secret.
type stubSecretProvider struct {
	values   map[string]string // key → value (project-scoped tests use one project)
	requests []string          // append-only list of keys passed to Get
}

func (s *stubSecretProvider) Get(_ context.Context, _, key string) (string, error) {
	s.requests = append(s.requests, key)
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", secrets.ErrNotFound
}
func (s *stubSecretProvider) Set(context.Context, string, string, string) error { return nil }
func (s *stubSecretProvider) List(context.Context, string) ([]secrets.SecretEntry, error) {
	return nil, nil
}

// --- ListLiveLLMModels (cloud-neutral; POST body with credentials) ---

func TestProvidersHandler_ListLiveLLMModels_UnknownProvider(t *testing.T) {
	h := NewProvidersHandler()
	req := httptest.NewRequest("POST", "/api/v1/providers/llm/bogus/models/live",
		strings.NewReader(`{"config":{}}`))
	req.SetPathValue("id", "bogus")
	w := httptest.NewRecorder()

	h.ListLiveLLMModels(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

func TestProvidersHandler_ListLiveLLMModels_MissingPathValue(t *testing.T) {
	h := NewProvidersHandler()
	req := httptest.NewRequest("POST", "/api/v1/providers/llm//models/live",
		strings.NewReader(`{"config":{}}`))
	w := httptest.NewRecorder()

	h.ListLiveLLMModels(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestProvidersHandler_ListLiveLLMModels_InvalidJSON(t *testing.T) {
	h := NewProvidersHandler()
	req := httptest.NewRequest("POST", "/api/v1/providers/llm/claude/models/live",
		strings.NewReader(`not-json`))
	req.SetPathValue("id", "claude")
	w := httptest.NewRecorder()

	h.ListLiveLLMModels(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
}

// When the factory fails (e.g. missing credentials), the handler treats
// it as "provider can't list right now" — 200 with the catalog rows and
// NO live_error. The list endpoint exists to discover models before
// credentials are set, so a missing-api-key factory rejection is
// expected, not an error to surface. The user sees the catalog + can
// type a free-text model name. Real upstream errors (rate-limited,
// network down) still propagate via the ListModels() return path.
func TestProvidersHandler_ListLiveLLMModels_FactoryFailureReturnsCatalog(t *testing.T) {
	h := NewProvidersHandler()
	// claude factory requires api_key — send empty body to provoke failure.
	req := httptest.NewRequest("POST", "/api/v1/providers/llm/claude/models/live",
		strings.NewReader(`{"config":{}}`))
	req.SetPathValue("id", "claude")
	w := httptest.NewRecorder()

	h.ListLiveLLMModels(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		Data struct {
			Models    []map[string]any `json:"models"`
			LiveError string           `json:"live_error"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.LiveError != "" {
		t.Errorf("factory rejection must not surface as live_error, got %q", resp.Data.LiveError)
	}
	// Catalog rows must still be returned so the UI has something.
	if len(resp.Data.Models) == 0 {
		t.Error("expected catalog rows in models[] even when upstream fails")
	}
	// All rows should be Source=catalog since no live rows merged.
	for _, m := range resp.Data.Models {
		if m["source"] != "catalog" {
			t.Errorf("row %v has non-catalog source when live fetch failed", m)
		}
	}
}

// --- ListLiveLLMModelsForProject (uses saved secret) ---

type stubProjectRepo struct {
	project *models.Project
	err     error
}

func (r *stubProjectRepo) GetByID(_ context.Context, _ string) (*models.Project, error) {
	return r.project, r.err
}
func (r *stubProjectRepo) Create(context.Context, *models.Project) error            { return nil }
func (r *stubProjectRepo) List(context.Context, int, int) ([]*models.Project, error) { return nil, nil }
func (r *stubProjectRepo) Update(context.Context, string, *models.Project) error     { return nil }
func (r *stubProjectRepo) Delete(context.Context, string) error                      { return nil }
func (r *stubProjectRepo) DeleteCascade(context.Context, string) error               { return nil }
func (r *stubProjectRepo) Count(context.Context) (int, error)                        { return 0, nil }
func (r *stubProjectRepo) CountWithWarehouse(context.Context) (int, error)           { return 0, nil }
func (r *stubProjectRepo) SetSchemaIndexStatus(context.Context, string, string, string) error {
	return nil
}

func TestProvidersHandler_ListLiveLLMModelsForProject_ProjectNotFound(t *testing.T) {
	repo := &stubProjectRepo{project: nil}
	h := NewProvidersHandlerWithProject(repo, nil)

	req := httptest.NewRequest("POST", "/api/v1/projects/missing/providers/llm/models/live",
		strings.NewReader(`{}`))
	req.SetPathValue("id", "missing")
	w := httptest.NewRecorder()

	h.ListLiveLLMModelsForProject(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestProvidersHandler_ListLiveLLMModelsForProject_ProjectLoadError(t *testing.T) {
	repo := &stubProjectRepo{err: errors.New("db down")}
	h := NewProvidersHandlerWithProject(repo, nil)

	req := httptest.NewRequest("POST", "/api/v1/projects/x/providers/llm/models/live",
		strings.NewReader(`{}`))
	req.SetPathValue("id", "x")
	w := httptest.NewRecorder()

	h.ListLiveLLMModelsForProject(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestProvidersHandler_ListLiveLLMModelsForProject_EmptyProviderField(t *testing.T) {
	repo := &stubProjectRepo{project: &models.Project{ID: "p"}}
	h := NewProvidersHandlerWithProject(repo, nil)

	req := httptest.NewRequest("POST", "/api/v1/projects/p/providers/llm/models/live",
		strings.NewReader(`{}`))
	req.SetPathValue("id", "p")
	w := httptest.NewRecorder()

	h.ListLiveLLMModelsForProject(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "no llm provider") {
		t.Errorf("expected actionable error: %s", w.Body.String())
	}
}

func TestProvidersHandler_ListLiveLLMModelsForProject_UnknownProvider(t *testing.T) {
	repo := &stubProjectRepo{project: &models.Project{
		ID:  "p",
		LLM: models.LLMConfig{Provider: "bogus-never-registered"},
	}}
	h := NewProvidersHandlerWithProject(repo, nil)

	req := httptest.NewRequest("POST", "/api/v1/projects/p/providers/llm/models/live",
		strings.NewReader(`{}`))
	req.SetPathValue("id", "p")
	w := httptest.NewRecorder()

	h.ListLiveLLMModelsForProject(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// With no secret provider wired, the handler still attempts the
// factory (cloud providers like Bedrock / Vertex use ambient creds);
// for api-key providers (Claude, OpenAI) the factory rejects the empty
// credential. Under the "lister or empty" contract the rejection is
// treated as "this provider can't list right now" — 200 with the
// catalog rows and NO live_error. The dashboard renders catalog +
// free-text so the user can still proceed.
func TestProvidersHandler_ListLiveLLMModelsForProject_NoSecretProviderReturnsCatalog(t *testing.T) {
	repo := &stubProjectRepo{project: &models.Project{
		ID:  "p",
		LLM: models.LLMConfig{Provider: "claude", Model: "claude-sonnet-4-6"},
	}}
	h := NewProvidersHandlerWithProject(repo, nil)

	req := httptest.NewRequest("POST", "/api/v1/projects/p/providers/llm/models/live",
		strings.NewReader(`{}`))
	req.SetPathValue("id", "p")
	w := httptest.NewRecorder()

	h.ListLiveLLMModelsForProject(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Models    []map[string]any `json:"models"`
			LiveError string           `json:"live_error"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.LiveError != "" {
		t.Errorf("factory rejection (missing secret) must not surface as live_error, got %q", resp.Data.LiveError)
	}
	if len(resp.Data.Models) == 0 {
		t.Error("catalog rows must still be returned")
	}
}

// --- ListLiveLLMModelsForProject slot routing ---

// callForProject is a small helper to invoke the handler with a slot body
// and return the recorder so the assertions stay tight.
func callForProject(t *testing.T, h *ProvidersHandler, pid, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/projects/"+pid+"/providers/llm/models/live",
		strings.NewReader(body))
	req.SetPathValue("id", pid)
	w := httptest.NewRecorder()
	h.ListLiveLLMModelsForProject(w, req)
	return w
}

func TestProvidersHandler_ListLiveLLMModelsForProject_DefaultSlotReadsLLMCredentials(t *testing.T) {
	repo := &stubProjectRepo{project: &models.Project{
		ID:  "p",
		LLM: models.LLMConfig{Provider: "claude", Model: "claude-sonnet-4-6"},
	}}
	secrets := &stubSecretProvider{values: map[string]string{
		"llm-credentials":       "sk-llm",
		"blurb-llm-credentials": "sk-blurb",
	}}
	h := NewProvidersHandlerWithProject(repo, secrets)

	w := callForProject(t, h, "p", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if len(secrets.requests) != 1 || secrets.requests[0] != "llm-credentials" {
		t.Errorf("requests = %v, want exactly [llm-credentials]", secrets.requests)
	}
}

func TestProvidersHandler_ListLiveLLMModelsForProject_BlurbSlotReadsBlurbCredentials(t *testing.T) {
	repo := &stubProjectRepo{project: &models.Project{
		ID:  "p",
		LLM: models.LLMConfig{Provider: "claude", Model: "claude-sonnet-4-6"},
		BlurbLLM: &models.BlurbLLMConfig{
			Provider: "openai",
			Model:    "gpt-4.1-nano",
		},
	}}
	secrets := &stubSecretProvider{values: map[string]string{
		"llm-credentials":       "sk-llm",
		"blurb-llm-credentials": "sk-blurb",
	}}
	h := NewProvidersHandlerWithProject(repo, secrets)

	w := callForProject(t, h, "p", `{"slot":"blurb_llm"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if len(secrets.requests) != 1 || secrets.requests[0] != "blurb-llm-credentials" {
		t.Errorf("requests = %v, want exactly [blurb-llm-credentials]", secrets.requests)
	}
}

// When the project has no blurb override the handler borrows the
// analysis LLM provider but still prefers the blurb-specific secret
// — matching the agent's resolveBlurbLLM lookup order
// (blurb-llm-credentials first, then llm-credentials). Without a
// blurb-credentials secret stored, the handler must fall back to
// llm-credentials, so the dashboard's Load-models reads exactly the
// same credential the agent would read at indexing time.
func TestProvidersHandler_ListLiveLLMModelsForProject_BlurbSlotFallsBackToLLM(t *testing.T) {
	repo := &stubProjectRepo{project: &models.Project{
		ID:  "p",
		LLM: models.LLMConfig{Provider: "claude", Model: "claude-sonnet-4-6"},
		// BlurbLLM intentionally unset.
	}}
	secrets := &stubSecretProvider{values: map[string]string{
		// blurb-llm-credentials NOT stored — handler must fall through.
		"llm-credentials": "sk-llm",
	}}
	h := NewProvidersHandlerWithProject(repo, secrets)

	w := callForProject(t, h, "p", `{"slot":"blurb_llm"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	want := []string{"blurb-llm-credentials", "llm-credentials"}
	if len(secrets.requests) != len(want) {
		t.Fatalf("requests = %v, want %v (blurb first, then fallback)", secrets.requests, want)
	}
	for i, k := range want {
		if secrets.requests[i] != k {
			t.Errorf("request[%d] = %q, want %q", i, secrets.requests[i], k)
		}
	}
}

// When a blurb-specific secret IS stored, the handler uses it directly
// and never reads llm-credentials. Mirrors the agent's resolveBlurbLLM
// — the blurb-specific credential always wins when present, even if
// the blurb slot borrows the analysis LLM's provider/model.
func TestProvidersHandler_ListLiveLLMModelsForProject_BlurbSlotPrefersBlurbSecretEvenOnFallback(t *testing.T) {
	repo := &stubProjectRepo{project: &models.Project{
		ID:  "p",
		LLM: models.LLMConfig{Provider: "claude", Model: "claude-sonnet-4-6"},
		// No BlurbLLM override — falls through to LLM provider, but
		// the blurb-credentials secret IS present and must win.
	}}
	secrets := &stubSecretProvider{values: map[string]string{
		"blurb-llm-credentials": "sk-blurb",
		"llm-credentials":       "sk-llm",
	}}
	h := NewProvidersHandlerWithProject(repo, secrets)

	w := callForProject(t, h, "p", `{"slot":"blurb_llm"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	// Only one request — the blurb-specific secret short-circuits.
	if len(secrets.requests) != 1 || secrets.requests[0] != "blurb-llm-credentials" {
		t.Errorf("requests = %v, want exactly [blurb-llm-credentials]", secrets.requests)
	}
}

func TestProvidersHandler_ListLiveLLMModelsForProject_InvalidSlot(t *testing.T) {
	repo := &stubProjectRepo{project: &models.Project{ID: "p"}}
	h := NewProvidersHandlerWithProject(repo, nil)

	w := callForProject(t, h, "p", `{"slot":"warehouse"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid slot") {
		t.Errorf("body = %s, want invalid-slot error", w.Body.String())
	}
}

// --- writeLiveModelsResponse merge: PreferLiveModelID ---

// When a provider sets PreferLiveModelID (Ollama), the merge must keep
// the live ID as the picker row's ID even when it matches a catalog
// alias. Other providers (the default) project onto the catalog
// canonical so identical IDs don't double-list. This guards against
// the Ollama-specific 404 where saving the canonical (e.g. "qwen3")
// fails because the upstream only knows the tagged form ("qwen3:32b").
func TestWriteLiveModelsResponse_PreferLiveModelIDKeepsTaggedID(t *testing.T) {
	meta := gollm.ProviderMeta{
		Name: "test-provider",
		Models: []gollm.ModelEntry{
			{
				ID:      "qwen3",
				Aliases: []string{"qwen3:32b", "qwen3:14b"},
				Wire:    gollm.WireUnknown,
			},
		},
		PreferLiveModelID:  true,
		DispatchAnyModelID: true,
	}
	live := []gollm.RemoteModel{
		{ID: "qwen3:32b", DisplayName: "qwen3:32b"},
	}

	w := httptest.NewRecorder()
	writeLiveModelsResponse(w, meta, live, nil)

	var resp struct {
		Data struct {
			Models []map[string]any `json:"models"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Expect a row with the tagged ID, plus the catalog row "qwen3"
	// (which is catalog-only — the dashboard filters it out of the
	// picker, but it still gets serialized).
	var taggedRow, canonicalRow map[string]any
	for _, r := range resp.Data.Models {
		switch r["id"] {
		case "qwen3:32b":
			taggedRow = r
		case "qwen3":
			canonicalRow = r
		}
	}
	if taggedRow == nil {
		t.Fatalf("expected a row with id=qwen3:32b; got models=%+v", resp.Data.Models)
	}
	if taggedRow["source"] != "live" {
		t.Errorf("tagged row source = %v, want live", taggedRow["source"])
	}
	if taggedRow["dispatchable"] != true {
		t.Errorf("tagged row dispatchable = %v, want true (DispatchAnyModelID is set)", taggedRow["dispatchable"])
	}
	if canonicalRow == nil {
		t.Errorf("expected canonical catalog row qwen3 to remain in response")
	} else if canonicalRow["source"] != "catalog" {
		t.Errorf("canonical row source = %v, want catalog", canonicalRow["source"])
	}
}

// Regression guard for the OTHER providers (OpenAI, Bedrock, etc.):
// when PreferLiveModelID is NOT set, a live ID that matches a catalog
// alias must collapse onto the canonical row, preserving the existing
// "one row per canonical ID" picker behaviour.
func TestWriteLiveModelsResponse_DefaultProjectsAliasOntoCanonical(t *testing.T) {
	meta := gollm.ProviderMeta{
		Name: "test-provider",
		Models: []gollm.ModelEntry{
			{
				ID:      "gpt-4o",
				Aliases: []string{"gpt-4o-2024-08-06"},
				Wire:    gollm.WireOpenAICompat,
			},
		},
	}
	live := []gollm.RemoteModel{
		{ID: "gpt-4o-2024-08-06", DisplayName: "gpt-4o-2024-08-06"},
	}

	w := httptest.NewRecorder()
	writeLiveModelsResponse(w, meta, live, nil)

	var resp struct {
		Data struct {
			Models []map[string]any `json:"models"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data.Models) != 1 {
		t.Fatalf("expected one merged row, got %d: %+v", len(resp.Data.Models), resp.Data.Models)
	}
	r := resp.Data.Models[0]
	if r["id"] != "gpt-4o" {
		t.Errorf("merged row id = %v, want canonical gpt-4o", r["id"])
	}
	if r["source"] != "both" {
		t.Errorf("merged row source = %v, want both", r["source"])
	}
}

func TestProvidersHandler_ListLiveLLMModelsForProject_EmptyBodyDefaultsToLLMSlot(t *testing.T) {
	repo := &stubProjectRepo{project: &models.Project{
		ID:  "p",
		LLM: models.LLMConfig{Provider: "claude", Model: "claude-sonnet-4-6"},
	}}
	secrets := &stubSecretProvider{values: map[string]string{
		"llm-credentials": "sk-llm",
	}}
	h := NewProvidersHandlerWithProject(repo, secrets)

	// Empty body (Content-Length: 0) — should not error and should
	// route to the default LLM slot.
	req := httptest.NewRequest("POST", "/api/v1/projects/p/providers/llm/models/live", nil)
	req.SetPathValue("id", "p")
	w := httptest.NewRecorder()
	h.ListLiveLLMModelsForProject(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if len(secrets.requests) != 1 || secrets.requests[0] != "llm-credentials" {
		t.Errorf("requests = %v, want exactly [llm-credentials]", secrets.requests)
	}
}

// --- Merge logic exercised via the public handler path ---

func TestProvidersHandler_Merge_CatalogOnlyRowsKeptWithNoLive(t *testing.T) {
	// No live rows come back (bad credentials) → every catalog row
	// ends up with Source=catalog and Dispatchable matching its wire.
	h := NewProvidersHandler()
	req := httptest.NewRequest("POST", "/api/v1/providers/llm/openai/models/live",
		strings.NewReader(`{"config":{}}`))
	req.SetPathValue("id", "openai")
	w := httptest.NewRecorder()

	h.ListLiveLLMModels(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		Data struct {
			Models []struct {
				ID           string `json:"id"`
				Source       string `json:"source"`
				Wire         string `json:"wire"`
				Dispatchable bool   `json:"dispatchable"`
			} `json:"models"`
			LiveError string `json:"live_error"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	foundDispatchable := false
	for _, m := range resp.Data.Models {
		if m.Source != "catalog" {
			t.Errorf("unexpected source %q for %s when live failed", m.Source, m.ID)
		}
		// Every OpenAI catalog row is wire=openai-compat → dispatchable
		if m.Wire == string(gollm.WireOpenAICompat) && !m.Dispatchable {
			t.Errorf("row %s has wire=openai-compat but dispatchable=false", m.ID)
		}
		if m.Dispatchable {
			foundDispatchable = true
		}
	}
	if !foundDispatchable {
		t.Error("expected at least one dispatchable catalog row for openai")
	}
}

// TestProvidersHandler_Merge_OllamaCatalogRowsDispatchable is the
// regression test for the issue Copilot flagged on PR #193: Ollama
// catalog entries leave Wire blank because the provider has no
// dispatch switch (single-wire), but the live-list merge previously
// derived Dispatchable from `Wire != ""` — making every Ollama
// catalog row appear non-dispatchable in the dashboard. Catalog rows
// are dispatchable by construction.
func TestProvidersHandler_Merge_OllamaCatalogRowsDispatchable(t *testing.T) {
	h := NewProvidersHandler()
	req := httptest.NewRequest("POST", "/api/v1/providers/llm/ollama/models/live",
		strings.NewReader(`{"config":{"host":"http://127.0.0.1:1"}}`))
	req.SetPathValue("id", "ollama")
	w := httptest.NewRecorder()

	h.ListLiveLLMModels(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		Data struct {
			Models []struct {
				ID           string `json:"id"`
				Source       string `json:"source"`
				Wire         string `json:"wire"`
				Dispatchable bool   `json:"dispatchable"`
			} `json:"models"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data.Models) == 0 {
		t.Fatal("ollama catalog should expose at least one row")
	}
	for _, m := range resp.Data.Models {
		if m.Source != "catalog" {
			continue
		}
		if !m.Dispatchable {
			t.Errorf("ollama catalog row %q must be dispatchable", m.ID)
		}
	}
}

// --- validateLLMConfig unit checks ---

func TestValidateLLMConfig_ValidForProvider(t *testing.T) {
	// Provider-scoped allowlist accepts anthropic for bedrock.
	_ = gollm.RegisteredProvidersMeta() // force provider init
	msg := validateLLMConfig("bedrock", map[string]string{"wire_override": "anthropic"})
	if msg != "" {
		t.Errorf("expected pass, got: %q", msg)
	}
}

func TestValidateLLMConfig_RejectsUnsupportedWireForProvider(t *testing.T) {
	msg := validateLLMConfig("bedrock", map[string]string{"wire_override": "google-native"})
	if msg == "" {
		t.Fatal("expected rejection")
	}
	if !strings.Contains(msg, "not supported by provider") {
		t.Errorf("message should mention provider-scoped rejection: %q", msg)
	}
}

func TestValidateLLMConfig_EmptyOverridePasses(t *testing.T) {
	if msg := validateLLMConfig("bedrock", map[string]string{}); msg != "" {
		t.Errorf("empty config should pass, got: %q", msg)
	}
	if msg := validateLLMConfig("bedrock", map[string]string{"wire_override": ""}); msg != "" {
		t.Errorf("empty wire_override should pass, got: %q", msg)
	}
}

func TestValidateLLMConfig_GenericFallbackForProvidersWithoutOptions(t *testing.T) {
	// claude provider has no wire_override field, so the fallback
	// generic ParseWire check fires. "anthropic" is a syntactically
	// valid wire → passes; "antropik" → fails.
	if msg := validateLLMConfig("claude", map[string]string{"wire_override": "anthropic"}); msg != "" {
		t.Errorf("generic fallback should accept valid wire, got: %q", msg)
	}
	if msg := validateLLMConfig("claude", map[string]string{"wire_override": "antropik"}); msg == "" {
		t.Error("generic fallback should reject typo")
	}
}
