package handler

import (
	"context"
	"strings"
	"testing"

	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// notFoundSecretProvider models managed-inference gateway mode: no
// per-project AI secret is stored, so every Get is ErrNotFound.
type notFoundSecretProvider struct{}

func (notFoundSecretProvider) Get(_ context.Context, _, _ string) (string, error) {
	return "", gosecrets.ErrNotFound
}
func (notFoundSecretProvider) Set(_ context.Context, _, _, _ string) error { return nil }
func (notFoundSecretProvider) List(_ context.Context, _ string) ([]gosecrets.SecretEntry, error) {
	return nil, nil
}

// TestCreateEmbeddingProvider_EnvFallback pins the API-side search/ask
// resolution order (now via the shared gosecrets.ResolveCredential): with
// no per-project embedding secret, EMBEDDING_API_KEY is the fallback, so
// the provider builds; with neither, the openai factory fails as before.
func TestCreateEmbeddingProvider_EnvFallback(t *testing.T) {
	h := &SearchHandler{secretProvider: notFoundSecretProvider{}}

	t.Run("env fallback builds the provider", func(t *testing.T) {
		t.Setenv("EMBEDDING_API_KEY", "sk-env-embed")
		if _, err := h.createEmbeddingProvider(context.Background(), "openai", "text-embedding-3-small", "proj-1", nil); err != nil {
			t.Fatalf("expected provider built from env credential, got err: %v", err)
		}
	})

	t.Run("no secret and no env fails at the factory", func(t *testing.T) {
		t.Setenv("EMBEDDING_API_KEY", "")
		_, err := h.createEmbeddingProvider(context.Background(), "openai", "text-embedding-3-small", "proj-1", nil)
		if err == nil || !strings.Contains(err.Error(), "API key is required") {
			t.Fatalf("expected 'API key is required', got: %v", err)
		}
	})
}

// TestCreateLLMProvider_EnvFallback is the LLM-slot counterpart: managed
// mode's RAG synthesis reaches the gateway via LLM_API_KEY.
func TestCreateLLMProvider_EnvFallback(t *testing.T) {
	h := &SearchHandler{secretProvider: notFoundSecretProvider{}}
	project := &models.Project{LLM: models.LLMConfig{Provider: "openai", Model: "gpt-4o-mini"}}

	t.Run("env fallback builds the provider", func(t *testing.T) {
		t.Setenv("LLM_API_KEY", "sk-env-llm")
		if _, err := h.createLLMProvider(context.Background(), project, "proj-1"); err != nil {
			t.Fatalf("expected provider built from env credential, got err: %v", err)
		}
	})

	t.Run("no secret and no env fails at the factory", func(t *testing.T) {
		t.Setenv("LLM_API_KEY", "")
		_, err := h.createLLMProvider(context.Background(), project, "proj-1")
		if err == nil || !strings.Contains(err.Error(), "API key is required") {
			t.Fatalf("expected 'API key is required', got: %v", err)
		}
	})
}
