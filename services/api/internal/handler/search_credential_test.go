package handler

import (
	"context"
	"strings"
	"testing"

	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
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

// TestAskLayerProvider_EnvFallback is the LLM counterpart: managed mode's RAG
// synthesis reaches the gateway via LLM_API_KEY. Since per-layer models (#407)
// Ask resolves its own layer, so the assertion is on the `ask` layer's spec —
// the default entry keeps the environment fallback that managed mode depends
// on.
func TestAskLayerProvider_EnvFallback(t *testing.T) {
	h := &SearchHandler{secretProvider: notFoundSecretProvider{}}
	project := &models.Project{LLM: models.LLMConfig{Provider: "openai", Model: "gpt-4o-mini"}}
	project.SeedLLMLayersFromLegacy()

	spec, err := project.LayerSpec(llmlayer.LayerAsk)
	if err != nil {
		t.Fatalf("resolve the ask layer: %v", err)
	}
	if spec.Model != "gpt-4o-mini" || spec.ProviderID != llmlayer.DefaultProviderID {
		t.Fatalf("ask layer resolved to %+v, want the default entry's model", spec)
	}

	t.Run("env fallback builds the provider", func(t *testing.T) {
		t.Setenv("LLM_API_KEY", "sk-env-llm")
		if _, _, err := llmlayer.BuildProvider(context.Background(), h.secretProvider, "proj-1", spec, llmlayer.BuildOptions{}); err != nil {
			t.Fatalf("expected provider built from env credential, got err: %v", err)
		}
	})

	t.Run("no secret and no env fails at the factory", func(t *testing.T) {
		t.Setenv("LLM_API_KEY", "")
		_, _, err := llmlayer.BuildProvider(context.Background(), h.secretProvider, "proj-1", spec, llmlayer.BuildOptions{})
		if err == nil || !strings.Contains(err.Error(), "API key is required") {
			t.Fatalf("expected 'API key is required', got: %v", err)
		}
	})
}

// A secondary provider entry must NOT borrow the default entry's environment
// fallback, or a secret-store blip could ship the primary's key to a
// third-party base_url. Asserted at the API too, because Ask runs here.
func TestAskLayerOnASecondaryEntryDoesNotBorrowTheDefaultsEnvKey(t *testing.T) {
	t.Setenv("LLM_API_KEY", "sk-primary")
	h := &SearchHandler{secretProvider: notFoundSecretProvider{}}
	project := &models.Project{
		LLMProviders: []llmlayer.ProviderEntry{
			{ID: llmlayer.DefaultProviderID, Provider: "openai"},
			{ID: "thirdparty", Provider: "openai", Config: map[string]string{"base_url": "https://not-ours.example"}},
		},
		LayerModels: map[string]llmlayer.LayerModel{
			llmlayer.DefaultLayer: {ProviderID: llmlayer.DefaultProviderID, Model: "gpt-4o-mini"},
			llmlayer.LayerAsk:     {ProviderID: "thirdparty", Model: "some-model"},
		},
		LLMConfigVersion: llmlayer.ConfigVersion,
	}

	spec, err := project.LayerSpec(llmlayer.LayerAsk)
	if err != nil {
		t.Fatalf("resolve the ask layer: %v", err)
	}
	cred, err := llmlayer.ResolveCredential(context.Background(), h.secretProvider, "proj-1", spec, llmlayer.BuildOptions{})
	if err != nil {
		t.Fatalf("ResolveCredential: %v", err)
	}
	if cred.Value != "" {
		t.Fatalf("the ask layer on a secondary entry resolved %q — the default entry's key must never reach a third-party base_url", cred.Value)
	}
}
