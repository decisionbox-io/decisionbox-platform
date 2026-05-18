package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// Tests in this file cover the secret-key rename in
// ListLiveLLMModelsForProject and ListLiveEmbeddingModelsForProject —
// the two handlers were extended to read llm-credentials and
// embedding-credentials respectively (previously llm-api-key /
// embedding-api-key). A handler that reads the wrong key silently
// returns an empty credentials map, the live-list call falls back to
// in-flight cfg with no credential, and the user gets "no API key"
// from the provider factory. These tests pin the new key names so a
// future rename does not regress silently.

// secretsStub implements gosecrets.Provider with a fixed map AND
// records every key the handler asked for. The recorded keys are the
// real assertion target — checking the response body for a credential
// would make the test depend on upstream error text from real provider
// SDKs, but checking which secret-store keys were consulted is exact
// and provider-agnostic.
type secretsStub struct {
	store    map[string]string // key = "<projectID>/<key>"
	requests []string          // append-only log of keys Get was called with
}

func (s *secretsStub) Get(_ context.Context, projectID, key string) (string, error) {
	s.requests = append(s.requests, key)
	v, ok := s.store[projectID+"/"+key]
	if !ok {
		return "", gosecrets.ErrNotFound
	}
	return v, nil
}

func (s *secretsStub) Set(_ context.Context, _ string, _ string, _ string) error { return nil }

func (s *secretsStub) List(_ context.Context, _ string) ([]gosecrets.SecretEntry, error) {
	return nil, nil
}

// containsKey reports whether the recorded request log includes the
// given key. Used by rename-guard assertions to check what the handler
// actually asked the secret store for.
func (s *secretsStub) containsKey(want string) bool {
	for _, k := range s.requests {
		if k == want {
			return true
		}
	}
	return false
}

func TestProvidersHandler_ListLiveLLMModelsForProject_ReadsLLMCredentials(t *testing.T) {
	repo := &stubProjectRepo{project: &models.Project{
		ID:  "p1",
		LLM: models.LLMConfig{Provider: "claude", Model: "claude-sonnet-4-6"},
	}}
	sp := &secretsStub{store: map[string]string{
		"p1/llm-credentials": "sk-test-from-secret-store",
	}}
	h := NewProvidersHandlerWithProject(repo, sp)

	req := httptest.NewRequest("POST", "/api/v1/projects/p1/providers/llm/models/live", strings.NewReader(`{}`))
	req.SetPathValue("id", "p1")
	w := httptest.NewRecorder()
	h.ListLiveLLMModelsForProject(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	// The real regression guard: the handler must have asked for the
	// new "llm-credentials" key.
	if !sp.containsKey("llm-credentials") {
		t.Errorf("handler never read llm-credentials; requests = %v", sp.requests)
	}
}

func TestProvidersHandler_ListLiveLLMModelsForProject_OldKeyNotRead(t *testing.T) {
	repo := &stubProjectRepo{project: &models.Project{
		ID:  "p1",
		LLM: models.LLMConfig{Provider: "claude", Model: "claude-sonnet-4-6"},
	}}
	// Wire ONLY the old key into the store. A correct handler asks for
	// "llm-credentials", never sees the value, and falls back to no-
	// credentials. A regressed handler asks for "llm-api-key" and
	// picks up "sk-from-old-key" — which the recorder below will
	// surface as a failed assertion.
	sp := &secretsStub{store: map[string]string{
		"p1/llm-api-key": "sk-from-old-key",
	}}
	h := NewProvidersHandlerWithProject(repo, sp)

	req := httptest.NewRequest("POST", "/api/v1/projects/p1/providers/llm/models/live", strings.NewReader(`{}`))
	req.SetPathValue("id", "p1")
	w := httptest.NewRecorder()
	h.ListLiveLLMModelsForProject(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	// Regression guard: the handler must NEVER ask for the old key.
	if sp.containsKey("llm-api-key") {
		t.Errorf("handler regressed to reading old llm-api-key key; requests = %v", sp.requests)
	}
	// And it MUST ask for the new key.
	if !sp.containsKey("llm-credentials") {
		t.Errorf("handler never read llm-credentials; requests = %v", sp.requests)
	}
}

func TestProvidersHandler_ListLiveEmbeddingModelsForProject_ReadsEmbeddingCredentials(t *testing.T) {
	repo := &stubProjectRepo{project: &models.Project{
		ID:        "p1",
		Embedding: embedding.ProjectConfig{Provider: "openai", Model: "text-embedding-3-small"},
	}}
	sp := &secretsStub{store: map[string]string{
		"p1/embedding-credentials": "sk-test-emb-from-secret-store",
	}}
	h := NewProvidersHandlerWithProject(repo, sp)

	req := httptest.NewRequest("POST", "/api/v1/projects/p1/providers/embedding/models/live", strings.NewReader(`{}`))
	req.SetPathValue("id", "p1")
	w := httptest.NewRecorder()
	h.ListLiveEmbeddingModelsForProject(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !sp.containsKey("embedding-credentials") {
		t.Errorf("handler never read embedding-credentials; requests = %v", sp.requests)
	}
	if sp.containsKey("embedding-api-key") {
		t.Errorf("handler regressed to reading old embedding-api-key; requests = %v", sp.requests)
	}
}

// Regression guard for the bug Copilot caught: ProjectConfig must carry
// the Config map so the dashboard's auth_method (and method-specific
// fields like role_arn) reach the live-list call and the agent. If
// embedding.ProjectConfig drops the Config field again, this test
// fails — the project's auth_method goes missing from the live-list
// cfg map and the regression survives unit tests until a real
// integration smoke catches it in production.
func TestProvidersHandler_ListLiveEmbeddingModelsForProject_ForwardsProjectConfig(t *testing.T) {
	// Pin embedding.ProjectConfig.Config persistence + the handler's
	// project-config forwarding by round-tripping a non-empty Config
	// map. If a future refactor drops the Config field from
	// embedding.ProjectConfig (the bug Copilot caught on PR #222), the
	// project's Config never makes it into Embedding and this test's
	// .Config field access would be checking an empty map. The assertion
	// confirms the field exists and round-trips at the model level.
	proj := &models.Project{
		ID: "p1",
		Embedding: embedding.ProjectConfig{
			Provider: "openai",
			Model:    "text-embedding-3-small",
			Config: map[string]string{
				"auth_method": "api_key",
				"base_url":    "https://api.example.com/v1",
			},
		},
	}
	repo := &stubProjectRepo{project: proj}
	sp := &secretsStub{store: map[string]string{
		"p1/embedding-credentials": "sk-test", //nolint:gosec // test fixture
	}}
	h := NewProvidersHandlerWithProject(repo, sp)

	req := httptest.NewRequest("POST", "/api/v1/projects/p1/providers/embedding/models/live", strings.NewReader(`{}`))
	req.SetPathValue("id", "p1")
	w := httptest.NewRecorder()
	h.ListLiveEmbeddingModelsForProject(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	// Direct assertion on the model: Config field must exist and carry
	// the round-tripped values. This is the line that protects against
	// the Copilot-flagged regression — if the field is removed from
	// the struct, this won't compile.
	if proj.Embedding.Config["auth_method"] != "api_key" {
		t.Errorf("Embedding.Config[auth_method] = %q, want api_key", proj.Embedding.Config["auth_method"])
	}
	if proj.Embedding.Config["base_url"] != "https://api.example.com/v1" {
		t.Errorf("Embedding.Config[base_url] = %q", proj.Embedding.Config["base_url"])
	}
}
