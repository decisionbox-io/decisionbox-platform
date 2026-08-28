package agentserver

import (
	"context"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
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
func resolveModelBudget(ctx context.Context, provider gollm.Provider, providerName, model string, cfg gollm.ProviderConfig, persistedWindow int) (window, outputCap int) {
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

	base := gollm.GetMaxOutputTokens(providerName, model)
	if live.MaxOutputTokens > 0 {
		base = live.MaxOutputTokens
	}
	outputCap = gollm.ClampMaxTokens(base, gollm.MaxOutputOverride(cfg))

	applog.WithFields(applog.Fields{
		"provider":      providerName,
		"model":         model,
		"window":        window,
		"window_source": windowSource,
		"output_cap":    outputCap,
	}).Info("Resolved model context budget")

	return window, outputCap
}

// projectModelWindowStore binds a project id to the model-window repo so it
// satisfies the discovery orchestrator's writer interface (which is
// project-scoped by construction and passes only provider+model).
type projectModelWindowStore struct {
	repo      *database.LLMModelWindowRepository
	projectID string
}

func (s projectModelWindowStore) SaveWindow(ctx context.Context, provider, model string, window int) error {
	return s.repo.SaveWindow(ctx, s.projectID, provider, model, window)
}
