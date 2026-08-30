package discovery

import (
	"context"
	"strings"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
)

// ModelWindowKey is the identifier under which a model's self-calibrated
// context window is persisted (project + provider + this key). It is the model
// id, except for endpoint-based projects (user-deployed Vertex endpoints) whose
// llm.model is intentionally blank and whose deployment is identified by
// endpoint_id — there it keys by "endpoint:<id>" so calibration still persists
// and reloads across runs. Returns "" only when neither a model nor an endpoint
// id is present (persistence is then skipped). Used by both the run-start
// resolver (read) and the calibration observer (write) so the keys match.
func ModelWindowKey(model string, cfg gollm.ProviderConfig) string {
	if m := strings.TrimSpace(model); m != "" {
		return m
	}
	if cfg != nil {
		if ep := strings.TrimSpace(cfg["endpoint_id"]); ep != "" {
			return "endpoint:" + ep
		}
	}
	return ""
}

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

// effectiveReasoning reports whether the run's analysis model should be treated
// as reasoning-capable for the exploration output-headroom budget (R3). True
// when the operator enabled the model-agnostic per-project "Enable reasoning"
// toggle (o.reasoningEnabled), a legacy llm.config flag is set (back-compat), or
// the catalog flags the model. Catalog-independent by design — an uncatalogued
// reasoning model the operator opted into (e.g. Kimi via LiteLLM) still gets
// headroom, and a big non-reasoning model (Opus, GPT, ...) with the toggle off
// returns false, so its exploration budget is unchanged.
func (o *Orchestrator) effectiveReasoning() bool {
	return o.reasoningEnabled || gollm.ReasoningEnabled(o.llmConfig) || gollm.IsReasoningModel(o.llmProvider, o.llmModel)
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

	// Key by the persistence key (endpoint id for endpoint-based projects whose
	// model is blank), so endpoint deployments also persist/reload calibration.
	key := ModelWindowKey(model, o.llmConfig)
	if o.modelWindowRepo != nil && key != "" {
		// Persist under a detached context so a cancelled run still records what
		// it learned, but bounded by a short timeout so a slow/unavailable Mongo
		// can't stall the inline LLM retry path. Best-effort — a write failure
		// must not affect the run.
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), modelWindowSaveTimeout)
		defer cancel()
		if err := o.modelWindowRepo.SaveWindow(saveCtx, o.llmProvider, key, window); err != nil {
			applog.WithFields(applog.Fields{
				"provider": o.llmProvider,
				"model":    model,
				"error":    err.Error(),
			}).Warn("Failed to persist calibrated model context window")
		}
	}
}
