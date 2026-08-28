package discovery

import (
	"context"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
)

// modelWindowSaveTimeout bounds the best-effort calibration write. The observer
// fires inline on the LLM call path (before the adaptive retry), so an
// unavailable/slow Mongo must not stall the retry — the write is detached from
// the run context (so a cancelled run still records what it learned) but capped
// so it can't block for Mongo's default server-selection window.
const modelWindowSaveTimeout = 5 * time.Second

// modelWindowPersister stores a model's context window learned at run time from
// a context-overflow 400, keyed by project + provider + model, so a later run
// budgets against the real window before the first call. The read side is used
// by the run-start resolver (agentserver); the orchestrator only writes.
type modelWindowPersister interface {
	SaveWindow(ctx context.Context, provider, model string, window int) error
}

// resolveModelBudget returns the effective context window and output cap for
// the analysis + recommendation phases. Precedence:
//  1. calibratedWindow — the model's true window learned from an overflow 400
//     this run (ground truth; wins over everything).
//  2. llmInputWindow / llmOutputCap resolved at run start (operator override →
//     live auto-detection → catalog → default).
//  3. Catalog / operator-override fallback (the path unit tests take when the
//     orchestrator is constructed without pre-resolved values).
func (o *Orchestrator) resolveModelBudget() (window, outputCap int) {
	o.calibMu.Lock()
	cal := o.calibratedWindow
	o.calibMu.Unlock()

	switch {
	case cal > 0:
		window = cal
	case o.llmInputWindow > 0:
		window = o.llmInputWindow
	default:
		window = gollm.GetEffectiveInputWindow(o.llmProvider, o.llmModel, o.llmConfig)
	}

	outputCap = o.llmOutputCap
	if outputCap <= 0 {
		outputCap = gollm.ClampMaxTokens(
			gollm.GetMaxOutputTokens(o.llmProvider, o.llmModel),
			gollm.MaxOutputOverride(o.llmConfig),
		)
	}
	return window, outputCap
}

// installContextWindowObserver wires the ai.Client's context-window observer to
// this run's self-calibration: when a context-overflow 400 reveals the model's
// true window, cache it in memory (so the remaining areas re-budget against it)
// and persist it (so a later run for the same model budgets correctly up
// front). Safe no-op when there is no aiClient.
func (o *Orchestrator) installContextWindowObserver(ctx context.Context) {
	if o.aiClient == nil {
		return
	}
	o.aiClient.SetContextWindowObserver(func(model string, window int) {
		o.applyCalibratedWindow(ctx, model, window)
	})
}

// applyCalibratedWindow records a context window learned from an overflow 400:
// it caches the value in memory (so the remaining areas re-budget against it)
// and, on the first change, persists it best-effort. Ignores non-positive
// windows and no-op repeats.
func (o *Orchestrator) applyCalibratedWindow(ctx context.Context, model string, window int) {
	if window <= 0 {
		return
	}
	o.calibMu.Lock()
	changed := window != o.calibratedWindow
	o.calibratedWindow = window
	o.calibMu.Unlock()
	if !changed {
		return
	}
	applog.WithFields(applog.Fields{
		"provider": o.llmProvider,
		"model":    model,
		"window":   window,
	}).Info("Calibrated model context window from a context-overflow error")

	if o.modelWindowRepo != nil {
		// Persist under a detached context so a cancelled run still records what
		// it learned, but bounded by a short timeout so a slow/unavailable Mongo
		// can't stall the inline LLM retry path. Best-effort — a write failure
		// must not affect the run.
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), modelWindowSaveTimeout)
		defer cancel()
		if err := o.modelWindowRepo.SaveWindow(saveCtx, o.llmProvider, model, window); err != nil {
			applog.WithFields(applog.Fields{
				"provider": o.llmProvider,
				"model":    model,
				"error":    err.Error(),
			}).Warn("Failed to persist calibrated model context window")
		}
	}
}
