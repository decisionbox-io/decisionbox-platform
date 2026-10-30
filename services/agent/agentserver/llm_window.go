package agentserver

import (
	"context"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/discovery"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
)

// modelInfoResolveTimeout bounds the best-effort live model-info lookup done at
// run start so a slow/unreachable metadata endpoint can't delay discovery. On
// timeout the resolver falls through to persisted/catalog/default.
const modelInfoResolveTimeout = 8 * time.Second

// resolveModelBudget resolves the effective context window and output cap for
// the discovery run, without depending on the model being catalogued.
//
// Window precedence:  operator max_input_tokens override → persisted
// (self-calibrated from a prior overflow) → live auto-detection (provider
// ModelInfoResolver) → catalog / provider hook / global default.
//
// Output precedence:  live auto-detection → catalog / default, then capped by
// the operator max_output_tokens override.
//
// reasoning is what live detection reported, and is only ever additive: the
// caller ORs it with the layer's own setting. It matters behind a managed
// gateway, where the alias hides the backend model from the catalog and every
// catalog-keyed reasoning decision would otherwise read false.
func resolveModelBudget(ctx context.Context, provider gollm.Provider, providerName, model string, cfg gollm.ProviderConfig, persistedWindow int) (window, outputCap int, reasoning bool) {
	// Live auto-detection: one best-effort call. Providers without the
	// capability (or that don't report the numbers) leave live zero-valued.
	var live gollm.ModelCapabilities
	if r, ok := provider.(gollm.ModelInfoResolver); ok {
		lctx, cancel := context.WithTimeout(ctx, modelInfoResolveTimeout)
		caps, err := r.ResolveModelInfo(lctx, model)
		cancel()
		if err != nil {
			applog.WithFields(applog.Fields{
				"provider": providerName,
				"model":    model,
				"error":    err.Error(),
			}).Debug("Live model-info lookup failed; falling back to persisted/catalog/default")
		} else {
			live = caps
		}
	}

	var windowSource string
	switch {
	case gollm.MaxInputOverride(cfg) > 0:
		window, windowSource = gollm.MaxInputOverride(cfg), "operator_override"
	case persistedWindow > 0:
		window, windowSource = persistedWindow, "persisted_calibration"
	case live.MaxInputTokens > 0:
		window, windowSource = live.MaxInputTokens, "live_autodetect"
	default:
		window, windowSource = gollm.GetEffectiveInputWindow(providerName, model, cfg), "catalog_default"
	}

	// Cap the chosen window by the current live-detected window. Live detection
	// reflects the endpoint's real limit right now — the gateway's reported
	// window, or Ollama's num_ctx (ResolveModelInfo clamps to it) — and the
	// endpoint physically won't accept more than it reports. This caps both a
	// stale self-calibration AND a stale operator override (e.g. a window the
	// dashboard prefilled once, that the gateway later lowered), so neither
	// budgets above what Chat will actually send. When there is no live
	// detection (e.g. Bedrock), the chosen value stands — there the override /
	// calibration is the best signal we have.
	if live.MaxInputTokens > 0 && live.MaxInputTokens < window {
		window, windowSource = live.MaxInputTokens, windowSource+"_capped_by_live"
	}

	base := gollm.GetMaxOutputTokens(providerName, model)
	if live.MaxOutputTokens > 0 {
		base = live.MaxOutputTokens
	}
	outputCap = gollm.ClampMaxTokens(base, gollm.MaxOutputOverride(cfg))

	applog.WithFields(applog.Fields{
		"provider":       providerName,
		"model":          model,
		"window":         window,
		"window_source":  windowSource,
		"output_cap":     outputCap,
		"live_reasoning": live.Reasoning,
	}).Info("Resolved model context budget")

	return window, outputCap, live.Reasoning
}

// projectModelWindowStore binds a project id to the model-window repo so it
// satisfies the discovery orchestrator's writer interface (which is
// project-scoped by construction and passes the provider entry, provider and
// model).
type projectModelWindowStore struct {
	repo      *database.LLMModelWindowRepository
	projectID string
}

func (s projectModelWindowStore) SaveWindow(ctx context.Context, providerID, provider, model string, window int) error {
	return s.repo.SaveWindow(ctx, s.projectID, providerID, provider, model, window)
}

// window reads a target's persisted self-calibrated context window, keyed by
// the same identity the observer writes. Satisfies modelWindowReader so
// buildLayerSet resolves each target's budget against what the project has
// already learned about THAT entry and model.
func (s projectModelWindowStore) window(ctx context.Context, spec llmlayer.LayerSpec) int {
	key := discovery.ModelWindowKey(spec.Model, gollm.ProviderConfig(spec.Config))
	if key == "" {
		return 0
	}
	w, err := s.repo.GetWindow(ctx, s.projectID, spec.ProviderID, spec.Provider, key)
	if err != nil {
		applog.WithFields(applog.Fields{
			"provider_id": spec.ProviderID,
			"model":       spec.Model,
			"error":       err.Error(),
		}).Debug("Failed to read persisted model window; continuing with live/catalog resolution")
		return 0
	}
	return w
}
