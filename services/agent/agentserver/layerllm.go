package agentserver

import (
	"context"
	"fmt"
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	"github.com/decisionbox-io/decisionbox/libs/go-common/policy"
	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/config"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/debug"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/discovery"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// layerSet is the resolved LLM for every layer a run needs. It satisfies
// ai.LayerSet.
type layerSet struct {
	byLayer map[string]*ai.LayerLLM
	// fallback is the default layer's LLM, returned for any layer that was
	// not built. Never nil once buildLayerSet has returned without error.
	fallback *ai.LayerLLM
}

// For returns a layer's LLM, falling back to the default layer for a layer
// this set did not build — an unknown key, or one the run does not use.
//
// Never nil: a run that dies on a layer-key typo is worse than one that runs
// on the default model, and the API already refuses an unknown key on write.
func (s *layerSet) For(layer string) *ai.LayerLLM {
	if s == nil {
		return nil
	}
	if l, ok := s.byLayer[layer]; ok {
		return l
	}
	return s.fallback
}

// Layers returns every distinct layer this set built, for logging.
func (s *layerSet) built() []*ai.LayerLLM {
	out := make([]*ai.LayerLLM, 0, len(s.byLayer))
	seen := map[*ai.LayerLLM]bool{}
	for _, l := range s.byLayer {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

// layerSetDeps are the constructors buildLayerSet calls, injectable so the
// dedupe guarantees can be asserted by counting calls rather than by reading
// the code and hoping.
type layerSetDeps struct {
	// resolveCredential resolves one layer's credential. Separate from
	// buildProvider because the credential decides whether two layers may
	// SHARE a provider, so it has to be known before that decision.
	resolveCredential func(ctx context.Context, spec llmlayer.LayerSpec) (llmlayer.Credential, error)
	// buildProvider constructs a provider from a resolved spec and its
	// credential. Defaults to llmlayer.BuildProviderWith with the
	// deployment's LLM settings.
	buildProvider func(ctx context.Context, spec llmlayer.LayerSpec, cred llmlayer.Credential) (gollm.Provider, gollm.ProviderConfig, error)
	// resolveBudget resolves (window, outputCap) for one target. Defaults to
	// resolveModelBudget, which includes the live ModelInfoResolver probe.
	resolveBudget func(ctx context.Context, provider gollm.Provider, spec llmlayer.LayerSpec, persistedWindow int) (window, outputCap int, reasoning bool)
	// persistedWindow reads a target's self-calibrated window. Defaults to a
	// no-op (0) so callers without a Mongo handle need no stub.
	persistedWindow func(ctx context.Context, spec llmlayer.LayerSpec) int
}

// layerSetInput is everything buildLayerSet needs.
type layerSetInput struct {
	project   *models.Project
	projectID string
	runID     string
	// layers lists the layers to build. Only these are resolved — a discovery
	// run does not pay for the Ask or pack-generation layers.
	layers []string
	// testMode and debugLogger are applied to EVERY layer's client. Anything
	// applied to only one would silently switch a feature off for sixteen
	// layers.
	testMode    bool
	debugLogger *debug.Logger

	deps layerSetDeps
}

// buildLayerSet resolves every requested layer to a provider, a budget and an
// ai.Client, sharing what can be shared.
//
// Three dedupe keys, because three things cost differently:
//
//   - gollm.Provider, keyed by (provider entry, merged config) and NOT the
//     model. Every provider treats its construction-time model as a fallback
//     only and ai.Client sets ChatRequest.Model on every call, so one instance
//     serves every model on an entry. This is what keeps "every layer on the
//     default" to a single HTTP client, pool and AWS/GCP credential load.
//     Prompt caching IS part of the key, because the four Anthropic-wire
//     providers read it at construction — so two layers on one entry with
//     different caching settings are two instances, by design.
//   - the token budget, keyed by (provider entry, model, merged config),
//     because the catalog lookup and the live ModelInfoResolver probe are per
//     model. One probe per distinct model, not per layer.
//   - ai.Client, one PER LAYER. It carries the model, the phase tag,
//     provenance, the reasoning flag and mutable step/debug/capture state,
//     and it is a small struct — so splitting the cheap part is what makes
//     per-layer attribution and per-layer reasoning free.
func buildLayerSet(ctx context.Context, in layerSetInput) (*layerSet, error) {
	if in.project == nil {
		return nil, fmt.Errorf("buildLayerSet: project is required")
	}
	deps := in.deps
	if deps.buildProvider == nil {
		return nil, fmt.Errorf("buildLayerSet: buildProvider is required")
	}
	if deps.resolveCredential == nil {
		return nil, fmt.Errorf("buildLayerSet: resolveCredential is required")
	}
	if deps.resolveBudget == nil {
		return nil, fmt.Errorf("buildLayerSet: resolveBudget is required")
	}
	if deps.persistedWindow == nil {
		deps.persistedWindow = func(context.Context, llmlayer.LayerSpec) int { return 0 }
	}

	layers := in.layers
	if len(layers) == 0 {
		layers = llmlayer.Keys()
	}
	// The default layer is always built: it is the fallback For() returns, and
	// an un-overridden layer resolves to it anyway.
	want := append([]string{llmlayer.DefaultLayer}, layers...)

	type budget struct {
		window, outputCap int
		// reasoning is what live detection reported for this target; it only
		// ever turns the layer's reasoning setting ON, never off.
		reasoning bool
	}
	providers := map[string]gollm.Provider{}
	configs := map[string]gollm.ProviderConfig{}
	budgets := map[string]budget{}
	// One secret read per credential CHAIN, not per layer: a single-model
	// project reads the default chain once and the blurb chain once, and
	// every layer sharing a chain reuses the result.
	creds := map[string]llmlayer.Credential{}

	set := &layerSet{byLayer: make(map[string]*ai.LayerLLM, len(want))}

	for _, layer := range want {
		if _, done := set.byLayer[layer]; done {
			continue
		}

		// Unchecked: the "has this project been configured for per-layer
		// models" gate is a PRODUCT precondition and lives in the API, which
		// is the only place that knows whether it can be true. On a managed
		// deployment the whole AI configuration belongs to the gateway —
		// managedai.Apply overwrites it on every write — so there is nothing
		// for a user to review and no settings page to be sent to, and a
		// project configured before #407 carries no version stamp. Repeating
		// the gate here, where that is invisible, refused every existing
		// cloud project after the API had already let the run through.
		//
		// Nothing is lost: the API refuses an unconfigured project at both
		// entry points (StartRun and Ask), and a project with no provider at
		// all still fails below with ErrNoProviderConfigured.
		spec, err := in.project.LayerSpecUnchecked(layer)
		if err != nil {
			return nil, fmt.Errorf("resolve layer %q: %w", layer, err)
		}
		if spec.FellBackFromProviderID != "" {
			applog.WithFields(applog.Fields{
				"layer":                layer,
				"missing_provider_id":  spec.FellBackFromProviderID,
				"resolved_provider_id": spec.ProviderID,
			}).Warn("Layer names an LLM provider entry this project does not have; using the default entry")
		}

		chainKey := llmlayer.ChainKey(llmlayer.CredentialChain(spec.Layer, spec.ProviderID, spec.Config))
		cred, haveCred := creds[chainKey]
		if !haveCred {
			var cerr error
			cred, cerr = deps.resolveCredential(ctx, spec)
			if cerr != nil {
				// The agent's long-standing policy: an unexpected
				// secret-store error is logged and the resolved fallback is
				// used, so an env-only or managed deployment keeps working
				// through a transient blip.
				applog.WithFields(applog.Fields{
					"layer":             layer,
					"credential_source": cred.Source,
					"error":             cerr.Error(),
				}).Warn("Secret-store error while resolving a layer's credential; continuing on the fallback")
			}
			creds[chainKey] = cred
		}

		// One provider per (entry, merged config, RESOLVED credential).
		//
		// The credential belongs in the key because it is not a function of
		// the entry alone: table descriptions tries the blurb secret before
		// the default one, so two layers on ONE entry with identical config
		// can authenticate as different principals. Keyed without it, the
		// first layer built won, and a project with a separate blurb
		// credential indexed its tables on the analysis key — which is what
		// resolveBlurbLLM used to get right by building its own provider.
		//
		// It is keyed on the credential's IDENTITY (which key it came from,
		// and whether that was the store or the environment), never its
		// value, and on the RESOLVED one rather than the chain — so a
		// project whose blurb chain falls through to the default key still
		// shares one provider instance, as it should.
		// ...and separately for whether the layer HAS a model at all.
		//
		// The model itself is deliberately not in the key: ai.Client sets it
		// on every call, so one instance serves many models. But an
		// endpoint-backed provider (a user-deployed Vertex endpoint) falls
		// back to its CONSTRUCTION model when a request leaves the model
		// empty — which is the whole point of an endpoint layer. Sharing an
		// instance built with a named model would hand that layer another
		// layer's model instead of passing the empty value through.
		modelled := "model"
		if strings.TrimSpace(spec.Model) == "" {
			modelled = "endpoint"
		}
		providerKey := spec.ProviderID + "\x00" + spec.ConnectionKey() +
			"\x00" + cred.SecretKey + "\x00" + cred.Source + "\x00" + modelled
		provider, built := providers[providerKey]
		var mergedCfg gollm.ProviderConfig
		if built {
			mergedCfg = configs[providerKey]
		} else {
			provider, mergedCfg, err = deps.buildProvider(ctx, spec, cred)
			if err != nil {
				return nil, fmt.Errorf("build LLM for layer %q: %w", layer, err)
			}
			providers[providerKey] = provider
			configs[providerKey] = mergedCfg
		}

		// One budget resolution — and one live probe — per (entry, model,
		// merged config).
		budgetKey := providerKey + "\x00" + spec.Model
		b, resolved := budgets[budgetKey]
		if !resolved {
			window, outputCap, reasoning := deps.resolveBudget(ctx, provider, spec, deps.persistedWindow(ctx, spec))
			b = budget{window: window, outputCap: outputCap, reasoning: reasoning}
			budgets[budgetKey] = b
		}

		// One client per layer.
		client, err := ai.New(provider, spec.Model)
		if err != nil {
			return nil, fmt.Errorf("create AI client for layer %q: %w", layer, err)
		}
		client.SetProvenance(in.projectID, in.runID, spec.Provider)
		client.SetPhase(layerPhase(layer))
		// The layer's own setting, widened by what live detection reported:
		// behind a managed gateway the catalog cannot see the backend model,
		// so this is the only signal that the model thinks.
		layerReasoning := spec.Reasoning || b.reasoning
		client.SetReasoning(layerReasoning)
		// Both of these reached only the single client before per-layer
		// models. Applying them to one client now would switch the feature
		// off for every other layer.
		if in.testMode {
			client.SetTestMode(true)
		}
		if in.debugLogger != nil {
			client.SetDebugLogger(in.debugLogger)
		}

		set.byLayer[layer] = &ai.LayerLLM{
			Layer:      layer,
			ProviderID: spec.ProviderID,
			Provider:   spec.Provider,
			Model:      spec.Model,
			Config:     mergedCfg,
			LLM:        provider,
			Client:     client,
			Window:     b.window,
			OutputCap:  b.outputCap,
			Reasoning:  layerReasoning,
		}
	}

	set.fallback = set.byLayer[llmlayer.DefaultLayer]
	if set.fallback == nil {
		return nil, fmt.Errorf("buildLayerSet: the default layer did not resolve")
	}

	applog.WithFields(applog.Fields{
		"layers":    len(set.byLayer),
		"providers": len(providers),
		"budgets":   len(budgets),
	}).Info("Per-layer LLM set built")
	for _, l := range set.built() {
		applog.WithFields(applog.Fields{
			"layer":        l.Layer,
			"provider_id":  l.ProviderID,
			"provider":     l.Provider,
			"model":        l.Model,
			"window":       l.Window,
			"output_cap":   l.OutputCap,
			"reasoning":    l.Reasoning,
			"prompt_cache": gollm.PromptCacheEnabled(l.Config),
		}).Info("Layer LLM resolved")
	}

	return set, nil
}

// layerPhase is the usage-log phase a layer's calls are attributed to.
//
// Most layer keys ARE the phase constant, deliberately, so an operator can
// grep one for the other. The exception is the validation pair: one phase now
// spends two models, so both report phase=validation while the layer keys keep
// them apart.
func layerPhase(layer string) string {
	switch layer {
	case llmlayer.LayerValidationVerifier, llmlayer.LayerValidationRefuter:
		return models.PhaseValidation
	case llmlayer.DefaultLayer:
		return models.PhaseAnalysis
	default:
		return layer
	}
}

// newLayerSetDeps wires the production constructors: llmlayer.BuildProvider
// with the deployment's LLM defaults, the live-probing budget resolver, and
// the per-target calibration lookup.
func newLayerSetDeps(cfg *config.Config, secretProvider gosecrets.Provider, projectID string, windows modelWindowReader) layerSetDeps {
	opts := llmlayer.BuildOptions{
		MaxRetries:     cfg.LLM.MaxRetries,
		TimeoutSeconds: int(cfg.LLM.Timeout.Seconds()),
		RequestDelayMs: cfg.LLM.RequestDelayMs,
		// The agent's long-standing policy: log an unexpected secret-store
		// error and continue on the environment fallback, so an env-only or
		// managed deployment keeps working through a transient blip.
		Logf: func(format string, args ...any) {
			applog.Info(fmt.Sprintf(format, args...))
		},
	}
	return layerSetDeps{
		resolveCredential: func(ctx context.Context, spec llmlayer.LayerSpec) (llmlayer.Credential, error) {
			return llmlayer.ResolveCredential(ctx, secretProvider, projectID, spec, opts)
		},
		buildProvider: func(_ context.Context, spec llmlayer.LayerSpec, cred llmlayer.Credential) (gollm.Provider, gollm.ProviderConfig, error) {
			return llmlayer.BuildProviderWith(spec, cred, opts)
		},
		resolveBudget: func(ctx context.Context, provider gollm.Provider, spec llmlayer.LayerSpec, persisted int) (int, int, bool) {
			return resolveModelBudget(ctx, provider, spec.Provider, spec.Model, gollm.ProviderConfig(spec.Config), persisted)
		},
		persistedWindow: func(ctx context.Context, spec llmlayer.LayerSpec) int {
			if windows == nil {
				return 0
			}
			return windows.window(ctx, spec)
		},
	}
}

// modelWindowReader reads a target's self-calibrated context window. Narrow
// interface so buildLayerSet is testable without Mongo.
type modelWindowReader interface {
	window(ctx context.Context, spec llmlayer.LayerSpec) int
}

// discoveryPhases says which optional discovery phases THIS run will execute.
type discoveryPhases struct {
	validation bool
	refuter    bool
	questions  bool
	reflection bool
}

// discoveryPhasesFor resolves those phases from EVERY gate each one answers
// to, because any one of them off means the phase does not run — and a phase
// that does not run must not have its provider built.
//
// Questions and reflection answer to three: the per-project toggle (Layer B),
// the deployment's availability flag (Layer A), and the sources entitlement
// the two phases check for themselves. Resolving only the first two left a
// deployment without that entitlement building providers for two phases it
// then skipped, so a broken override on either aborted the run before
// exploration started — the failure this gating exists to prevent.
func discoveryPhasesFor(ctx context.Context, project *models.Project) discoveryPhases {
	sources, _ := policy.GetChecker().FeatureEnabled(ctx, "", policy.FeatureSources)
	return discoveryPhases{
		validation: project.EffectiveValidationEnabled(),
		refuter:    refuterEnabled(),
		questions:  project.EffectiveClarifyingQuestionsEnabled() && discovery.QuestionsAvailable() && sources,
		reflection: project.EffectiveReflectionEnabled() && discovery.ReflectionAvailable() && sources,
	}
}

// discoveryLayers are the layers a DISCOVERY run resolves.
//
// Listed rather than taking the whole set so a run does not build providers
// for Ask, pack generation, file ingestion or the executive summary — none of
// which it calls. Gated on the enabled phases for a sharper reason: building
// a provider reads a credential and constructs a connection, so a broken
// override on a layer this run will never call aborted the run before
// exploration started. VALIDATION_REFUTER_ENABLED=false could not route
// around a broken refuter endpoint, which is the one thing it is for.
//
// A layer that is not built still resolves through layerSet.For, which falls
// back to the default layer — so being wrong here costs the default model,
// not a nil client.
func discoveryLayers(phases discoveryPhases) []string {
	layers := []string{
		llmlayer.LayerExploration,
		llmlayer.LayerAnalysis,
		llmlayer.LayerRecommendations,
		// The query fixer runs inside exploration, so it is never optional.
		llmlayer.LayerSQLRepair,
	}
	if phases.validation {
		layers = append(layers, llmlayer.LayerValidationVerifier)
		if phases.refuter {
			layers = append(layers, llmlayer.LayerValidationRefuter)
		}
	}
	if phases.questions {
		layers = append(layers, llmlayer.LayerQuestions)
	}
	if phases.reflection {
		layers = append(layers, llmlayer.LayerReflection)
	}
	return layers
}

// askServeLayers are the layers the ask-serve process resolves: the answer
// model, the repair model its loop may need, and the datasource router when
// a route can actually happen.
//
// The router runs only on a multi-datasource turn with routing enabled, and
// building a provider reads a credential and opens a connection — so a
// single-datasource project with a broken ask_router override used to fail
// its whole Ask runtime over a layer it would never call.
func askServeLayers(routerPossible bool) []string {
	layers := []string{llmlayer.LayerAsk, llmlayer.LayerSQLRepair}
	if routerPossible {
		layers = append(layers, llmlayer.LayerAskRouter)
	}
	return layers
}

// validationLayers are the layers the standalone document validator
// resolves. The refuter is included only when it is enabled, for the reason
// discoveryLayers gates its own list: a provider built for a layer this run
// will never call can only fail work it was not needed for.
func validationLayers(refuterEnabled bool) []string {
	layers := []string{llmlayer.LayerValidationVerifier}
	if refuterEnabled {
		layers = append(layers, llmlayer.LayerValidationRefuter)
	}
	return layers
}
