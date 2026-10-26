package agentserver

import (
	"context"
	"fmt"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/config"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/debug"
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
	// buildProvider constructs a provider from a resolved spec. Defaults to
	// llmlayer.BuildProvider with the deployment's LLM settings.
	buildProvider func(ctx context.Context, spec llmlayer.LayerSpec) (gollm.Provider, gollm.ProviderConfig, error)
	// resolveBudget resolves (window, outputCap) for one target. Defaults to
	// resolveModelBudget, which includes the live ModelInfoResolver probe.
	resolveBudget func(ctx context.Context, provider gollm.Provider, spec llmlayer.LayerSpec, persistedWindow int) (window, outputCap int)
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

	type budget struct{ window, outputCap int }
	providers := map[string]gollm.Provider{}
	configs := map[string]gollm.ProviderConfig{}
	budgets := map[string]budget{}

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

		// One provider per (entry, merged config).
		providerKey := spec.ProviderID + "\x00" + llmlayer.ConfigKey(gollm.ProviderConfig(spec.Config))
		provider, built := providers[providerKey]
		var mergedCfg gollm.ProviderConfig
		if built {
			mergedCfg = configs[providerKey]
		} else {
			provider, mergedCfg, err = deps.buildProvider(ctx, spec)
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
			window, outputCap := deps.resolveBudget(ctx, provider, spec, deps.persistedWindow(ctx, spec))
			b = budget{window: window, outputCap: outputCap}
			budgets[budgetKey] = b
		}

		// One client per layer.
		client, err := ai.New(provider, spec.Model)
		if err != nil {
			return nil, fmt.Errorf("create AI client for layer %q: %w", layer, err)
		}
		client.SetProvenance(in.projectID, in.runID, spec.Provider)
		client.SetPhase(layerPhase(layer))
		client.SetReasoning(spec.Reasoning)
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
			Reasoning:  spec.Reasoning,
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
		buildProvider: func(ctx context.Context, spec llmlayer.LayerSpec) (gollm.Provider, gollm.ProviderConfig, error) {
			return llmlayer.BuildProvider(ctx, secretProvider, projectID, spec, opts)
		},
		resolveBudget: func(ctx context.Context, provider gollm.Provider, spec llmlayer.LayerSpec, persisted int) (int, int) {
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

// discoveryLayers are the layers a DISCOVERY run resolves. Listed rather than
// taking the whole set so a run does not build providers for Ask, pack
// generation, file ingestion or the executive summary — none of which it
// calls.
var discoveryLayers = []string{
	llmlayer.LayerExploration,
	llmlayer.LayerAnalysis,
	llmlayer.LayerValidationVerifier,
	llmlayer.LayerValidationRefuter,
	llmlayer.LayerRecommendations,
	llmlayer.LayerQuestions,
	llmlayer.LayerReflection,
	llmlayer.LayerSQLRepair,
}

// askServeLayers are the layers the ask-serve process resolves: the answer
// model, the datasource router and the repair model its loop may need.
var askServeLayers = []string{
	llmlayer.LayerAsk,
	llmlayer.LayerAskRouter,
	llmlayer.LayerSQLRepair,
}

// validationLayers are the layers the standalone document validator resolves.
var validationLayers = []string{
	llmlayer.LayerValidationVerifier,
	llmlayer.LayerValidationRefuter,
}
