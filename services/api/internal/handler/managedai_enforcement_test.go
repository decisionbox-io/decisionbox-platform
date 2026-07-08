package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/services/api/internal/managedai"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// enableManagedForTest turns on managed-inference mode for the duration
// of a test. It registers the reset cleanup BEFORE mutating the
// environment: t.Cleanup is LIFO, so this reload runs AFTER t.Setenv has
// restored the env, leaving the process-wide managedai singleton back in
// its (disabled) default for every subsequent test in this binary.
func enableManagedForTest(t *testing.T) {
	t.Helper()
	t.Cleanup(managedai.Load)
	t.Setenv(managedai.EnvGatewayURL, "https://ai.example.com/v1")
	t.Setenv(managedai.EnvAnalysisModel, "decisionbox-analysis-model")
	t.Setenv(managedai.EnvBlurbModel, "decisionbox-blurb-model")
	t.Setenv(managedai.EnvEmbedModel, "decisionbox-embed-model")
	managedai.Load()
}

// recordingSecretProvider records Set calls so a test can assert the
// managed-mode guard refused to persist an AI credential.
type recordingSecretProvider struct {
	setKeys []string
	store   map[string]string
}

func (s *recordingSecretProvider) Get(_ context.Context, projectID, key string) (string, error) {
	if v, ok := s.store[projectID+"/"+key]; ok {
		return v, nil
	}
	return "", gosecrets.ErrNotFound
}

func (s *recordingSecretProvider) Set(_ context.Context, projectID, key, value string) error {
	s.setKeys = append(s.setKeys, key)
	if s.store == nil {
		s.store = map[string]string{}
	}
	s.store[projectID+"/"+key] = value
	return nil
}

func (s *recordingSecretProvider) List(_ context.Context, _ string) ([]gosecrets.SecretEntry, error) {
	return nil, nil
}

// TestProjectsHandler_Create_ManagedOverride verifies that in managed
// mode a crafted POST — different provider/model/base_url and a smuggled
// credentials_json — is overridden with the gateway preset, not honored
// (issue #16 acceptance: server-authoritative enforcement).
func TestProjectsHandler_Create_ManagedOverride(t *testing.T) {
	enableManagedForTest(t)
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	body := `{
		"name":"Attacker","domain":"gaming","category":"match3",
		"llm":{"provider":"anthropic","model":"claude-x","config":{"base_url":"https://evil.example.com","credentials_json":"sk-attacker"}},
		"embedding":{"provider":"cohere","model":"embed-x","config":{"credentials_json":"sk-attacker"}},
		"blurb_llm":{"provider":"google","model":"gemini-x"}
	}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	if len(repo.projects) != 1 {
		t.Fatalf("repo should have 1 project, got %d", len(repo.projects))
	}
	var saved *models.Project
	for _, p := range repo.projects {
		saved = p
	}

	if saved.LLM.Provider != "openai" || saved.LLM.Model != "decisionbox-analysis-model" {
		t.Errorf("analysis LLM = %s/%s; want openai/decisionbox-analysis-model", saved.LLM.Provider, saved.LLM.Model)
	}
	if saved.LLM.Config["base_url"] != "https://ai.example.com/v1" {
		t.Errorf("analysis base_url = %q; want the gateway URL", saved.LLM.Config["base_url"])
	}
	if _, leaked := saved.LLM.Config["credentials_json"]; leaked {
		t.Error("crafted credentials_json survived onto the stored analysis LLM")
	}
	if saved.Embedding.Provider != "openai" || saved.Embedding.Model != "decisionbox-embed-model" {
		t.Errorf("embedding = %s/%s; want openai/decisionbox-embed-model", saved.Embedding.Provider, saved.Embedding.Model)
	}
	if _, leaked := saved.Embedding.Config["credentials_json"]; leaked {
		t.Error("crafted credentials_json survived onto the stored embedding")
	}
	if saved.BlurbLLM == nil || saved.BlurbLLM.Provider != "openai" || saved.BlurbLLM.Model != "decisionbox-blurb-model" {
		t.Errorf("blurb LLM = %+v; want openai/decisionbox-blurb-model", saved.BlurbLLM)
	}
}

// TestProjectsHandler_Update_ManagedOverride verifies a crafted PUT is
// neutralized: the persisted doc stays the gateway preset regardless of
// what the request body sets.
func TestProjectsHandler_Update_ManagedOverride(t *testing.T) {
	enableManagedForTest(t)
	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	// Seed a project directly (bypassing the handler) so we can prove the
	// Update path re-asserts the preset over a hostile body.
	p := &models.Project{Name: "Seed", Domain: "gaming", Category: "match3"}
	repo.Create(context.Background(), p)

	body := `{"llm":{"provider":"anthropic","model":"claude-x","config":{"credentials_json":"sk-attacker"}},
		"embedding":{"provider":"cohere","model":"embed-x"},
		"blurb_llm":{"provider":"google","model":"gemini-x"}}`
	req := httptest.NewRequest("PUT", "/api/v1/projects/"+p.ID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()

	h.Update(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	updated, _ := repo.GetByID(context.Background(), p.ID)
	if updated.LLM.Provider != "openai" || updated.LLM.Model != "decisionbox-analysis-model" {
		t.Errorf("analysis LLM = %s/%s; want the gateway preset", updated.LLM.Provider, updated.LLM.Model)
	}
	if _, leaked := updated.LLM.Config["credentials_json"]; leaked {
		t.Error("crafted credentials_json survived onto the updated analysis LLM")
	}
	if updated.Embedding.Provider != "openai" || updated.BlurbLLM == nil || updated.BlurbLLM.Provider != "openai" {
		t.Errorf("embedding/blurb not forced to gateway preset: emb=%s blurb=%+v", updated.Embedding.Provider, updated.BlurbLLM)
	}
}

// TestProjectsHandler_Create_UnmanagedHonorsBody is the off-by-default
// control: with managed mode off, the request body is honored unchanged.
func TestProjectsHandler_Create_UnmanagedHonorsBody(t *testing.T) {
	// Ensure a clean disabled state regardless of prior tests in this binary.
	t.Cleanup(managedai.Load)
	t.Setenv(managedai.EnvGatewayURL, "")
	managedai.Load()

	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)
	body := `{"name":"Self-hosted","domain":"gaming","category":"match3",
		"llm":{"provider":"anthropic","model":"claude"}}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	var saved *models.Project
	for _, p := range repo.projects {
		saved = p
	}
	if saved.LLM.Provider != "anthropic" || saved.LLM.Model != "claude" {
		t.Errorf("unmanaged create mutated LLM = %s/%s; want anthropic/claude", saved.LLM.Provider, saved.LLM.Model)
	}
}

// TestSecretsHandler_Set_ManagedGuard verifies AI-credential writes are
// refused (403) in managed mode and never reach the secret store, while
// a non-AI key still writes.
func TestSecretsHandler_Set_ManagedGuard(t *testing.T) {
	enableManagedForTest(t)
	repo := newMockProjectRepo()
	p := &models.Project{Name: "S", Domain: "gaming", Category: "match3"}
	repo.Create(context.Background(), p)
	sp := &recordingSecretProvider{}
	h := NewSecretsHandler(sp, repo)

	for _, key := range []string{"llm-credentials", "embedding-credentials", "blurb-llm-credentials"} {
		req := httptest.NewRequest("PUT", "/api/v1/projects/"+p.ID+"/secrets/"+key, strings.NewReader(`{"value":"sk-attacker"}`))
		req.SetPathValue("id", p.ID)
		req.SetPathValue("key", key)
		w := httptest.NewRecorder()
		h.Set(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("Set(%q) status = %d, want 403; body = %s", key, w.Code, w.Body.String())
		}
	}
	if len(sp.setKeys) != 0 {
		t.Errorf("secret store was written in managed mode: %v", sp.setKeys)
	}

	// A non-AI key is unaffected.
	req := httptest.NewRequest("PUT", "/api/v1/projects/"+p.ID+"/secrets/warehouse-credentials", strings.NewReader(`{"value":"wh"}`))
	req.SetPathValue("id", p.ID)
	req.SetPathValue("key", "warehouse-credentials")
	w := httptest.NewRecorder()
	h.Set(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("Set(warehouse-credentials) status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if len(sp.setKeys) != 1 || sp.setKeys[0] != "warehouse-credentials" {
		t.Errorf("warehouse-credentials should have been written once; got %v", sp.setKeys)
	}
}

func TestAppConfig(t *testing.T) {
	t.Run("managed", func(t *testing.T) {
		enableManagedForTest(t)
		req := httptest.NewRequest("GET", "/api/v1/config", nil)
		w := httptest.NewRecorder()
		AppConfig(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", cc)
		}
		var resp APIResponse
		json.NewDecoder(w.Body).Decode(&resp)
		data := resp.Data.(map[string]interface{})
		if data["ai_config_managed"] != true {
			t.Errorf("ai_config_managed = %v, want true", data["ai_config_managed"])
		}
	})

	t.Run("unmanaged", func(t *testing.T) {
		t.Cleanup(managedai.Load)
		t.Setenv(managedai.EnvGatewayURL, "")
		managedai.Load()
		req := httptest.NewRequest("GET", "/api/v1/config", nil)
		w := httptest.NewRecorder()
		AppConfig(w, req)

		var resp APIResponse
		json.NewDecoder(w.Body).Decode(&resp)
		data := resp.Data.(map[string]interface{})
		if data["ai_config_managed"] != false {
			t.Errorf("ai_config_managed = %v, want false", data["ai_config_managed"])
		}
	})
}
