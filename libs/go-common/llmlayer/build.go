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
	// FellBackFromProviderID is set when the layer named a provider entry the
	// project does not have and resolution fell back to the default. Empty
	// normally. Callers log it; the API refuses the delete that would cause
	// it, so a non-empty value means the document was edited outside the
	// handler.
	FellBackFromProviderID string
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

// Target identifies the deployment this spec resolves to: the provider entry,
// the model, AND the merged configuration. Two layers with the same Target
// share a connection, a model window, a budget resolution and a live
// model-info probe.
//
// The configuration is part of the identity, not decoration. Two layers can
// name the same entry and model while differing in endpoint_id,
// wire_override, a token cap or prompt caching — and prompt caching in
// particular is applied when the connection is BUILT, so collapsing those
// would replay one layer's calls against another layer's endpoint and
// settings. It is the same key agentserver builds a provider under.
func (s LayerSpec) Target() string {
	return s.ProviderID + "/" + s.Provider + "/" + s.Model + "/" + ConfigKey(gollm.ProviderConfig(s.Config))
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
// The merge order matters and is NOT simply "deployment defaults win":
//
//  1. the deployment defaults (max_retries, timeout_seconds,
//     request_delay_ms), each written only when it is set,
//  2. the spec's Config — the provider entry's config with the layer's
//     merged over it,
//  3. the RESOLVED values, pinned last: model, credentials_json and
//     prompt_cache.
//
// Step 2 overriding step 1 is deliberate and long-standing: a project that
// raised its own timeout_seconds for a slow on-prem model must keep it, which
// libs/go-common/llm/timeout.go documents as intentional.
//
// Step 3 is pinned AFTER the merge because those three are resolved, not
// configured. A `model` or `credentials_json` entry inside a config map is a
// stray — the API strips credential keys on write, and the model comes from
// the layer — so letting one win would send a different model than the user
// chose, or a credential from the project document instead of the secret
// store. Fine-tuning evaluation guarded this explicitly and had a test for
// it; the agent's own initLLMProvider did not, so centralising the order here
// closes that hole on every surface rather than spreading it.
func ProviderConfig(spec LayerSpec, cred Credential, opts BuildOptions) gollm.ProviderConfig {
	out := gollm.ProviderConfig{}
	// A deployment default is written only when it is set. Writing "0" would
	// not mean "use the provider's default" — it would mean zero retries and
	// a zero-second timeout — so a caller with no deployment config (an
	// enterprise plugin, say) must leave the keys absent exactly as it does
	// today.
	if opts.MaxRetries > 0 {
		out["max_retries"] = strconv.Itoa(opts.MaxRetries)
	}
	if opts.TimeoutSeconds > 0 {
		out["timeout_seconds"] = strconv.Itoa(opts.TimeoutSeconds)
	}
	if opts.RequestDelayMs > 0 {
		out["request_delay_ms"] = strconv.Itoa(opts.RequestDelayMs)
	}
	for k, v := range spec.Config {
		out[k] = v
	}
	out["model"] = spec.Model
	if cred.Value != "" || !opts.OmitEmptyCredential {
		out["credentials_json"] = cred.Value
	} else {
		// Nothing resolved and the caller wants the key absent — so drop a
		// stray one that came out of a config map rather than leaving it to
		// be sent as a bearer token.
		delete(out, "credentials_json")
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
	return BuildProviderWith(spec, cred, opts)
}

// BuildProviderWith is BuildProvider with the credential already resolved.
//
// It exists for a caller that builds several layers at once and shares
// provider instances between them: the credential is part of what makes two
// layers the same connection, so it has to be known BEFORE the sharing
// decision, not inside the construction that follows it.
func BuildProviderWith(spec LayerSpec, cred Credential, opts BuildOptions) (gollm.Provider, gollm.ProviderConfig, error) {
	if err := spec.Validate(); err != nil {
		return nil, nil, err
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
// The model is deliberately NOT part of it, because gollm providers treat the
// construction-time model as a fallback only.
//
// Neither is the credential, which is NOT derived from the entry alone: the
// chain depends on the LAYER (table descriptions tries the blurb secret
// first), so a caller sharing providers has to fold the resolved credential
// into its own key alongside this one. buildLayerSet does.
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
