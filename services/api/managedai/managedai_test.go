package managedai

import (
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// setManaged wires a full, valid managed-mode environment for a test and
// installs it. t.Setenv restores the previous value (and unset-ness) at
// test end, so each test starts from a clean slate.
func setManaged(t *testing.T) {
	t.Helper()
	t.Setenv(EnvGatewayURL, "https://ai.example.com/v1")
	t.Setenv(EnvAnalysisModel, "decisionbox-analysis-model")
	t.Setenv(EnvBlurbModel, "decisionbox-blurb-model")
	t.Setenv(EnvEmbedModel, "decisionbox-embed-model")
	t.Setenv(envLLMAPIKey, "dbx-live-abc")
	t.Setenv(envEmbeddingAPIKey, "dbx-live-abc")
	Load()
}

func TestDisabledByDefault(t *testing.T) {
	// No AI_GATEWAY_URL in the environment.
	t.Setenv(EnvGatewayURL, "")
	Load()

	if Enabled() {
		t.Fatal("Enabled() = true with AI_GATEWAY_URL unset; want false")
	}
	if err := Validate(); err != nil {
		t.Fatalf("Validate() = %v with managed mode off; want nil", err)
	}
	if IsManagedSecretKey("llm-credentials") {
		t.Fatal("IsManagedSecretKey should be false when managed mode is off")
	}
}

func TestApplyNoOpWhenDisabled(t *testing.T) {
	t.Setenv(EnvGatewayURL, "")
	Load()

	p := &models.Project{
		LLM: models.LLMConfig{Provider: "anthropic", Model: "claude", Config: map[string]string{"x": "y"}},
	}
	Apply(p)

	if p.LLM.Provider != "anthropic" || p.LLM.Model != "claude" {
		t.Fatalf("Apply mutated project with managed mode off: %+v", p.LLM)
	}
}

func TestApplyOverridesAllThreeRoles(t *testing.T) {
	setManaged(t)

	// A hostile request body: different providers, and — critically — a
	// crafted credentials_json/base_url smuggled into Config that must NOT
	// survive the whole-object replacement.
	p := &models.Project{
		LLM: models.LLMConfig{
			Provider: "anthropic",
			Model:    "claude-attacker",
			Config:   map[string]string{"credentials_json": "sk-attacker", "base_url": "https://evil.example.com"},
		},
		BlurbLLM: &models.BlurbLLMConfig{
			Provider: "google",
			Model:    "gemini-attacker",
			Config:   map[string]string{"credentials_json": "sk-attacker"},
		},
	}
	p.Embedding.Provider = "cohere"
	p.Embedding.Model = "embed-attacker"
	p.Embedding.Config = map[string]string{"credentials_json": "sk-attacker"}

	Apply(p)

	if p.LLM.Provider != "openai" || p.LLM.Model != "decisionbox-analysis-model" {
		t.Errorf("analysis LLM = %+v; want openai/decisionbox-analysis-model", p.LLM)
	}
	if got := p.LLM.Config["base_url"]; got != "https://ai.example.com/v1" {
		t.Errorf("analysis base_url = %q; want the gateway URL", got)
	}
	if _, leaked := p.LLM.Config["credentials_json"]; leaked {
		t.Error("crafted credentials_json survived Apply on the analysis LLM")
	}

	if p.BlurbLLM == nil || p.BlurbLLM.Provider != "openai" || p.BlurbLLM.Model != "decisionbox-blurb-model" {
		t.Errorf("blurb LLM = %+v; want openai/decisionbox-blurb-model", p.BlurbLLM)
	}
	if _, leaked := p.BlurbLLM.Config["credentials_json"]; leaked {
		t.Error("crafted credentials_json survived Apply on the blurb LLM")
	}

	if p.Embedding.Provider != "openai" || p.Embedding.Model != "decisionbox-embed-model" {
		t.Errorf("embedding = %+v; want openai/decisionbox-embed-model", p.Embedding)
	}
	if _, leaked := p.Embedding.Config["credentials_json"]; leaked {
		t.Error("crafted credentials_json survived Apply on the embedding")
	}
	// No dimensions env set ⇒ let the agent probe; no dimensions key.
	if _, ok := p.Embedding.Config["dimensions"]; ok {
		t.Error("dimensions should be absent when AI_GATEWAY_EMBED_DIMENSIONS is unset")
	}
}

func TestApplyEmbedDimensionsFastPath(t *testing.T) {
	setManaged(t)
	t.Setenv(EnvEmbedDimensions, "3072")
	Load()

	p := &models.Project{}
	Apply(p)

	if got := p.Embedding.Config["dimensions"]; got != "3072" {
		t.Errorf("embedding dimensions = %q; want 3072", got)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	setManaged(t)
	p := &models.Project{}
	Apply(p)
	firstProvider, firstModel, firstBase := p.LLM.Provider, p.LLM.Model, p.LLM.Config["base_url"]
	Apply(p)
	if p.LLM.Provider != firstProvider || p.LLM.Model != firstModel || p.LLM.Config["base_url"] != firstBase {
		t.Errorf("Apply not idempotent: %s/%s/%s then %s/%s/%s",
			firstProvider, firstModel, firstBase, p.LLM.Provider, p.LLM.Model, p.LLM.Config["base_url"])
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr bool
	}{
		{
			name: "valid",
			env: map[string]string{
				EnvGatewayURL: "https://ai.example.com/v1", EnvAnalysisModel: "a",
				EnvBlurbModel: "b", EnvEmbedModel: "e",
				envLLMAPIKey: "k", envEmbeddingAPIKey: "k",
			},
			wantErr: false,
		},
		{
			name: "missing analysis alias",
			env: map[string]string{
				EnvGatewayURL: "https://ai.example.com/v1", EnvBlurbModel: "b",
				EnvEmbedModel: "e", envLLMAPIKey: "k", envEmbeddingAPIKey: "k",
			},
			wantErr: true,
		},
		{
			name: "missing llm api key",
			env: map[string]string{
				EnvGatewayURL: "https://ai.example.com/v1", EnvAnalysisModel: "a",
				EnvBlurbModel: "b", EnvEmbedModel: "e", envEmbeddingAPIKey: "k",
			},
			wantErr: true,
		},
		{
			name: "url missing /v1 path",
			env: map[string]string{
				EnvGatewayURL: "https://ai.example.com", EnvAnalysisModel: "a",
				EnvBlurbModel: "b", EnvEmbedModel: "e",
				envLLMAPIKey: "k", envEmbeddingAPIKey: "k",
			},
			wantErr: true,
		},
		{
			name: "url not absolute",
			env: map[string]string{
				EnvGatewayURL: "ai.example.com/v1", EnvAnalysisModel: "a",
				EnvBlurbModel: "b", EnvEmbedModel: "e",
				envLLMAPIKey: "k", envEmbeddingAPIKey: "k",
			},
			wantErr: true,
		},
		{
			name: "url wrong scheme",
			env: map[string]string{
				EnvGatewayURL: "ftp://ai.example.com/v1", EnvAnalysisModel: "a",
				EnvBlurbModel: "b", EnvEmbedModel: "e",
				envLLMAPIKey: "k", envEmbeddingAPIKey: "k",
			},
			wantErr: true,
		},
		{
			name: "bad dimensions",
			env: map[string]string{
				EnvGatewayURL: "https://ai.example.com/v1", EnvAnalysisModel: "a",
				EnvBlurbModel: "b", EnvEmbedModel: "e", EnvEmbedDimensions: "-1",
				envLLMAPIKey: "k", envEmbeddingAPIKey: "k",
			},
			wantErr: true,
		},
	}

	// Every var this package reads — cleared per-case, then set from the
	// case map, so a case that omits a var truly tests its absence.
	allVars := []string{
		EnvGatewayURL, EnvAnalysisModel, EnvBlurbModel, EnvEmbedModel,
		EnvEmbedDimensions, envLLMAPIKey, envEmbeddingAPIKey,
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, v := range allVars {
				t.Setenv(v, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			Load()
			err := Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() err = %v; wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestURLTrailingSlashNormalized(t *testing.T) {
	setManaged(t)
	t.Setenv(EnvGatewayURL, "https://ai.example.com/v1/")
	Load()

	if err := Validate(); err != nil {
		t.Fatalf("Validate() = %v; want nil (trailing slash should normalize)", err)
	}
	p := &models.Project{}
	Apply(p)
	if got := p.LLM.Config["base_url"]; got != "https://ai.example.com/v1" {
		t.Errorf("base_url = %q; want trailing slash trimmed", got)
	}
}

func TestIsManagedSecretKey(t *testing.T) {
	setManaged(t)

	managed := []string{"llm-credentials", "embedding-credentials", "blurb-llm-credentials"}
	for _, k := range managed {
		if !IsManagedSecretKey(k) {
			t.Errorf("IsManagedSecretKey(%q) = false; want true", k)
		}
	}
	for _, k := range []string{"warehouse-credentials", "random-key", ""} {
		if IsManagedSecretKey(k) {
			t.Errorf("IsManagedSecretKey(%q) = true; want false", k)
		}
	}
}
