package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"go.mongodb.org/mongo-driver/bson"
)

// The replay judges with the project's own LLM, so it needs the project's
// API key the way the agent gets it. Without it every call to an API-key
// provider (LiteLLM, OpenAI-compatible hosts) failed with 401.
func TestReplayProviderConfigCarriesTheCredential(t *testing.T) {
	project := &ProjectDoc{}
	project.LLM.Provider = "litellm"
	project.LLM.Model = "some/model"
	project.LLM.Config = map[string]string{"base_url": "https://example.test/v1/openai"}

	spec, err := project.llmLayerConfig().SpecUnchecked(llmlayer.LayerValidationVerifier)
	if err != nil {
		t.Fatalf("resolve the verifier layer: %v", err)
	}
	cfg := llmlayer.ProviderConfig(spec, llmlayer.Credential{Value: "sk-test"}, llmlayer.BuildOptions{})
	if cfg["credentials_json"] != "sk-test" {
		t.Errorf("credentials_json = %q, want the resolved key", cfg["credentials_json"])
	}
	if cfg["base_url"] != "https://example.test/v1/openai" || cfg["model"] != "some/model" {
		t.Errorf("project settings lost: %v", cfg)
	}
	// An ambient-auth provider must not be handed an empty string that looks
	// like a key — the option that governs it is the caller's choice, so pin
	// the one the replay would use.
	empty := llmlayer.ProviderConfig(spec, llmlayer.Credential{}, llmlayer.BuildOptions{OmitEmptyCredential: true})
	if _, set := empty["credentials_json"]; set {
		t.Error("no credential resolved: the key must be absent, not empty")
	}
}

// The replay must resolve the SAME per-layer target the run did, which means
// its narrow project document has to decode the per-layer fields. A missing
// bson tag here would silently replay on the project's analysis model.
func TestReplayDecodesThePerLayerConfiguration(t *testing.T) {
	raw := bson.M{
		"llm": bson.M{"provider": "bedrock", "model": "opus"},
		"llm_providers": bson.A{
			bson.M{"id": "default", "provider": "bedrock"},
			bson.M{"id": "cheap", "provider": "litellm", "config": bson.M{"base_url": "http://cheap"}},
		},
		"layer_models": bson.M{
			"default":            bson.M{"provider_id": "default", "model": "opus"},
			"validation_refuter": bson.M{"provider_id": "cheap", "model": "haiku"},
		},
		"llm_config_version":   llmlayer.ConfigVersion,
		"reasoning_enabled":    true,
		"prompt_cache_enabled": false,
	}
	data, err := bson.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc ProjectDoc
	if err := bson.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	cfg := doc.llmLayerConfig()
	if !cfg.Configured() {
		t.Error("llm_config_version did not decode")
	}
	ver, err := cfg.Spec(llmlayer.LayerValidationVerifier)
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	ref, err := cfg.Spec(llmlayer.LayerValidationRefuter)
	if err != nil {
		t.Fatalf("refuter: %v", err)
	}
	if ver.Model != "opus" || ver.Provider != "bedrock" {
		t.Errorf("verifier resolved to %s/%s, want bedrock/opus", ver.Provider, ver.Model)
	}
	if ref.Model != "haiku" || ref.Provider != "litellm" {
		t.Errorf("refuter resolved to %s/%s, want litellm/haiku — the replay would otherwise judge with the wrong model", ref.Provider, ref.Model)
	}
	if ref.Target() == ver.Target() {
		t.Error("the two layers must be distinct targets so the replay opens a client for each")
	}
	if !ver.Reasoning {
		t.Error("reasoning_enabled did not reach the layer")
	}
	// Caching has always been opt-out, and the project field can turn it off
	// over a legacy "true". A replay that cached when the run did not would
	// not be reproducing the run.
	if ver.PromptCache {
		t.Error("prompt_cache_enabled=false did not reach the layer")
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

// Two validation layers on one connection that differ only by reasoning
// need two CLIENTS, because the client is what carries the setting.
//
// Target deliberately excludes reasoning — it is requested per call and
// does not change the connection — so sharing on Target alone made the
// refuter send the verifier's reasoning setting, and a replay that does not
// reproduce the run's requests is worse than no replay.
func TestSameReplayClientSeparatesLayersThatDifferOnlyByReasoning(t *testing.T) {
	base := llmlayer.LayerSpec{
		ProviderID: llmlayer.DefaultProviderID, Provider: "bedrock", Model: "opus",
		Config: map[string]string{"region": "us-east-1"},
	}
	thinking := base
	thinking.Reasoning = true

	if sameReplayClient(base, thinking) {
		t.Error("two layers with different reasoning settings shared one client")
	}
	// The connection itself is still shared: same target.
	if base.Target() != thinking.Target() {
		t.Error("reasoning changed the connection identity; it is a per-request setting")
	}
	// And identical layers still share, so an unmixed project opens one.
	same := base
	if !sameReplayClient(base, same) {
		t.Error("two identical layers were split into two clients")
	}
}

// Building a provider reads a credential and opens a connection, so a layer
// whose client is never called must not be built: a broken refuter override
// used to abort a verifier-only replay, and — until this pair was derived
// together — missing VERIFIER credentials aborted `--mode=refute` with a
// perfectly good refuter configured.
func TestReplayLayers_OnlyTheLayersThatWillBeCalled(t *testing.T) {
	for _, tc := range []struct {
		mode           string
		refuterEnabled bool
		verifier       bool
		refuter        bool
	}{
		{"both", true, true, true},
		{"both", false, true, false},
		{"verify", true, true, false},
		{"verify", false, true, false},
		{"refute", true, false, true},
		// Nothing to run: the report says so rather than the replay dying
		// on a credential for a layer it would never have called.
		{"refute", false, false, false},
	} {
		t.Run(fmt.Sprintf("%s/refuter=%v", tc.mode, tc.refuterEnabled), func(t *testing.T) {
			v, r := replayLayers(tc.mode, tc.refuterEnabled)
			if v != tc.verifier || r != tc.refuter {
				t.Errorf("replayLayers(%q, %v) = (%v, %v), want (%v, %v)",
					tc.mode, tc.refuterEnabled, v, r, tc.verifier, tc.refuter)
			}
		})
	}
}
