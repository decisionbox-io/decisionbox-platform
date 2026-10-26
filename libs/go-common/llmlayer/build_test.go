package llmlayer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
)

// --- fakes ---

// fakeSecrets is a map-backed secrets.Provider that can also be told to fail
// a specific key, so the "unexpected backend error" paths are testable.
type fakeSecrets struct {
	mu     sync.Mutex
	values map[string]string
	failOn map[string]error
	reads  []string
}

func newFakeSecrets(values map[string]string) *fakeSecrets {
	return &fakeSecrets{values: values, failOn: map[string]error{}}
}

func (f *fakeSecrets) Get(_ context.Context, _, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, key)
	if err, ok := f.failOn[key]; ok {
		return "", err
	}
	v, ok := f.values[key]
	if !ok {
		return "", gosecrets.ErrNotFound
	}
	return v, nil
}

func (f *fakeSecrets) Set(_ context.Context, _, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[key] = value
	return nil
}

func (f *fakeSecrets) List(_ context.Context, _ string) ([]gosecrets.SecretEntry, error) {
	return nil, nil
}

// recordingProvider captures the config it was constructed with, so the merge
// order is assertable.
type recordingProvider struct{ cfg gollm.ProviderConfig }

func (p *recordingProvider) Chat(context.Context, gollm.ChatRequest) (*gollm.ChatResponse, error) {
	return &gollm.ChatResponse{}, nil
}
func (p *recordingProvider) Validate(context.Context) error { return nil }

var (
	buildMu sync.Mutex
	lastCfg gollm.ProviderConfig
)

func init() {
	gollm.Register("llmlayer-test", func(cfg gollm.ProviderConfig) (gollm.Provider, error) {
		buildMu.Lock()
		defer buildMu.Unlock()
		lastCfg = cfg
		return &recordingProvider{cfg: cfg}, nil
	})
}

func buildOnce(t *testing.T, secrets gosecrets.Provider, spec LayerSpec, opts BuildOptions) gollm.ProviderConfig {
	t.Helper()
	buildMu.Lock()
	lastCfg = nil
	buildMu.Unlock()
	if _, _, err := BuildProvider(context.Background(), secrets, "proj1", spec, opts); err != nil {
		t.Fatalf("BuildProvider: %v", err)
	}
	buildMu.Lock()
	defer buildMu.Unlock()
	return lastCfg
}

func testSpec(layer, providerID string) LayerSpec {
	return LayerSpec{
		Layer:      layer,
		ProviderID: providerID,
		Provider:   "llmlayer-test",
		Model:      "m1",
		Config:     map[string]string{},
	}
}

// --- credential chain through the real resolver ---

func TestTableDescriptionsUsesTheStoredBlurbSecretWithNoBlurbOverride(t *testing.T) {
	// The exact case that would break on a real deployment: the project has
	// no blurb_llm block, so table descriptions resolve to the DEFAULT entry
	// — but a blurb-llm-credentials secret is stored and is what the schema
	// index is using today.
	secrets := newFakeSecrets(map[string]string{
		BlurbCredentialsKey:  "blurb-key",
		LegacyCredentialsKey: "analysis-key",
	})
	cred, err := ResolveCredential(context.Background(), secrets, "proj1", testSpec(LayerTableDescriptions, DefaultProviderID), BuildOptions{})
	if err != nil {
		t.Fatalf("ResolveCredential: %v", err)
	}
	if cred.Value != "blurb-key" {
		t.Errorf("credential = %q, want the stored blurb secret", cred.Value)
	}
	if cred.SecretKey != BlurbCredentialsKey {
		t.Errorf("credential came from %q, want %q", cred.SecretKey, BlurbCredentialsKey)
	}
}

func TestTableDescriptionsFallsThroughToTheAnalysisCredential(t *testing.T) {
	secrets := newFakeSecrets(map[string]string{LegacyCredentialsKey: "analysis-key"})
	cred, err := ResolveCredential(context.Background(), secrets, "proj1", testSpec(LayerTableDescriptions, DefaultProviderID), BuildOptions{})
	if err != nil {
		t.Fatalf("ResolveCredential: %v", err)
	}
	if cred.Value != "analysis-key" {
		t.Errorf("credential = %q, want the analysis key via the chain's second step", cred.Value)
	}
}

func TestOrdinaryLayerNeverReadsTheBlurbSecret(t *testing.T) {
	secrets := newFakeSecrets(map[string]string{
		BlurbCredentialsKey:  "blurb-key",
		LegacyCredentialsKey: "analysis-key",
	})
	cred, err := ResolveCredential(context.Background(), secrets, "proj1", testSpec(LayerAnalysis, DefaultProviderID), BuildOptions{})
	if err != nil {
		t.Fatalf("ResolveCredential: %v", err)
	}
	if cred.Value != "analysis-key" {
		t.Errorf("credential = %q, want the analysis key", cred.Value)
	}
	for _, k := range secrets.reads {
		if k == BlurbCredentialsKey {
			t.Error("the analysis layer read blurb-llm-credentials")
		}
	}
}

func TestSecondaryEntryDoesNotBorrowTheDefaultsEnvFallback(t *testing.T) {
	t.Setenv(LegacyCredentialsEnv, "primary-env-key")
	secrets := newFakeSecrets(map[string]string{LegacyCredentialsKey: "analysis-key"})
	spec := testSpec(LayerExploration, "deepinfra")

	cred, err := ResolveCredential(context.Background(), secrets, "proj1", spec, BuildOptions{})
	if err != nil {
		t.Fatalf("ResolveCredential: %v", err)
	}
	if cred.Value != "" {
		t.Fatalf("secondary entry resolved %q; it must never reach the default entry's secret or LLM_API_KEY — an OpenAI-compatible entry sends whatever it is handed as a bearer token to its own base_url", cred.Value)
	}
	if cred.Source != gosecrets.SourceNone {
		t.Errorf("credential source = %q, want %q", cred.Source, gosecrets.SourceNone)
	}
}

func TestSecondaryEntryDoesNotBorrowTheDefaultsKeyOnABackendError(t *testing.T) {
	// ResolveCredential falls back to the env var when the secret is missing,
	// empty OR the backend read FAILS. That is the blip that would otherwise
	// ship the primary's key to a third-party endpoint.
	t.Setenv(LegacyCredentialsEnv, "primary-env-key")
	secrets := newFakeSecrets(map[string]string{})
	secrets.failOn[CredentialsKey("deepinfra")] = errors.New("secret store unavailable")

	cred, err := ResolveCredential(context.Background(), secrets, "proj1", testSpec(LayerExploration, "deepinfra"), BuildOptions{})
	if err == nil {
		t.Error("the backend error must be reported to the caller so it can be logged")
	}
	if cred.Value != "" {
		t.Fatalf("secondary entry resolved %q through a backend error", cred.Value)
	}
}

func TestSecondaryEntryUsesTheEnvVarItNames(t *testing.T) {
	t.Setenv("DBX_LLM_DEEPINFRA_KEY", "named-env-key")
	spec := testSpec(LayerExploration, "deepinfra")
	spec.Config[EnvVarConfigKey] = "DBX_LLM_DEEPINFRA_KEY"

	cred, err := ResolveCredential(context.Background(), newFakeSecrets(map[string]string{}), "proj1", spec, BuildOptions{})
	if err != nil {
		t.Fatalf("ResolveCredential: %v", err)
	}
	if cred.Value != "named-env-key" {
		t.Errorf("credential = %q, want the explicitly named env var's value", cred.Value)
	}
}

func TestFailOnSecretErrorIsFatalForCallersThatAskForIt(t *testing.T) {
	secrets := newFakeSecrets(map[string]string{})
	secrets.failOn[LegacyCredentialsKey] = errors.New("secret store unavailable")
	t.Setenv(LegacyCredentialsEnv, "env-key")

	// The agent's policy: log and continue on the env fallback.
	cred, err := ResolveCredential(context.Background(), secrets, "proj1", testSpec(LayerAnalysis, DefaultProviderID), BuildOptions{})
	if err == nil {
		t.Error("the error must still reach the caller for logging")
	}
	if cred.Value != "env-key" {
		t.Errorf("default entry credential = %q, want the env fallback (the agent continues through a blip)", cred.Value)
	}

	// Fine-tuning evaluation's policy: running against the wrong credential
	// is worse than not running.
	if _, err := ResolveCredential(context.Background(), secrets, "proj1", testSpec(LayerAnalysis, DefaultProviderID), BuildOptions{FailOnSecretError: true}); err == nil {
		t.Error("FailOnSecretError must make the read failure fatal")
	}
}

// --- provider config assembly ---

func TestProjectConfigWinsOverDeploymentDefaults(t *testing.T) {
	// initLLMProvider seeds the deployment defaults and THEN merges the
	// project's config over them, which libs/go-common/llm/timeout.go
	// documents as intentional: a long-running on-prem project raised its own
	// timeout and must keep it.
	spec := testSpec(LayerAnalysis, DefaultProviderID)
	spec.Config["timeout_seconds"] = "3600"
	spec.Config["max_retries"] = "9"
	spec.Config["region"] = "eu-central-1"

	cfg := buildOnce(t, newFakeSecrets(map[string]string{LegacyCredentialsKey: "k"}), spec,
		BuildOptions{MaxRetries: 3, TimeoutSeconds: 300, RequestDelayMs: 50})

	if cfg["timeout_seconds"] != "3600" {
		t.Errorf("timeout_seconds = %q, want the project's 3600 — the deployment default must not win", cfg["timeout_seconds"])
	}
	if cfg["max_retries"] != "9" {
		t.Errorf("max_retries = %q, want the project's 9", cfg["max_retries"])
	}
	if cfg["request_delay_ms"] != "50" {
		t.Errorf("request_delay_ms = %q, want the deployment default 50 (the project set none)", cfg["request_delay_ms"])
	}
	if cfg["region"] != "eu-central-1" {
		t.Errorf("region = %q, want the entry's connection field to reach the factory", cfg["region"])
	}
	if cfg["credentials_json"] != "k" {
		t.Errorf("credentials_json = %q, want the resolved credential", cfg["credentials_json"])
	}
	if cfg["model"] != "m1" {
		t.Errorf("model = %q, want m1", cfg["model"])
	}
}

// A `model` or `credentials_json` entry inside a config map is a stray, and
// letting one win would send a model the user did not choose, or a credential
// out of the project document instead of the secret store.
func TestResolvedModelAndCredentialBeatAStrayConfigEntry(t *testing.T) {
	spec := testSpec(LayerAnalysis, DefaultProviderID)
	spec.Config["model"] = "WRONG-MODEL"
	spec.Config["credentials_json"] = "WRONG-CREDS"

	cfg := ProviderConfig(spec, Credential{Value: "real-key"}, BuildOptions{})
	if cfg["model"] != "m1" {
		t.Errorf("model = %q, want the layer's resolved model", cfg["model"])
	}
	if cfg["credentials_json"] != "real-key" {
		t.Errorf("credentials_json = %q, want the resolved credential", cfg["credentials_json"])
	}

	// And with nothing resolved, OmitEmptyCredential must drop the stray
	// rather than ship it as a bearer token.
	cfg = ProviderConfig(spec, Credential{}, BuildOptions{OmitEmptyCredential: true})
	if v, ok := cfg["credentials_json"]; ok {
		t.Errorf("credentials_json = %q, want the stray config entry dropped", v)
	}
}

func TestResolvedPromptCacheBeatsAStaleLiteralInEitherConfigMap(t *testing.T) {
	spec := testSpec(LayerAnalysis, DefaultProviderID)
	spec.Config[gollm.PromptCacheKey] = "false" // a stale literal from before the toggle moved
	spec.PromptCache = true

	cfg := buildOnce(t, newFakeSecrets(map[string]string{LegacyCredentialsKey: "k"}), spec, BuildOptions{})
	if cfg[gollm.PromptCacheKey] != "true" {
		t.Errorf("%s = %q, want \"true\" — the layer's resolved setting is pinned after the merge", gollm.PromptCacheKey, cfg[gollm.PromptCacheKey])
	}
	if gollm.PromptCacheEnabled(cfg) != true {
		t.Error("the providers read PromptCacheKey through gollm.PromptCacheEnabled; it must agree")
	}

	spec.PromptCache = false
	spec.Config[gollm.PromptCacheKey] = "true"
	cfg = buildOnce(t, newFakeSecrets(map[string]string{LegacyCredentialsKey: "k"}), spec, BuildOptions{})
	if gollm.PromptCacheEnabled(cfg) != false {
		t.Error("a layer with caching off must construct with caching off even when a config map says otherwise")
	}
}

func TestUnsetDeploymentDefaultsAreOmittedNotWrittenAsZero(t *testing.T) {
	// A caller with no deployment config — an enterprise plugin, the replay
	// tool — must leave these keys absent, as it does today. "0" would mean
	// zero retries and a zero-second timeout, not "use the provider default".
	cfg := ProviderConfig(testSpec(LayerPackGen, DefaultProviderID), Credential{Value: "k"}, BuildOptions{})
	for _, key := range []string{"max_retries", "timeout_seconds", "request_delay_ms"} {
		if v, ok := cfg[key]; ok {
			t.Errorf("%s = %q, want the key absent", key, v)
		}
	}
	// And a set value is still written.
	cfg = ProviderConfig(testSpec(LayerPackGen, DefaultProviderID), Credential{Value: "k"}, BuildOptions{MaxRetries: 3})
	if cfg["max_retries"] != "3" {
		t.Errorf("max_retries = %q, want 3", cfg["max_retries"])
	}
}

func TestOmitEmptyCredentialLeavesTheKeyOutEntirely(t *testing.T) {
	spec := testSpec(LayerAnalysis, DefaultProviderID)
	secrets := newFakeSecrets(map[string]string{})

	cfg := buildOnce(t, secrets, spec, BuildOptions{})
	if _, ok := cfg["credentials_json"]; !ok {
		t.Error("the default policy writes credentials_json even when empty, matching initLLMProvider today")
	}

	cfg = buildOnce(t, secrets, spec, BuildOptions{OmitEmptyCredential: true})
	if _, ok := cfg["credentials_json"]; ok {
		t.Error("OmitEmptyCredential must leave credentials_json out so an empty string is not mistaken for a key by an ambient-auth provider")
	}
}

// --- spec validation ---

func TestSpecValidateRejectsWhatCannotBuild(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*LayerSpec)
		wantErr error
	}{
		{"no provider slug", func(s *LayerSpec) { s.Provider = "" }, ErrNoProviderConfigured},
		{"no model and no endpoint", func(s *LayerSpec) { s.Model = "" }, ErrModelMissing},
		{"reasoning model on table descriptions", func(s *LayerSpec) {
			s.Layer = LayerTableDescriptions
			s.Model = "gpt-5"
		}, ErrReasoningModelNotSupported},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := testSpec(LayerAnalysis, DefaultProviderID)
			c.mutate(&spec)
			err := spec.Validate()
			if !errors.Is(err, c.wantErr) {
				t.Errorf("Validate() = %v, want %v", err, c.wantErr)
			}
		})
	}
}

func TestEndpointBackedSpecMayCarryNoModel(t *testing.T) {
	// A user-deployed endpoint serves its own model and must never be sent
	// another layer's.
	spec := testSpec(LayerTableDescriptions, DefaultProviderID)
	spec.Model = ""
	spec.Config[EndpointIDConfigKey] = "ep-123"
	if !spec.HasEndpoint() {
		t.Fatal("HasEndpoint() = false with endpoint_id set")
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil for an endpoint-backed spec with no model", err)
	}

	spec.Config[EndpointIDConfigKey] = "   "
	if spec.HasEndpoint() {
		t.Error("a whitespace-only endpoint_id is not an endpoint")
	}
	if !errors.Is(spec.Validate(), ErrModelMissing) {
		t.Error("a whitespace-only endpoint_id with no model must be rejected")
	}
}

// --- dedupe identity ---

func TestConfigKeyIgnoresTheModelSoOneProviderServesEveryModelOnAnEntry(t *testing.T) {
	// ai.Client sets ChatRequest.Model on every call and every provider treats
	// its construction-time model as a fallback only, so two layers on one
	// entry with different models share ONE provider instance.
	a := gollm.ProviderConfig{"model": "opus", "region": "us-east-1"}
	b := gollm.ProviderConfig{"model": "haiku", "region": "us-east-1"}
	if ConfigKey(a) != ConfigKey(b) {
		t.Error("ConfigKey must not depend on the model, or every layer with its own model buys its own HTTP client")
	}

	c := gollm.ProviderConfig{"model": "opus", "region": "eu-west-1"}
	if ConfigKey(a) == ConfigKey(c) {
		t.Error("ConfigKey must distinguish differing connection config")
	}
}

func TestConfigKeyDistinguishesPromptCacheBecauseItIsReadAtConstruction(t *testing.T) {
	// This is the one difference from per-layer reasoning: prompt caching is
	// read when the provider is BUILT (the four Anthropic-wire providers), so
	// two layers on the same entry with different caching settings are two
	// provider instances. Pinned here so it is a known cost, not a surprise.
	on := gollm.ProviderConfig{"region": "us-east-1", gollm.PromptCacheKey: "true"}
	off := gollm.ProviderConfig{"region": "us-east-1", gollm.PromptCacheKey: "false"}
	if ConfigKey(on) == ConfigKey(off) {
		t.Error("ConfigKey must distinguish prompt_cache, or a layer with caching off would silently share a cached provider")
	}
}

func TestConfigKeyIsOrderIndependent(t *testing.T) {
	a := gollm.ProviderConfig{"a": "1", "b": "2", "c": "3"}
	b := gollm.ProviderConfig{"c": "3", "b": "2", "a": "1"}
	if ConfigKey(a) != ConfigKey(b) {
		t.Error("ConfigKey must not depend on map iteration order")
	}
}

func TestTargetDistinguishesEntriesSharingAModelAlias(t *testing.T) {
	// Two LiteLLM entries pointing at different deployments can name the same
	// model alias; their calibrated windows must not contaminate each other.
	a := LayerSpec{ProviderID: "deploy-a", Provider: "litellm", Model: "qwen3-32b"}
	b := LayerSpec{ProviderID: "deploy-b", Provider: "litellm", Model: "qwen3-32b"}
	if a.Target() == b.Target() {
		t.Error("Target() must include the provider entry id")
	}
}

// Target is used to decide when two layers may SHARE a connection, so a
// difference that changes the connection has to show in it. Prompt caching is
// the sharpest case: it is applied when the provider is constructed, not per
// request, so two layers that disagree about it cannot be one client.
func TestTargetDistinguishesLayersThatDisagreeAboutTheConnection(t *testing.T) {
	base := LayerSpec{ProviderID: DefaultProviderID, Provider: "litellm", Model: "qwen3-32b"}
	cases := []struct {
		name string
		cfg  map[string]string
	}{
		{"a different endpoint", map[string]string{"endpoint_id": "ep-2"}},
		{"a wire override", map[string]string{"wire_override": "anthropic-messages"}},
		{"prompt caching off", map[string]string{gollm.PromptCacheKey: "false"}},
		{"a tighter output cap", map[string]string{"max_output_tokens": "1024"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			other := base
			other.Config = c.cfg
			if base.Target() == other.Target() {
				t.Errorf("Target() collapsed two layers differing by %v", c.cfg)
			}
		})
	}
	// And identical configuration still collapses, or every layer on one
	// model would open its own connection and pay for its own probe.
	same := base
	same.Config = map[string]string{"base_url": "https://x/v1"}
	alsoSame := base
	alsoSame.Config = map[string]string{"base_url": "https://x/v1"}
	if same.Target() != alsoSame.Target() {
		t.Error("Target() must still collapse two layers with the same entry, model and configuration")
	}
}

func TestBuildProviderReportsAnUnknownProviderSlugWithTheLayerNamed(t *testing.T) {
	spec := testSpec(LayerAnalysis, DefaultProviderID)
	spec.Provider = "no-such-provider"
	_, _, err := BuildProvider(context.Background(), newFakeSecrets(map[string]string{}), "proj1", spec, BuildOptions{})
	if err == nil {
		t.Fatal("BuildProvider accepted an unregistered provider slug")
	}
	if !strings.Contains(err.Error(), LayerAnalysis) {
		t.Errorf("error %q does not name the layer; an operator needs to know WHICH layer is misconfigured", err)
	}
}

func TestIsNotConfigured(t *testing.T) {
	if !IsNotConfigured(ErrLLMNotConfigured) {
		t.Error("IsNotConfigured must match the sentinel")
	}
	if !IsNotConfigured(fmt.Errorf("resolve ask layer: %w", ErrLLMNotConfigured)) {
		t.Error("IsNotConfigured must match a wrapped sentinel — every entry point wraps it with its own context")
	}
	if IsNotConfigured(ErrModelMissing) {
		t.Error("IsNotConfigured must not match an unrelated error")
	}
}

func TestAnUnsanctionedEnvVarIsNotResolvedEvenWhenItExists(t *testing.T) {
	// The exfiltration path, end to end: a project member names a variable
	// the pod really does carry and points the entry at their own endpoint.
	// ResolveCredential must come back empty.
	t.Setenv("MONGODB_URI", "mongodb://user:password@cluster/db")
	spec := testSpec(LayerAnalysis, "exfil")
	spec.Config[EnvVarConfigKey] = "MONGODB_URI"
	spec.Config["base_url"] = "https://attacker.example/v1"

	cred, err := ResolveCredential(context.Background(), newFakeSecrets(map[string]string{}), "proj1", spec, BuildOptions{})
	if err != nil {
		t.Fatalf("ResolveCredential: %v", err)
	}
	if cred.Value != "" {
		t.Fatalf("resolved %q from a variable the project document named; the deployment's own environment must be unreachable", cred.Value)
	}
}
