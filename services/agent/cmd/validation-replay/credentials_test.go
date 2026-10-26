package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
)

// The replay judges with the project's own LLM, so it needs the project's
// API key the way the agent gets it. Without it every call to an API-key
// provider (LiteLLM, OpenAI-compatible hosts) failed with 401.
func TestLLMProviderConfig_CarriesTheCredential(t *testing.T) {
	project := &ProjectDoc{}
	project.LLM.Provider = "litellm"
	project.LLM.Model = "some/model"
	project.LLM.Config = map[string]string{"base_url": "https://example.test/v1/openai"}

	cfg := llmProviderConfig(project, cliFlags{}, "sk-test")
	if cfg["credentials_json"] != "sk-test" {
		t.Errorf("credentials_json = %q, want the resolved key", cfg["credentials_json"])
	}
	if cfg["base_url"] != "https://example.test/v1/openai" || cfg["model"] != "some/model" {
		t.Errorf("project settings lost: %v", cfg)
	}
	if _, set := llmProviderConfig(project, cliFlags{}, "")["credentials_json"]; set {
		t.Error("no credential resolved: the key must be absent, not empty")
	}
}

// The agent reads secrets from whichever store SECRET_PROVIDER names; the
// replay links the same stores, so a cloud-stored key resolves too.
func TestSecretProvidersRegistered(t *testing.T) {
	got := gosecrets.RegisteredProviders()
	for _, want := range []string{"aws", "azure", "gcp"} {
		if !slices.Contains(got, want) {
			t.Errorf("secret provider %q not linked into the replay (registered: %v)", want, got)
		}
	}
}

type stubProvider struct{ err error }

func (s stubProvider) Chat(context.Context, gollm.ChatRequest) (*gollm.ChatResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &gollm.ChatResponse{Content: "{}"}, nil // no usage reported, as some proxies do
}
func (stubProvider) Validate(context.Context) error { return nil }

// A replay whose every LLM call failed produced no verdicts, only
// "unverifiable" placeholders; it must not pass. A successful call counts
// even when the upstream reports no token usage.
func TestCountingProvider(t *testing.T) {
	failing := &countingProvider{Provider: stubProvider{err: errors.New("401 missing API key")}}
	_, _ = failing.Chat(context.Background(), gollm.ChatRequest{})
	_, _ = failing.Chat(context.Background(), gollm.ChatRequest{})
	if !failing.everyCallFailed() {
		t.Error("two failed calls: want everyCallFailed")
	}
	working := &countingProvider{Provider: stubProvider{}}
	_, _ = working.Chat(context.Background(), gollm.ChatRequest{})
	if working.everyCallFailed() {
		t.Error("a successful call without usage counts as success")
	}
	if (&countingProvider{Provider: stubProvider{}}).everyCallFailed() {
		t.Error("no calls made: not a failure")
	}
}
