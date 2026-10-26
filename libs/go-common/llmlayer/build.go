package llmlayer

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
)

// EndpointIDConfigKey names a user-deployed inference endpoint in a provider
// entry's or a layer's config. An endpoint serves its own model, so a layer
// resolving to one may legitimately carry no model id — and must never be
// sent another layer's.
const EndpointIDConfigKey = "endpoint_id"

// LayerSpec is one layer's fully resolved LLM target: which provider entry,
// which model, and the merged configuration to build the provider from.
//
// Produced by models.Project.LayerSpec(layer) — the only place the
// legacy-versus-plural reading rules live — and consumed by BuildProvider.
// Everything on it is already resolved: Config is the provider entry's config
// with the layer's config merged over it, and PromptCache / Reasoning are the
// layer's effective values.
type LayerSpec struct {
	// Layer is the layer key this spec was resolved for. It selects the
	// credential chain for the table-description special case and is the tag
	// the agent stamps on the layer's ai.Client.
	Layer string
	// ProviderID is the resolved provider entry's id, normalised (never "").
	ProviderID string
	// Provider is the provider slug to pass to gollm.NewProvider
	// ("bedrock", "claude", "litellm", …).
	Provider string
	// Model is the model id to send on every request. May be empty only when
	// Config names an endpoint.
	Model string
	// Config is the provider-entry config with the layer's config merged
	// OVER it. Connection fields come from the entry; model-scoped fields
	// (token overrides, wire_override, endpoint_id, reasoning_enabled,
	// prompt_cache) come from either and the layer wins.
	Config map[string]string
	// PromptCache is the layer's effective prompt-caching setting. Pinned
	// into the provider config last, so a stale literal in either config map
	// loses to the setting the user chose for this layer.
	PromptCache bool
	// Reasoning is the layer's effective reasoning setting. Applied per
	// REQUEST by ai.Client.SetReasoning rather than at construction, so it
	// does not participate in provider dedupe.
	Reasoning bool
}

// HasEndpoint reports whether this spec points at a user-deployed endpoint,
// which serves its own model.
func (s LayerSpec) HasEndpoint() bool {
	return strings.TrimSpace(s.Config[EndpointIDConfigKey]) != ""
}

// Validate checks that the spec can actually build a provider. A spec with no
// provider slug, or with no model and no endpoint, is a misconfiguration the
// caller should report rather than send to a factory.
func (s LayerSpec) Validate() error {
	if strings.TrimSpace(s.Provider) == "" {
		return ErrNoProviderConfigured
	}
	if strings.TrimSpace(s.Model) == "" && !s.HasEndpoint() {
		return fmt.Errorf("%w (layer=%q)", ErrModelMissing, s.Layer)
	}
	return CheckLayerModel(s.Layer, s.Provider, s.Model)
}

// Target identifies the (provider entry, model) pair this spec resolves to.
// Two layers with the same Target share a model window, a budget resolution
// and a live model-info probe.
func (s LayerSpec) Target() string {
	return s.ProviderID + "/" + s.Provider + "/" + s.Model
}

// BuildOptions carries the deployment-level provider defaults and the
// caller's credential policy.
//
// The two policy flags are options rather than one flattened behaviour
// because the existing call sites genuinely differ: the agent logs an
// unexpected secret-store error and continues, fine-tuning evaluation treats
// it as fatal, and most enterprise blocks insert the credential only when it
// is non-empty. Collapsing those would change one caller's behaviour
// silently.
type BuildOptions struct {
	// MaxRetries, TimeoutSeconds and RequestDelayMs are the deployment
	// defaults (config.Config.LLM). They are written FIRST so a project's own
	// values override them — see ProviderConfig.
	MaxRetries     int
	TimeoutSeconds int
	RequestDelayMs int

	// FailOnSecretError makes an unexpected secret-store read failure fatal
	// instead of falling through to the environment fallback. Fine-tuning
	// evaluation sets it: running an evaluation against the wrong credential
	// is worse than not running it.
	FailOnSecretError bool

	// OmitEmptyCredential leaves credentials_json out of the config when no
	// credential resolved, instead of writing an empty string. Callers whose
	// providers authenticate ambiently (Bedrock IRSA, Vertex workload
	// identity) set it so an empty string is not mistaken for a key.
	OmitEmptyCredential bool

	// Logf, when set, receives one line per build describing the resolved
	// target and where its credential came from. Optional — the package does
	// not depend on a logger.
	Logf func(format string, args ...any)
}

// Credential is a resolved credential and the chain step it came from.
type Credential struct {
	// Value is the credential, empty when nothing resolved.
	Value string
	// SecretKey is the secret-store key that supplied it, empty when the
	// value came from the environment or nothing resolved.
	SecretKey string
	// Source is one of secrets.SourceDashboard, SourceEnv or SourceNone.
	Source string
}

// ResolveCredential walks a layer's credential chain and returns the first
// non-empty value.
//
// Each step is resolved through secrets.ResolveCredential, so the
// dashboard-secret-wins-then-environment order stays in the single place that
// owns it. The chain itself — which keys, in what order, for which entry — is
// CredentialChain's business, and it is deliberately narrow for non-reserved
// entries.
func ResolveCredential(ctx context.Context, secrets gosecrets.Provider, projectID string, spec LayerSpec, opts BuildOptions) (Credential, error) {
	var firstErr error
	for _, ref := range CredentialChain(spec.Layer, spec.ProviderID, spec.Config) {
		value, source, err := gosecrets.ResolveCredential(ctx, secrets, projectID, ref.SecretKey, ref.EnvVar)
		if err != nil {
			if opts.FailOnSecretError {
				return Credential{}, fmt.Errorf("read credential %q: %w", ref.SecretKey, err)
			}
			if firstErr == nil {
				firstErr = err
			}
		}
		if value != "" {
			return Credential{Value: value, SecretKey: ref.SecretKey, Source: source}, firstErr
		}
	}
	return Credential{Source: gosecrets.SourceNone}, firstErr
}

// ProviderConfig assembles the gollm.ProviderConfig for a layer.
//
// The merge order matters and is NOT "deployment defaults win":
//
//  1. deployment defaults (max_retries, timeout_seconds, request_delay_ms)
//     and the resolved credential + model,
//  2. the spec's Config — the provider entry's config with the layer's
//     merged over it,
//  3. the layer's resolved prompt_cache, pinned last.
//
// Step 2 overriding step 1 is deliberate and long-standing: a project that
// raised its own timeout_seconds for a slow on-prem model must keep it, which
// libs/go-common/llm/timeout.go documents as intentional. Step 3 is pinned
// after the merge so a stale literal in either config map loses to the
// setting the user actually chose for this layer.
func ProviderConfig(spec LayerSpec, cred Credential, opts BuildOptions) gollm.ProviderConfig {
	out := gollm.ProviderConfig{
		"model":            spec.Model,
		"max_retries":      strconv.Itoa(opts.MaxRetries),
		"timeout_seconds":  strconv.Itoa(opts.TimeoutSeconds),
		"request_delay_ms": strconv.Itoa(opts.RequestDelayMs),
	}
	if cred.Value != "" || !opts.OmitEmptyCredential {
		out["credentials_json"] = cred.Value
	}
	for k, v := range spec.Config {
		out[k] = v
	}
	out[gollm.PromptCacheKey] = strconv.FormatBool(spec.PromptCache)
	return out
}

// BuildProvider resolves a layer's credential, assembles its provider config
// and constructs the provider.
//
// Returns the config alongside the provider because callers need it: the
// agent derives its token budget from the same merged map (max_input_tokens /
// max_output_tokens overrides), and the dedupe key that keeps "every layer on
// the default" down to one provider instance is (provider entry, merged
// config).
func BuildProvider(ctx context.Context, secrets gosecrets.Provider, projectID string, spec LayerSpec, opts BuildOptions) (gollm.Provider, gollm.ProviderConfig, error) {
	if err := spec.Validate(); err != nil {
		return nil, nil, err
	}
	cred, err := ResolveCredential(ctx, secrets, projectID, spec, opts)
	if err != nil && opts.FailOnSecretError {
		return nil, nil, err
	}
	if err != nil && opts.Logf != nil {
		opts.Logf("llmlayer: layer=%s unexpected secret-store error, continuing with %s credential: %v", spec.Layer, cred.Source, err)
	}
	cfg := ProviderConfig(spec, cred, opts)
	provider, perr := gollm.NewProvider(spec.Provider, cfg)
	if perr != nil {
		return nil, nil, fmt.Errorf("build LLM provider for layer %q (%s): %w", spec.Layer, spec.Provider, perr)
	}
	if opts.Logf != nil {
		opts.Logf("llmlayer: layer=%s provider_id=%s provider=%s model=%s credential_src=%s prompt_cache=%t",
			spec.Layer, spec.ProviderID, spec.Provider, spec.Model, cred.Source, spec.PromptCache)
	}
	return provider, cfg, nil
}

// ConfigKey is the dedupe identity of a merged provider config: two specs
// with the same provider entry and the same ConfigKey can share ONE provider
// instance, because gollm providers treat their construction-time model as a
// fallback only and ai.Client sets ChatRequest.Model on every call.
//
// The model is deliberately NOT part of it. The credential is not either —
// it is derived from the entry, so two specs on one entry always resolve the
// same one.
func ConfigKey(cfg gollm.ProviderConfig) string {
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		if k == "model" {
			continue
		}
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(cfg[k])
		b.WriteByte('\x00')
	}
	return b.String()
}

