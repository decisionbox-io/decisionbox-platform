package discovery

import (
	"context"
	"strings"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
)

// ModelWindowKey is the identifier under which a model's self-calibrated
// context window is persisted (project + provider entry + this key).
//
// It is the model id, qualified by endpoint_id whenever one is set. A
// user-deployed Vertex endpoint can carry no model id at all — llm.model is
// intentionally blank there — and it can equally carry one: two layers on the
// same entry naming the same model while overriding endpoint_id differently
// are TWO deployments, and a window learned by overflowing one must not
// shrink the other's budget or overwrite its stored value. Provider
// construction and budget resolution already tell them apart; this is the
// third place that has to.
//
// Returns "" only when neither a model nor an endpoint id is present
// (persistence is then skipped). Used by both the run-start resolver (read)
// and the calibration observer (write), so the keys match.
func ModelWindowKey(model string, cfg gollm.ProviderConfig) string {
	m := strings.TrimSpace(model)
	var endpoint string
	if cfg != nil {
		endpoint = strings.TrimSpace(cfg["endpoint_id"])
	}
	switch {
	case m != "" && endpoint != "":
		return m + "@endpoint:" + endpoint
	case m != "":
		return m
	case endpoint != "":
		return "endpoint:" + endpoint
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
// a context-overflow 400, keyed by project + provider entry + provider +
// model, so a later run budgets against the real window before the first call.
// The read side is used by the run-start resolver (agentserver); the
// orchestrator only writes.
//
// providerID is the project's LLM provider-ENTRY id: two entries pointing at
// different deployments can name the same model alias, and a window learned
// on one must not be applied to the other.
type modelWindowPersister interface {
	SaveWindow(ctx context.Context, providerID, provider, model string, window int) error
}

// budgetFor returns the effective context window and output cap for one
// layer's model. Precedence:
//  1. a window calibrated for THIS TARGET from an overflow 400 this run
//     (ground truth; wins over everything).
//  2. the layer's own budget resolved at run start (operator override → live
//     auto-detection → catalog → default).
//  3. the run-start default-layer budget, for a run with no per-layer set.
//  4. Catalog / operator-override fallback (the path unit tests take when the
//     orchestrator is constructed without pre-resolved values).
func (o *Orchestrator) budgetFor(layer string) (window, outputCap int) {
	l := o.layer(layer)

	provider, model, cfg := o.llmProvider, o.llmModel, o.llmConfig
	resolvedWindow, resolvedCap := o.llmInputWindow, o.llmOutputCap
	if l != nil {
		provider, model, cfg = l.Provider, l.Model, l.Config
		resolvedWindow, resolvedCap = l.Window, l.OutputCap
	}

	o.calibMu.Lock()
	cal := o.calibratedWindows[calibrationKey(l, provider, model, cfg)]
	o.calibMu.Unlock()

	switch {
	case cal > 0:
		window = cal
	case resolvedWindow > 0:
		window = resolvedWindow
	default:
		window = gollm.GetEffectiveInputWindow(provider, model, cfg)
	}

	outputCap = resolvedCap
	if outputCap <= 0 {
		outputCap = gollm.ClampMaxTokens(
			gollm.GetMaxOutputTokens(provider, model),
			gollm.MaxOutputOverride(cfg),
		)
	}
	return window, outputCap
}

// calibrationKey identifies the target a learned window belongs to.
//
// It includes the provider-ENTRY id, not just the provider slug: two LiteLLM
// entries pointing at different deployments can name the same model alias, and
// a window learned on one must not be applied to the other.
func calibrationKey(l *ai.LayerLLM, provider, model string, cfg gollm.ProviderConfig) string {
	entry := llmlayer.DefaultProviderID
	if l != nil {
		entry = l.ProviderID
	}
	key := ModelWindowKey(model, cfg)
	if key == "" {
		key = model
	}
	return entry + "\x00" + provider + "\x00" + key
}

// effectiveReasoning reports whether the DEFAULT layer's model should be
// treated as reasoning-capable for the exploration output-headroom budget
// (R3).
//
// Kept as a no-argument method because the existing suite calls it directly
// (reasoning_toggle_test.go). The per-layer form takes the layer.
func (o *Orchestrator) effectiveReasoning() bool {
	return o.effectiveReasoningFor(llmlayer.DefaultLayer)
}

// effectiveReasoningFor reports whether a layer's model should be treated as
// reasoning-capable. True when that LAYER opted in (its own
// reasoning_enabled), when the project-level toggle is on, when a legacy
// llm.config flag is set (back-compat), or when the catalog flags the model.
//
// Catalog-independent by design — an uncatalogued reasoning model the operator
// opted into (Kimi via LiteLLM, say) still gets headroom, and a big
// non-reasoning model (Opus, GPT, …) with the toggle off returns false, so its
// exploration budget is unchanged.
func (o *Orchestrator) effectiveReasoningFor(layer string) bool {
	if l := o.layer(layer); l != nil {
		return l.Reasoning || gollm.IsReasoningModel(l.Provider, l.Model)
	}
	return o.reasoningEnabled || gollm.ReasoningEnabled(o.llmConfig) || gollm.IsReasoningModel(o.llmProvider, o.llmModel)
}

// installContextWindowObserver wires each client's context-window observer to
// this run's self-calibration: when a context-overflow 400 reveals a model's
// true window, cache it against THAT TARGET (so the remaining areas re-budget
// against it) and persist it under that target's identity (so a later run
// budgets correctly up front).
//
// Every layer's client gets an observer, not just the default one — otherwise
// a mixed pipeline would learn nothing about sixteen of its seventeen models.
// Safe no-op when there is no client at all.
func (o *Orchestrator) installContextWindowObserver(ctx context.Context) {
	install := func(l *ai.LayerLLM, client *ai.Client) {
		if client == nil {
			return
		}
		client.SetContextWindowObserver(func(model string, window int) {
			o.applyCalibratedWindowFor(ctx, l, model, window)
		})
	}
	if o.layers == nil {
		install(nil, o.aiClient)
		return
	}
	seen := map[*ai.Client]bool{}
	for _, name := range append([]string{llmlayer.DefaultLayer}, llmlayer.Keys()...) {
		l := o.layers.For(name)
		if l == nil || seen[l.Client] {
			continue
		}
		seen[l.Client] = true
		install(l, l.Client)
	}
}

// applyCalibratedWindow records a window learned by the DEFAULT layer.
//
// Kept as the three-argument form the existing suite calls, for the same
// reason as the no-argument resolveModelBudget: a rename would break
// compilation whatever the runtime fallback does.
func (o *Orchestrator) applyCalibratedWindow(ctx context.Context, model string, window int) {
	o.applyCalibratedWindowFor(ctx, o.layer(llmlayer.DefaultLayer), model, window)
}

// applyCalibratedWindowFor records a context window learned from an overflow 400:
// it caches the value against the target that learned it (so the remaining
// areas re-budget against it) and, on the first change, persists it
// best-effort. Ignores non-positive windows and no-op repeats.
//
// Scoped to the target rather than the run: a window learned from the analysis
// model must not shrink the budget of the cheap model a small layer uses. With
// one model that distinction could not show, which is why it was wrong and
// invisible before per-layer models.
func (o *Orchestrator) applyCalibratedWindowFor(ctx context.Context, l *ai.LayerLLM, model string, window int) {
	if window <= 0 {
		return
	}
	provider, cfg := o.llmProvider, o.llmConfig
	providerID := llmlayer.DefaultProviderID
	if l != nil {
		provider, cfg, providerID = l.Provider, l.Config, l.ProviderID
	}

	ck := calibrationKey(l, provider, model, cfg)
	o.calibMu.Lock()
	if o.calibratedWindows == nil {
		// An Orchestrator built as a struct literal rather than through
		// NewOrchestrator — which the unit tests do — has no map yet.
		o.calibratedWindows = map[string]int{}
	}
	changed := window != o.calibratedWindows[ck]
	o.calibratedWindows[ck] = window
	o.calibMu.Unlock()
	if !changed {
		return
	}
	applog.WithFields(applog.Fields{
		"provider_id": providerID,
		"provider":    provider,
		"model":       model,
		"window":      window,
	}).Info("Calibrated model context window from a context-overflow error")

	// Key by the persistence key (endpoint id for endpoint-based projects whose
	// model is blank), so endpoint deployments also persist/reload calibration.
	key := ModelWindowKey(model, cfg)
	if o.modelWindowRepo != nil && key != "" {
		// Persist under a detached context so a cancelled run still records what
		// it learned, but bounded by a short timeout so a slow/unavailable Mongo
		// can't stall the inline LLM retry path. Best-effort — a write failure
		// must not affect the run.
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), modelWindowSaveTimeout)
		defer cancel()
		if err := o.modelWindowRepo.SaveWindow(saveCtx, providerID, provider, key, window); err != nil {
			applog.WithFields(applog.Fields{
				"provider_id": providerID,
				"provider":    provider,
				"model":       model,
				"error":       err.Error(),
			}).Warn("Failed to persist calibrated model context window")
		}
	}
}
